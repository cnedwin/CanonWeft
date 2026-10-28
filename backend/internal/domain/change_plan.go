package domain

// Change plans are proposed edits and historical impact evidence, never grants
// to overwrite author content or publish changes.
type ChangePlan struct {
	AdditionalReview       []ChangeReviewScope    `json:"additional_review,omitempty"`
	Recovery               []ChangePlanRecovery   `json:"recovery,omitempty"`
	GraphSnapshot          *DependencyGraph       `json:"graph_snapshot,omitempty"`
	SourceEventID          string                 `json:"source_event_id,omitempty"`
	SourceEventKind        string                 `json:"source_event_kind,omitempty"`
	SourceState            string                 `json:"source_state,omitempty"`
	ReleaseID              string                 `json:"release_id,omitempty"`
	Differences            []DependencyDifference `json:"differences,omitempty"`
	ID                     string                 `json:"id"`
	BranchID               string                 `json:"branch_id"`
	CandidateID            string                 `json:"candidate_id"`
	BaseHead               string                 `json:"base_head"`
	GraphHash              string                 `json:"graph_hash"`
	Changes                []DependencyChange     `json:"changes"`
	Items                  []ImpactItem           `json:"items"`
	Untracked              []DependencyRef        `json:"untracked"`
	OutsideRecordedClosure []DependencyRef        `json:"outside_recorded_closure"`
	SemanticReview         string                 `json:"semantic_review"`
	Stale                  bool                   `json:"stale"`
}

// Recovery records historical attribution without a live outbox reference or
// permission to replay the original event or its review runs.
type ChangePlanRecovery struct {
	ProjectID       string `json:"project_id"`
	PlanID          string `json:"plan_id"`
	ContentHash     string `json:"content_hash"`
	SourceEventID   string `json:"source_event_id,omitempty"`
	SourceEventKind string `json:"source_event_kind,omitempty"`
}

type ChangedField struct {
	Name   string `json:"name"`
	Before string `json:"before"`
	After  string `json:"after"`
}

type DependencyDifference struct {
	Ref    DependencyRef  `json:"ref"`
	Name   string         `json:"name"`
	Fields []ChangedField `json:"fields"`
}

type ChangeReviewTarget struct {
	BodyHash     string            `json:"body_hash,omitempty"`
	Ref          DependencyRef     `json:"ref"`
	Name         string            `json:"name"`
	Text         string            `json:"text"`
	Policy       *KnowledgePolicy  `json:"policy,omitempty"`
	Dependencies *DependencyPolicy `json:"dependencies,omitempty"`
}

type ChangeReviewContext struct {
	FocusTargets  []DependencyRef      `json:"focus_targets,omitempty"`
	FocusBranchID string               `json:"focus_branch_id,omitempty"`
	FocusBaseHead string               `json:"focus_base_head,omitempty"`
	Plan          ChangePlan           `json:"plan"`
	Targets       []ChangeReviewTarget `json:"targets"`
}
