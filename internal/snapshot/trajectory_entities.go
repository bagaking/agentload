package snapshot

// Entity identity combines a typed literal with its recorded scope. A shared
// basename, label, or spelling does not prove two entities are identical.
type TrajectoryEntity struct {
	ID          string                       `json:"id"`
	Kind        string                       `json:"kind"`
	Literal     string                       `json:"literal"`
	Label       string                       `json:"label"`
	Scope       string                       `json:"scope"`
	Count       int                          `json:"count"`
	Occurrences []TrajectoryEntityOccurrence `json:"occurrences"`
	// OccurrenceQuery retrieves all matching events with the shared bounded
	// query cursor; Occurrences above is only a bounded example projection.
	OccurrenceQuery TrajectorySelector `json:"occurrence_query"`
	Coverage        TrajectoryCoverage `json:"coverage"`
}

// An occurrence is a source-backed predicate, not a statement about later
// model-input membership. requested_read, read, and loaded remain distinct.
type TrajectoryEntityOccurrence struct {
	ID          string              `json:"id"`
	EntityID    string              `json:"entity_id"`
	EventID     string              `json:"event_id"`
	SessionID   string              `json:"session_id"`
	Kind        string              `json:"kind"`
	Literal     string              `json:"literal"`
	Label       string              `json:"label"`
	Scope       string              `json:"scope"`
	Predicate   string              `json:"predicate"`
	NativeField string              `json:"native_field"`
	Source      TrajectorySourceRef `json:"source"`
}
