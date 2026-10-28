package retention

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"slices"
	"strings"
	"time"
)

// EvidenceSummary has no free-text field. Typed handlers derive these values
// from authoritative rows before replacing a private payload; no raw JSON map
// or dependency error can be copied into the retained evidence by this API.
type EvidenceSummary struct {
	// RecoveryManifestHash binds a remapped, still-retained inline manifest.
	// ValueHash remains the erased original object; this is not a new blob.
	RecoveryManifestHash    string                          `json:"recovery_manifest_hash,omitempty"`
	Event                   *EventExpirySummary             `json:"event,omitempty"`
	CapabilityContent       *CapabilityContentExpirySummary `json:"capability_content,omitempty"`
	Release                 *ReleaseExpirySummary           `json:"release,omitempty"`
	Schedule                *ScheduleExpirySummary          `json:"schedule,omitempty"`
	RecipeRun               *RecipeRunSummary               `json:"recipe_run,omitempty"`
	RecipeStep              *RecipeStepSummary              `json:"recipe_step,omitempty"`
	Context                 *ContextBindingSummary          `json:"context,omitempty"`
	Generation              *GenerationBindingSummary       `json:"generation,omitempty"`
	RequestIdentity         *RequestIdentitySummary         `json:"request_identity,omitempty"`
	QualificationAccounting *QualificationAccountingSummary `json:"qualification_accounting,omitempty"`
	Receipt                 *ReceiptSummary                 `json:"receipt,omitempty"`
	Reconciliation          *ReconciliationSummary          `json:"reconciliation,omitempty"`
	LegacyEvidenceHash      string                          `json:"legacy_evidence_hash,omitempty"`
	Suite                   *SuiteSummary                   `json:"suite,omitempty"`
	Capability              *CapabilitySummary              `json:"capability,omitempty"`
	WeeklyGrant             *WeeklyGrantSummary             `json:"weekly_grant,omitempty"`
	Price                   *PriceSummary                   `json:"price,omitempty"`
	EstimateMicros          int64                           `json:"estimate_micros"`
	ContentScopeHash        string                          `json:"content_scope_hash,omitempty"`
	ValueHash               string                          `json:"value_hash,omitempty"`
	CallID                  string                          `json:"call_id,omitempty"`
	ConfigID                string                          `json:"config_id,omitempty"`
	ConfigRevision          int64                           `json:"config_revision,omitempty"`
	ConfigHash              string                          `json:"config_hash,omitempty"`
	InputHash               string                          `json:"input_hash,omitempty"`
	OutputHash              string                          `json:"output_hash,omitempty"`
	ReportHash              string                          `json:"report_hash,omitempty"`
	State                   string                          `json:"state,omitempty"`
	Verdict                 string                          `json:"verdict,omitempty"`
	Currency                string                          `json:"currency,omitempty"`
	ReservedMicros          int64                           `json:"reserved_micros"`
	ChargedMicros           int64                           `json:"charged_micros"`
	InputTokens             int64                           `json:"input_tokens"`
	OutputTokens            int64                           `json:"output_tokens"`
	AccountingRevision      int64                           `json:"accounting_revision"`
}

type CapabilitySummary struct {
	Mode             string `json:"mode"`
	EvaluationID     string `json:"evaluation_id,omitempty"`
	EvaluationStepID string `json:"evaluation_step_id,omitempty"`
	SuiteHash        string `json:"suite_hash,omitempty"`
}
type WeeklyGrantSummary struct {
	ID         string `json:"id"`
	ScheduleID string `json:"schedule_id"`
	Revision   int64  `json:"revision"`
	Week       string `json:"week"`
	CapMicros  int64  `json:"cap_micros"`
}
type PriceSummary struct {
	Currency string `json:"currency"`
}

type SuiteSummary struct {
	Policy  string `json:"policy"`
	Version int    `json:"version"`
	Hash    string `json:"hash"`
}

var currencyCode = regexp.MustCompile(`^[A-Z]{3}$`)
var weekKey = regexp.MustCompile(`^\d{4}-W(0[1-9]|[1-4]\d|5[0-3])$`)

type ExpiredProjection struct {
	Materialization     Materialization `json:"materialization"`
	Schema              int             `json:"schema"`
	State               PayloadState    `json:"state"`
	ReferenceID         string          `json:"reference_id"`
	OriginalPayloadHash string          `json:"original_payload_hash"`
	ExpiredAt           time.Time       `json:"expired_at"`
	Summary             EvidenceSummary `json:"summary"`
}

type Materialization string

const (
	InlineEnvelope       Materialization = "INLINE_ENVELOPE"
	BlobReference        Materialization = "BLOB_REFERENCE"
	ClearScalar          Materialization = "CLEAR_SCALAR"
	AnonymizedRequestKey Materialization = "ANONYMIZED_REQUEST_KEY"
	AnonymizedLabel      Materialization = "ANONYMIZED_LABEL"
)

// ClearScalarAllowed applies only to non-key fields with no uniqueness-based
// identity. Short text cannot hold an envelope; its evidence remains separate.
func ClearScalarAllowed(rule string) bool {
	parts := strings.Split(rule, ".")
	if len(parts) != 2 {
		return false
	}
	column, ok := Column(parts[0], parts[1])
	if !ok || column.Privacy != PrivateText || column.Handling != ClearText || column.Shape != ShapeText {
		return false
	}
	for _, key := range PrimaryKey(parts[0]) {
		if key.Name == parts[1] {
			return false
		}
	}
	return true
}

func ProjectClearedScalar(expiry Expiry, raw []byte, summary EvidenceSummary, at time.Time) (ExpiredProjection, error) {
	if expiry.Storage != Inline || !ClearScalarAllowed(expiry.Rule) {
		return ExpiredProjection{}, ErrInvalidInventory
	}
	p, err := ProjectExpired(expiry, raw, summary, at)
	if err != nil {
		return ExpiredProjection{}, err
	}
	p.Materialization = ClearScalar
	return p, nil
}

func ProjectExpired(expiry Expiry, raw []byte, summary EvidenceSummary, at time.Time) (ExpiredProjection, error) {
	if (summary.Event != nil) != (expiry.Rule == "outbox.payload") {
		return ExpiredProjection{}, ErrInvalidInventory
	}
	if (summary.Schedule != nil) != (expiry.Rule == "qualification_schedules.config") {
		return ExpiredProjection{}, ErrInvalidInventory
	}
	if len(TransientColumns(expiry.Rule)) > 0 && !validTransientSummary(expiry.Rule, summary) {
		return ExpiredProjection{}, ErrInvalidInventory
	}
	rule, ok := ConcreteRule(expiry.Rule)
	if !ok || rule.Storage != expiry.Storage || (expiry.Storage != Inline && expiry.Storage != Blob) || !hash64.MatchString(expiry.ReferenceID) || expiry.Revision < 1 || expiry.Deadline.IsZero() || at.Before(expiry.Deadline) {
		return ExpiredProjection{}, ErrInvalidInventory
	}
	hash := sha256.Sum256(raw)
	if hex.EncodeToString(hash[:]) != expiry.PayloadHash {
		return ExpiredProjection{}, ErrInvalidInventory
	}
	if err := validateFinancialSource(expiry.Rule, raw, summary); err != nil {
		return ExpiredProjection{}, err
	}
	if err := validateRecipeProjectionSource(expiry.Rule, raw, summary); err != nil {
		return ExpiredProjection{}, err
	}
	if expiry.Rule == "context_packages.payload" {
		if summary.Context == nil {
			return ExpiredProjection{}, ErrInvalidInventory
		}
		expected, err := ContextEvidence(raw, summary.Context.ContentHash)
		if err != nil || *expected.Context != *summary.Context {
			return ExpiredProjection{}, ErrInvalidInventory
		}
	} else if summary.Context != nil {
		return ExpiredProjection{}, ErrInvalidInventory
	}
	if expiry.Storage == Blob {
		if !hash64.MatchString(string(raw)) || summary.ValueHash != string(raw) {
			return ExpiredProjection{}, ErrInvalidInventory
		}
	} else if summary.ValueHash != "" {
		return ExpiredProjection{}, ErrInvalidInventory
	}
	projection := ExpiredProjection{Schema: 1, State: Expired, ReferenceID: expiry.ReferenceID, OriginalPayloadHash: expiry.PayloadHash, ExpiredAt: at.UTC(), Summary: summary}
	projection.Materialization = InlineEnvelope
	if expiry.Storage == Blob {
		projection.Materialization = BlobReference
	}
	if err := ValidateProjection(projection, expiry.Storage); err != nil {
		return ExpiredProjection{}, err
	}
	return projection, nil
}

// ValidateProjection validates an already persisted projection without needing
// erased source bytes. The caller must independently bind its identity, clock,
// revision and digest to the exact owner row and retention evidence record.
func ValidateProjection(p ExpiredProjection, storage StorageKind) error {
	if (storage == Inline && p.Materialization != InlineEnvelope && p.Materialization != ClearScalar && p.Materialization != AnonymizedRequestKey && p.Materialization != AnonymizedLabel) || (storage == Blob && p.Materialization != BlobReference) || (p.Materialization == AnonymizedRequestKey) != (p.Summary.RequestIdentity != nil) {
		return ErrInvalidInventory
	}
	if p.Schema != 1 || p.State != Expired || !hash64.MatchString(p.ReferenceID) || !hash64.MatchString(p.OriginalPayloadHash) || p.ExpiredAt.IsZero() {
		return ErrInvalidInventory
	}
	return ValidateSummary(p.Summary, storage)
}

// ValidateSummary checks only the closed metadata vocabulary, not permissions
// or qualification. An EXPIRED summary can never authorize new execution.
// ValidateProjectionForRule adds the representation requirements that cannot be
// inferred from an erased source body. Archive and online readers use the exact
// persisted rule, not merely the storage kind.
func ValidateProjectionForRule(projection ExpiredProjection, key string) error {
	if projection.Summary.RecoveryManifestHash != "" && key != "artifact_releases.content_hash" {
		return ErrInvalidInventory
	}
	if (projection.Summary.Event != nil) != (key == "outbox.payload") {
		return ErrInvalidInventory
	}
	if (projection.Materialization == AnonymizedLabel) != AnonymizedLabelAllowed(key) {
		return ErrInvalidInventory
	}
	if (projection.Summary.Schedule != nil) != (key == "qualification_schedules.config") {
		return ErrInvalidInventory
	}
	if len(TransientColumns(key)) > 0 && !validTransientSummary(key, projection.Summary) {
		return ErrInvalidInventory
	}
	rule, ok := ConcreteRule(key)
	if !ok {
		return ErrInvalidInventory
	}
	if err := ValidateProjection(projection, rule.Storage); err != nil {
		return err
	}
	if projection.Summary.Generation != nil && key != "generation_tasks.frozen" {
		return ErrInvalidInventory
	}
	if (projection.Summary.Context != nil) != (key == "context_packages.payload") {
		return ErrInvalidInventory
	}
	if (projection.Summary.RecipeRun != nil) != (key == "recipe_runs.frozen") || (projection.Summary.RecipeStep != nil) != (key == "recipe_steps.frozen") {
		return ErrInvalidInventory
	}
	if projection.Materialization == AnonymizedRequestKey {
		if !requestKeyRule(key) {
			return ErrInvalidInventory
		}
		_, _, err := requestIdentityOwnerKeys(strings.SplitN(key, ".", 2)[0], projection.OriginalPayloadHash, *projection.Summary.RequestIdentity)
		return err
	}
	if requestKeyRule(key) {
		return ErrInvalidInventory
	}
	return validateFinancialRule(key, projection.Summary)
}

func ValidateSummary(summary EvidenceSummary, storage StorageKind) error {
	if summary.RecoveryManifestHash != "" && (storage != Blob || !hash64.MatchString(summary.RecoveryManifestHash)) {
		return ErrInvalidInventory
	}
	if summary.Event != nil && (storage != Inline || !validTransientSummary("outbox.payload", summary)) {
		return ErrInvalidInventory
	}
	if summary.Schedule != nil && (storage != Inline || !validTransientSummary("qualification_schedules.config", summary)) {
		return ErrInvalidInventory
	}
	if s := summary.RecipeRun; s != nil {
		if storage != Inline || !validRecipeRunSummary(*s) || summary != (EvidenceSummary{RecipeRun: s}) {
			return ErrInvalidInventory
		}
		return nil
	}
	if s := summary.RecipeStep; s != nil {
		if storage != Inline || !validRecipeStepSummary(*s) || summary != (EvidenceSummary{RecipeStep: s}) {
			return ErrInvalidInventory
		}
		return nil
	}
	if binding := summary.Context; binding != nil {
		if storage != Inline || !validContextBinding(*binding) || summary != (EvidenceSummary{Context: binding}) {
			return ErrInvalidInventory
		}
		return nil
	}
	if binding := summary.Generation; binding != nil {
		if storage != Inline || !validGenerationBinding(*binding) || summary.ConfigHash == "" || summary.Price == nil || summary.RequestIdentity != nil || summary.Receipt != nil || summary.Reconciliation != nil {
			return ErrInvalidInventory
		}
		if binding.AgentVersionID != "" && summary.Capability == nil {
			return ErrInvalidInventory
		}
	}
	if identity := summary.RequestIdentity; identity != nil {
		if storage != Inline || !validRequestIdentity(*identity) || summary != (EvidenceSummary{RequestIdentity: identity}) {
			return ErrInvalidInventory
		}
		return nil
	}
	if err := validateQualificationAccounting(summary); err != nil {
		return err
	}
	if summary.Receipt != nil && (!validReceiptSummary(*summary.Receipt) || summary.CallID != summary.Receipt.CallID) {
		return ErrInvalidInventory
	}
	if summary.Reconciliation != nil && (!validReconciliationSummary(*summary.Reconciliation) || summary.CallID != summary.Reconciliation.CallID) {
		return ErrInvalidInventory
	}
	if suite := summary.Suite; suite != nil {
		if suite.Version != 1 || !hash64.MatchString(suite.Hash) || !slices.Contains([]string{"repository-deterministic-v1", "repository-deterministic-v2", "repository-deterministic-v3", "repository-semantic-review-v1"}, suite.Policy) {
			return ErrInvalidInventory
		}
	}
	if (storage != Inline && storage != Blob) || (storage == Inline && summary.ValueHash != "") || (storage == Blob && !hash64.MatchString(summary.ValueHash)) {
		return ErrInvalidInventory
	}
	for _, id := range []string{summary.CallID, summary.ConfigID} {
		if id != "" && !id32.MatchString(id) {
			return ErrInvalidInventory
		}
	}
	for _, hash := range []string{summary.ValueHash, summary.InputHash, summary.OutputHash, summary.ReportHash, summary.ConfigHash, summary.ContentScopeHash, summary.LegacyEvidenceHash} {
		if hash != "" && !hash64.MatchString(hash) {
			return ErrInvalidInventory
		}
	}
	if summary.Currency != "" && !currencyCode.MatchString(summary.Currency) {
		return ErrInvalidInventory
	}
	if !slices.Contains([]string{"", "PREPARED", "QUEUED", "RUNNING", "RESERVED", "DISPATCHED", "UNKNOWN", "SUCCEEDED", "FAILED", "CANCELLED", "RECONCILED", "COMPLETED", "BLOCKED", "PASSED", "INVALID_OUTPUT", "REJECTED"}, summary.State) || !slices.Contains([]string{"", "PASS", "FAIL", "UNKNOWN", "PENDING"}, summary.Verdict) {
		return ErrInvalidInventory
	}
	for _, number := range []int64{summary.ConfigRevision, summary.ReservedMicros, summary.ChargedMicros, summary.InputTokens, summary.OutputTokens, summary.AccountingRevision, summary.EstimateMicros} {
		if number < 0 {
			return ErrInvalidInventory
		}
	}
	if price := summary.Price; price != nil && !currencyCode.MatchString(price.Currency) {
		return ErrInvalidInventory
	}
	if capability := summary.Capability; capability != nil {
		if !slices.Contains([]string{"TRIAL", "EVALUATION", "QUALIFIED"}, capability.Mode) {
			return ErrInvalidInventory
		}
		for _, id := range []string{capability.EvaluationID, capability.EvaluationStepID} {
			if id != "" && !id32.MatchString(id) {
				return ErrInvalidInventory
			}
		}
		if capability.SuiteHash != "" && !hash64.MatchString(capability.SuiteHash) {
			return ErrInvalidInventory
		}
		if capability.Mode == "EVALUATION" {
			if capability.EvaluationID == "" {
				return ErrInvalidInventory
			}
		} else if capability.EvaluationID != "" || capability.EvaluationStepID != "" {
			return ErrInvalidInventory
		}
	}
	if grant := summary.WeeklyGrant; grant != nil {
		if !id32.MatchString(grant.ID) || !id32.MatchString(grant.ScheduleID) || grant.Revision < 1 || !weekKey.MatchString(grant.Week) || grant.CapMicros < 0 || grant.CapMicros > 1000000000000 {
			return ErrInvalidInventory
		}
	}
	return nil
}
