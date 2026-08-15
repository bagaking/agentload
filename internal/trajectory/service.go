package trajectory

import (
	"agentload/internal/historyfile"
	"agentload/internal/snapshot"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	bolt "go.etcd.io/bbolt"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

var ErrNotFound = errors.New("trajectory object not found")
var ErrStale = errors.New("source changed; query again")
var ErrInvalid = errors.New("invalid trajectory parameters")

const MaxSliceBytes = 5120
const maxRecordBytes = 1024 * 1024
const maxScanBytes = 32 * 1024 * 1024
const queryPreparationBudget = 100 * time.Millisecond

type Source struct {
	Agent, Path, NativeID string
	Decoder               Decoder
	Info                  os.FileInfo // inventory observation; point reads still re-stat
}
type SourceSet struct {
	Sources         []Source
	Coverage        snapshot.TrajectoryCoverage
	Revision        uint64
	CatalogComplete bool // directory inventory, independent of format/authorization gaps
}
type Provider func(context.Context) SourceSet
type sourceState struct {
	Source
	ID, Generation string
	Info           os.FileInfo
	owner          *Service
	checkpoint     sourceCheckpoint
}

// Service owns identity, evidence reading and query meaning; transports do not
// parse logs. Sources are supplied by the application's adapter registry.
type Service struct {
	opMu               sync.Mutex
	path               string
	temporary          bool
	store              *sourceStore
	checkpointSnapshot map[string]sourceCheckpoint
	search             *searchIndex
	annotationDB       *bolt.DB
	provider           Provider
	watch              *watchState
	prepareAfter       string
	searchAfter        string
	storageCheck       func(string) error
	checkpointsReady   bool
	storageReady       bool
	sourceMigration    *sourceMigrationRun
	capacityCheck      func(string, uint64) error
	storageOffline     bool
}

func New(provider Provider) *Service {
	s := &Service{provider: provider, watch: newWatchState(), storageCheck: historyfile.CheckStorageBudget, capacityCheck: historyfile.CheckStorageCapacity}
	return s
}

// The same operation mutex still pins authorization, source generations and
// all joined readers. Waiting for an explicit count cannot outlive another
// request's own cancellation budget.
func (s *Service) lockOperation(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.opMu.TryLock() {
		return nil
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := ctx.Err(); err != nil {
				return err
			}
			if s.opMu.TryLock() {
				return nil
			}
		}
	}
}
func digest(b []byte) string     { s := sha256.Sum256(b); return hex.EncodeToString(s[:])[:16] }
func sourceID(src Source) string { return digest([]byte(src.Agent + "\x00" + src.Path)) }
func coverage(scope string) snapshot.TrajectoryCoverage {
	return snapshot.TrajectoryCoverage{Complete: true, Scope: scope, Gaps: []string{}}
}
func gap(c *snapshot.TrajectoryCoverage, s string) {
	c.Complete = false
	for _, existing := range c.Gaps {
		if existing == s {
			return
		}
	}
	if len(c.Gaps) < 16 {
		c.Gaps = append(c.Gaps, shorten(s, 160))
	} else {
		c.Omitted++
	}
}
func mergeCoverage(a *snapshot.TrajectoryCoverage, b snapshot.TrajectoryCoverage) {
	a.Complete = a.Complete && b.Complete
	for _, g := range b.Gaps {
		gap(a, g)
	}
	a.Omitted += b.Omitted
}

func sessionID(st *sourceState) string { return "s." + st.ID + "." + st.Generation }
func eventID(st *sourceState, offset int64, block int, hash string) string {
	return fmt.Sprintf("e.%s.%s.%s.%d.%s", st.ID, st.Generation, strconv.FormatInt(offset, 36), block, hash)
}

func (s *Service) collect(ctx context.Context) ([]*sourceState, snapshot.TrajectoryCoverage) {
	set := s.provider(ctx)
	return s.collectSet(ctx, set)
}

func (s *Service) collectSet(ctx context.Context, set SourceSet) ([]*sourceState, snapshot.TrajectoryCoverage) {
	return s.collectSetBudget(ctx, set, time.Second)
}

func (s *Service) collectSetBudget(ctx context.Context, set SourceSet, prepareBudget time.Duration) ([]*sourceState, snapshot.TrajectoryCoverage) {
	cov := coverage(set.Coverage.Scope)
	mergeCoverage(&cov, set.Coverage)
	if cov.Scope == "" {
		cov.Scope = "configured local transcript roots"
	}
	if cov.Gaps == nil {
		cov.Gaps = []string{}
	}
	if err := s.openIndexContext(ctx); err != nil {
		if errors.Is(err, errStorageMigration) {
			gap(&cov, "index_storage_pending")
			gap(&cov, "index_pending")
		} else {
			gap(&cov, "index_unavailable")
		}
		s.observeLocked(nil, cov, set.Revision)
		return nil, cov
	}
	if !s.checkpointsReady || !s.storageReady {
		migrationCtx, cancel := context.WithTimeout(ctx, prepareBudget)
		err := s.migrateCheckpoints(migrationCtx)
		cancel()
		if err != nil || !s.checkpointsReady {
			gap(&cov, "index_metadata_pending")
			gap(&cov, "index_pending")
			cov.Index = &snapshot.TrajectoryIndexProgress{}
			for _, src := range set.Sources {
				if src.Decoder != nil {
					cov.Index.KnownSources++
				}
			}
			s.observeLocked(nil, cov, set.Revision)
			return nil, cov
		}
	}
	var checkpointErr error
	s.checkpointSnapshot, checkpointErr = s.store.checkpoints(ctx)
	if checkpointErr != nil {
		gap(&cov, "index_unavailable")
		s.observeLocked(nil, cov, set.Revision)
		return nil, cov
	}
	defer func() { s.checkpointSnapshot = nil }()
	states := make([]*sourceState, 0, len(set.Sources))
	cov.Index = &snapshot.TrajectoryIndexProgress{}
	prepareCtx, cancelPrepare := context.WithTimeout(ctx, prepareBudget)
	defer cancelPrepare()
	allowed := map[string]bool{}
	type identifiedSource struct {
		Source
		id string
	}
	ordered := make([]identifiedSource, len(set.Sources))
	for i, src := range set.Sources {
		ordered[i] = identifiedSource{Source: src, id: sourceID(src)}
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].id < ordered[j].id })
	start := sort.Search(len(ordered), func(i int) bool { return ordered[i].id > s.prepareAfter })
	// File identity checks are independent. The checkpoint snapshot remains
	// immutable under opMu until all readers join; writes below stay sequential.
	cached := make([]*sourceState, len(ordered))
	pending := make([]bool, len(ordered))
	var cacheReaders sync.WaitGroup
	const cacheWorkers = 8
	for worker := range cacheWorkers {
		cacheReaders.Add(1)
		go func() {
			defer cacheReaders.Done()
			for i := worker; i < len(ordered) && ctx.Err() == nil; i += cacheWorkers {
				if ordered[i].Decoder != nil {
					cached[i], pending[i] = s.cachedSource(ordered[i].Source)
				}
			}
		}()
	}
	cacheReaders.Wait()
	for _, src := range set.Sources {
		if src.Decoder != nil {
			allowed[sourceID(src)] = true
		}
	}
	for n := range ordered {
		i := (start + n) % len(ordered)
		entry := ordered[i]
		src := entry.Source
		if ctx.Err() != nil {
			gap(&cov, "collection_cancelled")
			break
		}
		if src.Decoder == nil {
			gap(&cov, "trajectory_unsupported:"+src.Agent)
			continue
		}
		cov.Index.KnownSources++
		st, needsIndex := cached[i], pending[i]
		if needsIndex {
			if prepareCtx.Err() == nil {
				updated, err := s.indexSourceBatch(prepareCtx, src, 256, 2*1024*1024)
				s.prepareAfter = entry.id
				if err != nil {
					if errors.Is(err, context.DeadlineExceeded) && prepareCtx.Err() != nil && ctx.Err() == nil {
						gap(&cov, "index_pending")
					} else {
						gap(&cov, "source_unreadable:"+sourceID(src))
						continue
					}
				} else {
					st = updated
				}
			}
		}
		if st != nil {
			states = append(states, st)
		}
	}
	// Small archives can finish in one request. Revisit pending sources in fair
	// rounds only after every source has had its first quantum; the shared time
	// budget still bounds cold preparation. The background queue owns catch-up;
	// a foreground query only contributes a short quantum. Half-lines wait for
	// new evidence.
	for prepareCtx.Err() == nil {
		advanced := false
		for i, st := range states {
			if st.checkpoint.Offset >= st.Info.Size() {
				continue
			}
			partial := false
			for _, g := range st.checkpoint.Coverage.Gaps {
				partial = partial || g == "partial_record"
			}
			if partial || prepareCtx.Err() != nil {
				continue
			}
			updated, err := s.indexSourceBatch(prepareCtx, st.Source, 256, 2*1024*1024)
			if err != nil {
				if errors.Is(err, context.DeadlineExceeded) && prepareCtx.Err() != nil && ctx.Err() == nil {
					gap(&cov, "index_pending")
				} else {
					gap(&cov, "source_unreadable:"+st.ID)
				}
				continue
			}
			advanced = advanced || updated.checkpoint.Offset > st.checkpoint.Offset
			states[i] = updated
			s.prepareAfter = st.ID
		}
		if !advanced {
			break
		}
	}
	for _, st := range states {
		cov.Index.DecodedEvents += st.checkpoint.EventCount
		if st.checkpoint.Offset == st.Info.Size() {
			cov.Index.DecodedSources++
		} else {
			gap(&cov, "index_pending")
		}
	}
	if len(states) < cov.Index.KnownSources {
		gap(&cov, "index_pending")
	}
	// An incomplete discovery is not evidence that an absent source was
	// deleted. It stays inaccessible in this response, but its physical identity
	// must survive until a complete catalog confirms removal.
	if set.CatalogComplete || set.Coverage.Complete {
		if err := s.pruneSources(allowed); err != nil {
			gap(&cov, "index_prune_failed")
		}
	}
	sort.Slice(states, func(i, j int) bool {
		left, right := states[i].Info.ModTime(), states[j].Info.ModTime()
		if left.Equal(right) {
			return states[i].ID < states[j].ID
		}
		return left.After(right)
	})
	// Attention, context, relations and Watch consume canonical coverage.
	// Auxiliary full-text preparation is a separate readiness boundary.
	s.observeLocked(states, cov, set.Revision)
	if prepareBudget == 0 {
		// Background catalog maintenance does not decode records; it owns one
		// bounded cleanup/backfill batch after checking the current whole scope.
		if err := s.syncSearchScope(ctx, states, &cov, true, 1); err != nil {
			if errors.Is(err, errStorageMigration) {
				gap(&cov, "index_storage_pending")
			} else {
				gap(&cov, "search_index_unavailable")
			}
		}
	}
	return states, cov
}

func nativeType(m, payload map[string]json.RawMessage) string {
	if method := field(m, "method"); method != "" {
		return method + ":" + field(object(object(m["params"])["update"]), "sessionUpdate")
	}
	return field(m, "type") + ":" + field(payload, "type")
}

func boundedUsage(u *snapshot.TrajectoryUsage, omissions *[]string) *snapshot.TrajectoryUsage {
	if u == nil {
		return nil
	}
	b, _ := json.Marshal(u)
	if len(b) > 1024 {
		copy := *u
		copy.Models = nil
		u = &copy
		*omissions = append(*omissions, "usage_models_omitted")
	}
	b, _ = json.Marshal(u)
	if len(b) > 1024 {
		*omissions = append(*omissions, "usage_omitted_for_size")
		return nil
	}
	return u
}
func matches(e snapshot.TrajectoryEvent, q snapshot.TrajectorySelector) bool {
	if q.Kind != "" && e.Kind != q.Kind {
		return false
	}
	if q.Role != "" && e.Role != q.Role {
		return false
	}
	if q.ActorID != "" && e.Actor.ID != q.ActorID {
		return false
	}
	if q.ActorKind != "" && q.ActorKind != e.Actor.Kind {
		return false
	}
	if q.Tool != "" && (e.Tool == nil || !strings.EqualFold(e.Tool.Name, q.Tool)) {
		return false
	}
	text := strings.ToLower(e.Text)
	if e.Tool != nil {
		text += " " + strings.ToLower(e.Tool.Name+" "+string(e.Tool.Arguments))
	}
	for _, word := range strings.Fields(strings.ToLower(q.Text)) {
		if !strings.Contains(text, word) {
			return false
		}
	}
	return MatchEntitySelector(e, q)
}

func ValidateSelector(q *snapshot.TrajectorySelector) error {
	if q.Collection == "" {
		q.Collection = "sessions"
	}
	if q.Collection != "sessions" && q.Collection != "events" {
		return fmt.Errorf("%w: unsupported collection", ErrInvalid)
	}
	if q.Limit == 0 {
		q.Limit = 20
	}
	if q.Limit < 1 || q.Limit > 50 || len(q.Text) > 1024 {
		return ErrInvalid
	}
	if q.State != "" || q.RelationKind != "" || (q.ContextScope != "" && (q.ContextID == "" || q.ContextScope != "actual_input")) {
		return fmt.Errorf("%w: selector not available", ErrInvalid)
	}
	if len(q.Agent) > 80 || len(q.SessionID) > 256 || len(q.ActorID) > 256 || len(q.Tool) > 256 || len(q.Kind) > 80 {
		return ErrInvalid
	}
	if q.Role != "" && q.Role != "user" && q.Role != "assistant" && q.Role != "tool" && q.Role != "system" && q.Role != "unknown" {
		return ErrInvalid
	}
	if q.ActorKind != "" && q.ActorKind != "unknown" && q.ActorKind != "human" && q.ActorKind != "agent" && q.ActorKind != "tool" && q.ActorKind != "system" {
		return ErrInvalid
	}
	return ValidateEntitySelector(*q)
}

func (s *Service) Query(ctx context.Context, q snapshot.TrajectorySelector) (snapshot.TrajectoryQueryResult, error) {
	if q.Count && (q.ContextID != "" || (q.Collection != "" && q.Collection != "sessions" && q.Collection != "events")) {
		return snapshot.TrajectoryQueryResult{}, ErrInvalid
	}
	if q.Collection == "entities" {
		return s.QueryEntities(ctx, q)
	}
	if q.Collection == "knowledge" {
		return s.QueryKnowledge(ctx, q)
	}
	if err := s.lockOperation(ctx); err != nil {
		return snapshot.TrajectoryQueryResult{}, err
	}
	defer s.opMu.Unlock()
	out := snapshot.TrajectoryQueryResult{Sessions: []snapshot.TrajectorySession{}, Events: []snapshot.TrajectoryEvent{}}
	if err := ValidateSelector(&q); err != nil {
		return out, err
	}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	if err := s.storageCheck(s.path); err != nil {
		gap(&out.Coverage, "storage_low")
		return out, err
	}
	states, cov := s.collectSetBudget(ctx, s.provider(ctx), queryPreparationBudget)
	if err := ctx.Err(); err != nil {
		return out, err
	}
	if err := s.storageCheck(s.path); err != nil {
		gap(&out.Coverage, "storage_low")
		return out, err
	}
	out.Coverage = cov
	if !s.checkpointsReady || !s.storageReady {
		return out, nil
	}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	if q.ContextID == "" {
		if q.Collection == "sessions" && plainCatalogSelector(q) {
			return s.querySessionCatalog(ctx, q, states, cov)
		}
		if err := s.syncSearch(ctx, states, &cov); err != nil {
			if errors.Is(err, errStorageMigration) {
				gap(&out.Coverage, "index_storage_pending")
				return out, nil
			}
			gap(&out.Coverage, "search_index_unavailable")
			return out, err
		}
		return s.querySearch(ctx, q, states, cov)
	}
	var contextMembers map[string]bool
	if q.ContextID != "" {
		ids, local, err := s.contextMembersLocked(ctx, states, q.ContextID, cov)
		if err != nil {
			return out, err
		}
		mergeCoverage(&out.Coverage, local)
		contextMembers = make(map[string]bool, len(ids))
		for _, id := range ids {
			contextMembers[id] = true
		}
	}
	out.Revision = sourceRevision(states)
	out.WatchCursor = s.watchCursorLocked(q)
	pagination := q
	pagination.Cursor, pagination.Limit = "", 0
	selectorBytes, _ := json.Marshal(pagination)
	cursorPrefix := "query." + out.Revision + "." + digest(selectorBytes)
	start := 0
	if q.Cursor != "" {
		parts := strings.Split(q.Cursor, ":")
		if len(parts) != 2 || parts[0] != cursorPrefix {
			return out, ErrStale
		}
		n, err := strconv.Atoi(parts[1])
		if err != nil || n < 0 {
			return out, ErrInvalid
		}
		start = n
	}
	matched := 0
	for _, st := range states {
		if q.Agent != "" && q.Agent != st.Agent {
			continue
		}
		if q.SessionID != "" && q.SessionID != sessionID(st) {
			continue
		}
		summary := snapshot.TrajectorySession{ID: sessionID(st), NativeID: st.NativeID, Agent: st.Agent, MatchedIDs: []string{}, Tools: []string{}}

		tools := map[string]bool{}
		local, err := scan(ctx, st, func(e snapshot.TrajectoryEvent) bool {
			if e.EntityCoverage != nil && (q.Skill != "" || q.EntityKind != "" || q.EntityID != "" || q.Predicate != "") {
				mergeCoverage(&out.Coverage, *e.EntityCoverage)
			}
			summary.EventCount++
			if e.Timestamp != nil {
				summary.LastEvent = e.Timestamp
			}
			if e.Kind == "session" && e.NativeID != "" {
				summary.NativeID = e.NativeID
			}
			if e.Role == "user" && e.Text != "" && summary.Title == "" {
				summary.Title = shorten(e.Text, 160)
			}
			if e.Tool != nil && e.Tool.Name != "" {
				if len(e.Tool.Name) <= 256 && len(tools) < 16 {
					tools[e.Tool.Name] = true
				} else {
					gap(&out.Coverage, "session_tool_preview_limit")
				}
				if e.Kind == "tool_call" {
					summary.LastAction = shorten(e.Tool.Name, 160)
				}
			}
			if matches(e, q) && (contextMembers == nil || contextMembers[e.ID]) {
				summary.MatchedCount++
				if summary.MatchedCount == 1 {
					summary.MatchedPreview = matchPreview(e, q)
				}
				if len(summary.MatchedIDs) < 50 {
					summary.MatchedIDs = append(summary.MatchedIDs, e.ID)
				}
				if q.Collection == "events" {
					if matched >= start && len(out.Events) < q.Limit {
						e = boundedEventPreview(e)
						e.Text = shorten(e.Text, 512)
						if e.Tool != nil && len(e.Tool.Arguments) > 512 {
							e.Tool.Arguments = nil
							e.Omissions = append(e.Omissions, "arguments_omitted")
						}
						e.Usage = boundedUsage(e.Usage, &e.Omissions)
						out.Events = append(out.Events, e)
					}
					matched++
				}
			}
			return true
		})
		if err != nil {
			return snapshot.TrajectoryQueryResult{}, err
		}
		summary.Coverage = local
		mergeCoverage(&out.Coverage, local)
		for name := range tools {
			summary.Tools = append(summary.Tools, name)
		}
		sort.Strings(summary.Tools)
		if len(summary.NativeID) > 256 {
			summary.NativeID = ""
			gap(&summary.Coverage, "native_id_omitted_for_size")
		}
		if summary.MatchedCount > len(summary.MatchedIDs) {
			gap(&summary.Coverage, "matched_reference_limit")
		}
		if summary.Title == "" {
			summary.Title = st.Agent + " · " + summary.NativeID
		}
		if len(summary.MatchedIDs) == 0 {
			continue
		}
		if q.Collection == "sessions" {
			if matched >= start && len(out.Sessions) < q.Limit {
				out.Sessions = append(out.Sessions, summary)
			}
			matched++
		}
	}
	err := boundQueryPage(&out, cursorPrefix, start, matched)
	return out, err
}

func shorten(text string, n int) string {
	if len(text) <= n {
		return text
	}
	b := []byte(text[:n])
	for !utf8.Valid(b) {
		b = b[:len(b)-1]
	}
	return string(b) + "…"
}

// Search results show the recorded match before the user opens its context.
// Whitespace folding and excerpting are presentation only.
func matchPreview(e snapshot.TrajectoryEvent, q snapshot.TrajectorySelector) string {
	text := e.Text
	if e.Tool != nil {
		text += " " + e.Tool.Name + " " + string(e.Tool.Arguments)
	}
	text = strings.Join(strings.Fields(text), " ")
	needle := strings.TrimSpace(q.Text)
	if needle == "" {
		needle = q.Skill
	}
	if words := strings.Fields(needle); len(words) > 0 {
		if at := strings.Index(strings.ToLower(text), strings.ToLower(words[0])); at > 80 {
			start := at - 80
			for start > 0 && !utf8.RuneStart(text[start]) {
				start--
			}
			text = "… " + text[start:]
		}
	}
	return shorten(text, 320)
}

func (s *Service) Get(ctx context.Context, p snapshot.TrajectoryGetParams) (snapshot.TrajectoryGetResult, error) {
	if strings.HasPrefix(p.ID, "ent.") {
		return s.GetEntity(ctx, p)
	}
	if strings.HasPrefix(p.ID, "k.") {
		return s.GetKnowledge(ctx, p)
	}
	if err := s.lockOperation(ctx); err != nil {
		return snapshot.TrajectoryGetResult{}, err
	}
	defer s.opMu.Unlock()
	out := snapshot.TrajectoryGetResult{FocusID: p.ID, Events: []snapshot.TrajectoryEvent{}, ByteLimit: MaxSliceBytes}
	if p.MemberOffset != 0 || p.Around < 0 || p.Around > 5 || p.MaxBytes < 0 || p.MaxBytes > MaxSliceBytes || p.RawOffset < 0 || (p.View != "" && p.View != "slice" && p.View != "details" && p.View != "raw") {
		return out, ErrInvalid
	}
	if p.MaxBytes > 0 {
		out.ByteLimit = p.MaxBytes
	}
	if out.ByteLimit < 1024 {
		return out, ErrInvalid
	}
	parts := strings.Split(p.ID, ".")
	if (parts[0] == "s" && len(parts) != 3) || (parts[0] == "e" && len(parts) != 6) || (len(parts) != 3 && len(parts) != 6) {
		return out, ErrInvalid
	}
	if parts[0] != "s" && parts[0] != "e" {
		return out, ErrInvalid
	}
	if (p.View == "raw" && parts[0] != "e") || (p.View != "raw" && p.RawOffset != 0) {
		return out, ErrInvalid
	}
	st, cov, readErr := s.collectSource(ctx, parts[1])
	out.Coverage = cov
	if readErr != nil {
		return out, readErr
	}
	if st == nil {
		return out, ErrNotFound
	}
	if st.Generation != parts[2] {
		return out, ErrStale
	}
	var offset int64
	var block int
	if parts[0] == "e" {
		if len(parts) != 6 {
			return out, ErrInvalid
		}
		var err error
		offset, err = strconv.ParseInt(parts[3], 36, 64)
		if err != nil || offset < 0 {
			return out, ErrInvalid
		}
		block, err = strconv.Atoi(parts[4])
		if err != nil || block < 0 {
			return out, ErrInvalid
		}
	}
	windowParams := p
	if p.View == "raw" {
		windowParams.Around = 0
	}
	window, before, after, err := s.indexedWindow(ctx, st, windowParams, offset, block)
	if err != nil {
		return out, err
	}
	out.Before = before
	out.After = after
	mergeCoverage(&out.Coverage, st.checkpoint.Coverage)
	if parts[0] == "s" {
		out.Coverage.Omitted += max(0, st.checkpoint.EventCount-len(window))
	}
	if len(window) == 0 {
		return out, ErrStale
	}
	if p.View == "raw" {
		e := boundedEventPreview(window[0])
		body, err := readRecord(st, e.Source)
		if err != nil {
			return out, err
		}
		if p.RawOffset > len(body) {
			return out, ErrInvalid
		}
		e.Text = ""
		e.Raw = ""
		e.Usage = nil
		if e.Tool != nil {
			e.Tool.Arguments = nil
		}
		e.Omissions = append(e.Omissions, "raw_byte_view")
		out.Events = []snapshot.TrajectoryEvent{e}
		out.Before = ""
		out.After = ""
		length := min(2048, len(body)-p.RawOffset)
		for {
			out.RawChunk = &snapshot.TrajectoryRawChunk{Encoding: "base64", Data: base64.StdEncoding.EncodeToString(body[p.RawOffset : p.RawOffset+length]), Offset: p.RawOffset, TotalBytes: len(body)}
			if next := p.RawOffset + length; next < len(body) {
				out.RawChunk.NextOffset = &next
				out.Truncated = true
			} else {
				out.Truncated = false
			}
			encoded, _ := json.Marshal(out)
			if length == 0 && p.RawOffset < len(body) {
				return out, fmt.Errorf("%w: budget cannot include raw bytes", ErrInvalid)
			}
			if len(encoded) <= out.ByteLimit {
				return out, nil
			}
			if length == 0 {
				return out, fmt.Errorf("%w: budget cannot include provenance", ErrInvalid)
			}
			length /= 2
		}
	}
	focus := len(window) - 1
	for i, e := range window {
		if e.ID == p.ID {
			focus = i
			break
		}
	}
	prepared := make([]snapshot.TrajectoryEvent, len(window))
	for i, e := range window {
		e = boundedEventPreview(e)
		if e.Tool != nil && (e.Kind == "tool_call" || e.Kind == "tool_result") && e.PairID == "" {
			e.Omissions = append(e.Omissions, "pair_not_observed")
		}
		if len(e.Text) > 480 {
			e.Text = shorten(e.Text, 480)
			e.Omissions = append(e.Omissions, "text_truncated")
		}
		if e.Tool != nil && len(e.Tool.Arguments) > 480 {
			e.Tool.Arguments = nil
			e.Omissions = append(e.Omissions, "arguments_omitted")
		}
		if p.Raw {
			body, err := readRecord(st, e.Source)
			if err != nil {
				return out, err
			}
			if len(body) <= 1200 {
				e.Raw = string(body)
			} else {
				e.Omissions = append(e.Omissions, "raw_record_size_limit")
			}
		}
		e.Usage = boundedUsage(e.Usage, &e.Omissions)
		prepared[i] = e
	}
	left, right := focus, focus
	out.Events = []snapshot.TrajectoryEvent{prepared[focus]}
	encoded, _ := json.Marshal(out)
	if len(encoded) > out.ByteLimit-200 {
		out.Events[0].Raw = ""
		if out.Events[0].Usage != nil {
			out.Events[0].Usage.Models = nil
			out.Events[0].Omissions = append(out.Events[0].Omissions, "usage_models_omitted_for_budget")
		}
		out.Events[0].Text = shorten(out.Events[0].Text, 120)
		if out.Events[0].Tool != nil {
			out.Events[0].Tool.Arguments = nil
		}
		out.Events[0].Omissions = append(out.Events[0].Omissions, "content_omitted_for_budget")
	}
	encoded, _ = json.Marshal(out)
	if len(encoded) > out.ByteLimit-200 {
		return out, fmt.Errorf("%w: budget cannot include provenance", ErrInvalid)
	}
	leftBlocked, rightBlocked := false, false
	for distance := 1; distance < len(window); distance++ {
		for _, index := range []int{focus - distance, focus + distance} {
			if index < 0 || index >= len(window) || (index < focus && leftBlocked) || (index > focus && rightBlocked) {
				continue
			}
			previous := out.Events
			if index < focus {
				out.Events = append([]snapshot.TrajectoryEvent{prepared[index]}, out.Events...)
			} else {
				out.Events = append(append([]snapshot.TrajectoryEvent{}, out.Events...), prepared[index])
			}
			encoded, _ = json.Marshal(out)
			if len(encoded) > out.ByteLimit-200 {
				out.Events = previous
				if index < focus {
					leftBlocked = true
				} else {
					rightBlocked = true
				}
				continue
			}
			if index < focus {
				left = index
			} else {
				right = index
			}
		}
	}
	if left > 0 {
		out.Before = window[left-1].ID
	}
	if right < len(window)-1 {
		out.After = window[right+1].ID
	}
	if len(out.Events) < len(window) {
		out.Truncated = true
		out.Coverage.Omitted += len(window) - len(out.Events)
	}
	if out.Before != "" || out.After != "" {
		out.Truncated = true
	}
	encoded, _ = json.Marshal(out)
	if len(encoded) > out.ByteLimit {
		return out, fmt.Errorf("%w: budget cannot include provenance", ErrInvalid)
	}

	return out, nil
}

// Entity descriptors have their own bounded view. Ordinary slices retain the
// event locator and expose the omission rather than multiplying body size.
func omitEntities(e snapshot.TrajectoryEvent) snapshot.TrajectoryEvent {
	if len(e.Entities) > 0 {
		e.Entities = nil
		e.Omissions = append(e.Omissions, "entities_in_entity_view")
	}
	return e
}

func readRecord(st *sourceState, ref snapshot.TrajectorySourceRef) ([]byte, error) {
	f, err := os.Open(st.Path)
	if err != nil {
		return nil, ErrStale
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !os.SameFile(st.Info, info) {
		return nil, ErrStale
	}
	buf := make([]byte, ref.Length)
	n, err := f.ReadAt(buf, ref.Offset)
	if err != nil || n != len(buf) || digest(buf) != ref.Digest {
		return nil, ErrStale
	}
	return buf, nil
}
