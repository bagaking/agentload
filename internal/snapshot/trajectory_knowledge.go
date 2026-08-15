package snapshot

import "time"

// Knowledge records are local statements with references to recorded evidence.
// A verification is a reported, scoped observation; it never changes a
// candidate into a generally proven conclusion.
type TrajectoryKnowledge struct {
	ID            string                            `json:"id"`
	Kind          string                            `json:"kind"`
	State         string                            `json:"state"`
	Origin        string                            `json:"origin"`
	RuleID        string                            `json:"rule_id,omitempty"`
	RuleVersion   string                            `json:"rule_version,omitempty"`
	Experience    *TrajectoryKnowledgeExperience    `json:"experience,omitempty"`
	Text          string                            `json:"text"`
	Sources       []TrajectoryKnowledgeSource       `json:"sources"`
	Links         []TrajectoryKnowledgeLink         `json:"links,omitempty"`
	Applicability *TrajectoryKnowledgeApplicability `json:"applicability,omitempty"`
	EvidenceState string                            `json:"evidence_state"`
	ScopeStatus   string                            `json:"scope_status"`
	CreatedAt     *time.Time                        `json:"created_at,omitempty"`
	UpdatedAt     *time.Time                        `json:"updated_at,omitempty"`
	Omissions     []string                          `json:"omissions,omitempty"`
}

// A deterministic experience reports recorded observations. It makes no causal
// claim, task-completion claim, or recommendation to repeat an action.
type TrajectoryKnowledgeExperience struct {
	Pattern         string                       `json:"pattern"` // repeated_error or outcome_change
	Basis           string                       `json:"basis"`   // same_recorded_action or native_relation
	Scope           TrajectoryKnowledgeScope     `json:"scope"`
	Steps           []TrajectoryKnowledgeStep    `json:"steps"`
	Relation        *TrajectoryKnowledgeRelation `json:"relation,omitempty"`
	Counterexamples []TrajectoryKnowledgeStep    `json:"counterexamples,omitempty"`
	Causality       string                       `json:"causality"`       // unproven
	TaskCompletion  string                       `json:"task_completion"` // unproven
	Verification    string                       `json:"verification"`    // unprovided
}

type TrajectoryKnowledgeScope struct {
	Kind string `json:"kind"` // recorded_turn, source_session, or native_relation
	ID   string `json:"id"`
}

type TrajectoryKnowledgeStep struct {
	CallEventID   string `json:"call_event_id"`
	ResultEventID string `json:"result_event_id"`
	Tool          string `json:"tool"`
	Outcome       string `json:"outcome"` // error or not_error; not_error is not task success
	OutcomeField  string `json:"outcome_field"`
}

type TrajectoryKnowledgeRelation struct {
	Kind          string `json:"kind"`
	NativeField   string `json:"native_field"`
	FromEventID   string `json:"from_event_id"`
	TargetEventID string `json:"target_event_id"`
}

// Source references retain coordinates and opaque identities, never a cached
// source body. Status is re-evaluated under current authorized source roots.
type TrajectoryKnowledgeSource struct {
	EventID   string              `json:"event_id"`
	SessionID string              `json:"session_id"`
	Agent     string              `json:"agent"`
	Source    TrajectorySourceRef `json:"source"`
	Status    string              `json:"status"` // valid, stale, missing, unavailable
}

type TrajectoryKnowledgeLink struct {
	Kind     string `json:"kind"` // verification_of or counterexample_to
	TargetID string `json:"target_id"`
	Status   string `json:"status"` // active, withdrawn, missing
}

// These values are supplied explicitly. Missing values remain missing. A
// reference string is retained locally without fetching it or running an eval.
type TrajectoryKnowledgeApplicability struct {
	Configuration  map[string]string `json:"configuration,omitempty"`
	Environment    map[string]string `json:"environment,omitempty"`
	Version        string            `json:"version,omitempty"`
	EvaluationRefs []string          `json:"evaluation_refs,omitempty"`
	ArtifactRefs   []string          `json:"artifact_refs,omitempty"`
}

type TrajectoryAnnotationParams struct {
	Operation     string                            `json:"operation"`
	ID            string                            `json:"id,omitempty"`
	Kind          string                            `json:"kind,omitempty"`
	SourceIDs     []string                          `json:"source_ids,omitempty"`
	Text          string                            `json:"text,omitempty"`
	TargetID      string                            `json:"target_id,omitempty"`
	Applicability *TrajectoryKnowledgeApplicability `json:"applicability,omitempty"`
}

type TrajectoryAnnotationResult struct {
	Record    *TrajectoryKnowledge `json:"record,omitempty"`
	DeletedID string               `json:"deleted_id,omitempty"`
	Revision  string               `json:"revision"`
}
