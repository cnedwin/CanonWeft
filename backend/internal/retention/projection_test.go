package retention

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestExpiredProjectionDropsNestedPrivateTextWithoutForgingOriginalDigest(t *testing.T) {
	raw := []byte(`{"prompt":"PRIVATE_CANARY","messages":[{"content":"PRIVATE_CANARY"}],"frozen":{"capability":{"input":"PRIVATE_CANARY"}},"metrics":{"output":"PRIVATE_CANARY","reason":"PRIVATE_CANARY"},"summary":"PRIVATE_CANARY","unknown_future":{"context":"PRIVATE_CANARY"}}`)
	hash := sha256.Sum256(raw)
	original := hex.EncodeToString(hash[:])
	when := time.Date(2026, 10, 25, 1, 0, 0, 0, time.UTC)
	expiry := Expiry{ReferenceID: strings.Repeat("a", 64), Rule: "qualification_steps.metrics", PayloadHash: original, Revision: 2, Deadline: when, Storage: Inline}
	summary := EvidenceSummary{ContentScopeHash: strings.Repeat("b", 64)}
	projection, err := ProjectExpired(expiry, raw, summary, when)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(projection)
	if bytes.Contains(encoded, []byte("PRIVATE_CANARY")) || bytes.Contains(encoded, []byte(`"prompt"`)) || projection.OriginalPayloadHash != original || projection.State != Expired || projection.Summary.ContentScopeHash != summary.ContentScopeHash {
		t.Fatal("expiry leaked text or altered accounting/evidence", string(encoded))
	}
	if stableHash(projection) == original {
		t.Fatal("expired projection impersonates original signature")
	}
	if _, err = ProjectExpired(expiry, []byte("changed"), summary, when); err == nil {
		t.Fatal("concurrent payload replacement accepted")
	}
	if _, err = ProjectExpired(expiry, raw, summary, when.Add(-time.Nanosecond)); err == nil {
		t.Fatal("payload expired prematurely")
	}
	summary.State = "PRIVATE_CANARY"
	if _, err = ProjectExpired(expiry, raw, summary, when); err == nil {
		t.Fatal("free-form metadata state accepted")
	}
}

func TestExpiredBlobReferenceKeepsScalarHashAsNonReadableEvidence(t *testing.T) {
	hash := strings.Repeat("d", 64)
	payload := sha256.Sum256([]byte(hash))
	at := time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)
	expiry := Expiry{ReferenceID: strings.Repeat("e", 64), Rule: "model_calls.output_hash", PayloadHash: hex.EncodeToString(payload[:]), Revision: 1, Deadline: at, Storage: Blob}
	proof, err := ProjectExpired(expiry, []byte(hash), EvidenceSummary{ValueHash: hash}, at)
	if err != nil || proof.Summary.ValueHash != hash || proof.OriginalPayloadHash != expiry.PayloadHash {
		t.Fatal("scalar expiry lost original immutable reference", proof, err)
	}
	if _, err := ProjectExpired(expiry, []byte(hash), EvidenceSummary{ValueHash: strings.Repeat("f", 64)}, at); err == nil {
		t.Fatal("projection names a different blob")
	}
}

func TestSavedProjectionValidationAndFinancialSummary(t *testing.T) {
	p := ExpiredProjection{Materialization: InlineEnvelope, Schema: 1, State: Expired, ReferenceID: strings.Repeat("a", 64), OriginalPayloadHash: strings.Repeat("b", 64), ExpiredAt: time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC), Summary: EvidenceSummary{
		State: "UNKNOWN", Currency: "USD", InputHash: strings.Repeat("c", 64), ContentScopeHash: strings.Repeat("d", 64), EstimateMicros: 5,
		Price: &PriceSummary{Currency: "USD"}, Capability: &CapabilitySummary{Mode: "EVALUATION", EvaluationID: strings.Repeat("e", 32), EvaluationStepID: strings.Repeat("f", 32), SuiteHash: strings.Repeat("a", 64)},
		WeeklyGrant: &WeeklyGrantSummary{ID: strings.Repeat("b", 32), ScheduleID: strings.Repeat("c", 32), Revision: 1, Week: "2026-W43", CapMicros: 9},
	}}
	if err := ValidateProjection(p, Inline); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var restored ExpiredProjection
	if err = json.Unmarshal(raw, &restored); err != nil || ValidateProjection(restored, Inline) != nil || restored.Summary.WeeklyGrant.Week != "2026-W43" || restored.Summary.Capability.EvaluationID != p.Summary.Capability.EvaluationID || restored.Summary.State != "UNKNOWN" {
		t.Fatal("financial projection did not survive serialization")
	}
	for name, change := range map[string]func(*ExpiredProjection){
		"schema": func(p *ExpiredProjection) { p.Schema = 2 }, "state": func(p *ExpiredProjection) { p.State = Retained },
		"ref": func(p *ExpiredProjection) { p.ReferenceID = "bad" }, "original": func(p *ExpiredProjection) { p.OriginalPayloadHash = "bad" },
		"clock": func(p *ExpiredProjection) { p.ExpiredAt = time.Time{} }, "inline scalar": func(p *ExpiredProjection) { p.Summary.ValueHash = strings.Repeat("a", 64) },
		"negative": func(p *ExpiredProjection) { p.Summary.EstimateMicros = -1 }, "scope hash": func(p *ExpiredProjection) { p.Summary.ContentScopeHash = "PRIVATE_CANARY" },
		"mode": func(p *ExpiredProjection) { p.Summary.Capability.Mode = "PRIVATE_CANARY" }, "run": func(p *ExpiredProjection) { p.Summary.Capability.EvaluationID = "" },
		"price": func(p *ExpiredProjection) { p.Summary.Price.Currency = "PRIVATE_CANARY" }, "week": func(p *ExpiredProjection) { p.Summary.WeeklyGrant.Week = "2026-W54" },
		"grant revision": func(p *ExpiredProjection) { p.Summary.WeeklyGrant.Revision = 0 }, "grant cap": func(p *ExpiredProjection) { p.Summary.WeeklyGrant.CapMicros = 1000000000001 },
	} {
		t.Run(name, func(t *testing.T) {
			var candidate ExpiredProjection
			_ = json.Unmarshal(raw, &candidate)
			change(&candidate)
			if ValidateProjection(candidate, Inline) == nil {
				t.Fatal("invalid saved projection accepted")
			}
		})
	}
	if ValidateProjection(p, Blob) == nil || ValidateProjection(p, Metadata) == nil {
		t.Fatal("invalid storage semantics accepted")
	}
}

func TestSummaryKeepsOnlyRecognizedSuitePolicyAndDigest(t *testing.T) {
	for _, policy := range []string{"repository-deterministic-v1", "repository-deterministic-v2", "repository-deterministic-v3", "repository-semantic-review-v1"} {
		summary := EvidenceSummary{Suite: &SuiteSummary{Policy: policy, Version: 1, Hash: strings.Repeat("a", 64)}}
		if err := ValidateSummary(summary, Inline); err != nil {
			t.Fatal(policy, err)
		}
	}
	for _, suite := range []SuiteSummary{{Policy: "PRIVATE_CANARY", Version: 1, Hash: strings.Repeat("a", 64)}, {Policy: "repository-deterministic-v1", Version: 2, Hash: strings.Repeat("a", 64)}, {Policy: "repository-deterministic-v1", Version: 1, Hash: "bad"}} {
		if ValidateSummary(EvidenceSummary{Suite: &suite}, Inline) == nil {
			t.Fatal("unrecognized suite proof metadata accepted", suite)
		}
	}
}

func TestClearScalarRequiresExplicitSafeFieldAndMaterialization(t *testing.T) {
	for _, field := range []string{"projects.name", "model_calls.provider_receipt", "index_jobs.reason", "source_imports.filename", "working_chunks.source_name"} {
		if !ClearScalarAllowed(field) {
			t.Fatal("catalogued non-key text cannot be cleared", field)
		}
	}
	for _, field := range []string{"operations.operation_id", "branches.name", "capability_heads.capability_key", "skill_discovery_candidates.source_path", "model_calls.state", "model_calls.id", "generation_tasks.frozen", "projects.name.extra", "unknown.name"} {
		if ClearScalarAllowed(field) {
			t.Fatal("key, typed document or structural value could be cleared", field)
		}
	}
	raw := []byte("PRIVATE_FILENAME")
	sum := sha256.Sum256(raw)
	at := time.Now().UTC()
	e := Expiry{ReferenceID: strings.Repeat("a", 64), Rule: "source_imports.filename", PayloadHash: hex.EncodeToString(sum[:]), Revision: 1, Deadline: at, Storage: Inline}
	p, err := ProjectClearedScalar(e, raw, EvidenceSummary{}, at)
	if err != nil || p.Materialization != ClearScalar || ValidateProjection(p, Inline) != nil || !ClearScalarAllowed(e.Rule) {
		t.Fatal("safe clear proof rejected", p, err)
	}
	if ClearScalarAllowed("generation_tasks.frozen") {
		t.Fatal("frozen may not skip whole-body projection")
	}
	e.Rule = "generation_tasks.frozen"
	if _, err := ProjectClearedScalar(e, raw, EvidenceSummary{}, at); err == nil {
		t.Fatal("frozen CLEAR bypass accepted")
	}
	p.Materialization = BlobReference
	if ValidateProjection(p, Inline) == nil {
		t.Fatal("storage mismatch accepted")
	}
	p.Materialization = ""
	if ValidateProjection(p, Inline) == nil {
		t.Fatal("implicit materialization accepted")
	}
	p.Materialization = "ROW_REMOVED"
	if ValidateProjection(p, Inline) == nil {
		t.Fatal("unimplemented row removal accepted")
	}
}
