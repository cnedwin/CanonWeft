package domain

// CapabilityUse is a frozen execution identity, never a grant of current rights.
type CapabilityUse struct {
	AgentVersionID   string `json:"agent_version_id"`
	ManifestHash     string `json:"manifest_hash"`
	Mode             string `json:"mode"`
	EvaluationID     string `json:"evaluation_id,omitempty"`
	EvaluationStepID string `json:"evaluation_step_id,omitempty"`
	BindingID        string `json:"binding_id,omitempty"`
	QualificationID  string `json:"qualification_id,omitempty"`
	TaskKind         string `json:"task_kind,omitempty"`
	SuiteHash        string `json:"suite_hash,omitempty"`
	ConfigHash       string `json:"config_hash,omitempty"`
	MaxOutputTokens  int    `json:"max_output_tokens,omitempty"`
}
