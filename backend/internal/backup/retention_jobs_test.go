package backup

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"novelstudio/internal/retention"
)

func archiveCleanupFixture(t *testing.T) (*Archive, Row) {
	t.Helper()
	a := emptyArchive(t)
	a.Manifest.Schema = retentionSchemaVersion
	a.Manifest.SnapshotAt = time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	migration, _, err := retentionMigration()
	if err != nil {
		t.Fatal(err)
	}
	migration.Origin = "EXECUTED"
	a.Manifest.Migrations = append(a.Manifest.Migrations, migration)
	for _, spec := range retentionTableCatalog() {
		a.Data[spec.name] = nil
	}
	project, workspace, actor := strings.Repeat("a", 32), strings.Repeat("b", 32), strings.Repeat("c", 32)
	testRow(a, "workspaces", map[string]string{"id": workspace})
	testRow(a, "users", map[string]string{"id": actor, "workspace_id": workspace, "enabled": "0"})
	testRow(a, "projects", map[string]string{"id": project, "workspace_id": workspace})
	retentionMetadataRow(t, a.Data, "project_lifecycle", map[string]string{
		"project_id": project, "source_ledger_id": a.Manifest.Authority.LedgerID,
		"time_basis": "SOURCE_UTC", "first_verified_at": "2026-08-01 00:00:00",
		"inventory_revision": "3", "catalog_version": "1", "state": "UNSCANNED", "updated_at": "2026-10-26 12:00:00",
	})
	cutoff := a.Manifest.SnapshotAt.Add(-time.Hour)
	plan, err := retention.BuildPlan(retention.Inventory{ProjectID: project, Revision: 2, CatalogVersion: 1,
		CoveredTables: retention.KnownTables(), CapturedAt: cutoff, Complete: true}, cutoff)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(plan)
	row := retentionMetadataRow(t, a.Data, "retention_jobs", map[string]string{
		"id": strings.Repeat("d", 32), "project_id": project, "actor_id": actor, "operation_id": "cleanup-1",
		"request_hash": strings.Repeat("e", 64), "inventory_revision": "2", "inventory_hash": plan.InventoryHash,
		"plan_hash": plan.Hash, "plan": string(raw), "state": "PLANNED", "cursor_key": "", "attempt": "0",
		"reason_code": "", "restored_disabled": "0", "created_at": "2026-10-26 11:00:00", "updated_at": "2026-10-26 11:00:00",
	})
	return a, row
}

func TestRetentionArchiveCleanupJobsZIPGate(t *testing.T) {
	a, _ := archiveCleanupFixture(t)
	raw := archiveBytes(t, a)
	if _, err := ValidateArchive(bytes.NewReader(raw), int64(len(raw))); err != nil {
		t.Fatal("valid historical plan failed archive validation", err)
	}
	for _, change := range []struct{ field, value string }{
		{"project_id", strings.Repeat("f", 32)}, {"actor_id", strings.Repeat("f", 32)},
		{"operation_id", ""}, {"request_hash", "bad"}, {"inventory_revision", "4"},
		{"inventory_hash", strings.Repeat("0", 64)}, {"plan_hash", strings.Repeat("0", 64)},
		{"plan", "{}"}, {"state", "RUNNING"}, {"cursor_key", "../file"},
		{"attempt", "-1"}, {"attempt", "4294967296"}, {"restored_disabled", "2"},
		{"created_at", "2026-10-26 10:00:00"}, {"updated_at", "2026-10-27 00:00:00"},
	} {
		t.Run(change.field+"/"+change.value, func(t *testing.T) {
			a, row := archiveCleanupFixture(t)
			for _, spec := range retentionTableCatalog() {
				if spec.name == "retention_jobs" {
					spec.set(row, change.field, change.value)
				}
			}
			raw := archiveBytes(t, a)
			if _, err := ValidateArchive(bytes.NewReader(raw), int64(len(raw))); err == nil || !strings.Contains(err.Error(), "cleanup job") {
				t.Fatal("invalid cleanup job passed its archive gate", err)
			}
		})
	}
}

func TestRetentionArchiveCleanupReceiptCannotBeOrphaned(t *testing.T) {
	a, _ := archiveCleanupFixture(t)
	retentionMetadataRow(t, a.Data, "retention_receipts", map[string]string{
		"job_id": strings.Repeat("f", 32), "item_key": strings.Repeat("e", 64), "phase": "DETACHED",
		"result": "SUCCEEDED", "evidence_hash": strings.Repeat("e", 64), "recorded_at": "2026-10-26 11:00:00",
	})
	raw := archiveBytes(t, a)
	if _, err := ValidateArchive(bytes.NewReader(raw), int64(len(raw))); err == nil || !strings.Contains(err.Error(), "cleanup job") {
		t.Fatal("orphan cleanup receipt passed archive gate", err)
	}
}

func objectCleanupFixture(t *testing.T) (*Archive, Row) {
	t.Helper()
	a, job := archiveCleanupFixture(t)
	cutoff, hash := a.Manifest.SnapshotAt.Add(-time.Hour), strings.Repeat("f", 64)
	plan, err := retention.BuildPlan(retention.Inventory{ProjectID: strings.Repeat("a", 32), Revision: 2, CatalogVersion: 1,
		CoveredTables: retention.KnownTables(), CapturedAt: cutoff, Complete: true,
		Objects: []retention.Object{{Hash: hash, Revision: 1, State: retention.ObjectPresent, FirstSeenAt: cutoff.Add(-31 * 24 * time.Hour)}}}, cutoff)
	if err != nil || len(plan.DeleteObjects) != 1 {
		t.Fatal("missing test removal plan", err)
	}
	raw, _ := json.Marshal(plan)
	for _, spec := range retentionTableCatalog() {
		if spec.name == "retention_jobs" {
			for key, value := range map[string]string{"plan": string(raw), "plan_hash": plan.Hash, "inventory_hash": plan.InventoryHash, "state": "COMPLETE", "attempt": "1", "updated_at": "2026-10-26 12:00:00"} {
				spec.set(job, key, value)
			}
		}
	}
	retentionMetadataRow(t, a.Data, "retention_objects", map[string]string{
		"project_id": plan.ProjectID, "object_hash": hash, "first_seen_at": cutoff.Add(-31 * 24 * time.Hour).Format("2006-01-02 15:04:05"),
		"state": "DELETED", "revision": "2", "deleted_at": "2026-10-26 11:01:00",
	})
	for _, phase := range []string{"REMOVED", "VERIFIED"} {
		retentionMetadataRow(t, a.Data, "retention_receipts", map[string]string{
			"job_id": strings.Repeat("d", 32), "item_key": "object:" + hash, "phase": phase,
			"result": "ABSENT", "evidence_hash": strings.Repeat("e", 64), "recorded_at": "2026-10-26 11:02:00",
		})
	}
	return a, job
}

func TestRetentionArchiveCleanupReceiptsBindPlanAndPhases(t *testing.T) {
	validate := func(a *Archive) error {
		raw := archiveBytes(t, a)
		_, err := ValidateArchive(bytes.NewReader(raw), int64(len(raw)))
		return err
	}
	a, _ := objectCleanupFixture(t)
	if err := validate(a); err != nil {
		t.Fatal("completed historical object plan rejected", err)
	}
	for _, name := range []string{"missing removal", "missing verification", "duplicate receipt", "wrong item", "wrong phase", "early clock", "late clock", "blocked completion", "bad evidence", "missing object", "duplicate job", "duplicate operation", "cross workspace actor", "future plan"} {
		t.Run(name, func(t *testing.T) {
			a, job := objectCleanupFixture(t)
			var spec tableSpec
			for _, s := range retentionTableCatalog() {
				if s.name == "retention_receipts" {
					spec = s
				}
			}
			row := a.Data[spec.name][1]
			switch name {
			case "missing removal":
				a.Data[spec.name] = a.Data[spec.name][1:]
			case "missing verification":
				a.Data[spec.name] = a.Data[spec.name][:1]
			case "duplicate receipt":
				a.Data[spec.name] = append(a.Data[spec.name], row)
			case "wrong item":
				spec.set(row, "item_key", "object:"+strings.Repeat("0", 64))
			case "wrong phase":
				spec.set(row, "phase", "DETACHED")
			case "early clock":
				spec.set(row, "recorded_at", "2026-10-26 11:01:00")
			case "late clock":
				spec.set(row, "recorded_at", "2026-10-26 12:01:00")
			case "blocked completion":
				spec.set(row, "result", "BLOCKED")
			case "bad evidence":
				spec.set(row, "evidence_hash", "unverified")
			case "missing object":
				a.Data["retention_objects"] = nil
			case "duplicate job":
				a.Data["retention_jobs"] = append(a.Data["retention_jobs"], job)
			case "duplicate operation":
				other := append(Row(nil), job...)
				for _, s := range retentionTableCatalog() {
					if s.name == "retention_jobs" {
						s.set(other, "id", strings.Repeat("0", 32))
					}
				}
				a.Data["retention_jobs"] = append(a.Data["retention_jobs"], other)
			case "cross workspace actor":
				testRow(a, "workspaces", map[string]string{"id": strings.Repeat("0", 32)})
				for _, s := range tableCatalog {
					if s.name == "users" {
						s.set(a.Data["users"][0], "workspace_id", strings.Repeat("0", 32))
					}
				}
			case "future plan":
				for _, s := range retentionTableCatalog() {
					if s.name == "retention_jobs" {
						s.set(job, "created_at", "2026-10-26 10:00:00")
					}
				}
			}
			if err := validate(a); err == nil || !strings.Contains(err.Error(), "cleanup job") {
				t.Fatal("invalid receipt/job relation accepted", err)
			}
		})
	}
}

func TestRetentionArchiveCleanupSanitizerDisablesWithoutRewritingHistory(t *testing.T) {
	a, job := objectCleanupFixture(t)
	before := append(Row(nil), job...)
	receipts, _ := json.Marshal(a.Data["retention_receipts"])
	sanitize(a.Data)
	for _, spec := range retentionTableCatalog() {
		if spec.name != "retention_jobs" {
			continue
		}
		if spec.value(job, "restored_disabled") != "1" {
			t.Fatal("restored cleanup job remains executable")
		}
		spec.set(before, "restored_disabled", "1")
		if !reflect.DeepEqual(before, job) {
			t.Fatal("disabling rewrote original cleanup history")
		}
	}
	sanitize(a.Data)
	after, _ := json.Marshal(a.Data["retention_receipts"])
	if !bytes.Equal(receipts, after) || !reflect.DeepEqual(before, job) {
		t.Fatal("repeated sanitization changed history")
	}
}

func detachedCleanupFixture(t *testing.T) (*Archive, Row) {
	t.Helper()
	a, job := archiveCleanupFixture(t)
	metadata, at := retentionHistoryFixture(t)
	history, err := readArchiveReferenceHistory(metadata, at)
	if err != nil {
		t.Fatal(err)
	}
	for name, rows := range metadata {
		if len(rows) > 0 {
			a.Data[name] = rows
		}
	}
	var parent retention.ReferenceVersion
	var journal retention.ExpiryJournalRecord
	for _, v := range history.Versions {
		if v.Reference.Revision == 1 {
			parent = v
		}
	}
	for _, r := range history.Expiry {
		journal = r
	}
	cutoff := parent.Deadline
	plan, err := retention.BuildPlan(retention.Inventory{ProjectID: parent.ProjectID, Revision: 2, CatalogVersion: 1,
		CoveredTables: retention.KnownTables(), CapturedAt: cutoff, Complete: true, References: []retention.Reference{parent.Reference}}, cutoff)
	if err != nil || len(plan.Expire) != 1 {
		t.Fatal("missing inline expiry plan", err)
	}
	raw, _ := json.Marshal(plan)
	for _, s := range retentionTableCatalog() {
		if s.name == "retention_jobs" {
			for key, value := range map[string]string{"plan": string(raw), "plan_hash": plan.Hash, "inventory_hash": plan.InventoryHash,
				"state": "COMPLETE", "attempt": "1", "created_at": cutoff.Format("2006-01-02 15:04:05.000000"), "updated_at": at.Format("2006-01-02 15:04:05.000000")} {
				s.set(job, key, value)
			}
		}
	}
	branch := strings.Repeat("1", 32)
	testRow(a, "branches", map[string]string{"id": branch, "project_id": parent.ProjectID})
	testRow(a, "generation_tasks", map[string]string{"id": strings.Repeat("b", 32), "project_id": parent.ProjectID, "workspace_id": strings.Repeat("b", 32), "actor_id": strings.Repeat("c", 32), "branch_id": branch, "frozen": journal.Evidence, "state": "PREPARED"})
	for _, phase := range []string{"DETACHED", "VERIFIED"} {
		retentionMetadataRow(t, a.Data, "retention_receipts", map[string]string{"job_id": strings.Repeat("d", 32), "item_key": parent.Reference.ID, "phase": phase,
			"result": "SUCCEEDED", "evidence_hash": journal.EvidenceHash, "recorded_at": at.Format("2006-01-02 15:04:05.000000")})
	}
	return a, job
}

func TestRetentionArchiveCleanupDetachedReceiptRequiresExactProof(t *testing.T) {
	a, _ := detachedCleanupFixture(t)
	raw := archiveBytes(t, a)
	if _, err := ValidateArchive(bytes.NewReader(raw), int64(len(raw))); err != nil {
		t.Fatal("real inline expiry receipt rejected", err)
	}
	for _, name := range []string{"proof hash", "claims absence", "predates expiry", "different deadline"} {
		t.Run(name, func(t *testing.T) {
			a, job := detachedCleanupFixture(t)
			var receiptSpec, jobSpec tableSpec
			for _, s := range retentionTableCatalog() {
				if s.name == "retention_receipts" {
					receiptSpec = s
				}
				if s.name == "retention_jobs" {
					jobSpec = s
				}
			}
			switch name {
			case "proof hash":
				receiptSpec.set(a.Data[receiptSpec.name][0], "evidence_hash", strings.Repeat("0", 64))
			case "claims absence":
				receiptSpec.set(a.Data[receiptSpec.name][0], "result", "ABSENT")
			case "predates expiry":
				receiptSpec.set(a.Data[receiptSpec.name][0], "recorded_at", jobSpec.value(job, "created_at"))
			case "different deadline":
				var plan retention.Plan
				if err := json.Unmarshal([]byte(jobSpec.value(job, "plan")), &plan); err != nil {
					t.Fatal(err)
				}
				plan.Expire[0].Deadline = plan.Expire[0].Deadline.Add(-time.Second)
				plan.Hash = ""
				unsigned, _ := json.Marshal(plan)
				plan.Hash = digest(unsigned)
				encoded, _ := json.Marshal(plan)
				jobSpec.set(job, "plan", string(encoded))
				jobSpec.set(job, "plan_hash", plan.Hash)
			}
			raw := archiveBytes(t, a)
			if _, err := ValidateArchive(bytes.NewReader(raw), int64(len(raw))); err == nil || !strings.Contains(err.Error(), "cleanup job") {
				t.Fatal("unproven detached receipt accepted", err)
			}
		})
	}
}

func TestRetentionArchiveUnexecutedPlanCannotInventDeadline(t *testing.T) {
	a, job := detachedCleanupFixture(t)
	a.Data["retention_receipts"] = nil
	for _, spec := range retentionTableCatalog() {
		if spec.name != "retention_jobs" {
			continue
		}
		spec.set(job, "state", "PLANNED")
		var plan retention.Plan
		if err := json.Unmarshal([]byte(spec.value(job, "plan")), &plan); err != nil {
			t.Fatal(err)
		}
		plan.Expire[0].Deadline = plan.Expire[0].Deadline.Add(-time.Second)
		plan.Hash = ""
		unsigned, _ := json.Marshal(plan)
		plan.Hash = digest(unsigned)
		raw, _ := json.Marshal(plan)
		spec.set(job, "plan", string(raw))
		spec.set(job, "plan_hash", plan.Hash)
	}
	raw := archiveBytes(t, a)
	if _, err := ValidateArchive(bytes.NewReader(raw), int64(len(raw))); err == nil || !strings.Contains(err.Error(), "cleanup job") {
		t.Fatal("unexecuted rehashed plan accepted wrong effective deadline", err)
	}
}
