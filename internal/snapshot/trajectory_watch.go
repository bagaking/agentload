package snapshot

// A change is an index notification, not a transcript body. Read EventID with
// traj.get, or requery the named session, to retrieve authorized evidence.
type TrajectoryChange struct {
	Sequence  uint64              `json:"sequence"`
	Kind      string              `json:"kind"`
	EventID   string              `json:"event_id"`
	EventKind string              `json:"event_kind"`
	SessionID string              `json:"session_id"`
	Agent     string              `json:"agent"`
	Revision  string              `json:"revision"`
	Source    TrajectorySourceRef `json:"source"`
}

// Cursor is a change-stream position, independent of Selector.Cursor, which is
// a query pagination cursor and is invalid in a watch request.
type TrajectoryWatchParams struct {
	Selector  TrajectorySelector `json:"selector"`
	Cursor    string             `json:"cursor,omitempty"`
	TimeoutMS int                `json:"timeout_ms,omitempty"`
}

type TrajectoryWatchResult struct {
	Changes       []TrajectoryChange `json:"changes"`
	Cursor        string             `json:"cursor,omitempty"`
	Revision      string             `json:"revision,omitempty"`
	Coverage      TrajectoryCoverage `json:"coverage"`
	ResetRequired bool               `json:"reset_required"`
	More          bool               `json:"more"`
}
