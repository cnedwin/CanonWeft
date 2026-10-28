package tooltrial

import "novelstudio/internal/isolation"

// Output preserves the raw guest receipt alongside its display projection.
type Output struct {
	Result isolation.Result `json:"receipt"`
	Stdout string           `json:"stdout"`
	Stderr string           `json:"stderr"`
	Valid  bool             `json:"valid"`
}

// Frozen records the original input and executable identity, never authority
// to run the tool. Current access and qualification remain service decisions.
type Frozen struct {
	BindingID        string `json:"binding_id,omitempty"`
	QualificationID  string `json:"qualification_id,omitempty"`
	TaskKind         string `json:"task_kind,omitempty"`
	EvaluationID     string `json:"evaluation_id,omitempty"`
	EvaluationStepID string `json:"evaluation_step_id,omitempty"`
	VersionID        string `json:"version_id"`
	VersionHash      string `json:"version_hash"`
	ManifestHash     string `json:"manifest_hash"`
	PackageID        string `json:"package_id"`
	PackageHash      string `json:"package_hash"`
	ImageHash        string `json:"image_hash"`
	InputHash        string `json:"input_hash"`
}
