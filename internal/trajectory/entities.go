package trajectory

import (
	"agentload/internal/snapshot"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const maxEntityOccurrences = 32
const maxEntityProjection = 256
const maxEntityExamples = 8
const maxEntityScanEvents = 100000
const maxEntityQueryBytes = 64 * 1024

// EntityContext contains only source-recorded adapter identity and cwd. It does
// not guess a workspace from a source filename or a path's basename.
type EntityContext struct {
	Agent            string
	WorkingDirectory string
}

type entityExtractor struct {
	event       snapshot.TrajectoryEvent
	context     EntityContext
	occurrences []snapshot.TrajectoryEntityOccurrence
	seen        map[string]bool
	gaps        []string
}

var entityPathPattern = regexp.MustCompile(`(?:file://[^\s<>"'` + "`" + `]+|(?:/|\./|\.\./)[^\s<>"'` + "`" + `,;\)\]]+|[A-Za-z0-9_.-]+(?:/[A-Za-z0-9_.-]+)+)`)
var entityFilePattern = regexp.MustCompile(`\b[A-Za-z0-9][A-Za-z0-9_.-]*\.(?:md|go|ts|tsx|js|jsx|json|jsonl|toml|yaml|yml|txt|py|html|css|sh)\b`)
var entitySkillPattern = regexp.MustCompile(`(?:\$|\bskill:)([A-Za-z][A-Za-z0-9_.-]{0,79})`)
var entityToolPattern = regexp.MustCompile(`\btool:([A-Za-z][A-Za-z0-9_.-]{0,79})`)
var entityVersionPattern = regexp.MustCompile(`\bv?[0-9]+\.[0-9]+\.[0-9]+(?:-[A-Za-z0-9_.-]+)?\b`)
var entityTermPattern = regexp.MustCompile(`[\p{L}][\p{L}\p{N}_-]{2,79}`)

// ExtractEntities runs before display cleaning and preserves the exact native
// field that supports each occurrence. A successful tool result, a command
// string, or a file read never proves a skill load or later model visibility.
func ExtractEntities(e snapshot.TrajectoryEvent, raw []byte, ctx EntityContext) ([]snapshot.TrajectoryEntityOccurrence, []string) {
	var record map[string]json.RawMessage
	var err error
	if len(raw) > 0 {
		err = json.Unmarshal(raw, &record)
	}
	return extractRecordEntities(e, record, object(record["payload"]), err, ctx)
}

// The online/replay shaper shares this parsed physical record between blocks.
// Public raw-byte extraction uses the same owner without retaining raw history.
func extractRecordEntities(e snapshot.TrajectoryEvent, record, payload map[string]json.RawMessage, recordErr error, ctx EntityContext) ([]snapshot.TrajectoryEntityOccurrence, []string) {
	x := entityExtractor{event: e, context: ctx, seen: map[string]bool{}, occurrences: []snapshot.TrajectoryEntityOccurrence{}}
	if e.ID == "" {
		return x.occurrences, []string{"entity_event_id_unavailable"}
	}
	if e.Tool != nil {
		if e.Kind == "tool_call" {
			x.add("tool", e.Tool.Name, "called", "tool.name")
		}
		if len(e.Tool.Arguments) > 0 {
			x.arguments(e.Tool.Arguments)
		}
	}
	if recordErr != nil {
		x.omission("entity_native_record_invalid")
	} else if record != nil {
		x.recorded(record, payload)
		if text, nativeField := entityNativeText(record, payload, e, ctx.Agent); nativeField != "" {
			x.text(text, nativeField)
		}
	}
	sort.Slice(x.occurrences, func(i, j int) bool { return x.occurrences[i].ID < x.occurrences[j].ID })
	sort.Strings(x.gaps)
	return x.occurrences, x.gaps
}

func (x *entityExtractor) omission(g string) {
	for _, existing := range x.gaps {
		if existing == g {
			return
		}
	}
	if len(x.gaps) < 16 {
		x.gaps = append(x.gaps, g)
	}
}

func (x *entityExtractor) sessionScope() string {
	if x.event.SessionID != "" {
		return "session:" + x.event.SessionID
	}
	if x.event.Source.ID != "" && x.event.Source.Generation != "" {
		return "source:" + x.event.Source.ID + ":" + x.event.Source.Generation
	}
	x.omission("entity_scope_unavailable")
	return ""
}

func (x *entityExtractor) add(kind, literal, predicate, nativeField string) {
	literal = strings.TrimSpace(literal)
	if literal == "" {
		return
	}
	if len(literal) > 512 || len(nativeField) > 160 {
		x.omission("entity_literal_size_limit")
		return
	}
	canonical, label, scope := literal, literal, ""
	switch kind {
	case "path":
		var ok bool
		canonical, scope, ok = x.path(literal)
		if !ok {
			return
		}
		label = canonical
	case "skill":
		if strings.Contains(literal, "/") || strings.HasSuffix(literal, "SKILL.md") {
			var ok bool
			canonical, scope, ok = x.path(literal)
			if !ok {
				return
			}
			if filepath.Base(canonical) == "SKILL.md" {
				if dir := filepath.Dir(canonical); dir != "." {
					label = filepath.Base(dir)
				}
			}
		} else {
			// A label alone does not identify a particular installed skill.
			scope = x.sessionScope()
		}
	case "tool":
		if x.context.Agent != "" {
			scope = "agent:" + x.context.Agent
		} else {
			scope = x.sessionScope()
			x.omission("entity_tool_namespace_unavailable")
		}
	case "term", "version":
		scope = x.sessionScope()
	default:
		x.omission("entity_kind_unsupported")
		return
	}
	if scope == "" {
		return
	}
	if len(canonical) > 512 || len(label) > 512 || len(scope) > 512 {
		x.omission("entity_literal_size_limit")
		return
	}
	entityID := "ent." + digest([]byte(kind+"\x00"+scope+"\x00"+canonical))
	key := entityID + "\x00" + predicate + "\x00" + nativeField
	if x.seen[key] {
		return
	}
	if len(x.occurrences) >= maxEntityOccurrences {
		x.omission("entity_occurrence_limit")
		return
	}
	x.seen[key] = true
	x.occurrences = append(x.occurrences, snapshot.TrajectoryEntityOccurrence{ID: "occ." + digest([]byte(x.event.ID+"\x00"+key)), EntityID: entityID, EventID: x.event.ID, SessionID: x.event.SessionID, Kind: kind, Literal: canonical, Label: label, Scope: scope, Predicate: predicate, NativeField: nativeField, Source: x.event.Source})
}

func (x *entityExtractor) path(literal string) (string, string, bool) {
	if strings.HasPrefix(literal, "file://") {
		u, err := url.Parse(literal)
		if err != nil || (u.Host != "" && u.Host != "localhost") || u.RawQuery != "" || u.Fragment != "" || !filepath.IsAbs(u.Path) {
			x.omission("entity_path_scope_unavailable")
			return "", "", false
		}
		literal = u.Path
	}
	if filepath.IsAbs(literal) {
		return filepath.Clean(literal), "local_path", true
	}
	if filepath.IsAbs(x.context.WorkingDirectory) {
		return filepath.Clean(filepath.Join(x.context.WorkingDirectory, literal)), "local_path", true
	}
	scope := x.sessionScope()
	if scope == "" {
		return "", "", false
	}
	x.omission("entity_relative_path_scope_unavailable")
	return filepath.Clean(literal), scope, true
}

func (x *entityExtractor) pathOccurrence(literal, predicate, nativeField string) {
	x.add("path", literal, predicate, nativeField)
	if filepath.Base(literal) == "SKILL.md" {
		x.add("skill", literal, predicate, nativeField)
	}
}

func (x *entityExtractor) arguments(raw json.RawMessage) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		var text string
		if json.Unmarshal(raw, &text) == nil {
			if json.Valid([]byte(text)) {
				x.arguments(json.RawMessage(text))
			} else {
				x.text(text, "tool.arguments")
			}
		}
		return
	}
	if cwd := entityString(fields, "workdir"); filepath.IsAbs(cwd) {
		x.context.WorkingDirectory = cwd
	}
	predicate := "mention"
	if x.event.Kind == "tool_call" && entityReadTool(x.event.Tool.Name) {
		predicate = "requested_read"
	}
	for _, key := range []string{"path", "file_path", "filePath", "filename", "file", "skill_path"} {
		if path := entityString(fields, key); path != "" {
			x.pathOccurrence(path, predicate, "tool.arguments."+key)
		}
	}
	for _, key := range []string{"paths", "files"} {
		var paths []string
		if json.Unmarshal(fields[key], &paths) == nil {
			for i, path := range paths {
				if i >= maxEntityOccurrences {
					x.omission("entity_occurrence_limit")
					break
				}
				x.pathOccurrence(path, predicate, fmt.Sprintf("tool.arguments.%s[%d]", key, i))
			}
		}
	}
	for _, key := range []string{"skill", "skill_name"} {
		if skill := entityString(fields, key); skill != "" {
			p := "mention"
			if x.event.Kind == "tool_call" && entityLoadTool(x.event.Tool.Name) {
				p = "requested_load"
			}
			x.add("skill", skill, p, "tool.arguments."+key)
		}
	}
	// Commands and other argument strings are mentions only. In particular,
	// parsing `cat .../SKILL.md` does not establish a read or a skill load.
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if len(keys) > 64 {
		keys = keys[:64]
		x.omission("entity_argument_scan_limit")
	}
	for _, key := range keys {
		switch key {
		case "path", "file_path", "filePath", "filename", "file", "skill_path", "skill", "skill_name", "workdir", "paths", "files":
			continue
		}
		if text := entityString(fields, key); text != "" && len(key) <= 80 {
			x.text(text, "tool.arguments."+key)
		}
	}
}

func entityReadTool(name string) bool {
	switch strings.ToLower(name) {
	case "read", "read_file", "readfile", "view_file", "view_image", "functions.read_file", "functions.view_image":
		return true
	}
	return false
}
func entityLoadTool(name string) bool {
	switch strings.ToLower(name) {
	case "skill", "load_skill", "skills.load":
		return true
	}
	return false
}

func (x *entityExtractor) text(text, nativeField string) {
	if len(text) > 16384 {
		text = text[:16384]
		x.omission("entity_text_scan_limit")
	}
	// These literals are necessary for the corresponding regexp to match.
	// Avoid full scans of long plain prose, preserving the original regexp,
	// occurrence order and limits whenever its required literal is present.
	var pathRanges [][]int
	if strings.Contains(text, "/") {
		pathRanges = entityPathPattern.FindAllStringIndex(text, maxEntityOccurrences+1)
	}
	for _, position := range pathRanges {
		start := strings.LastIndexAny(text[:position[0]], " \n\t\r<>\"'`") + 1
		if prefix := text[start:position[0]]; strings.Contains(prefix, ":") {
			// A non-file URL is not a local path.
			continue
		}
		x.pathOccurrence(text[position[0]:position[1]], "mention", nativeField)
	}
	var files [][]int
	if strings.Contains(text, ".") {
		files = entityFilePattern.FindAllStringIndex(text, maxEntityOccurrences+1)
	}
	for _, position := range files {
		withinPath := false
		for _, pathPosition := range pathRanges {
			if position[0] >= pathPosition[0] && position[1] <= pathPosition[1] {
				withinPath = true
				break
			}
		}
		if !withinPath {
			x.pathOccurrence(text[position[0]:position[1]], "mention", nativeField)
		}
	}
	if strings.Contains(text, "$") || strings.Contains(text, "skill:") {
		for _, match := range entitySkillPattern.FindAllStringSubmatch(text, maxEntityOccurrences+1) {
			x.add("skill", match[1], "mention", nativeField)
		}
	}
	if strings.Contains(text, "tool:") {
		for _, match := range entityToolPattern.FindAllStringSubmatch(text, maxEntityOccurrences+1) {
			x.add("tool", match[1], "mention", nativeField)
		}
	}
	if strings.Contains(text, ".") {
		for _, literal := range entityVersionPattern.FindAllString(text, maxEntityOccurrences+1) {
			x.add("version", literal, "mention", nativeField)
		}
	}
	for _, literal := range entityTermPattern.FindAllString(text, maxEntityOccurrences+1) {
		x.add("term", literal, "mention", nativeField)
	}
}

func (x *entityExtractor) recorded(record, payload map[string]json.RawMessage) {
	fields, prefix := payload, "payload."
	if len(fields) == 0 {
		fields, prefix = record, ""
	}
	typ := entityString(fields, "type")
	if typ == "" {
		typ = x.event.Kind
	}
	switch typ {
	case "file_read":
		if path := entityString(fields, "path"); path != "" {
			x.pathOccurrence(path, "read", prefix+"path")
		} else {
			x.omission("entity_recorded_path_unavailable")
		}
	case "skill_loaded":
		if path := entityString(fields, "skill_path"); path != "" {
			x.add("skill", path, "loaded", prefix+"skill_path")
		} else if name := entityString(fields, "skill_name"); name != "" {
			x.add("skill", name, "loaded", prefix+"skill_name")
		} else {
			x.omission("entity_recorded_skill_unavailable")
		}
	}
}

func entityString(fields map[string]json.RawMessage, key string) string {
	var value string
	_ = json.Unmarshal(fields[key], &value)
	return value
}

// Locate the original block rather than searching cleaned display text. This
// prevents a neighboring block from acquiring another block's entity facts.
func entityNativeText(record, payload map[string]json.RawMessage, e snapshot.TrajectoryEvent, agent string) (string, string) {
	if e.Kind == "usage" || e.Kind == "tool_call" || e.Kind == "tool_update" {
		return "", ""
	}
	if message := object(record["message"]); len(message) > 0 {
		return entityContentText(message["content"], e.Source.Block, "message.content")
	}
	if params := object(record["params"]); len(params) > 0 {
		update := object(params["update"])
		if text := entityString(object(update["content"]), "text"); text != "" {
			return text, "params.update.content.text"
		}
		if text := entityString(update, "summary"); text != "" {
			return text, "params.update.summary"
		}
		if e.Kind == "tool_result" {
			return entityContentText(update["content"], -1, "params.update.content")
		}
	}
	if entityString(record, "type") == "history_mutation" {
		var items []map[string]json.RawMessage
		_ = json.Unmarshal(payload["items"], &items)
		block := e.Source.Block
		for i, item := range items {
			count := 1
			if typ := entityString(item, "type"); typ == "message" || typ == "reasoning" {
				var content []json.RawMessage
				raw := item["content"]
				if len(raw) == 0 || string(raw) == "null" {
					raw = item["summary"]
				}
				if json.Unmarshal(raw, &content) == nil && len(content) > 0 {
					count = len(content)
				}
			}
			if block < count {
				return entityItemText(item, block, fmt.Sprintf("payload.items[%d].", i))
			}
			block -= count
		}
		return "", ""
	}
	if len(payload) > 0 {
		if agent == "codex" && entityString(payload, "type") == "reasoning" {
			return entityContentText(payload["summary"], e.Source.Block, "payload.summary")
		}
		return entityItemText(payload, e.Source.Block, "payload.")
	}
	for _, key := range []string{"text", "summary", "content"} {
		if text := entityString(record, key); text != "" {
			return text, key
		}
	}
	return "", ""
}

func entityItemText(item map[string]json.RawMessage, block int, prefix string) (string, string) {
	for _, key := range []string{"content", "summary"} {
		if len(item[key]) > 0 && string(item[key]) != "null" {
			if text, native := entityContentText(item[key], block, prefix+key); native != "" {
				return text, native
			}
		}
	}
	for _, key := range []string{"message", "text", "output"} {
		if text := entityString(item, key); text != "" {
			return text, prefix + key
		}
	}
	return "", ""
}

func entityContentText(raw json.RawMessage, block int, prefix string) (string, string) {
	var text string
	if json.Unmarshal(raw, &text) == nil && string(raw) != "null" {
		return text, prefix
	}
	var blocks []map[string]json.RawMessage
	if json.Unmarshal(raw, &blocks) != nil {
		return "", ""
	}
	if block >= 0 {
		if block >= len(blocks) {
			return "", ""
		}
		for _, key := range []string{"text", "thinking"} {
			if text := entityString(blocks[block], key); text != "" {
				return text, fmt.Sprintf("%s[%d].%s", prefix, block, key)
			}
		}
		if entityString(blocks[block], "type") == "tool_result" {
			return entityContentText(blocks[block]["content"], -1, fmt.Sprintf("%s[%d].content", prefix, block))
		}
		return "", ""
	}
	texts := []string{}
	for _, b := range blocks {
		text := entityString(b, "text")
		if text == "" {
			text = entityString(object(b["content"]), "text")
		}
		if text != "" {
			texts = append(texts, text)
		}
	}
	if len(texts) > 0 {
		return strings.Join(texts, "\n"), prefix
	}
	return "", ""
}

func ValidateEntitySelector(q snapshot.TrajectorySelector) error {
	if q.EntityKind != "" {
		switch q.EntityKind {
		case "tool", "skill", "path", "term", "version":
		default:
			return fmt.Errorf("%w: unsupported entity kind", ErrInvalid)
		}
	}
	if q.Predicate != "" {
		switch q.Predicate {
		case "mention", "requested_read", "read", "requested_load", "loaded", "called":
		default:
			return fmt.Errorf("%w: unsupported entity predicate", ErrInvalid)
		}
	}
	if q.EntityID != "" {
		if !strings.HasPrefix(q.EntityID, "ent.") || len(q.EntityID) != 20 {
			return fmt.Errorf("%w: invalid entity ID", ErrInvalid)
		}
		if _, err := hex.DecodeString(q.EntityID[4:]); err != nil {
			return fmt.Errorf("%w: invalid entity ID", ErrInvalid)
		}
	}
	if len(q.Skill) > 512 || len(q.EntityKind) > 16 || len(q.Predicate) > 32 {
		return ErrInvalid
	}
	return nil
}

func entityOccurrenceMatches(o snapshot.TrajectoryEntityOccurrence, q snapshot.TrajectorySelector) bool {
	if q.EntityKind != "" && o.Kind != q.EntityKind || q.EntityID != "" && o.EntityID != q.EntityID || q.Predicate != "" && o.Predicate != q.Predicate {
		return false
	}
	if q.Skill != "" && (o.Kind != "skill" || (!strings.EqualFold(o.Label, q.Skill) && o.Literal != q.Skill)) {
		return false
	}
	return true
}

func MatchEntitySelector(e snapshot.TrajectoryEvent, q snapshot.TrajectorySelector) bool {
	if q.Skill == "" && q.EntityKind == "" && q.EntityID == "" && q.Predicate == "" {
		return true
	}
	for _, occurrence := range e.Entities {
		if entityOccurrenceMatches(occurrence, q) {
			return true
		}
	}
	return false
}

func entityQueryRevision(states []*sourceState, q snapshot.TrajectorySelector) string {
	q.Cursor, q.Limit = "", 0
	encoded, _ := json.Marshal(q)
	return digest([]byte(sourceRevision(states) + "\x00" + string(encoded)))
}

func entityCursorStart(cursor, revision string) (int, error) {
	if cursor == "" {
		return 0, nil
	}
	parts := strings.Split(cursor, ":")
	if len(parts) != 2 || parts[0] != revision {
		return 0, ErrStale
	}
	start, err := strconv.Atoi(parts[1])
	if err != nil || start < 0 || start > maxEntityProjection {
		return 0, ErrInvalid
	}
	return start, nil
}

func (s *Service) QueryEntities(ctx context.Context, q snapshot.TrajectorySelector) (snapshot.TrajectoryQueryResult, error) {
	if err := s.lockOperation(ctx); err != nil {
		return snapshot.TrajectoryQueryResult{}, err
	}
	defer s.opMu.Unlock()
	out := snapshot.TrajectoryQueryResult{Sessions: []snapshot.TrajectorySession{}, Events: []snapshot.TrajectoryEvent{}, Entities: []snapshot.TrajectoryEntity{}}
	if q.Collection == "" {
		q.Collection = "entities"
	}
	if q.Collection != "entities" {
		return out, ErrInvalid
	}
	if q.Limit == 0 {
		q.Limit = 20
	}
	if q.Limit < 1 || q.Limit > 50 || len(q.Text) > 1024 || q.State != "" || q.ContextID != "" || q.ContextScope != "" || q.RelationKind != "" {
		return out, ErrInvalid
	}
	if err := ValidateEntitySelector(q); err != nil {
		return out, err
	}
	states, cov := s.collect(ctx)
	if err := ctx.Err(); err != nil {
		return snapshot.TrajectoryQueryResult{}, err
	}
	out.Coverage = cov
	out.Revision = entityQueryRevision(states, q)
	start, err := entityCursorStart(q.Cursor, out.Revision)
	if err != nil {
		return out, err
	}
	projection := map[string]*snapshot.TrajectoryEntity{}
	scanned := 0
	for _, st := range states {
		if q.Agent != "" && q.Agent != st.Agent || q.SessionID != "" && q.SessionID != sessionID(st) {
			continue
		}
		local, err := scan(ctx, st, func(e snapshot.TrajectoryEvent) bool {
			scanned++
			if scanned > maxEntityScanEvents {
				gap(&out.Coverage, "entity_scan_limit")
				return false
			}
			base := q
			base.Text, base.Skill, base.EntityKind, base.EntityID, base.Predicate = "", "", "", "", ""
			if !matches(e, base) {
				return true
			}
			if e.EntityCoverage != nil {
				mergeCoverage(&out.Coverage, *e.EntityCoverage)
			}
			for _, occurrence := range e.Entities {
				if !entityOccurrenceMatches(occurrence, q) || !entityTextMatches(occurrence, q.Text) {
					continue
				}
				entity := projection[occurrence.EntityID]
				if entity == nil {
					if len(projection) >= maxEntityProjection {
						gap(&out.Coverage, "entity_projection_limit")
						out.Coverage.Omitted++
						continue
					}
					follow := q
					follow.Collection, follow.EntityID, follow.Cursor, follow.Text, follow.Limit = "events", occurrence.EntityID, "", "", 20
					entity = &snapshot.TrajectoryEntity{ID: occurrence.EntityID, Kind: occurrence.Kind, Literal: occurrence.Literal, Label: occurrence.Label, Scope: occurrence.Scope, Occurrences: []snapshot.TrajectoryEntityOccurrence{}, OccurrenceQuery: follow, Coverage: coverage("entity:" + occurrence.EntityID)}
					projection[occurrence.EntityID] = entity
				}
				entity.Count++
				if len(entity.Occurrences) < maxEntityExamples {
					entity.Occurrences = append(entity.Occurrences, occurrence)
				} else {
					entity.Coverage.Omitted++
					gap(&entity.Coverage, "entity_occurrences_omitted")
				}
			}
			return true
		})
		if err != nil {
			return snapshot.TrajectoryQueryResult{}, err
		}
		mergeCoverage(&out.Coverage, local)
		if scanned > maxEntityScanEvents {
			break
		}
	}
	ids := make([]string, 0, len(projection))
	for id := range projection {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for i := start; i < len(ids) && len(out.Entities) < q.Limit; i++ {
		entity := *projection[ids[i]]
		if !out.Coverage.Complete {
			gap(&entity.Coverage, "entity_source_coverage_incomplete")
		}
		out.Entities = append(out.Entities, entity)
		if encoded, _ := json.Marshal(out); len(encoded) > maxEntityQueryBytes-256 {
			out.Entities = out.Entities[:len(out.Entities)-1]
			gap(&out.Coverage, "entity_result_byte_limit")
			break
		}
	}
	if start+len(out.Entities) < len(ids) {
		out.Next = fmt.Sprintf("%s:%d", out.Revision, start+len(out.Entities))
	}
	return out, nil
}

func entityTextMatches(o snapshot.TrajectoryEntityOccurrence, query string) bool {
	text := strings.ToLower(o.Label + " " + o.Literal)
	for _, word := range strings.Fields(strings.ToLower(query)) {
		if !strings.Contains(text, word) {
			return false
		}
	}
	return true
}

func (s *Service) GetEntity(ctx context.Context, p snapshot.TrajectoryGetParams) (snapshot.TrajectoryGetResult, error) {
	out := snapshot.TrajectoryGetResult{FocusID: p.ID, Events: []snapshot.TrajectoryEvent{}, ByteLimit: MaxSliceBytes}
	if p.Raw || p.RawOffset != 0 || p.MemberOffset > 0 || p.Around < 0 || p.Around > 5 || p.MaxBytes < 0 || p.MaxBytes > MaxSliceBytes || p.View != "" && p.View != "details" && p.View != "slice" {
		return out, ErrInvalid
	}
	if p.MaxBytes > 0 {
		out.ByteLimit = p.MaxBytes
	}
	if out.ByteLimit < 1024 {
		return out, ErrInvalid
	}
	query, err := s.QueryEntities(ctx, snapshot.TrajectorySelector{Collection: "entities", EntityID: p.ID, Limit: 1})
	if err != nil {
		return out, err
	}
	out.Coverage = query.Coverage
	if len(query.Entities) == 0 {
		return out, ErrNotFound
	}
	entity := query.Entities[0]
	for len(entity.Occurrences) > 0 {
		out.Entity = &entity
		encoded, _ := json.Marshal(out)
		if len(encoded) <= out.ByteLimit-160 {
			break
		}
		entity.Occurrences = entity.Occurrences[:len(entity.Occurrences)-1]
		entity.Coverage.Omitted++
		gap(&entity.Coverage, "entity_occurrences_omitted_for_budget")
	}
	out.Entity = &entity
	out.Truncated = entity.Count > len(entity.Occurrences)
	if out.Truncated {
		gap(&out.Coverage, "entity_occurrences_omitted")
		out.Coverage.Omitted += entity.Count - len(entity.Occurrences)
	}
	if encoded, _ := json.Marshal(out); len(encoded) > out.ByteLimit {
		return out, fmt.Errorf("%w: budget cannot include entity identity", ErrInvalid)
	}
	return out, nil
}
