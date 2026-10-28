package backup

import (
	"bytes"
	"slices"
	"testing"

	"novelstudio/internal/binlog"
)

func TestRetentionArchiveKeepsOutboxHistoryWithoutPendingDelivery(t *testing.T) {
	a := emptyArchive(t)
	testRow(a, "outbox", map[string]string{"id": "11111111111111111111111111111111", "project_id": "22222222222222222222222222222222", "resource_id": "33333333333333333333333333333333", "kind": "ip.published", "payload": "{\"head\":\"original\"}", "created_at": "2026-10-25 01:02:03.000004"})
	original := append(Row(nil), a.Data["outbox"][0]...)
	sanitizeArchiveData(a.Data, retentionSchemaVersion)
	if len(a.Data["outbox"]) != 1 {
		t.Fatal("registered event history removed")
	}
	var spec tableSpec
	for _, s := range tableCatalog {
		if s.name == "outbox" {
			spec = s
		}
	}
	row := a.Data["outbox"][0]
	if !bytes.Equal(row[spec.index("payload")], original[spec.index("payload")]) || spec.value(row, "processed_at") == "" {
		t.Fatal("event source changed or delivery remains pending")
	}
	_, _, baseline := retentionLogFixture(t)
	tx := binlog.Transaction{Changes: []binlog.Change{{Table: "outbox", Kind: "insert", After: binlog.Row(original)}}}
	clean, err := prepareLogTransaction(baseline.Tables, tx, retentionSchemaVersion)
	if err != nil || len(clean.Changes) != 1 || !equalLogRow(clean.Changes[0].After, binlog.Row(row)) {
		t.Fatal("log and snapshot disagree on disabled event history", err)
	}
	sanitizeArchiveData(a.Data, supportedSchemaVersion)
	if len(a.Data["outbox"]) != 0 {
		t.Fatal("legacy exclusion changed")
	}
}

func TestRetentionOutboxAdmissionRequiresHistoryContractAndDisabledDelivery(t *testing.T) {
	a := emptyArchive(t)
	testRow(a, "outbox", map[string]string{"id": "11111111111111111111111111111111", "project_id": "22222222222222222222222222222222", "resource_id": "33333333333333333333333333333333", "kind": "ip.published", "payload": "{}", "created_at": "2026-10-25 01:02:03", "processed_at": "2026-10-25 01:02:03"})
	if validateData(a.Data) == nil {
		t.Fatal("legacy archive admitted event history")
	}
	if err := validateDataWithEvidence(a.Data, qualificationArchiveEvidence{historicalOutbox: true}); err != nil {
		t.Fatal(err)
	}
	spec := memorySpec("outbox")
	a.Data["outbox"][0][spec.index("processed_at")] = nil
	if validateDataWithEvidence(a.Data, qualificationArchiveEvidence{historicalOutbox: true}) == nil {
		t.Fatal("pending delivery admitted")
	}
	a.Manifest.Exclusions = slices.Clone(retentionExclusions)
	if validateManifest(a.Manifest) == nil {
		t.Fatal("legacy manifest accepted changed exclusions")
	}
}
