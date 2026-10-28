package qualification

import (
	"novelstudio/internal/domain"
	"novelstudio/internal/models"
)

// Shared frozen wire contracts. Schedule is server-only and excluded from JSON.
type QualificationInput struct {
	Schedule        *ScheduleRef `json:"-"`
	OperationID     string       `json:"operation_id"`
	VersionID       string       `json:"version_id"`
	BaselineID      string       `json:"baseline_id"`
	BranchID        string       `json:"branch_id"`
	ExpectedIPHead  string       `json:"expected_ip_head"`
	ExpectedBinding string       `json:"expected_binding"`
	SuiteID         string       `json:"suite_id"`
	Role            string       `json:"role"`
	PriceID         string       `json:"price_id"`
	MaxOutputTokens int          `json:"max_output_tokens"`
}

type QualificationFrozen struct {
	Input             QualificationInput        `json:"input"`
	Suite             CatalogEntry              `json:"suite"`
	Candidate         domain.CapabilityRef      `json:"candidate"`
	CandidateManifest string                    `json:"candidate_manifest"`
	Baseline          domain.CapabilityRef      `json:"baseline"`
	BaselineManifest  string                    `json:"baseline_manifest"`
	NoMethod          domain.CapabilityRef      `json:"no_method"`
	Config            domain.ModelConfig        `json:"config"`
	Price             models.PriceCard          `json:"price"`
	RuntimeHash       string                    `json:"runtime_hash"`
	TokenProfile      string                    `json:"token_profile"`
	Estimate          int64                     `json:"estimate_micros"`
	QuoteProfile      string                    `json:"quote_profile"`
	RequestQuotes     []EvaluationRequestQuote  `json:"request_quotes"`
	Weekly            *QualificationWeeklyGrant `json:"weekly_grant,omitempty"`
}

type EvaluationRequestQuote struct {
	Arm         string `json:"arm"`
	CaseID      string `json:"case_id"`
	RequestHash string `json:"request_hash"`
	InputBytes  int    `json:"input_bytes"`
	Estimate    int64  `json:"estimate_micros"`
}

type EvaluationMetrics struct {
	Score              Score  `json:"score"`
	OutputHash         string `json:"output_hash"`
	Output             string `json:"output"`
	InputTokens        int64  `json:"input_tokens"`
	OutputTokens       int64  `json:"output_tokens"`
	Cost               int64  `json:"cost_micros"`
	DurationMS         int64  `json:"duration_ms"`
	DurationKnown      bool   `json:"duration_known"`
	ManualRepairs      int    `json:"manual_repairs"`
	UsageKnown         bool   `json:"usage_known"`
	CostKnown          bool   `json:"cost_known"`
	AccountingRevision int64  `json:"accounting_revision"`
	ReceiptCount       int    `json:"receipt_count"`
	Reason             string `json:"reason"`
}

type QualificationStep struct {
	ID        string            `json:"id"`
	Ordinal   int               `json:"ordinal"`
	Arm       string            `json:"arm"`
	CaseID    string            `json:"case_id"`
	Repeat    int               `json:"repeat"`
	ChildID   string            `json:"child_id"`
	ChildKind string            `json:"child_kind"`
	State     string            `json:"state"`
	Metrics   EvaluationMetrics `json:"metrics"`
}

type QualificationReport struct {
	RunID                 string                `json:"run_id"`
	Frozen                QualificationFrozen   `json:"frozen"`
	Steps                 []QualificationStep   `json:"steps"`
	Passed                bool                  `json:"passed"`
	ProfessionalQualified bool                  `json:"professional_qualified"`
	Cost                  int64                 `json:"cost_micros"`
	DurationMS            int64                 `json:"duration_ms"`
	ManualRepairs         int                   `json:"manual_repairs"`
	Boundary              string                `json:"boundary"`
	NovelSemantics        *NovelSemanticSummary `json:"novel_semantics,omitempty"`
}

const ExecutableApprovalPolicy = "owner-maintenance-separate-from-authored-skill-v1"

// ExecutableApproval records an owner's review of one frozen mechanical run.
// It neither approves authored content nor certifies professional quality.
type ExecutableApproval struct {
	ReportHash   string `json:"report_hash"`
	ManifestHash string `json:"manifest_hash"`
	RuntimeHash  string `json:"runtime_hash"`
	SuiteHash    string `json:"suite_hash"`
	Note         string `json:"note"`
	Policy       string `json:"policy"`
}

type ScheduleRef struct {
	ID       string
	Revision int64
	Week     string
}

type QualificationWeeklyGrant struct {
	ID         string `json:"id"`
	ScheduleID string `json:"schedule_id"`
	Revision   int64  `json:"revision"`
	Week       string `json:"week"`
	Cap        int64  `json:"cap_micros"`
}
