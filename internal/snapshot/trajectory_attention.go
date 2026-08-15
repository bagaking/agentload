package snapshot

// These references are populated only from explicit native identifiers. A
// response's own event ID does not identify the request it closes.
type TrajectoryAttentionEvidence struct {
	RequestID       string `json:"request_id,omitempty"`
	RequestIDField  string `json:"request_id_field,omitempty"`
	ResponseToID    string `json:"response_to_id,omitempty"`
	ResponseToField string `json:"response_to_field,omitempty"`
	OutcomeField    string `json:"outcome_field,omitempty"`
}

type TrajectoryAttentionReference struct {
	EventID     string              `json:"event_id"`
	NativeField string              `json:"native_field,omitempty"`
	Source      TrajectorySourceRef `json:"source"`
}

type TrajectoryObservedTurn struct {
	TurnID string                         `json:"turn_id,omitempty"`
	Status string                         `json:"status"` // open_observed, closed_observed, aborted_observed, unknown
	Starts []TrajectoryAttentionReference `json:"starts"`
	Ends   []TrajectoryAttentionReference `json:"ends"`
}

type TrajectoryExecutionProgress struct {
	Status string                   `json:"status"` // observed, partial, unknown
	Turns  []TrajectoryObservedTurn `json:"turns"`
}

type TrajectoryIntervention struct {
	ID        string                         `json:"id"`
	Kind      string                         `json:"kind"`   // waiting_permission, waiting_input, stuck_candidate, question_hint
	Status    string                         `json:"status"` // open_observed, closed_observed, unknown, candidate, hint, unavailable
	RequestID string                         `json:"request_id,omitempty"`
	TurnID    string                         `json:"turn_id,omitempty"`
	Tool      string                         `json:"tool,omitempty"`
	Count     int                            `json:"count,omitempty"`
	Evidence  []TrajectoryAttentionReference `json:"evidence"`
	Omissions []string                       `json:"omissions,omitempty"`
}

type TrajectoryAttention struct {
	ID            string                        `json:"id"`
	SessionID     string                        `json:"session_id"`
	Agent         string                        `json:"agent"`
	Progress      TrajectoryExecutionProgress   `json:"progress"`
	Interventions []TrajectoryIntervention      `json:"interventions"`
	LatestAction  *TrajectoryAttentionReference `json:"latest_action,omitempty"`
	Liveness      string                        `json:"liveness"` // always unknown; historical logs do not prove actual execution
	Coverage      TrajectoryCoverage            `json:"coverage"`
}

type TrajectoryAttentionRules struct {
	Version            string   `json:"version"`
	ErrorThreshold     int      `json:"error_threshold"`
	ActionWindow       int      `json:"action_window"`
	ErrorScope         string   `json:"error_scope"`
	RecoveryConditions []string `json:"recovery_conditions"`
	PermissionClosure  string   `json:"permission_closure"`
	QuestionRule       string   `json:"question_rule"`
}

type TrajectoryAttentionQueryResult struct {
	Attention []TrajectoryAttention    `json:"attention"`
	Coverage  TrajectoryCoverage       `json:"coverage"`
	Rules     TrajectoryAttentionRules `json:"rules"`
	Revision  string                   `json:"revision"`
	Next      string                   `json:"next,omitempty"`
}

type TrajectoryAttentionGetResult struct {
	Attention *TrajectoryAttention     `json:"attention,omitempty"`
	Coverage  TrajectoryCoverage       `json:"coverage"`
	Rules     TrajectoryAttentionRules `json:"rules"`
	Revision  string                   `json:"revision"`
	FocusID   string                   `json:"focus_id"`
	Truncated bool                     `json:"truncated"`
	ByteLimit int                      `json:"byte_limit"`
}
