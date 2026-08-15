package main

import (
	"agentload/internal/historyfile"
	"agentload/internal/snapshot"
	"agentload/internal/trajectory"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func readTrajectoryInstance(path string) (trajectoryInstance, error) {
	var instance trajectoryInstance
	info, err := os.Lstat(path)
	if err != nil {
		return instance, errors.New("local Agent Load instance unavailable")
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return instance, errors.New("instance file must be private and regular")
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok && stat.Uid != uint32(os.Getuid()) {
		return instance, errors.New("instance file belongs to another user")
	}
	data, err := os.ReadFile(path)
	if err != nil || len(data) > 4096 {
		return instance, errors.New("cannot read local instance")
	}
	if json.Unmarshal(data, &instance) != nil || len(instance.Token) < 32 {
		return instance, errors.New("invalid local instance")
	}
	u, err := url.Parse(instance.Endpoint)
	if err != nil {
		return instance, errors.New("invalid instance endpoint")
	}
	ip := net.ParseIP(u.Hostname())
	port, portErr := strconv.Atoi(u.Port())
	if u.Scheme != "http" || ip == nil || !ip.IsLoopback() || portErr != nil || port < 1 || port > 65535 || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return instance, errors.New("instance endpoint must be literal loopback HTTP")
	}
	return instance, nil
}
func trajectoryHTTP(ctx context.Context, instance trajectoryInstance, path string, body any) (json.RawMessage, error) {
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(instance.Endpoint, "/")+path, bytes.NewReader(encoded))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+instance.Token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-AgentLoad-Local", "1")
	transport := &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 2 * time.Second}).DialContext}
	defer transport.CloseIdleConnections()
	timeout := 7 * time.Second
	if deadline, ok := ctx.Deadline(); ok {
		timeout = time.Until(deadline)
	}
	client := &http.Client{Transport: transport, Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("local API redirect refused") }}
	response, err := client.Do(req)
	if err != nil {
		return nil, errors.New("local Agent Load instance unreachable")
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 1024*1024+1))
	if err != nil || len(data) > 1024*1024 {
		return nil, errors.New("local API response size limit")
	}
	if response.StatusCode != 200 {
		return nil, fmt.Errorf("local API returned HTTP %d", response.StatusCode)
	}
	return json.RawMessage(data), nil
}
func trajectoryRPC(ctx context.Context, instance trajectoryInstance, method string, params any) (json.RawMessage, error) {
	data, err := trajectoryHTTP(ctx, instance, "/api/rpc", map[string]any{"jsonrpc": "2.0", "id": "cli", "method": method, "params": params})
	if err != nil {
		return nil, err
	}
	var response struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      string          `json:"id"`
		Result  json.RawMessage `json:"result"`
		Error   *rpcError       `json:"error"`
	}
	if json.Unmarshal(data, &response) != nil || response.JSONRPC != "2.0" || response.ID != "cli" {
		return nil, errors.New("invalid JSON-RPC response")
	}
	if response.Error != nil {
		return nil, fmt.Errorf("%s (%d)", response.Error.Message, response.Error.Code)
	}
	return response.Result, nil
}
func runTrajectoryCLI(args []string, out, errOut io.Writer) int {
	return runTrajectoryCLIContext(context.Background(), args, out, errOut)
}

func runTrajectoryCompactCLI(ctx context.Context, args []string, out, errOut io.Writer) int {
	fs := flag.NewFlagSet("agentload traj compact", flag.ContinueOnError)
	fs.SetOutput(errOut)
	history := fs.String("history-file", envOr("AGENTLOAD_HISTORY_FILE", defaultHistoryFile()), "local history to optimize while its app is stopped")
	expectedInput := fs.String("expected-input-sha256", "", "previously recorded full input digest for a legacy checkpoint after remount")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(errOut, "compact accepts --history-file and --expected-input-sha256")
		return 2
	}
	owner, err := historyfile.TryAcquire(*history + ".owner")
	if err != nil {
		fmt.Fprintln(errOut, "cannot optimize a running Agent Load; quit its app first:", err)
		return 1
	}
	defer owner.Release()
	encoder := json.NewEncoder(out)
	cfg := defaultConfig()
	cfg.HistoryFile = *history
	local := &trayApp{cfg: cfg, observer: newObserver(cfg)}
	defer local.observer.evidenceIndex.stopIndex()
	err = trajectory.OptimizeStorage(ctx, local.archiveSources, filepath.Join(trajectoryRoot(*history), "trajectory.sqlite"), func(progress trajectory.StorageProgress) { _ = encoder.Encode(progress) }, *expectedInput)
	if err != nil {
		fmt.Fprintln(errOut, "local index optimization incomplete; committed recovery state retained:", err)
		return 1
	}
	return 0
}

func runTrajectoryCLIContext(parent context.Context, args []string, out, errOut io.Writer) int {
	usage := func() {
		fmt.Fprintln(out, "agentload traj query [sessions|events|entities|actors|relations|contexts|knowledge|attention] [--text WORDS] [--tool NAME] [--count] [--format json]\nagentload traj get ID [--view slice] [--around 3] [--raw] [--format json]\nagentload traj watch [sessions|events] [--cursor CURSOR] [--once] [--format ndjson]\nagentload traj annotate --file REQUEST.json [--format json]\nagentload traj access [on|off]\nagentload traj compact [--history-file PATH] (offline; NDJSON progress)\nContent commands use the running local app; --instance-file selects its private receipt.")
	}
	if len(args) == 0 || args[0] == "--help" || args[0] == "help" {
		usage()
		return 0
	}
	if args[0] == "compact" {
		return runTrajectoryCompactCLI(parent, args[1:], out, errOut)
	}
	command := args[0]
	args = args[1:]
	q := snapshot.TrajectorySelector{Collection: "sessions"}
	p := snapshot.TrajectoryGetParams{}
	if (command == "query" || command == "watch") && len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		q.Collection = args[0]
		args = args[1:]
	}
	if command == "get" {
		if len(args) == 0 {
			fmt.Fprintln(errOut, "get requires an object ID")
			return 2
		}
		p.ID = args[0]
		args = args[1:]
	}
	accessValue := ""
	if command == "access" && len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		accessValue = args[0]
		args = args[1:]
	}
	fs := flag.NewFlagSet("agentload traj "+command, flag.ContinueOnError)
	fs.SetOutput(errOut)
	instancePath := fs.String("instance-file", filepath.Join(trajectoryRoot(envOr("AGENTLOAD_HISTORY_FILE", defaultHistoryFile())), "instance.json"), "private instance receipt")
	format := fs.String("format", "text", "text or json")
	annotationFile := new(string)
	if command == "annotate" {
		fs.StringVar(annotationFile, "file", "", "bounded JSON annotation request; - reads stdin")
	}
	if command == "query" || command == "watch" {
		fs.StringVar(&q.Text, "text", "", "query words")
		fs.StringVar(&q.Tool, "tool", "", "recorded tool name")
		fs.StringVar(&q.Skill, "skill", "", "skill name")
		fs.StringVar(&q.Kind, "kind", "", "event/entity kind")
		fs.StringVar(&q.State, "state", "", "knowledge/signal state")
		fs.StringVar(&q.SessionID, "session", "", "session ID")
		fs.StringVar(&q.ActorID, "actor", "", "observed actor ID")
		fs.StringVar(&q.ActorKind, "actor-kind", "", "recorded actor kind")
		fs.StringVar(&q.RelationKind, "relation-kind", "", "recorded relation kind")
		fs.StringVar(&q.Role, "role", "", "protocol role")
		fs.StringVar(&q.Agent, "agent", "", "source vendor")
		fs.StringVar(&q.ContextID, "context", "", "context revision")
		fs.StringVar(&q.ContextScope, "context-scope", "", "archive, actual_input, workspace or query_window")
		fs.StringVar(&q.EntityKind, "entity-kind", "", "tool, skill, path, term or version")
		fs.StringVar(&q.EntityID, "entity", "", "entity ID")
		fs.StringVar(&q.Predicate, "predicate", "", "recorded entity relationship")
		fs.IntVar(&q.Limit, "limit", 20, "maximum results (1..50)")
		fs.StringVar(&q.Cursor, "cursor", "", "query continuation")
	}
	if command == "query" {
		fs.BoolVar(&q.Count, "count", false, "compute exact total for sessions/events (up to 60 seconds)")
	}
	once, timeoutMS := new(bool), new(int)
	if command == "watch" {
		fs.BoolVar(once, "once", false, "return one watch batch")
		fs.IntVar(timeoutMS, "timeout-ms", 5000, "watch wait (0..5000 ms)")
	}
	if command == "get" {
		fs.IntVar(&p.Around, "around", 3, "steps on each side (0..5)")
		fs.StringVar(&p.View, "view", "slice", "object view")
		fs.IntVar(&p.MaxBytes, "max-bytes", 5120, "slice byte budget")
		fs.BoolVar(&p.Raw, "raw", false, "include bounded original records")
		fs.IntVar(&p.RawOffset, "raw-offset", 0, "byte continuation for --view raw")
		fs.IntVar(&p.MemberOffset, "member-offset", 0, "context manifest continuation")
	}
	if command != "query" && command != "get" && command != "access" && command != "watch" && command != "annotate" {
		fmt.Fprintln(errOut, "unknown trajectory command")
		return 2
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if p.View == "attention" {
		provided := false
		fs.Visit(func(f *flag.Flag) {
			if f.Name == "around" {
				provided = true
			}
		})
		if !provided {
			p.Around = 0
		}
	}
	if strings.HasPrefix(p.ID, "ctx.") {
		provided := false
		fs.Visit(func(f *flag.Flag) {
			if f.Name == "view" {
				provided = true
			}
		})
		if !provided {
			p.View = "context"
		}
	}
	if fs.NArg() != 0 || (*format != "text" && *format != "json" && !(*format == "ndjson" && command == "watch")) {
		fmt.Fprintln(errOut, "invalid arguments or format")
		return 2
	}
	instance, err := readTrajectoryInstance(*instancePath)
	if err != nil {
		fmt.Fprintln(errOut, err)
		return 1
	}
	var result json.RawMessage
	if command == "watch" {
		if *timeoutMS < 0 || *timeoutMS > 5000 {
			fmt.Fprintln(errOut, "watch timeout must be 0..5000 ms")
			return 2
		}
		cursor := q.Cursor
		q.Cursor = ""
		for {
			ctx, cancel := context.WithTimeout(parent, 7*time.Second)
			body, watchErr := trajectoryRPC(ctx, instance, "traj.watch", snapshot.TrajectoryWatchParams{Selector: q, Cursor: cursor, TimeoutMS: *timeoutMS})
			cancel()
			if parent.Err() != nil {
				return 0
			}
			if watchErr != nil {
				fmt.Fprintln(errOut, watchErr)
				return 1
			}
			var batch snapshot.TrajectoryWatchResult
			if json.Unmarshal(body, &batch) != nil || batch.Cursor == "" {
				fmt.Fprintln(errOut, "invalid watch result")
				return 1
			}
			cursor = batch.Cursor
			if *format == "text" {
				for _, change := range batch.Changes {
					fmt.Fprintf(out, "%d\t%s\t%s\n", change.Sequence, change.EventID, change.EventKind)
				}
				if batch.ResetRequired {
					fmt.Fprintln(out, "reset required: requery before consuming")
				}
				printTrajectoryCoverage(out, batch.Coverage)
				fmt.Fprintln(out, "cursor:", batch.Cursor)
			} else {
				fmt.Fprintln(out, string(body))
			}
			if *once || batch.ResetRequired {
				return 0
			}
			// A caller selecting zero wait must not create a busy polling loop.
			if *timeoutMS == 0 && !batch.More {
				select {
				case <-parent.Done():
					return 0
				case <-time.After(100 * time.Millisecond):
				}
			}
		}
	}
	timeout := 7 * time.Second
	if command == "query" && q.Count {
		timeout = 61 * time.Second
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	switch command {
	case "query":
		result, err = trajectoryRPC(ctx, instance, "traj.query", q)
	case "get":
		result, err = trajectoryRPC(ctx, instance, "traj.get", p)
	case "access":
		if accessValue != "on" && accessValue != "off" {
			fmt.Fprintln(errOut, "access requires on or off")
			return 2
		}
		result, err = trajectoryHTTP(ctx, instance, "/api/trajectory/access", map[string]bool{"enabled": accessValue == "on"})
	case "annotate":
		if *annotationFile == "" {
			fmt.Fprintln(errOut, "annotate requires --file")
			return 2
		}
		var input io.Reader = os.Stdin
		if *annotationFile != "-" {
			f, openErr := os.Open(*annotationFile)
			if openErr != nil {
				fmt.Fprintln(errOut, "cannot open annotation file")
				return 2
			}
			defer f.Close()
			input = f
		}
		data, readErr := io.ReadAll(io.LimitReader(input, 32*1024+1))
		var annotation snapshot.TrajectoryAnnotationParams
		if readErr != nil || len(data) > 32*1024 || strictParams(data, &annotation) != nil {
			fmt.Fprintln(errOut, "invalid annotation request")
			return 2
		}
		result, err = trajectoryRPC(ctx, instance, "traj.annotate", annotation)
	}
	if err != nil {
		fmt.Fprintln(errOut, err)
		return 1
	}
	if *format == "json" {
		fmt.Fprintln(out, string(result))
		return 0
	}
	if command == "query" {
		if q.Collection == "attention" {
			var r snapshot.TrajectoryAttentionQueryResult
			if json.Unmarshal(result, &r) != nil {
				fmt.Fprintln(errOut, "invalid attention result")
				return 1
			}
			for _, item := range r.Attention {
				printTrajectoryAttention(out, item)
			}
			printTrajectoryCoverage(out, r.Coverage)
			if r.Next != "" {
				fmt.Fprintln(out, "next:", r.Next)
			}
			return 0
		}
		if q.Collection == "contexts" {
			var r snapshot.TrajectoryContextQueryResult
			if json.Unmarshal(result, &r) != nil {
				fmt.Fprintln(errOut, "invalid context result")
				return 1
			}
			for _, c := range r.Contexts {
				fmt.Fprintf(out, "%s\t%s\t%s\t%d\n", c.ID, c.Scope, c.Membership, c.KnownMembers)
			}
			printTrajectoryCoverage(out, r.Coverage)
			if r.Next != "" {
				fmt.Fprintln(out, "next:", r.Next)
			}
			return 0
		}
		if q.Collection == "actors" {
			var r snapshot.TrajectoryActorQueryResult
			if err := json.Unmarshal(result, &r); err != nil {
				fmt.Fprintln(errOut, err)
				return 1
			}
			for _, actor := range r.Actors {
				fmt.Fprintf(out, "%s\t%s\t%s\t%s\n", actor.ID, actor.Actor.Kind, actor.Actor.ID, actor.EventID)
			}
			printTrajectoryCoverage(out, r.Coverage)
			return 0
		}
		if q.Collection == "relations" {
			var r snapshot.TrajectoryRelationQueryResult
			if err := json.Unmarshal(result, &r); err != nil {
				fmt.Fprintln(errOut, err)
				return 1
			}
			printTrajectoryRelations(out, r)
			return 0
		}
		var r snapshot.TrajectoryQueryResult
		_ = json.Unmarshal(result, &r)
		for _, session := range r.Sessions {
			fmt.Fprintf(out, "%s\t%s\t%s\n", session.ID, session.Agent, session.Title)
		}
		for _, e := range r.Events {
			fmt.Fprintf(out, "%s\t%s\t%s\n", e.ID, e.Kind, e.Text)
		}
		for _, entity := range r.Entities {
			fmt.Fprintf(out, "%s\t%s\t%s\t%d\n", entity.ID, entity.Kind, entity.Label, entity.Count)
		}
		for _, knowledge := range r.Knowledge {
			fmt.Fprintf(out, "%s\t%s\t%s\t%s\n", knowledge.ID, knowledge.Kind, knowledge.State, knowledge.Text)
		}
		if q.Count && r.MatchedTotal != nil {
			fmt.Fprintf(out, "matched_total: %d\n", *r.MatchedTotal)
		}
		printTrajectoryCoverage(out, r.Coverage)
		if r.Next != "" {
			fmt.Fprintln(out, "next:", r.Next)
		}
	} else if command == "get" {
		if p.View == "attention" {
			var r snapshot.TrajectoryAttentionGetResult
			if json.Unmarshal(result, &r) != nil {
				fmt.Fprintln(errOut, "invalid attention result")
				return 1
			}
			if r.Attention != nil {
				printTrajectoryAttention(out, *r.Attention)
			}
			printTrajectoryCoverage(out, r.Coverage)
			return 0
		}
		if p.View == "context" || strings.HasPrefix(p.ID, "ctx.") {
			var r snapshot.TrajectoryContextGetResult
			if json.Unmarshal(result, &r) != nil {
				fmt.Fprintln(errOut, "invalid context manifest")
				return 1
			}
			if r.Context != nil {
				fmt.Fprintf(out, "%s\t%s\t%s\n", r.Context.ID, r.Context.Scope, r.Context.Membership)
			}
			for _, m := range r.Members {
				fmt.Fprintf(out, "%s\t%s\t%s\n", m.Status, m.Visibility, strings.Join(m.EventIDs, ","))
			}
			printTrajectoryCoverage(out, r.Coverage)
			if r.NextOffset != nil {
				fmt.Fprintln(out, "next member offset:", *r.NextOffset)
			}
			return 0
		}
		if p.View == "relations" {
			var r snapshot.TrajectoryRelationQueryResult
			if err := json.Unmarshal(result, &r); err != nil {
				fmt.Fprintln(errOut, err)
				return 1
			}
			printTrajectoryRelations(out, r)
			return 0
		}
		var r snapshot.TrajectoryGetResult
		_ = json.Unmarshal(result, &r)
		if r.Knowledge != nil {
			k := r.Knowledge
			fmt.Fprintf(out, "%s\t%s\t%s\t%s\n%s\n", k.ID, k.Kind, k.State, k.EvidenceState, k.Text)
			fmt.Fprintln(out, "scope:", k.ScopeStatus)
			if k.Applicability != nil {
				data, _ := json.Marshal(k.Applicability)
				fmt.Fprintln(out, string(data))
			}
			for _, source := range k.Sources {
				fmt.Fprintf(out, "%s\t%s\tL%d\n", source.EventID, source.Status, source.Source.Line)
			}
			for _, link := range k.Links {
				fmt.Fprintf(out, "%s\t%s\t%s\n", link.Kind, link.TargetID, link.Status)
			}
		}
		if r.Entity != nil {
			fmt.Fprintf(out, "%s\t%s\t%s\n%s\n", r.Entity.ID, r.Entity.Kind, r.Entity.Label, r.Entity.Scope)
			for _, o := range r.Entity.Occurrences {
				fmt.Fprintf(out, "%s\t%s\tL%d\n", o.Predicate, o.EventID, o.Source.Line)
			}
		}
		if r.RawChunk != nil {
			fmt.Fprintf(out, "raw bytes %d/%d (base64)\n%s\n", r.RawChunk.Offset, r.RawChunk.TotalBytes, r.RawChunk.Data)
			if r.RawChunk.NextOffset != nil {
				fmt.Fprintln(out, "next raw offset:", *r.RawChunk.NextOffset)
			}
		}
		for _, e := range r.Events {
			fmt.Fprintf(out, "%s\t%s\tL%d\n%s\n", e.ID, e.Kind, e.Source.Line, e.Text)
			if e.Tool != nil {
				fmt.Fprintln(out, e.Tool.Name, string(e.Tool.Arguments))
			}
			if len(e.Raw) > 0 {
				fmt.Fprintln(out, string(e.Raw))
			}
		}
		printTrajectoryCoverage(out, r.Coverage)
		if r.Truncated {
			fmt.Fprintln(out, "slice truncated; after:", r.After)
		}
	} else {
		fmt.Fprintln(out, string(result))
	}
	return 0
}

func printTrajectoryAttention(out io.Writer, item snapshot.TrajectoryAttention) {
	fmt.Fprintf(out, "%s\t%s\t%s\tliveness=%s\n", item.ID, item.SessionID, item.Progress.Status, item.Liveness)
	for _, i := range item.Interventions {
		fmt.Fprintf(out, "%s\t%s\t%s\n", i.Kind, i.Status, i.RequestID)
		for _, e := range i.Evidence {
			fmt.Fprintf(out, "  %s\tL%d\t%s\n", e.EventID, e.Source.Line, e.NativeField)
		}
	}
}
func printTrajectoryRelations(out io.Writer, r snapshot.TrajectoryRelationQueryResult) {
	for _, edge := range r.Relations {
		fmt.Fprintf(out, "%s\t%s\t%s\t%s\t%s\n", edge.ID, edge.From, edge.Kind, strings.Join(edge.TargetIDs, ","), edge.Status)
	}
	printTrajectoryCoverage(out, r.Coverage)
	if r.Next != "" {
		fmt.Fprintln(out, "next:", r.Next)
	}
}
func printTrajectoryCoverage(out io.Writer, c snapshot.TrajectoryCoverage) {
	if !c.Complete {
		fmt.Fprintln(out, "coverage gaps:", strings.Join(c.Gaps, ", "))
	}
}
