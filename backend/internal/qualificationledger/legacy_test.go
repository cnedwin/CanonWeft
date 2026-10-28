package qualificationledger

import (
	"strings"
	"testing"
)

func legacyRecord() Record {
	project, actor, run, step, gen, call := strings.Repeat("a", 32), strings.Repeat("b", 32), strings.Repeat("c", 32), strings.Repeat("d", 32), strings.Repeat("e", 32), strings.Repeat("f", 32)
	return Record{Project: project, Actor: actor, RunID: run, StepID: step, GenerationID: gen, CallID: call, RunProject: project, StepProject: project, StepRun: run, GenerationProject: project, GenerationActor: actor, CallProject: project, CallActor: actor, ChildID: gen, ChildKind: "generation", Operation: "evaluation-" + step, InputHash: strings.Repeat("1", 64), RunCreated: "2026-10-24T00:00:00Z", GenerationCreated: "2026-10-24T00:00:01Z", CallCreated: "2026-10-24T00:00:02Z", State: "UNKNOWN", Reserved: 50, Currency: "USD", RunFrozen: []byte(`{"suite":{"policy":"repository-deterministic-v2","version":1,"hash":"` + strings.Repeat("2", 64) + `"},"price":{"currency":"USD"}}`), GenerationFrozen: []byte(`{"input_hash":"` + strings.Repeat("1", 64) + `","estimate_micros":50,"price":{"currency":"USD"},"capability":{"mode":"EVALUATION","evaluation_id":"` + run + `","suite_hash":"` + strings.Repeat("2", 64) + `"}}`)}
}

func TestLegacyConversionRequiresExplicitOldSourceAndBoundary(t *testing.T) {
	record := legacyRecord()
	source := Source{Schema: 28, Kind: "DATABASE", Boundary: "2026-10-25T00:00:00Z", MigrationChecksum: BoundaryChecksum()}
	mapping, err := Convert(record, source)
	if err != nil || mapping.Held != 50 || mapping.Charged != 0 || mapping.Origin != "LEGACY_28_DB" {
		t.Fatal(mapping, err)
	}
	for _, bad := range []Source{{Schema: 29, Kind: "DATABASE", Boundary: source.Boundary, MigrationChecksum: source.MigrationChecksum}, {Schema: 28, Kind: "DATABASE", Boundary: "2026-10-23T00:00:00Z", MigrationChecksum: source.MigrationChecksum}, {Schema: 28, Kind: "DATABASE", Boundary: source.Boundary, MigrationChecksum: strings.Repeat("0", 64)}, {Schema: 28, Kind: "ARCHIVE", Boundary: source.Boundary}} {
		if _, err = Convert(record, bad); err == nil {
			t.Fatal("unproved legacy source accepted", bad)
		}
	}
	record.RunFrozen = []byte(strings.Replace(string(record.RunFrozen), "v2", "v3", 1))
	if _, err = Convert(record, source); err == nil {
		t.Fatal("native policy treated as legacy")
	}
}

func TestLegacyConversionPreservesCurrentBillAndStableProvenance(t *testing.T) {
	record := legacyRecord()
	source := Source{Schema: 28, Kind: "ARCHIVE", Boundary: "2026-10-25T00:00:00Z", ManifestHash: strings.Repeat("3", 64)}
	before, err := Convert(record, source)
	if err != nil {
		t.Fatal(err)
	}
	record.State = "RECONCILED"
	record.Charged = 71
	after, err := Convert(record, source)
	if err != nil || after.Held != 0 || after.Charged != 71 || after.Origin != "LEGACY_28_ARCHIVE" || string(before.Provenance) != string(after.Provenance) {
		t.Fatal("legacy invoice/provenance changed", after, err)
	}
	record.Operation = "invented-operation"
	if _, err = Convert(record, source); err == nil {
		t.Fatal("untrusted operation joined legacy accounting")
	}
}
