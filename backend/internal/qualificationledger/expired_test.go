package qualificationledger

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"novelstudio/internal/domain"
	"novelstudio/internal/retention"
	"strings"
	"testing"
	"time"
)

func expiredDigest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func expiredFixture(t *testing.T, sourceKind string) (Record, Mapping, retention.ExpiredProjection, retention.ExpiredProjection) {
	t.Helper()
	r := legacyRecord()
	r.RunFrozen = append([]byte(" \n"), []byte(strings.TrimSuffix(string(r.RunFrozen), "}")+`,"input":{"prompt":"PRIVATE_CANARY_RUN"}}`)...)
	r.GenerationFrozen = []byte(strings.TrimSuffix(string(r.GenerationFrozen), "}") + `,"private_context":"PRIVATE_CANARY_GENERATION"}`)
	source := Source{Schema: 28, Kind: sourceKind, Boundary: "2026-10-25T00:00:00Z"}
	if sourceKind == "DATABASE" {
		source.MigrationChecksum = BoundaryChecksum()
	} else {
		source.ManifestHash = strings.Repeat("3", 64)
	}
	old, err := Convert(r, source)
	if err != nil {
		t.Fatal(err)
	}
	converted, err := ExpireLegacyEvidence(r, old.Origin, old.Provenance)
	if err != nil {
		t.Fatal(err)
	}
	var proof ExpiredLegacyProof
	if err = json.Unmarshal(converted.Provenance, &proof); err != nil {
		t.Fatal(err)
	}
	config, _ := json.Marshal(domain.ModelConfig{})
	common := retention.EvidenceSummary{ConfigHash: expiredDigest(config), Price: &retention.PriceSummary{Currency: "USD"}, LegacyEvidenceHash: proof.ProjectionHash}
	runSummary := common
	sealed, sealErr := ExpireLegacyRunEvidence([]OriginalLegacyEvidence{{Record: r, Origin: old.Origin, OriginalProof: old.Provenance}})
	if sealErr != nil {
		t.Fatal(sealErr)
	}
	runSummary.LegacyEvidenceHash = sealed.RootHash
	runSummary.Suite = &retention.SuiteSummary{Policy: "repository-deterministic-v2", Version: 1, Hash: strings.Repeat("2", 64)}
	runSummary.State = "CANCELLED"
	genSummary := common
	genSummary.InputHash, genSummary.EstimateMicros, genSummary.CallID = r.InputHash, 50, r.CallID
	genSummary.State = "UNKNOWN"
	genSummary.Capability = &retention.CapabilitySummary{Mode: "EVALUATION", EvaluationID: r.RunID, SuiteHash: strings.Repeat("2", 64)}
	makeProjection := func(table, id string, raw []byte, summary retention.EvidenceSummary) retention.ExpiredProjection {
		key, e := retention.RowOwnerKey(table, map[string]string{"id": id})
		if e != nil {
			t.Fatal(e)
		}
		ref, e := retention.ReferenceID(r.Project, table, key, "frozen")
		if e != nil {
			t.Fatal(e)
		}
		when := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
		p, e := retention.ProjectExpired(retention.Expiry{ReferenceID: ref, Rule: table + ".frozen", PayloadHash: expiredDigest(raw), Revision: 1, Deadline: when, Storage: retention.Inline}, raw, summary, when)
		if e != nil {
			t.Fatal(e)
		}
		return p
	}
	run := makeProjection("qualification_runs", r.RunID, r.RunFrozen, runSummary)
	gen := makeProjection("generation_tasks", r.GenerationID, r.GenerationFrozen, genSummary)
	r.RunFrozen, _ = json.Marshal(run)
	r.GenerationFrozen, _ = json.Marshal(gen)
	return r, converted, run, gen
}

func TestExpiredLegacyPreservesVerifiedHistoryWithoutFrozenText(t *testing.T) {
	for _, kind := range []string{"DATABASE", "ARCHIVE"} {
		t.Run(kind, func(t *testing.T) {
			r, m, run, gen := expiredFixture(t, kind)
			if bytes.Contains(m.Provenance, []byte("PRIVATE_CANARY")) || bytes.Contains(m.Provenance, []byte(`"prompt"`)) || bytes.Contains(m.Provenance, []byte(`"private_context"`)) {
				t.Fatal("expired proof retained raw frozen content")
			}
			var proof ExpiredLegacyProof
			if json.Unmarshal(m.Provenance, &proof) != nil || proof.Rule != ExpiredLegacyRule || proof.OriginalProof.Rule != "legacy-evaluation-ledger-v1" || proof.RunFrozenHash != run.OriginalPayloadHash || proof.GenerationFrozenHash != gen.OriginalPayloadHash {
				t.Fatal("original proof and exact SQL byte hashes not preserved")
			}
			current, err := validateExpiredSingleFixture(r, m.Origin, m.Provenance, run, gen)
			if err != nil || current.Held != 50 || current.Charged != 0 || current.Origin != m.Origin {
				t.Fatal("UNKNOWN accounting changed", current, err)
			}
			if ValidateProof(r, m.Origin, m.Provenance) == nil {
				t.Fatal("expired evidence impersonated an original v1 proof")
			}
			r.State, r.Charged = "RECONCILED", 71
			late, err := validateExpiredSingleFixture(r, m.Origin, m.Provenance, run, gen)
			if err != nil || late.Held != 0 || late.Charged != 71 || !bytes.Equal(late.Provenance, m.Provenance) {
				t.Fatal("legitimate late invoice invalidated immutable history", late, err)
			}
		})
	}
}

func TestExpiredLegacyRequiresOriginalValidatedProof(t *testing.T) {
	r := legacyRecord()
	source := Source{Schema: 28, Kind: "DATABASE", Boundary: "2026-10-25T00:00:00Z", MigrationChecksum: BoundaryChecksum()}
	m, err := Convert(r, source)
	if err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*Record){
		"native": func(r *Record) { r.RunFrozen = []byte(strings.Replace(string(r.RunFrozen), "v2", "v3", 1)) },
		"weekly": func(r *Record) {
			r.RunFrozen = []byte(strings.TrimSuffix(string(r.RunFrozen), "}") + `,"weekly_grant":{"id":"native"}}`)
		},
		"actor":    func(r *Record) { r.GenerationActor = strings.Repeat("a", 32) },
		"step":     func(r *Record) { r.StepRun = strings.Repeat("a", 32) },
		"boundary": func(r *Record) { r.RunCreated = "2026-10-26T00:00:00Z" },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := r
			change(&candidate)
			if _, e := ExpireLegacyEvidence(candidate, m.Origin, m.Provenance); e == nil {
				t.Fatal("unverified original accepted")
			}
		})
	}
	if _, err = ExpireLegacyEvidence(r, "NATIVE_30", m.Provenance); err == nil {
		t.Fatal("native origin expired as legacy")
	}
	if _, err = ExpireLegacyEvidence(r, m.Origin, append(m.Provenance, []byte(" {}")...)); err == nil {
		t.Fatal("ambiguous original provenance accepted")
	}
}

func TestExpiredLegacyRejectsChangedScopeAndUnboundExpiryClaims(t *testing.T) {
	r, m, run, gen := expiredFixture(t, "DATABASE")
	for name, change := range map[string]func(*Record){
		"project":         func(r *Record) { r.Project = strings.Repeat("b", 32) },
		"actor":           func(r *Record) { r.CallActor = strings.Repeat("a", 32) },
		"run":             func(r *Record) { r.RunID = strings.Repeat("a", 32) },
		"step":            func(r *Record) { r.StepID = strings.Repeat("a", 32) },
		"generation":      func(r *Record) { r.GenerationID = strings.Repeat("a", 32) },
		"call":            func(r *Record) { r.CallID = strings.Repeat("a", 32) },
		"operation":       func(r *Record) { r.Operation = "generation-other" },
		"input":           func(r *Record) { r.InputHash = strings.Repeat("a", 64) },
		"time":            func(r *Record) { r.CallCreated = "2026-10-24T00:00:03Z" },
		"reserve":         func(r *Record) { r.Reserved = 49 },
		"currency":        func(r *Record) { r.Currency = "CNY" },
		"child":           func(r *Record) { r.ChildID = strings.Repeat("a", 32) },
		"bad bill":        func(r *Record) { r.State = "FAILED" },
		"negative bill":   func(r *Record) { r.Charged = -1 },
		"missing payload": func(r *Record) { r.RunFrozen = nil },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := r
			change(&candidate)
			if _, e := validateExpiredSingleFixture(candidate, m.Origin, m.Provenance, run, gen); e == nil {
				t.Fatal("changed current record accepted")
			}
		})
	}
	for _, state := range []string{"RESERVED", "DISPATCHED", "UNKNOWN"} {
		r.State = state
		current, e := validateExpiredSingleFixture(r, m.Origin, m.Provenance, run, gen)
		if e != nil || current.Held != 50 {
			t.Fatal("live uncertainty hold released", state, current, e)
		}
	}
	if _, err := validateExpiredSingleFixture(r, "NATIVE_29", m.Provenance, run, gen); err == nil {
		t.Fatal("native origin disguised as expired legacy")
	}
	bad := run
	bad.OriginalPayloadHash = strings.Repeat("a", 64)
	if _, err := validateExpiredSingleFixture(r, m.Origin, m.Provenance, bad, gen); err == nil {
		t.Fatal("unbound original SQL hash accepted")
	}
	bad = run
	bad.Summary.LegacyEvidenceHash = ""
	if _, err := validateExpiredSingleFixture(r, m.Origin, m.Provenance, bad, gen); err == nil {
		t.Fatal("self-asserted expired projection accepted")
	}
	bad = run
	bad.Materialization = retention.ClearScalar
	if _, err := validateExpiredSingleFixture(r, m.Origin, m.Provenance, bad, gen); err == nil {
		t.Fatal("non-envelope frozen expiry accepted")
	}
	if _, err := validateExpiredSingleFixture(r, m.Origin, m.Provenance, gen, run); err == nil {
		t.Fatal("owner projections swapped")
	}
}

func TestExpiredLegacyRejectsAmbiguousJSONAndExternalBindingReplacement(t *testing.T) {
	r, m, run, gen := expiredFixture(t, "DATABASE")
	duplicate := []byte(strings.Replace(string(m.Provenance), `"rule":"`+ExpiredLegacyRule+`"`, `"rule":"PRIVATE_CANARY","rule":"`+ExpiredLegacyRule+`"`, 1))
	if _, err := validateExpiredSingleFixture(r, m.Origin, duplicate, run, gen); err == nil {
		t.Fatal("duplicate proof fields hid raw content")
	}
	caseAlias := []byte(strings.Replace(string(m.Provenance), `"rule":"`+ExpiredLegacyRule+`"`, `"RULE":"PRIVATE_CANARY","rule":"`+ExpiredLegacyRule+`"`, 1))
	if _, err := validateExpiredSingleFixture(r, m.Origin, caseAlias, run, gen); err == nil {
		t.Fatal("case-insensitive proof alias hid raw content")
	}
	duplicateScope := r
	duplicateScope.RunFrozen = []byte(strings.Replace(string(r.RunFrozen), `"state":"EXPIRED"`, `"state":"PRIVATE_CANARY","state":"EXPIRED"`, 1))
	if _, err := validateExpiredSingleFixture(duplicateScope, m.Origin, m.Provenance, run, gen); err == nil {
		t.Fatal("duplicate envelope fields hid raw content")
	}
	var p ExpiredLegacyProof
	if json.Unmarshal(m.Provenance, &p) != nil {
		t.Fatal("proof fixture invalid")
	}
	p.OriginalProof.RecordHash = strings.Repeat("a", 64)
	p.ProjectionHash = expiredProjectionHash(p)
	forged, _ := json.Marshal(p)
	if _, err := validateExpiredSingleFixture(r, m.Origin, forged, run, gen); err == nil {
		t.Fatal("rehashed historical proof escaped trusted conversion binding")
	}
	// Altering just the stored JSON does not alter the separately verified
	// retention projection passed by the trusted owner.
	mutatedRun, mutatedGen := run, gen
	mutatedRun.Summary.LegacyEvidenceHash, mutatedGen.Summary.LegacyEvidenceHash = p.ProjectionHash, p.ProjectionHash
	r.RunFrozen, _ = json.Marshal(mutatedRun)
	r.GenerationFrozen, _ = json.Marshal(mutatedGen)
	if _, err := validateExpiredSingleFixture(r, m.Origin, forged, run, gen); err == nil {
		t.Fatal("self-asserted conversion replaced external evidence")
	}
}

func TestExpiredLegacyBindsExactSQLBytesAndStableCurrentAccounting(t *testing.T) {
	r := legacyRecord()
	source := Source{Schema: 28, Kind: "DATABASE", Boundary: "2026-10-25T00:00:00Z", MigrationChecksum: BoundaryChecksum()}
	old, err := Convert(r, source)
	if err != nil {
		t.Fatal(err)
	}
	a, err := ExpireLegacyEvidence(r, old.Origin, old.Provenance)
	if err != nil {
		t.Fatal(err)
	}
	r.RunFrozen = append([]byte(" \n\t"), r.RunFrozen...)
	b, err := ExpireLegacyEvidence(r, old.Origin, old.Provenance)
	if err != nil {
		t.Fatal("canonical v1 proof should accept whitespace-equivalent original", err)
	}
	var first, second ExpiredLegacyProof
	if json.Unmarshal(a.Provenance, &first) != nil || json.Unmarshal(b.Provenance, &second) != nil {
		t.Fatal("invalid proof")
	}
	if first.OriginalProof.RecordHash != second.OriginalProof.RecordHash || first.RunFrozenHash == second.RunFrozenHash || first.ProjectionHash == second.ProjectionHash {
		t.Fatal("canonical old hash was substituted for exact original SQL digest")
	}
	current, m, run, gen := expiredFixture(t, "DATABASE")
	current.RunCreated = "2026-10-24 00:00:00"
	current.GenerationCreated = "2026-10-24 00:00:01.000000"
	current.CallCreated = "2026-10-24 00:00:02"
	current.Charged = 7
	checked, err := validateExpiredSingleFixture(current, m.Origin, m.Provenance, run, gen)
	if err != nil || checked.Held != 50 || checked.Charged != 7 {
		t.Fatal("partial known fee or SQL clock representation changed retained UNKNOWN hold", checked, err)
	}
}

func TestExpiredLegacyRejectsAlteredFrozenMetadata(t *testing.T) {
	for name, change := range map[string]func(*retention.ExpiredProjection, *retention.ExpiredProjection){
		"suite":  func(r, g *retention.ExpiredProjection) { r.Summary.Suite.Hash = strings.Repeat("a", 64) },
		"native": func(r, g *retention.ExpiredProjection) { r.Summary.Suite.Policy = "repository-deterministic-v3" },
		"weekly": func(r, g *retention.ExpiredProjection) {
			r.Summary.WeeklyGrant = &retention.WeeklyGrantSummary{ID: strings.Repeat("a", 32), ScheduleID: strings.Repeat("b", 32), Revision: 1, Week: "2026-W43", CapMicros: 100}
		},
		"config":             func(r, g *retention.ExpiredProjection) { g.Summary.ConfigHash = strings.Repeat("a", 64) },
		"config revision":    func(r, g *retention.ExpiredProjection) { r.Summary.ConfigRevision = 2 },
		"estimate":           func(r, g *retention.ExpiredProjection) { g.Summary.EstimateMicros = 49 },
		"currency":           func(r, g *retention.ExpiredProjection) { g.Summary.Price.Currency = "CNY" },
		"capability missing": func(r, g *retention.ExpiredProjection) { g.Summary.Capability = nil },
		"mode": func(r, g *retention.ExpiredProjection) {
			g.Summary.Capability.Mode = "QUALIFIED"
			g.Summary.Capability.EvaluationID = ""
		},
		"run": func(r, g *retention.ExpiredProjection) { g.Summary.Capability.EvaluationID = strings.Repeat("a", 32) },
		"step": func(r, g *retention.ExpiredProjection) {
			g.Summary.Capability.EvaluationStepID = strings.Repeat("a", 32)
		},
		"suite binding": func(r, g *retention.ExpiredProjection) { g.Summary.Capability.SuiteHash = strings.Repeat("a", 64) },
		"input":         func(r, g *retention.ExpiredProjection) { g.Summary.InputHash = strings.Repeat("a", 64) },
		"call":          func(r, g *retention.ExpiredProjection) { g.Summary.CallID = strings.Repeat("a", 32) },
	} {
		t.Run(name, func(t *testing.T) {
			r, m, run, gen := expiredFixture(t, "DATABASE")
			change(&run, &gen)
			r.RunFrozen, _ = json.Marshal(run)
			r.GenerationFrozen, _ = json.Marshal(gen)
			if _, err := validateExpiredSingleFixture(r, m.Origin, m.Provenance, run, gen); err == nil {
				t.Fatal("changed immutable frozen metadata accepted")
			}
		})
	}
}

// This helper is only for fixtures that explicitly create exactly one original
// call. Production must pass the complete reverse-enumerated run to the batch API.
func validateExpiredSingleFixture(r Record, origin string, raw []byte, run, gen retention.ExpiredProjection) (Mapping, error) {
	result, err := ValidateExpiredLegacyRun([]ExpiredLegacyCall{{Record: r, Origin: origin, Proof: raw, GenerationProjection: gen}}, run)
	if err != nil {
		return Mapping{}, err
	}
	return result[0], nil
}
