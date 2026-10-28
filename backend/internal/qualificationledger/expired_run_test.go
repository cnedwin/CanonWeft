package qualificationledger

import (
	"encoding/json"
	"novelstudio/internal/retention"
	"strings"
	"testing"
	"time"
)

func batchOriginals(t *testing.T) []OriginalLegacyEvidence {
	t.Helper()
	a := legacyRecord()
	b := legacyRecord()
	b.StepID, b.GenerationID, b.CallID = strings.Repeat("1", 32), strings.Repeat("2", 32), strings.Repeat("3", 32)
	b.ChildID, b.Operation = b.GenerationID, "evaluation-"+b.StepID
	b.Reserved = 75
	b.GenerationFrozen = []byte(strings.Replace(string(b.GenerationFrozen), `"estimate_micros":50`, `"estimate_micros":75`, 1))
	source := Source{Schema: 28, Kind: "DATABASE", Boundary: "2026-10-25T00:00:00Z", MigrationChecksum: BoundaryChecksum()}
	result := []OriginalLegacyEvidence{}
	for _, record := range []Record{a, b} {
		old, err := Convert(record, source)
		if err != nil {
			t.Fatal(err)
		}
		result = append(result, OriginalLegacyEvidence{Record: record, Origin: old.Origin, OriginalProof: old.Provenance})
	}
	return result
}

func batchExpired(t *testing.T) ([]ExpiredLegacyCall, retention.ExpiredProjection, ExpiredLegacyRunEvidence) {
	t.Helper()
	originals := batchOriginals(t)
	converted, err := ExpireLegacyRunEvidence(originals)
	if err != nil {
		t.Fatal(err)
	}
	byCall := map[string]Mapping{}
	for _, m := range converted.Calls {
		byCall[m.CallID] = m.Mapping
	}
	makeProjection := func(record Record, table, id string, raw []byte, summary retention.EvidenceSummary) retention.ExpiredProjection {
		owner, e := retention.RowOwnerKey(table, map[string]string{"id": id})
		if e != nil {
			t.Fatal(e)
		}
		ref, e := retention.ReferenceID(record.Project, table, owner, "frozen")
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
	var runProjection retention.ExpiredProjection
	result := []ExpiredLegacyCall{}
	for i, original := range originals {
		mapping := byCall[original.Record.CallID]
		var proof ExpiredLegacyProof
		if json.Unmarshal(mapping.Provenance, &proof) != nil {
			t.Fatal("bad batch proof")
		}
		if i == 0 {
			meta := proof.RunMetadata
			runProjection = makeProjection(original.Record, "qualification_runs", original.Record.RunID, original.Record.RunFrozen, retention.EvidenceSummary{LegacyEvidenceHash: converted.RootHash, ConfigID: meta.ConfigID, ConfigRevision: meta.ConfigRevision, ConfigHash: meta.ConfigHash, EstimateMicros: meta.Estimate, Price: &retention.PriceSummary{Currency: meta.Currency}, Suite: meta.Suite, State: "CANCELLED"})
		}
		meta := proof.GenerationMetadata
		gen := makeProjection(original.Record, "generation_tasks", original.Record.GenerationID, original.Record.GenerationFrozen, retention.EvidenceSummary{LegacyEvidenceHash: proof.ProjectionHash, ConfigID: meta.ConfigID, ConfigRevision: meta.ConfigRevision, ConfigHash: meta.ConfigHash, InputHash: meta.InputHash, CallID: meta.CallID, EstimateMicros: meta.Estimate, Price: &retention.PriceSummary{Currency: meta.Currency}, Capability: meta.Capability, State: "UNKNOWN"})
		current := original.Record
		current.RunFrozen, _ = json.Marshal(runProjection)
		current.GenerationFrozen, _ = json.Marshal(gen)
		result = append(result, ExpiredLegacyCall{Record: current, Origin: mapping.Origin, Proof: mapping.Provenance, GenerationProjection: gen})
	}
	return result, runProjection, converted
}

func TestExpiredLegacyRunBindsMultipleCallsAndIgnoresInputOrder(t *testing.T) {
	calls, run, converted := batchExpired(t)
	if len(converted.Calls) != 2 {
		t.Fatal("batch did not retain both calls")
	}
	validated, err := ValidateExpiredLegacyRun(calls, run)
	if err != nil || len(validated) != 2 || validated[0].Held != 50 || validated[1].Held != 75 {
		t.Fatal("multi-call run cannot retain its shared frozen and independent UNKNOWN holds", validated, err)
	}
	flipped := []ExpiredLegacyCall{calls[1], calls[0]}
	if _, err = ValidateExpiredLegacyRun(flipped, run); err != nil {
		t.Fatal("record iteration order changed the run commitment", err)
	}
	originals := batchOriginals(t)
	reversed, err := ExpireLegacyRunEvidence([]OriginalLegacyEvidence{originals[1], originals[0]})
	if err != nil || reversed.RootHash != converted.RootHash {
		t.Fatal("conversion order changed the complete call-set root", err)
	}
	// A terminal parent's billing receipts remain independently reconcilable.
	calls[1].Record.State, calls[1].Record.Charged = "RECONCILED", 91
	validated, err = ValidateExpiredLegacyRun(calls, run)
	if err != nil || validated[0].Held != 50 || validated[1].Held != 0 || validated[1].Charged != 91 {
		t.Fatal("late settlement changed root or released another call's hold", validated, err)
	}
	if _, err = ValidateExpiredLegacyEvidence(calls[0].Record, calls[0].Origin, calls[0].Proof, run, calls[0].GenerationProjection); err == nil {
		t.Fatal("public single-call validator silently assumed a complete run")
	}
}

func TestExpiredLegacyRunRejectsMissingDuplicateForeignOrNewCalls(t *testing.T) {
	calls, run, _ := batchExpired(t)
	for name, candidate := range map[string][]ExpiredLegacyCall{
		"missing": calls[:1], "duplicate": {calls[0], calls[0]}, "extra duplicate": {calls[0], calls[1], calls[0]}, "empty": {},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ValidateExpiredLegacyRun(candidate, run); err == nil {
				t.Fatal("non-complete unique run set accepted")
			}
		})
	}
	foreign := append([]ExpiredLegacyCall(nil), calls...)
	foreign[1].Record.RunID = strings.Repeat("a", 32)
	if _, err := ValidateExpiredLegacyRun(foreign, run); err == nil {
		t.Fatal("cross-run record accepted")
	}
	added := append([]ExpiredLegacyCall(nil), calls...)
	added[1].Record.CallID = strings.Repeat("9", 32)
	if _, err := ValidateExpiredLegacyRun(added, run); err == nil {
		t.Fatal("call-set mutation after conversion accepted")
	}
	mixed := append([]ExpiredLegacyCall(nil), calls...)
	mixed[1].Record.GenerationFrozen = batchOriginals(t)[1].Record.GenerationFrozen
	if _, err := ValidateExpiredLegacyRun(mixed, run); err == nil {
		t.Fatal("partially materialized run accepted")
	}
	badRun := run
	badRun.Summary.LegacyEvidenceHash = calls[0].GenerationProjection.Summary.LegacyEvidenceHash
	if _, err := ValidateExpiredLegacyRun(calls, badRun); err == nil {
		t.Fatal("one member digest used in place of complete run root")
	}
}

func TestExpiredLegacyRunConversionRefusesSharedGenerationStepOrChangedRun(t *testing.T) {
	for name, change := range map[string]func(*Record){
		"same generation":     func(r *Record) { r.GenerationID = legacyRecord().GenerationID; r.ChildID = r.GenerationID },
		"same step":           func(r *Record) { r.StepID = legacyRecord().StepID; r.Operation = "evaluation-" + r.StepID },
		"different run bytes": func(r *Record) { r.RunFrozen = append([]byte(" "), r.RunFrozen...) },
		"foreign run": func(r *Record) {
			r.RunID = strings.Repeat("4", 32)
			r.StepRun = r.RunID
			r.GenerationFrozen = []byte(strings.Replace(string(r.GenerationFrozen), legacyRecord().RunID, r.RunID, 1))
		},
	} {
		t.Run(name, func(t *testing.T) {
			originals := batchOriginals(t)
			change(&originals[1].Record)
			var old Proof
			if json.Unmarshal(originals[1].OriginalProof, &old) != nil {
				t.Fatal("bad original")
			}
			mapping, err := Convert(originals[1].Record, old.Source)
			if err != nil {
				t.Fatal("fixture is not independently valid legacy", err)
			}
			originals[1].OriginalProof = mapping.Provenance
			if _, err = ExpireLegacyRunEvidence(originals); err == nil {
				t.Fatal("invalid run-wide ownership set accepted")
			}
		})
	}
}
