package qualificationledger

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"novelstudio/internal/domain"
	"novelstudio/internal/retention"
	"time"
)

const ExpiredLegacyRule = "legacy-evaluation-ledger-expired-v1"

var errExpiredLegacy = errors.New("expired legacy accounting evidence is invalid or unbound")

// ExpiredLegacyScope contains immutable owner/call identity and the original
// reservation. Current settlement state and charged amount are not frozen here.
type ExpiredLegacyScope struct {
	Project           string `json:"project"`
	Actor             string `json:"actor"`
	RunID             string `json:"run_id"`
	StepID            string `json:"step_id"`
	GenerationID      string `json:"generation_id"`
	CallID            string `json:"call_id"`
	Operation         string `json:"operation"`
	InputHash         string `json:"input_hash"`
	RunCreated        string `json:"run_created"`
	GenerationCreated string `json:"generation_created"`
	CallCreated       string `json:"call_created"`
	Reserved          int64  `json:"reserved_micros"`
	Currency          string `json:"currency"`
}

// ExpiredLegacyMetadata is the closed immutable subset of a retention summary.
// It intentionally has no raw text, provider configuration, result, bill state,
// charged amount or LegacyEvidenceHash (which would introduce a hash cycle).
type ExpiredLegacyMetadata struct {
	ConfigID       string                       `json:"config_id,omitempty"`
	ConfigRevision int64                        `json:"config_revision"`
	ConfigHash     string                       `json:"config_hash"`
	InputHash      string                       `json:"input_hash,omitempty"`
	CallID         string                       `json:"call_id,omitempty"`
	Estimate       int64                        `json:"estimate_micros"`
	Currency       string                       `json:"currency"`
	Suite          *retention.SuiteSummary      `json:"suite,omitempty"`
	Capability     *retention.CapabilitySummary `json:"capability,omitempty"`
}

// ExpiredLegacyProof records a conversion verified while original bytes still
// existed. OriginalProof is historical evidence, not a claim that RecordHash
// remains recomputable after erasure. This format never grants execution or
// qualification. Trusted retention evidence must bind ProjectionHash separately.
type ExpiredLegacyProof struct {
	Schema               int                   `json:"schema"`
	Rule                 string                `json:"rule"`
	OriginalProof        Proof                 `json:"original_proof"`
	RunFrozenHash        string                `json:"run_frozen_hash"`
	GenerationFrozenHash string                `json:"generation_frozen_hash"`
	Scope                ExpiredLegacyScope    `json:"scope"`
	RunMetadata          ExpiredLegacyMetadata `json:"run_metadata"`
	GenerationMetadata   ExpiredLegacyMetadata `json:"generation_metadata"`
	ProjectionHash       string                `json:"projection_hash"`
}

func expiredHash(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func expiredJSONHash(value any) string {
	raw, _ := json.Marshal(value)
	return expiredHash(raw)
}

func expiredProjectionHash(p ExpiredLegacyProof) string {
	return expiredJSONHash([]any{p.Schema, p.Rule, p.OriginalProof, p.RunFrozenHash, p.GenerationFrozenHash, p.Scope, p.RunMetadata, p.GenerationMetadata})
}

func expiredScope(r Record) (ExpiredLegacyScope, error) {
	scope := ExpiredLegacyScope{Project: r.Project, Actor: r.Actor, RunID: r.RunID, StepID: r.StepID, GenerationID: r.GenerationID, CallID: r.CallID, Operation: r.Operation, InputHash: r.InputHash, Reserved: r.Reserved, Currency: r.Currency}
	for _, id := range []string{r.Project, r.Actor, r.RunID, r.StepID, r.GenerationID, r.CallID} {
		if !idPattern.MatchString(id) {
			return scope, errExpiredLegacy
		}
	}
	if r.RunProject != r.Project || r.StepProject != r.Project || r.StepRun != r.RunID || r.GenerationProject != r.Project || r.CallProject != r.Project || r.GenerationActor != r.Actor || r.CallActor != r.Actor || r.Operation != "evaluation-"+r.StepID || (r.ChildID != "" && (r.ChildID != r.GenerationID || r.ChildKind != "generation")) || (r.ChildID == "" && r.ChildKind != "") || !hashPattern.MatchString(r.InputHash) || r.Reserved <= 0 || (r.Currency != "USD" && r.Currency != "CNY") {
		return scope, errExpiredLegacy
	}
	for _, clock := range []struct {
		raw    string
		target *string
	}{{r.RunCreated, &scope.RunCreated}, {r.GenerationCreated, &scope.GenerationCreated}, {r.CallCreated, &scope.CallCreated}} {
		parsed, err := ParseTime(clock.raw)
		if err != nil || parsed.IsZero() {
			return scope, errExpiredLegacy
		}
		*clock.target = parsed.Format(time.RFC3339Nano)
	}
	return scope, nil
}

func validateExpiredSource(origin string, p Proof, scope ExpiredLegacyScope) error {
	if p.Rule != "legacy-evaluation-ledger-v1" || !hashPattern.MatchString(p.RecordHash) || p.Source.Schema != 28 {
		return errExpiredLegacy
	}
	boundary, err := ParseTime(p.Source.Boundary)
	if err != nil || boundary.IsZero() {
		return errExpiredLegacy
	}
	switch origin {
	case "LEGACY_28_DB":
		if p.Source.Kind != "DATABASE" || p.Source.MigrationChecksum != BoundaryChecksum() || p.Source.ManifestHash != "" || p.Source.BoundaryClock != "MYSQL_DATETIME" || p.Source.Boundary != boundary.Format("2006-01-02 15:04:05.999999") {
			return errExpiredLegacy
		}
		for _, raw := range []string{scope.RunCreated, scope.GenerationCreated, scope.CallCreated} {
			clock, e := ParseTime(raw)
			if e != nil || !clock.Before(boundary) {
				return errExpiredLegacy
			}
		}
	case "LEGACY_28_ARCHIVE":
		if p.Source.Kind != "ARCHIVE" || p.Source.MigrationChecksum != "" || !hashPattern.MatchString(p.Source.ManifestHash) || p.Source.BoundaryClock != "UTC_SNAPSHOT" || p.Source.Boundary != boundary.Format(time.RFC3339Nano) {
			return errExpiredLegacy
		}
	default:
		return errExpiredLegacy
	}
	return nil
}

func originalExpiredMetadata(r Record) (ExpiredLegacyMetadata, ExpiredLegacyMetadata, error) {
	var run struct {
		Config   domain.ModelConfig     `json:"config"`
		Estimate int64                  `json:"estimate_micros"`
		Suite    retention.SuiteSummary `json:"suite"`
		Price    retention.PriceSummary `json:"price"`
	}
	var gen struct {
		Config     domain.ModelConfig           `json:"config"`
		InputHash  string                       `json:"input_hash"`
		Estimate   int64                        `json:"estimate_micros"`
		Price      retention.PriceSummary       `json:"price"`
		Capability *retention.CapabilitySummary `json:"capability"`
	}
	if json.Unmarshal(r.RunFrozen, &run) != nil || json.Unmarshal(r.GenerationFrozen, &gen) != nil {
		return ExpiredLegacyMetadata{}, ExpiredLegacyMetadata{}, errExpiredLegacy
	}
	a := ExpiredLegacyMetadata{ConfigID: run.Config.ID, ConfigRevision: run.Config.Revision, ConfigHash: expiredJSONHash(run.Config), Estimate: run.Estimate, Currency: run.Price.Currency, Suite: &run.Suite}
	b := ExpiredLegacyMetadata{ConfigID: gen.Config.ID, ConfigRevision: gen.Config.Revision, ConfigHash: expiredJSONHash(gen.Config), InputHash: gen.InputHash, CallID: r.CallID, Estimate: gen.Estimate, Currency: gen.Price.Currency, Capability: gen.Capability}
	return a, b, nil
}

func validateExpiredMetadata(p ExpiredLegacyProof) error {
	for _, m := range []ExpiredLegacyMetadata{p.RunMetadata, p.GenerationMetadata} {
		if (m.ConfigID != "" && !idPattern.MatchString(m.ConfigID)) || m.ConfigRevision < 0 || !hashPattern.MatchString(m.ConfigHash) || m.Estimate < 0 || m.Currency != p.Scope.Currency {
			return errExpiredLegacy
		}
	}
	run, gen := p.RunMetadata, p.GenerationMetadata
	if run.Suite == nil || (run.Suite.Policy != "repository-deterministic-v1" && run.Suite.Policy != "repository-deterministic-v2") || run.Suite.Version != 1 || !hashPattern.MatchString(run.Suite.Hash) || run.Capability != nil || run.InputHash != "" || run.CallID != "" || gen.Suite != nil || gen.Capability == nil {
		return errExpiredLegacy
	}
	trial := gen.Capability
	if gen.InputHash != p.Scope.InputHash || gen.CallID != p.Scope.CallID || gen.Estimate != p.Scope.Reserved || trial.Mode != "EVALUATION" || trial.EvaluationID != p.Scope.RunID || (trial.EvaluationStepID != "" && trial.EvaluationStepID != p.Scope.StepID) || trial.SuiteHash != run.Suite.Hash {
		return errExpiredLegacy
	}
	return nil
}

func expiredCurrentMapping(r Record, origin string, raw []byte) (Mapping, error) {
	result := Mapping{Origin: origin, Provenance: append([]byte(nil), raw...), Reserved: r.Reserved, Charged: r.Charged, State: r.State}
	if r.Charged < 0 {
		return Mapping{}, errExpiredLegacy
	}
	switch r.State {
	case "RESERVED", "DISPATCHED", "UNKNOWN":
		result.Held = r.Reserved
	case "SUCCEEDED", "INVALID_OUTPUT", "REJECTED", "CANCELLED", "RECONCILED":
	default:
		return Mapping{}, errExpiredLegacy
	}
	return result, nil
}

// ExpireLegacyEvidence validates v1 against original complete SQL values before
// emitting the distinct historical projection. The caller atomically stores its
// ProjectionHash in BOTH trusted retention summaries before erasing the values.
func ExpireLegacyEvidence(original Record, origin string, originalProof []byte) (Mapping, error) {
	if err := ValidateProof(original, origin, originalProof); err != nil {
		return Mapping{}, err
	}
	var old Proof
	if strictExpiredDecode(originalProof, &old) != nil {
		return Mapping{}, errExpiredLegacy
	}
	scope, err := expiredScope(original)
	if err != nil {
		return Mapping{}, err
	}
	if err = validateExpiredSource(origin, old, scope); err != nil {
		return Mapping{}, err
	}
	run, gen, err := originalExpiredMetadata(original)
	if err != nil {
		return Mapping{}, err
	}
	p := ExpiredLegacyProof{Schema: 1, Rule: ExpiredLegacyRule, OriginalProof: old, RunFrozenHash: expiredHash(original.RunFrozen), GenerationFrozenHash: expiredHash(original.GenerationFrozen), Scope: scope, RunMetadata: run, GenerationMetadata: gen}
	if err = validateExpiredMetadata(p); err != nil {
		return Mapping{}, err
	}
	p.ProjectionHash = expiredProjectionHash(p)
	raw, err := json.Marshal(p)
	if err != nil {
		return Mapping{}, errExpiredLegacy
	}
	return expiredCurrentMapping(original, origin, raw)
}

func expiredProjectionMetadata(p retention.ExpiredProjection) (ExpiredLegacyMetadata, error) {
	if p.Materialization != retention.InlineEnvelope || retention.ValidateProjection(p, retention.Inline) != nil || p.Summary.Price == nil || p.Summary.WeeklyGrant != nil {
		return ExpiredLegacyMetadata{}, errExpiredLegacy
	}
	s := p.Summary
	return ExpiredLegacyMetadata{ConfigID: s.ConfigID, ConfigRevision: s.ConfigRevision, ConfigHash: s.ConfigHash, InputHash: s.InputHash, CallID: s.CallID, Estimate: s.EstimateMicros, Currency: s.Price.Currency, Suite: s.Suite, Capability: s.Capability}, nil
}

func bindExpiredProjection(raw []byte, p retention.ExpiredProjection, project, table, id, original, conversion string) (ExpiredLegacyMetadata, error) {
	var stored retention.ExpiredProjection
	if strictExpiredDecode(raw, &stored) != nil || expiredJSONHash(stored) != expiredJSONHash(p) || p.OriginalPayloadHash != original || p.Summary.LegacyEvidenceHash != conversion {
		return ExpiredLegacyMetadata{}, errExpiredLegacy
	}
	key, err := retention.RowOwnerKey(table, map[string]string{"id": id})
	if err != nil {
		return ExpiredLegacyMetadata{}, errExpiredLegacy
	}
	ref, err := retention.ReferenceID(project, table, key, "frozen")
	if err != nil || p.ReferenceID != ref {
		return ExpiredLegacyMetadata{}, errExpiredLegacy
	}
	return expiredProjectionMetadata(p)
}

// ValidateExpiredLegacyEvidence refuses single-call validation: one shared run
// may contain many calls, and this API cannot infer that the supplied member is
// the complete set. Deprecated: use ValidateExpiredLegacyRun, including for a
// run whose locked reverse enumeration explicitly proves it has one call.
func ValidateExpiredLegacyEvidence(current Record, origin string, raw []byte, runProjection, generationProjection retention.ExpiredProjection) (Mapping, error) {
	return Mapping{}, ErrExpiredLegacyRunRequired
}

func validateExpiredLegacyMember(current Record, origin string, raw []byte, runProjection, generationProjection retention.ExpiredProjection, runRoot string) (Mapping, error) {
	var p ExpiredLegacyProof
	if strictExpiredDecode(raw, &p) != nil || p.Schema != 1 || p.Rule != ExpiredLegacyRule || !hashPattern.MatchString(p.RunFrozenHash) || !hashPattern.MatchString(p.GenerationFrozenHash) || !hashPattern.MatchString(p.ProjectionHash) || p.ProjectionHash != expiredProjectionHash(p) {
		return Mapping{}, errExpiredLegacy
	}
	scope, err := expiredScope(current)
	if err != nil || scope != p.Scope {
		return Mapping{}, errExpiredLegacy
	}
	if validateExpiredSource(origin, p.OriginalProof, scope) != nil || validateExpiredMetadata(p) != nil {
		return Mapping{}, errExpiredLegacy
	}
	run, err := bindExpiredProjection(current.RunFrozen, runProjection, scope.Project, "qualification_runs", scope.RunID, p.RunFrozenHash, runRoot)
	if err != nil {
		return Mapping{}, err
	}
	gen, err := bindExpiredProjection(current.GenerationFrozen, generationProjection, scope.Project, "generation_tasks", scope.GenerationID, p.GenerationFrozenHash, p.ProjectionHash)
	if err != nil {
		return Mapping{}, err
	}
	if expiredJSONHash(run) != expiredJSONHash(p.RunMetadata) || expiredJSONHash(gen) != expiredJSONHash(p.GenerationMetadata) {
		return Mapping{}, errExpiredLegacy
	}
	return expiredCurrentMapping(current, origin, raw)
}

func strictExpiredDecode(raw []byte, target any) error {
	if len(raw) == 0 || len(raw) > 64*1024 {
		return errExpiredLegacy
	}
	if uniqueExpiredJSON(raw) != nil {
		return errExpiredLegacy
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(target) != nil || decoder.Decode(new(any)) != io.EOF {
		return errExpiredLegacy
	}
	return nil
}
