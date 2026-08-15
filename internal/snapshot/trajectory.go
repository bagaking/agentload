package snapshot

import (
	"encoding/json"
	"time"
)

// Trajectory is a content-access surface. None of these records enter resource
// metrics. Source locators identify recorded evidence, not model-input visibility.
type TrajectorySourceRef struct {
	ID         string `json:"id"`
	Generation string `json:"generation"`
	Line       int    `json:"line"`
	Offset     int64  `json:"offset"`
	Length     int    `json:"length"`
	Block      int    `json:"block"`
	Digest     string `json:"digest"`
	NativeType string `json:"native_type"`
}

type TrajectoryActor struct {
	ID   string `json:"id,omitempty"`
	Kind string `json:"kind"` // unknown unless identity evidence was recorded
}

type TrajectoryTool struct {
	Name      string          `json:"name"`
	CallID    string          `json:"call_id,omitempty"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

type TrajectoryEvent struct {
	ID               string                       `json:"id"`
	SessionID        string                       `json:"session_id"`
	NativeID         string                       `json:"native_id,omitempty"`
	NativeEnvelopeID string                       `json:"native_envelope_id,omitempty"`
	ProtocolRole     string                       `json:"protocol_role,omitempty"`
	TurnID           string                       `json:"turn_id,omitempty"`
	Timestamp        *time.Time                   `json:"timestamp,omitempty"`
	Role             string                       `json:"role"`
	Actor            TrajectoryActor              `json:"actor"`
	Evidence         *TrajectoryEvidence          `json:"evidence,omitempty"`
	Workspace        *TrajectoryWorkspace         `json:"workspace,omitempty"`
	Context          *TrajectoryContextEvidence   `json:"context,omitempty"`
	Attention        *TrajectoryAttentionEvidence `json:"attention,omitempty"`
	Entities         []TrajectoryEntityOccurrence `json:"entities,omitempty"`
	EntityCoverage   *TrajectoryCoverage          `json:"entity_coverage,omitempty"`
	Kind             string                       `json:"kind"`
	Text             string                       `json:"text"`
	Tool             *TrajectoryTool              `json:"tool,omitempty"`
	Outcome          string                       `json:"outcome,omitempty"`
	PairID           string                       `json:"pair_id,omitempty"`
	Usage            *TrajectoryUsage             `json:"usage,omitempty"`
	Source           TrajectorySourceRef          `json:"source"`
	// Raw is populated only by explicit evidence reads and is never cleaned.
	Raw       string   `json:"raw,omitempty"` // exact UTF-8 source record, including its newline
	Omissions []string `json:"omissions,omitempty"`
}

// Workspace is a recorded working directory, independent of actual model input.
type TrajectoryWorkspace struct {
	Path        string `json:"path"`
	NativeField string `json:"native_field"`
}

// Counters contain only recorded fields. Model breakdown is descriptive and
// must never be added to the same update's counters a second time.
type TrajectoryUsage struct {
	Scope       string                      `json:"scope"`
	ScopeID     string                      `json:"scope_id,omitempty"`
	Aggregation string                      `json:"aggregation"`
	Counters    map[string]int64            `json:"counters"`
	Models      map[string]map[string]int64 `json:"models,omitempty"`
}

// A turn is an observed execution boundary, not an inferred goal or a chain of
// thought. Events can lack a turn ID and multiple turns can overlap.
type TrajectoryTurn struct {
	ID        string             `json:"id"`
	SessionID string             `json:"session_id"`
	Events    []TrajectoryEvent  `json:"events"`
	Coverage  TrajectoryCoverage `json:"coverage"`
}

type TrajectoryCoverage struct {
	Complete bool                     `json:"complete"`
	Scope    string                   `json:"scope"`
	Gaps     []string                 `json:"gaps"`
	Omitted  int                      `json:"omitted"`
	Index    *TrajectoryIndexProgress `json:"index,omitempty"`
}

// Preparation progress is separate from semantic gaps in decoded records.
type TrajectoryIndexProgress struct {
	KnownSources      int `json:"known_sources"`
	DecodedSources    int `json:"decoded_sources"`
	SearchableSources int `json:"searchable_sources"`
	DecodedEvents     int `json:"decoded_events"`
	SearchableEvents  int `json:"searchable_events"`
}

type TrajectorySession struct {
	ID           string     `json:"id"`
	NativeID     string     `json:"native_id,omitempty"`
	Agent        string     `json:"agent"`
	Title        string     `json:"title"`
	LastAction   string     `json:"last_action"`
	LastEvent    *time.Time `json:"last_event,omitempty"`
	EventCount   int        `json:"event_count"`
	MatchedCount *int       `json:"matched_count"` // nil means the exact count has not been computed
	MatchedIDs   []string   `json:"matched_ids"`
	// MatchedPreview is an excerpt from the first matched event, not the last
	// action or an inferred account of the session.
	MatchedPreview string             `json:"matched_preview,omitempty"`
	Tools          []string           `json:"tools"`
	Coverage       TrajectoryCoverage `json:"coverage"`
}

type TrajectorySelector struct {
	Count        bool   `json:"count,omitempty"` // request an exact prepared-scope total
	Collection   string `json:"collection,omitempty"`
	Text         string `json:"text,omitempty"`
	Tool         string `json:"tool,omitempty"`
	Skill        string `json:"skill,omitempty"`
	Kind         string `json:"kind,omitempty"`
	State        string `json:"state,omitempty"`
	SessionID    string `json:"session_id,omitempty"`
	ActorID      string `json:"actor_id,omitempty"`
	ActorKind    string `json:"actor_kind,omitempty"`
	RelationKind string `json:"relation_kind,omitempty"`
	EntityKind   string `json:"entity_kind,omitempty"`
	EntityID     string `json:"entity_id,omitempty"`
	Predicate    string `json:"predicate,omitempty"`
	Role         string `json:"role,omitempty"`
	Agent        string `json:"agent,omitempty"`
	ContextID    string `json:"context_id,omitempty"`
	ContextScope string `json:"context_scope,omitempty"`
	Limit        int    `json:"limit,omitempty"`
	Cursor       string `json:"cursor,omitempty"`
}

type TrajectoryQueryResult struct {
	WatchCursor  string                `json:"watch_cursor,omitempty"`
	Entities     []TrajectoryEntity    `json:"entities,omitempty"`
	Knowledge    []TrajectoryKnowledge `json:"knowledge,omitempty"`
	Sessions     []TrajectorySession   `json:"sessions"`
	Events       []TrajectoryEvent     `json:"events"`
	Coverage     TrajectoryCoverage    `json:"coverage"`
	Revision     string                `json:"revision"`
	Next         string                `json:"next,omitempty"`
	MatchedTotal *int                  `json:"matched_total,omitempty"` // absent unless explicitly counted; exact within prepared coverage
}

type TrajectoryGetParams struct {
	ID           string `json:"id"`
	View         string `json:"view,omitempty"`
	Around       int    `json:"around"`
	MaxBytes     int    `json:"max_bytes,omitempty"`
	Raw          bool   `json:"raw,omitempty"`
	RawOffset    int    `json:"raw_offset,omitempty"`
	MemberOffset int    `json:"member_offset,omitempty"`
}

type TrajectoryGetResult struct {
	Entity    *TrajectoryEntity    `json:"entity,omitempty"`
	Knowledge *TrajectoryKnowledge `json:"knowledge,omitempty"`
	Session   *TrajectorySession   `json:"session,omitempty"`
	Events    []TrajectoryEvent    `json:"events"`
	Coverage  TrajectoryCoverage   `json:"coverage"`
	FocusID   string               `json:"focus_id"`
	Truncated bool                 `json:"truncated"`
	Before    string               `json:"before,omitempty"`
	After     string               `json:"after,omitempty"`
	ByteLimit int                  `json:"byte_limit"`
	RawChunk  *TrajectoryRawChunk  `json:"raw_chunk,omitempty"`
}

type TrajectoryRawChunk struct {
	Encoding   string `json:"encoding"`
	Data       string `json:"data"`
	Offset     int    `json:"offset"`
	TotalBytes int    `json:"total_bytes"`
	NextOffset *int   `json:"next_offset,omitempty"`
}
