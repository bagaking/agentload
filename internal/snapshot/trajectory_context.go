package snapshot

// Native context metadata is optional. Completeness requires its own explicit
// source field; a turn ID or an archived transcript does not supply it.
type TrajectoryContextEvidence struct {
	NativeID                string `json:"native_id,omitempty"`
	Revision                string `json:"revision,omitempty"`
	NativeField             string `json:"native_field"`
	MembershipComplete      bool   `json:"membership_complete,omitempty"`
	CompletenessNativeField string `json:"completeness_native_field,omitempty"`
}

type TrajectoryContextTransformation struct {
	Kind            string              `json:"kind"` // summary or compaction, as recorded
	EventID         string              `json:"event_id"`
	Source          TrajectorySourceRef `json:"source"`
	BeforeID        string              `json:"before_id"`
	AfterID         string              `json:"after_id"`
	OriginalsStatus string              `json:"originals_status"` // unknown unless explicitly recorded
}

// Archive revisions are observed source segments. They do not describe the
// contents of a later model invocation.
type TrajectoryContext struct {
	ID             string                           `json:"id"`
	Scope          string                           `json:"scope"` // archive, actual_input, workspace, query_window
	SessionID      string                           `json:"session_id"`
	AnchorID       string                           `json:"anchor_id,omitempty"`
	NativeID       string                           `json:"native_id,omitempty"`
	NativeRevision string                           `json:"native_revision,omitempty"`
	SourceRevision string                           `json:"source_revision"`
	Membership     string                           `json:"membership"` // recorded, unknown, partial, complete
	KnownMembers   int                              `json:"known_members"`
	Source         TrajectorySourceRef              `json:"source"`
	Branch         *TrajectoryBranchEvidence        `json:"branch,omitempty"`
	Workspace      *TrajectoryWorkspace             `json:"workspace,omitempty"`
	Transformation *TrajectoryContextTransformation `json:"transformation,omitempty"`
	BeforeID       string                           `json:"before_id,omitempty"`
	AfterID        string                           `json:"after_id,omitempty"`
	Coverage       TrajectoryCoverage               `json:"coverage"`
}

type TrajectoryContextMember struct {
	ID          string              `json:"id"`
	EventIDs    []string            `json:"event_ids"`
	Target      TrajectoryReference `json:"target"`
	Status      string              `json:"status"`
	Visibility  string              `json:"visibility"` // archive_record, input_included, workspace_reference, retrieval_window
	NativeField string              `json:"native_field,omitempty"`
	Source      TrajectorySourceRef `json:"source"`
	Candidates  []string            `json:"candidates,omitempty"`
}

type TrajectoryContextQueryResult struct {
	Contexts []TrajectoryContext `json:"contexts"`
	Coverage TrajectoryCoverage  `json:"coverage"`
	Revision string              `json:"revision"`
	Next     string              `json:"next,omitempty"`
}

type TrajectoryContextGetResult struct {
	Context      *TrajectoryContext        `json:"context,omitempty"`
	Members      []TrajectoryContextMember `json:"members"`
	Coverage     TrajectoryCoverage        `json:"coverage"`
	Revision     string                    `json:"revision"`
	FocusID      string                    `json:"focus_id"`
	Before       string                    `json:"before,omitempty"`
	After        string                    `json:"after,omitempty"`
	Truncated    bool                      `json:"truncated"`
	ByteLimit    int                       `json:"byte_limit"`
	MemberOffset int                       `json:"member_offset"`
	NextOffset   *int                      `json:"next_offset,omitempty"`
}
