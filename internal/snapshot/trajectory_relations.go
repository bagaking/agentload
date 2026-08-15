package snapshot

// Evidence contains only source-recorded identity, references and branch
// membership. Delivery and model-input inclusion are separate relation kinds.
type TrajectoryEvidence struct {
	Sender     *TrajectoryIdentityEvidence  `json:"sender,omitempty"`
	Recipients []TrajectoryIdentityEvidence `json:"recipients,omitempty"`
	Relations  []TrajectoryRelationEvidence `json:"relations,omitempty"`
	Branch     *TrajectoryBranchEvidence    `json:"branch,omitempty"`
}

type TrajectoryIdentityEvidence struct {
	Actor       TrajectoryActor `json:"actor"`
	NativeField string          `json:"native_field"`
}

// References default to the observing source's agent, generation and physical
// source. Cross-source scope must be explicitly recorded by the decoder.
type TrajectoryReference struct {
	Kind            string `json:"kind"` // event, native_id, native_envelope, call, session
	ID              string `json:"id"`
	SourceID        string `json:"source_id,omitempty"`
	Generation      string `json:"generation,omitempty"`
	SessionNativeID string `json:"session_native_id,omitempty"`
	Agent           string `json:"agent,omitempty"`
}

type TrajectoryRelationEvidence struct {
	Kind        string              `json:"kind"`
	Target      TrajectoryReference `json:"target"`
	NativeField string              `json:"native_field"`
}

// A branch ID records membership. It does not imply a parent or resume edge.
type TrajectoryBranchEvidence struct {
	ID          string `json:"id"`
	NativeField string `json:"native_field"`
}

// An actor observation is one source-backed participation, not a distinct-
// participant count. Unidentified senders are never merged into one person.
type TrajectoryActorObservation struct {
	ID           string              `json:"id"`
	EventID      string              `json:"event_id"`
	SessionID    string              `json:"session_id"`
	Position     string              `json:"position"` // sender or recipient
	Actor        TrajectoryActor     `json:"actor"`
	Role         string              `json:"role"`
	ProtocolRole string              `json:"protocol_role,omitempty"`
	NativeField  string              `json:"native_field,omitempty"`
	Source       TrajectorySourceRef `json:"source"`
}

type TrajectoryRelationNode struct {
	ID               string                    `json:"id"`
	Type             string                    `json:"type"` // event or session
	SessionID        string                    `json:"session_id"`
	NativeID         string                    `json:"native_id,omitempty"`
	NativeEnvelopeID string                    `json:"native_envelope_id,omitempty"`
	Kind             string                    `json:"kind"`
	Role             string                    `json:"role,omitempty"`
	ProtocolRole     string                    `json:"protocol_role,omitempty"`
	Actor            TrajectoryActor           `json:"actor"`
	Branch           *TrajectoryBranchEvidence `json:"branch,omitempty"`
	Source           TrajectorySourceRef       `json:"source"`
}

type TrajectoryRelation struct {
	ID            string              `json:"id"`
	From          string              `json:"from"`
	Kind          string              `json:"kind"`
	Target        TrajectoryReference `json:"target"`
	TargetIDs     []string            `json:"target_ids"`
	CandidateIDs  []string            `json:"candidate_ids,omitempty"`
	Status        string              `json:"status"` // resolved, missing, ambiguous, coverage_incomplete, unsupported, missing_evidence
	NativeField   string              `json:"native_field"`
	Source        TrajectorySourceRef `json:"source"`
	OutsideWindow bool                `json:"outside_window"`
	Omissions     []string            `json:"omissions,omitempty"`
}

type TrajectoryActorQueryResult struct {
	Actors   []TrajectoryActorObservation `json:"actors"`
	Coverage TrajectoryCoverage           `json:"coverage"`
	Revision string                       `json:"revision"`
	Next     string                       `json:"next,omitempty"`
}

type TrajectoryRelationQueryResult struct {
	Nodes     []TrajectoryRelationNode `json:"nodes"`
	Relations []TrajectoryRelation     `json:"relations"`
	Coverage  TrajectoryCoverage       `json:"coverage"`
	Revision  string                   `json:"revision"`
	Next      string                   `json:"next,omitempty"`
	FocusID   string                   `json:"focus_id,omitempty"`
	Before    string                   `json:"before,omitempty"`
	After     string                   `json:"after,omitempty"`
	Truncated bool                     `json:"truncated"`
	ByteLimit int                      `json:"byte_limit"`
}
