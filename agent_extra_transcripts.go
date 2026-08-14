package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Layouts are relative to configured homes; credentials, caches and unrelated
// application stores are never traversed.
type extraTranscriptDiscovery struct{ kind string }

func extraEvidenceRelative(kind, rel string) bool {
	p := strings.Split(filepath.ToSlash(rel), "/")
	base := p[len(p)-1]
	switch kind {
	case "gemini":
		return len(p) == 4 && p[0] == "tmp" && p[2] == "chats" && strings.HasPrefix(base, "session-") && (strings.HasSuffix(base, ".json") || strings.HasSuffix(base, ".jsonl"))
	case "opencode":
		if len(p) == 1 && strings.HasPrefix(base, "opencode") && strings.HasSuffix(base, ".db") {
			return true
		}
		return len(p) >= 4 && p[0] == "storage" && p[1] == "message" && strings.HasSuffix(base, ".json")
	case "hermes":
		return rel == "state.db"
	case "openclaw":
		return len(p) == 4 && p[0] == "agents" && p[2] == "sessions" && strings.HasSuffix(base, ".jsonl") && !strings.Contains(base, ".trajectory.") && !strings.Contains(base, ".checkpoint.")
	case "pi":
		return len(p) >= 3 && p[0] == "agent" && (p[1] == "sessions" || p[1] == "session-artifacts") && strings.HasSuffix(base, ".jsonl")
	}
	return false
}

func extraEvidenceDirectory(kind, rel string) bool {
	p := strings.Split(filepath.ToSlash(rel), "/")
	switch kind {
	case "gemini":
		return len(p) <= 3 && p[0] == "tmp" && (len(p) < 3 || p[2] == "chats")
	case "openclaw":
		return len(p) <= 3 && p[0] == "agents" && (len(p) < 3 || p[2] == "sessions")
	case "opencode":
		return len(p) <= 3 && p[0] == "storage" && (len(p) < 2 || p[1] == "message")
	case "pi":
		return p[0] == "agent" && (len(p) < 2 || p[1] == "sessions" || p[1] == "session-artifacts")
	}
	return false
}

func (d extraTranscriptDiscovery) Discover(ctx context.Context, id string, roots []string, cutoff time.Time) transcriptDiscoveryResult {
	if ctx == nil {
		ctx = context.Background()
	}
	var out transcriptDiscoveryResult
	for _, root := range roots {
		// Database mtimes alone do not reflect committed WAL writes. Apply the
		// cutoff to combined database/WAL metadata after the bounded walk.
		horizon := cutoff
		if d.kind == "hermes" || d.kind == "opencode" {
			horizon = time.Time{}
		}
		result := walkEvidenceTree(ctx, root, id, horizon, func(rel string, _ fs.DirEntry) directoryDecision {
			if extraEvidenceDirectory(d.kind, rel) {
				return descendDirectory
			}
			return pruneDirectory
		}, func(path string, _ fs.DirEntry) bool {
			rel, err := filepath.Rel(root, path)
			return err == nil && extraEvidenceRelative(d.kind, rel)
		})
		files := result.Files[:0]
		for _, file := range result.Files {
			if isAgentDatabase(file.File) {
				info, err := agentEvidenceStat(file.File)
				if err != nil {
					result.Errors = append(result.Errors, err.Error())
					continue
				}
				file.Info = info
				if !cutoff.IsZero() && info.ModTime().Before(cutoff) {
					result.AgedOutFiles++
					continue
				}
			}
			files = append(files, file)
		}
		result.Files = files
		out.merge(result)
	}
	return out
}

func (d extraTranscriptDiscovery) Classify(id string, roots []string, path string) (TranscriptFile, bool) {
	if d.kind == "hermes" || d.kind == "opencode" {
		path = strings.TrimSuffix(path, "-wal")
	}
	for _, root := range roots {
		if rel, ok := relativeEvidencePath(root, path); ok && extraEvidenceRelative(d.kind, rel) {
			return TranscriptFile{Tool: id, Path: canonicalEvidencePath(path)}, true
		}
	}
	return TranscriptFile{}, false
}

// Process evidence must have a known vendor path. In particular, opening a
// shared database is not proof of owning any particular session inside it.
func isGeminiTranscriptPath(path string) bool {
	rel, ok := relativeAfterMarker(path, []string{".gemini"})
	return ok && extraEvidenceRelative("gemini", rel)
}
func isOpenClawTranscriptPath(path string) bool {
	rel, ok := relativeAfterMarker(path, []string{".openclaw"})
	return ok && extraEvidenceRelative("openclaw", rel)
}
func isPiTranscriptPath(path string) bool {
	rel, ok := relativeAfterMarker(path, []string{".pi"})
	return ok && extraEvidenceRelative("pi", rel)
}

// Shared SQLite stores are discovered from configured roots; an lsof hit alone
// cannot prove that a visible process owns a particular session.
func isOpenCodeEvidencePath(path string) bool {
	base := strings.ToLower(filepath.Base(path))
	return strings.HasPrefix(base, "opencode") && strings.HasSuffix(base, ".db")
}

func isHermesEvidencePath(path string) bool {
	return strings.EqualFold(filepath.Base(path), "state.db")
}

func isAgentDatabase(file TranscriptFile) bool {
	return (file.Tool == "hermes" || file.Tool == "opencode") && strings.HasSuffix(file.Path, ".db")
}

type databaseEvidenceInfo struct {
	os.FileInfo
	modified time.Time
	size     int64
}

func (i databaseEvidenceInfo) ModTime() time.Time { return i.modified }
func (i databaseEvidenceInfo) Size() int64        { return i.size }
func agentEvidenceStat(file TranscriptFile) (os.FileInfo, error) {
	info, err := os.Stat(file.Path)
	if err != nil || !isAgentDatabase(file) {
		return info, err
	}
	combined := databaseEvidenceInfo{info, info.ModTime(), info.Size()}
	wal, err := os.Stat(file.Path + "-wal")
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if err == nil {
		combined.size += wal.Size()
		if wal.ModTime().After(combined.modified) {
			combined.modified = wal.ModTime()
		}
	}
	return combined, nil
}

type extraTranscriptParser struct{ kind string }

// Compatibility helpers kept for focused parser fixtures; the registry uses
// ParseSessions so shared databases still emit every session.
func newExtraOutputUsageDecoder() agentOutputUsageDecoder {
	return extraOutputUsageDecoder{kind: "gemini"}
}

func parseExtraTrace(kind string, file TranscriptFile, _ int64, _ *SessionTrace) (*SessionTrace, error) {
	return parseExtraTraceContext(context.Background(), kind, file)
}

func (p extraTranscriptParser) ParseSessions(ctx context.Context, file TranscriptFile) ([]*SessionTrace, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if isAgentDatabase(file) {
		if file.Tool == "hermes" {
			return parseHermesDatabaseSessions(ctx, file.Path)
		}
		return parseOpenCodeDatabaseSessions(ctx, file.Path)
	}
	trace, err := parseExtraTraceContext(ctx, p.kind, file)
	if trace == nil {
		return nil, err
	}
	return []*SessionTrace{trace}, err
}

func parseHermesStateDB(path string) (*SessionTrace, error) {
	traces, err := parseHermesDatabaseSessions(context.Background(), path)
	if len(traces) == 0 {
		return nil, err
	}
	return traces[0], err
}

func parseOpenCodeDBTrace(path string) (*SessionTrace, error) {
	traces, err := parseOpenCodeDatabaseSessions(context.Background(), path)
	if len(traces) == 0 {
		return nil, err
	}
	sort.SliceStable(traces, func(i, j int) bool { return traces[i].LastEvent.After(traces[j].LastEvent) })
	return traces[0], err
}

func parseHermesDatabaseSessions(ctx context.Context, path string) ([]*SessionTrace, error) {
	columns, err := sqliteTableColumns(ctx, path, "sessions")
	if err != nil {
		return nil, err
	}
	selectColumns := []string{"id", "started_at", "input_tokens", "output_tokens", "cache_read_tokens", "cache_write_tokens", "reasoning_tokens"}
	if columns["source"] {
		selectColumns = append(selectColumns, "source")
	}
	if columns["model"] {
		selectColumns = append(selectColumns, "model")
	}
	if columns["ended_at"] {
		selectColumns = append(selectColumns, "ended_at")
	}
	if columns["parent_session_id"] {
		selectColumns = append(selectColumns, "parent_session_id")
	}
	query := fmt.Sprintf("SELECT %s FROM sessions WHERE started_at > 0 ORDER BY started_at DESC", strings.Join(selectColumns, ", "))
	rows, err := readSQLiteRows(ctx, path, query)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	byID := make(map[string]*SessionTrace, len(rows))
	for _, row := range rows {
		byID[row["id"]] = hermesTraceFromRow(path, row)
	}
	events, err := readSQLiteRows(ctx, path, `SELECT session_id, timestamp FROM messages WHERE role IN ('user', 'assistant', 'tool') AND timestamp > 0 ORDER BY timestamp`)
	if err != nil && !strings.Contains(strings.ToLower(err.Error()), "no such table") {
		return nil, err
	}
	for _, event := range events {
		if trace := byID[event["session_id"]]; trace != nil {
			if at := extraTimeValue(event["timestamp"]); !at.IsZero() {
				trace.EventTimes = append(trace.EventTimes, at)
			}
		}
	}
	// Hermes resumed sessions split usage by model in this table. Prefer that
	// attribution over the session aggregate when available; otherwise a
	// resume can pin all tokens to the original session start and hide the day
	// and model where the work actually happened.
	if usageColumns, usageErr := sqliteTableColumns(ctx, path, "session_model_usage"); usageErr == nil && len(usageColumns) > 0 {
		usageRows, queryErr := readSQLiteRows(ctx, path, `SELECT session_id, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, reasoning_tokens, first_seen, last_seen FROM session_model_usage`)
		if queryErr == nil {
			usageBySession := map[string]TokenUsage{}
			for _, row := range usageRows {
				sid := row["session_id"]
				if byID[sid] == nil {
					continue
				}
				u := usageBySession[sid]
				u.InputTokens += extraIntFromString(row["input_tokens"])
				u.OutputTokens += extraIntFromString(row["output_tokens"])
				u.CacheReadInputTokens += extraIntFromString(row["cache_read_tokens"])
				u.CacheCreationInputTokens += extraIntFromString(row["cache_write_tokens"])
				u.ReasoningOutputTokens += extraIntFromString(row["reasoning_tokens"])
				usageBySession[sid] = u
				trace := byID[sid]
				for _, key := range []string{"first_seen", "last_seen"} {
					if at := extraTimeValue(row[key]); !at.IsZero() {
						trace.EventTimes = append(trace.EventTimes, at)
					}
				}
			}
			for sid, usage := range usageBySession {
				usage.ReasoningOutputTokens = min(usage.ReasoningOutputTokens, usage.OutputTokens)
				usage.OutputTokens -= usage.ReasoningOutputTokens
				usage.TotalTokens = usage.DerivedTotal() + usage.ReasoningOutputTokens
				byID[sid].TokenUsage = usage
			}
		}
	}
	return finalizedExtraTraces(byID), nil
}

func finalizedExtraTraces(byID map[string]*SessionTrace) []*SessionTrace {
	traces := make([]*SessionTrace, 0, len(byID))
	for _, trace := range byID {
		finalizeTrace(trace)
		if nonEmptyTrace(trace) != nil {
			traces = append(traces, trace)
		}
	}
	sort.Slice(traces, func(i, j int) bool { return traces[i].SessionID < traces[j].SessionID })
	return traces
}

func hermesTraceFromRow(path string, row map[string]string) *SessionTrace {
	trace := &SessionTrace{Tool: "hermes", Path: path, SessionID: row["id"], IndependentlyRun: row["parent_session_id"] == ""}
	if parent := row["parent_session_id"]; parent != "" {
		trace.ParentThreadID, trace.ThreadSource, trace.RoleHintSource, trace.IndependentlyRun = parent, "subagent", "parent_session_id", false
	}
	if start := extraTimeValue(row["started_at"]); !start.IsZero() {
		trace.EventTimes = append(trace.EventTimes, start)
	}
	trace.TokenUsage = TokenUsage{InputTokens: extraIntFromString(row["input_tokens"]), OutputTokens: extraIntFromString(row["output_tokens"]), CacheReadInputTokens: extraIntFromString(row["cache_read_tokens"]), CacheCreationInputTokens: extraIntFromString(row["cache_write_tokens"]), ReasoningOutputTokens: extraIntFromString(row["reasoning_tokens"])}
	trace.TokenUsage.ReasoningOutputTokens = min(trace.TokenUsage.ReasoningOutputTokens, trace.TokenUsage.OutputTokens)
	trace.TokenUsage.OutputTokens -= trace.TokenUsage.ReasoningOutputTokens
	trace.TokenUsage.TotalTokens = trace.TokenUsage.DerivedTotal() + trace.TokenUsage.ReasoningOutputTokens
	finalizeTrace(trace)
	return trace
}

func sqliteTableColumns(ctx context.Context, path, table string) (map[string]bool, error) {
	rows, err := readSQLiteRows(ctx, path, fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return nil, err
	}
	columns := make(map[string]bool, len(rows))
	for _, row := range rows {
		if name := strings.TrimSpace(row["name"]); name != "" {
			columns[name] = true
		}
	}
	return columns, nil
}

func extraIntFromString(value string) int {
	var n int
	_, _ = fmt.Sscan(value, &n)
	if n < 0 {
		return 0
	}
	return n
}

// Current OpenCode uses one shared database; message tokens take precedence
// over step-finish part tokens, which take precedence over session aggregates.
func parseOpenCodeDatabaseSessions(ctx context.Context, path string) ([]*SessionTrace, error) {
	rows, err := readSQLiteRows(ctx, path, `SELECT id, session_id, time_created, data FROM message ORDER BY time_created, id`)
	if err != nil {
		return nil, err
	}
	byID := map[string]*SessionTrace{}
	messageHasUsage := map[string]bool{}
	sessionHasUsage := map[string]bool{}
	messageSessions := map[string]string{}
	for _, row := range rows {
		var m localMessage
		if err := json.Unmarshal([]byte(row["data"]), &m); err != nil {
			return nil, fmt.Errorf("invalid OpenCode message metadata: %w", err)
		}
		if m.Role != "user" && m.Role != "assistant" {
			continue
		}
		sid := row["session_id"]
		at := firstNonZeroTime(extraTimeValue(m.Time.Created), extraTimeValue(row["time_created"]))
		if sid == "" || at.IsZero() {
			continue
		}
		trace := byID[sid]
		if trace == nil {
			trace = &SessionTrace{Tool: "opencode", Path: path, SessionID: sid}
			byID[sid] = trace
		}
		trace.EventTimes = append(trace.EventTimes, at)
		if cwd := firstNonEmptyString(m.Path.CWD, m.Path.Root); cwd != "" {
			setTraceProjectPath(trace, cwd, "transcript_cwd")
		}
		messageSessions[row["id"]] = sid
		if m.Role == "assistant" {
			u := messageUsage("opencode", m)
			trace.TokenUsage.Add(u)
			if !u.Empty() {
				messageHasUsage[row["id"]] = true
				sessionHasUsage[sid] = true
			}
		}
	}
	partColumns, err := sqliteTableColumns(ctx, path, "part")
	if err != nil {
		return nil, err
	}
	if len(partColumns) > 0 {
		parts, err := readSQLiteRows(ctx, path, `SELECT message_id, data FROM part ORDER BY id`)
		if err != nil {
			return nil, err
		}
		for _, row := range parts {
			mid := row["message_id"]
			if messageHasUsage[mid] {
				continue
			}
			trace := byID[messageSessions[mid]]
			if trace == nil {
				continue
			}
			var part localMessage
			if err := json.Unmarshal([]byte(row["data"]), &part); err != nil {
				return nil, fmt.Errorf("invalid OpenCode part metadata: %w", err)
			}
			if part.Type != "step-finish" {
				continue
			}
			u := messageUsage("opencode", part)
			trace.TokenUsage.Add(u)
			if !u.Empty() {
				sessionHasUsage[trace.SessionID] = true
			}
		}
	}
	columns, err := sqliteTableColumns(ctx, path, "session")
	if err != nil {
		return nil, err
	}
	if !columns["id"] {
		return nil, errors.New("unsupported OpenCode session schema")
	}
	selectColumns := []string{"id"}
	for _, name := range []string{"directory", "path", "parent_id", "tokens_input", "tokens_output", "tokens_reasoning", "tokens_cache_read", "tokens_cache_write"} {
		if columns[name] {
			selectColumns = append(selectColumns, name)
		}
	}
	sessions, err := readSQLiteRows(ctx, path, "SELECT "+strings.Join(selectColumns, ", ")+" FROM session")
	if err != nil {
		return nil, err
	}
	for _, row := range sessions {
		trace := byID[row["id"]]
		if trace == nil {
			continue
		}
		if trace.Project == "" {
			if cwd := firstNonEmptyString(row["directory"], row["path"]); cwd != "" {
				setTraceProjectPath(trace, cwd, "session_directory")
			}
		}
		if parent := row["parent_id"]; parent != "" {
			trace.ParentThreadID, trace.ThreadSource, trace.RoleHintSource = parent, "subagent", "parent_id"
		}
		if !sessionHasUsage[trace.SessionID] {
			u := TokenUsage{InputTokens: extraIntFromString(row["tokens_input"]), OutputTokens: extraIntFromString(row["tokens_output"]), ReasoningOutputTokens: extraIntFromString(row["tokens_reasoning"]), CacheReadInputTokens: extraIntFromString(row["tokens_cache_read"]), CacheCreationInputTokens: extraIntFromString(row["tokens_cache_write"])}
			u.TotalTokens = u.DerivedTotal() + u.ReasoningOutputTokens
			trace.TokenUsage = u
		}
	}
	return finalizedExtraTraces(byID), nil
}

func readSQLiteRows(ctx context.Context, path, query string) ([]map[string]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	data, err := exec.CommandContext(ctx, "/usr/bin/sqlite3", "-readonly", "-json", path, query).Output()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("SQLite evidence query: %s", strings.TrimSpace(string(exitErr.Stderr)))
		}
		return nil, err
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil, nil
	}
	var rows []map[string]interface{}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.UseNumber()
	if err := decoder.Decode(&rows); err != nil {
		return nil, err
	}
	out := make([]map[string]string, len(rows))
	for i, row := range rows {
		out[i] = map[string]string{}
		for key, value := range row {
			if value != nil {
				out[i][key] = fmt.Sprint(value)
			}
		}
	}
	return out, nil
}
func (p extraTranscriptParser) Parse(file TranscriptFile) (*SessionTrace, error) {
	traces, err := p.ParseSessions(context.Background(), file)
	if len(traces) > 1 {
		return nil, errors.New("database contains multiple sessions; use ParseSessions")
	}
	if len(traces) == 0 {
		return nil, err
	}
	return traces[0], err
}
func (p extraTranscriptParser) ParseTail(file TranscriptFile) (*SessionTrace, error) {
	return p.Parse(file)
}
func (p extraTranscriptParser) ParseAppend(TranscriptFile, *SessionTrace, int64) (*SessionTrace, error) {
	return nil, errors.New("this store rewrites records; full snapshot required")
}
func (p extraTranscriptParser) CanAppend(TranscriptFile) bool { return false }

func cloneSessionTraces(in []*SessionTrace) []*SessionTrace {
	out := make([]*SessionTrace, len(in))
	for i, t := range in {
		out[i] = cloneSessionTrace(t)
	}
	return out
}
func insertSessionTraces(data *TranscriptData, traces []*SessionTrace) {
	for _, trace := range traces {
		if nonEmptyTrace(trace) == nil {
			continue
		}
		key := trace.Path
		if isAgentDatabase(TranscriptFile{Tool: trace.Tool, Path: trace.Path}) {
			key += "\x00" + trace.SessionID
		}
		data.Traces[key] = cloneSessionTrace(trace)
	}
}

// Only metadata and usage fields are decoded. Prompt/tool-result bodies are
// skipped by encoding/json and never copied into the snapshot.
type localUsage struct {
	Input           int `json:"input"`
	Output          int `json:"output"`
	Cached          int `json:"cached"`
	Thoughts        int `json:"thoughts"`
	CacheRead       int `json:"cacheRead"`
	CacheWrite      int `json:"cacheWrite"`
	Reasoning       int `json:"reasoning"`
	ReasoningTokens int `json:"reasoningTokens"`
	Total           int `json:"totalTokens"`
	Cache           struct {
		Read  int `json:"read"`
		Write int `json:"write"`
	} `json:"cache"`
	Prompt        int `json:"promptTokenCount"`
	Candidates    int `json:"candidatesTokenCount"`
	CachedContent int `json:"cachedContentTokenCount"`
	ThoughtCount  int `json:"thoughtsTokenCount"`
	TotalCount    int `json:"totalTokenCount"`
}
type localMessage struct {
	ID         string          `json:"id"`
	SessionID  string          `json:"sessionId"`
	Type       string          `json:"type"`
	Role       string          `json:"role"`
	Timestamp  json.RawMessage `json:"timestamp"`
	CreateTime string          `json:"createTime"`
	CWD        string          `json:"cwd"`
	Time       struct {
		Created int64 `json:"created"`
	} `json:"time"`
	Path struct {
		CWD  string `json:"cwd"`
		Root string `json:"root"`
	} `json:"path"`
	Tokens        *localUsage `json:"tokens"`
	Usage         *localUsage `json:"usage"`
	UsageMetadata *localUsage `json:"usageMetadata"`
}
type localRecord struct {
	localMessage
	Message         *localMessage `json:"message"`
	TargetID        string        `json:"targetId"`
	ChildUsage      *localUsage   `json:"childUsage"`
	ParentSessionID string        `json:"parentSessionId"`
}

func recordMessage(r localRecord) localMessage {
	if r.Message == nil {
		return r.localMessage
	}
	m := *r.Message
	if m.ID == "" {
		m.ID = r.ID
	}
	if len(m.Timestamp) == 0 {
		m.Timestamp = r.Timestamp
	}
	return m
}
func extraTimeValue(value interface{}) time.Time {
	switch v := value.(type) {
	case string:
		for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05.999999999 -0700 MST"} {
			if t, err := time.Parse(layout, strings.TrimSpace(v)); err == nil {
				return t
			}
		}
		var n float64
		if _, err := fmt.Sscan(strings.TrimSpace(v), &n); err == nil {
			return extraTimeValue(n)
		}
	case float64:
		if v >= 1e12 {
			return time.UnixMilli(int64(v)).UTC()
		}
		if v > 0 {
			seconds := int64(v)
			return time.Unix(seconds, int64((v-float64(seconds))*1e9)).UTC()
		}
	case json.Number:
		if n, err := v.Float64(); err == nil {
			return extraTimeValue(n)
		}
	case int64:
		return extraTimeValue(float64(v))
	}
	return time.Time{}
}
func messageTimestamp(m localMessage) time.Time {
	var s string
	if json.Unmarshal(m.Timestamp, &s) == nil {
		return extraTimeValue(s)
	}
	var n float64
	if json.Unmarshal(m.Timestamp, &n) == nil && n > 0 {
		if n >= 1e12 {
			return time.UnixMilli(int64(n)).UTC()
		}
		return time.Unix(0, int64(n*1e9)).UTC()
	}
	if m.Time.Created > 0 {
		return extraTimeValue(m.Time.Created)
	}
	return parseTimestampString(m.CreateTime)
}
func messageUsage(kind string, m localMessage) TokenUsage {
	u := m.Usage
	if u == nil {
		u = m.Tokens
	}
	if u == nil {
		u = m.UsageMetadata
	}
	if u == nil {
		return TokenUsage{}
	}
	t := TokenUsage{InputTokens: max(0, u.Input), OutputTokens: max(0, u.Output), CacheReadInputTokens: max(0, u.CacheRead), CacheCreationInputTokens: max(0, u.CacheWrite), ReasoningOutputTokens: max(0, max(u.Reasoning, u.ReasoningTokens)), TotalTokens: max(0, u.Total)}
	switch kind {
	case "gemini":
		if m.UsageMetadata != nil {
			t.InputTokens = max(0, u.Prompt-u.CachedContent)
			t.CacheReadInputTokens = max(0, u.CachedContent)
			t.OutputTokens = max(0, u.Candidates-u.ThoughtCount)
			t.ReasoningOutputTokens = max(0, u.ThoughtCount)
			t.TotalTokens = max(0, u.TotalCount)
		} else {
			t.InputTokens = max(0, u.Input-u.Cached)
			t.CacheReadInputTokens = max(0, u.Cached)
			t.OutputTokens = max(0, u.Output-u.Thoughts)
			t.ReasoningOutputTokens = max(0, u.Thoughts)
		}
	case "opencode":
		t.CacheReadInputTokens = max(0, u.Cache.Read)
		t.CacheCreationInputTokens = max(0, u.Cache.Write)
	case "pi":
		t.ReasoningOutputTokens = min(t.ReasoningOutputTokens, t.OutputTokens)
		t.OutputTokens = max(0, t.OutputTokens-t.ReasoningOutputTokens)
	}
	if t.TotalTokens == 0 {
		t.TotalTokens = t.DerivedTotal() + t.ReasoningOutputTokens
	}
	return t
}
func localMessageRole(m localMessage) string {
	role := firstNonEmptyString(m.Role, m.Type)
	if role == "gemini" || role == "model" {
		return "assistant"
	}
	return role
}

func parseExtraTraceContext(ctx context.Context, kind string, file TranscriptFile) (*SessionTrace, error) {
	trace := &SessionTrace{Tool: kind, Path: file.Path, SessionID: genericTranscriptSessionID(file.Path)}
	messages := map[string]localMessage{}
	childUsage := map[string]localUsage{}
	ordinal := 0
	accept := func(r localRecord) {
		if r.Type == "session" || r.SessionID != "" {
			trace.SessionID = firstNonEmptyString(r.SessionID, r.ID, trace.SessionID)
			if r.CWD != "" {
				setTraceProjectPath(trace, r.CWD, "transcript_cwd")
			}
		}
		// parentId is message-tree ancestry, not agent lineage.
		if r.ParentSessionID != "" {
			trace.ParentThreadID = r.ParentSessionID
			trace.ThreadSource = "subagent"
			trace.RoleHintSource = "parent_session_id"
		}
		if kind == "pi" && r.Type == "child_usage_attributed" && r.ChildUsage != nil {
			u := childUsage[r.TargetID]
			u.Input += max(0, r.ChildUsage.Input)
			u.Output += max(0, r.ChildUsage.Output)
			u.CacheRead += max(0, r.ChildUsage.CacheRead)
			u.CacheWrite += max(0, r.ChildUsage.CacheWrite)
			childUsage[r.TargetID] = u
			return
		}
		m := recordMessage(r)
		if m.CWD == "" {
			m.CWD = firstNonEmptyString(m.Path.CWD, m.Path.Root)
		}
		role := localMessageRole(m)
		if role != "user" && role != "assistant" && role != "toolResult" {
			return
		}
		if m.CWD != "" {
			setTraceProjectPath(trace, m.CWD, "transcript_cwd")
		}
		if messageTimestamp(m).IsZero() {
			return
		}
		key := m.ID
		if key == "" {
			key = fmt.Sprintf("@%d", ordinal)
			ordinal++
		}
		messages[key] = m
	}
	f, err := os.Open(file.Path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if strings.HasSuffix(file.Path, ".jsonl") {
		// Complete records only: a writer's unfinished final line is retried on the
		// next file change. Malformed complete records remain a visible diagnostic.
		reader := bufio.NewReader(io.LimitReader(f, 64<<20))
		for {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, ctxErr
			}
			line, readErr := reader.ReadBytes('\n')
			if readErr == io.EOF {
				break
			} // Retry unfinished records on the next write.
			if readErr != nil {
				err = readErr
				break
			}
			if len(bytes.TrimSpace(line)) == 0 {
				continue
			}
			var r localRecord
			if decodeErr := json.Unmarshal(line, &r); decodeErr != nil {
				if err == nil {
					err = fmt.Errorf("malformed session record: %w", decodeErr)
				}
				continue
			}
			accept(r)
		}
	} else {
		var doc struct {
			SessionID string        `json:"sessionId"`
			CWD       string        `json:"cwd"`
			Messages  []localRecord `json:"messages"`
			History   []localRecord `json:"history"`
		}
		err = json.NewDecoder(io.LimitReader(f, 64<<20)).Decode(&doc)
		if err == nil {
			trace.SessionID = firstNonEmptyString(doc.SessionID, trace.SessionID)
			if doc.CWD != "" {
				setTraceProjectPath(trace, doc.CWD, "transcript_cwd")
			}
			records := doc.Messages
			if records == nil {
				records = doc.History
			}
			for _, r := range records {
				accept(r)
			}
			if len(records) == 0 {
				// OpenCode's legacy storage keeps one message object per JSON
				// file rather than wrapping it in a history document.
				if raw, readErr := os.ReadFile(file.Path); readErr == nil {
					var record localRecord
					if json.Unmarshal(raw, &record) == nil {
						accept(record)
					}
				}
			}
		}
	}
	if info, statErr := f.Stat(); statErr == nil && info.Size() > 64<<20 {
		err = errors.New("session exceeds 64 MiB parser budget")
	}
	if kind == "gemini" && trace.Project == "" {
		rootPath := filepath.Join(filepath.Dir(filepath.Dir(file.Path)), ".project_root")
		if raw, e := os.ReadFile(rootPath); e == nil && len(raw) < 4096 {
			cwd := strings.TrimSpace(string(raw))
			if filepath.IsAbs(cwd) {
				setTraceProjectPath(trace, cwd, "transcript_cwd")
			}
		}
	}
	if kind == "pi" {
		if rel, ok := relativeAfterMarker(file.Path, []string{"agent", "session-artifacts"}); ok {
			parts := strings.Split(rel, string(filepath.Separator))
			if len(parts) >= 2 {
				trace.ParentThreadID = parts[0]
				trace.ThreadSource = "subagent"
				trace.RoleHintSource = "pi_artifact_path"
			}
		}
	}
	for id, m := range messages {
		trace.EventTimes = append(trace.EventTimes, messageTimestamp(m))
		if localMessageRole(m) != "assistant" {
			continue
		}
		if child, ok := childUsage[id]; ok && m.Usage != nil {
			own := *m.Usage
			own.Input = max(0, own.Input-child.Input)
			own.Output = max(0, own.Output-child.Output)
			own.CacheRead = max(0, own.CacheRead-child.CacheRead)
			own.CacheWrite = max(0, own.CacheWrite-child.CacheWrite)
			own.Total = 0
			m.Usage = &own
		}
		trace.TokenUsage.Add(messageUsage(kind, m))
	}
	finalizeTrace(trace)
	return nonEmptyTrace(trace), err
}

// Only append-only OpenClaw message records feed the live sampler. Gemini's
// JSON snapshots and Pi's cross-record child attribution require full parsing.
type extraOutputUsageDecoder struct{ kind string }

func (d extraOutputUsageDecoder) DecodeUsage(line []byte) (liveTokenRateObservation, bool) {
	var r localRecord
	if json.Unmarshal(line, &r) != nil {
		return liveTokenRateObservation{}, false
	}
	m := recordMessage(r)
	ts := messageTimestamp(m)
	if localMessageRole(m) != "assistant" || m.ID == "" || ts.IsZero() {
		return liveTokenRateObservation{}, false
	}
	u := messageUsage(d.kind, m)
	if u.OutputTokens <= 0 {
		return liveTokenRateObservation{}, false
	}
	return liveTokenRateObservation{At: ts, OutputTokens: int64(u.OutputTokens), MessageIdentity: m.ID}, true
}
