package backup

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"novelstudio/internal/retention"
)

func retentionHistoryFixture(t *testing.T) (Data, time.Time) {
	return retentionHistoryFixtureFor(t, "generation_tasks")
}

func retentionHistoryFixtureFor(t *testing.T, table string) (Data, time.Time) {
	t.Helper()
	data := Data{}
	for _, spec := range retentionTableCatalog() {
		data[spec.name] = nil
	}
	project := strings.Repeat("a", 32)
	owner, _ := retention.OwnerKey(strings.Repeat("b", 32))
	id, _ := retention.ReferenceID(project, table, owner, "frozen")
	born := time.Date(2026, 8, 1, 0, 0, 0, 123000, time.UTC)
	deadline, err := retention.Deadline(retention.RawRun, born, retention.Lifecycle{})
	if err != nil {
		t.Fatal(err)
	}
	hash := digest([]byte("private original input"))
	prior := retention.ReferenceVersion{Schema: 1, ProjectID: project, Table: table, OwnerKey: owner, Field: "frozen", Class: retention.RawRun, Storage: retention.Inline, SourceID: id, SourcePayloadHash: hash, ValueFirstVerifiedAt: born, LineageState: "PENDING", Deadline: deadline, RecordedAt: born, Reference: retention.Reference{ID: id, Rule: table + ".frozen", PayloadHash: hash, CreatedAt: born, FirstVerifiedAt: born, TimeBasis: retention.LegacyRegistered, Revision: 1, State: retention.Retained}}
	parentRaw, _ := json.Marshal(prior)
	if _, err := retention.ValidateReferenceVersionRecord(string(parentRaw), digest(parentRaw)); err != nil {
		t.Fatal("invalid test parent", err)
	}
	at := deadline.Add(time.Hour)
	proof, err := retention.ProjectExpired(retention.Expiry{ReferenceID: id, Rule: prior.Reference.Rule, PayloadHash: hash, Revision: 1, Deadline: deadline, Storage: retention.Inline}, []byte("private original input"), retention.EvidenceSummary{}, at)
	if err != nil {
		t.Fatal(err)
	}
	proofRaw, _ := json.Marshal(proof)
	next := prior
	next.Schema = 2
	next.Expiration = &retention.ReferenceExpiration{PriorRevision: 1, PriorHash: digest(parentRaw), PriorRecordedAt: born, EvidenceHash: digest(proofRaw), BaseDeadline: deadline}
	next.Reference.State, next.Reference.Revision, next.Reference.ExpiredAt = retention.Expired, 2, at
	next.RecordedAt = at
	nextRaw, _ := json.Marshal(next)
	if _, err := retention.ValidateReferenceVersionRecord(string(nextRaw), digest(nextRaw)); err != nil {
		t.Fatal("invalid test transition", err)
	}
	stamp := func(t time.Time) string { return t.Format("2006-01-02 15:04:05.000000") }
	add := func(table string, values map[string]string) {
		for _, spec := range retentionTableCatalog() {
			if spec.name != table {
				continue
			}
			row := make(Row, len(spec.cols()))
			for key, value := range values {
				row[spec.index(key)] = []byte(value)
			}
			data[table] = append(data[table], row)
			return
		}
		t.Fatal("unknown fixture table", table)
	}
	for i, raw := range [][]byte{parentRaw, nextRaw} {
		revision, recorded := "1", born
		if i == 1 {
			revision, recorded = "2", at
		}
		add("retention_reference_versions", map[string]string{"reference_id": id, "revision": revision, "project_id": project, "source_slot_id": id, "source_payload_hash": hash, "record_hash": digest(raw), "snapshot": string(raw), "recorded_at": stamp(recorded)})
	}
	add("retention_references", map[string]string{"id": id, "project_id": project, "owner_table": table, "owner_key": owner, "field_key": "frozen", "class": string(retention.RawRun), "storage_kind": "INLINE", "payload_hash": hash, "source_created_at": stamp(born), "time_basis": "LEGACY_REGISTERED", "first_verified_at": stamp(born), "deadline": stamp(deadline), "state": "EXPIRED", "expired_at": stamp(at), "content_root": "0", "accounting_open": "0", "revision": "2", "updated_at": stamp(at)})
	evidenceID, _ := retention.EvidenceID(id, 2)
	add("retention_evidence", map[string]string{"id": evidenceID, "project_id": project, "reference_id": id, "reference_revision": "2", "original_payload_hash": hash, "projection_hash": digest(proofRaw), "projection": string(proofRaw), "expired_at": stamp(at)})
	add("retention_source_slots", map[string]string{"id": id, "project_id": project, "owner_table": table, "owner_key": owner, "field_key": "frozen", "time_basis": "LEGACY_REGISTERED", "first_verified_at": stamp(born)})
	add("retention_source_values", map[string]string{"source_id": id, "payload_hash": hash, "is_null": "0", "first_verified_at": stamp(born)})
	return data, at.Add(time.Second)
}

func TestRetentionArchiveHistoryBindsRowsVersionsAndProofs(t *testing.T) {
	data, at := retentionHistoryFixture(t)
	history, err := readArchiveReferenceHistory(data, at)
	if err != nil || len(history.Current) != 1 || len(history.Versions) != 2 || len(history.Expiry) != 1 {
		t.Fatal("complete expiry history rejected", err)
	}
	if err := validateArchiveSourceHistory(data, history, at); err != nil {
		t.Fatal("original source clock lost", err)
	}
	a := emptyArchive(t)
	a.Manifest.Schema, a.Manifest.SnapshotAt = retentionSchemaVersion, at
	migration, _, err := retentionMigration()
	if err != nil {
		t.Fatal(err)
	}
	migration.Origin = "EXECUTED"
	a.Manifest.Migrations = append(a.Manifest.Migrations, migration)
	for name, rows := range data {
		a.Data[name] = rows
	}
	project, task := strings.Repeat("a", 32), strings.Repeat("b", 32)
	actor, branch, workspace := strings.Repeat("c", 32), strings.Repeat("d", 32), strings.Repeat("f", 32)
	testRow(a, "workspaces", map[string]string{"id": workspace})
	testRow(a, "users", map[string]string{"id": actor, "workspace_id": workspace, "enabled": "0"})
	testRow(a, "projects", map[string]string{"id": project, "workspace_id": workspace})
	testRow(a, "branches", map[string]string{"id": branch, "project_id": project})
	var projection string
	for _, spec := range retentionTableCatalog() {
		if spec.name == "retention_evidence" {
			projection = spec.value(data[spec.name][0], "projection")
		}
	}
	testRow(a, "generation_tasks", map[string]string{"id": task, "project_id": project, "workspace_id": workspace, "branch_id": branch, "actor_id": actor, "frozen": projection, "state": "PREPARED"})
	lifecycleRow := retentionMetadataRow(t, a.Data, "project_lifecycle", map[string]string{
		"project_id": project, "source_ledger_id": a.Manifest.Authority.LedgerID,
		"time_basis": "SOURCE_UTC", "first_verified_at": "2026-08-01 00:00:00.123000",
		"inventory_revision": "3", "catalog_version": "1", "state": "UNSCANNED",
		"updated_at": at.Format("2006-01-02 15:04:05.000000"),
	})
	zipBytes := archiveBytes(t, a)
	if _, err := ValidateArchive(bytes.NewReader(zipBytes), int64(len(zipBytes))); err != nil {
		t.Fatal("valid ZIP failed source history verification", err)
	}
	a.Data["project_lifecycle"] = nil
	zipBytes = archiveBytes(t, a)
	if _, err := ValidateArchive(bytes.NewReader(zipBytes), int64(len(zipBytes))); !errors.Is(err, errRetentionState) {
		t.Fatal("ZIP accepted references without their registered lifecycle", err)
	}
	a.Data["project_lifecycle"] = []Row{lifecycleRow}
	retentionMetadataRow(t, a.Data, "retention_objects", map[string]string{
		"project_id": strings.Repeat("e", 32), "object_hash": strings.Repeat("f", 64),
		"first_seen_at": "2026-08-01 00:00:00", "revision": "1", "state": "PRESENT",
	})
	zipBytes = archiveBytes(t, a)
	if _, err := ValidateArchive(bytes.NewReader(zipBytes), int64(len(zipBytes))); !errors.Is(err, errRetentionState) {
		t.Fatal("ZIP accepted object state belonging to absent project", err)
	}
	a.Data["retention_objects"] = nil
	a.Data["retention_reference_versions"] = a.Data["retention_reference_versions"][1:]
	zipBytes = archiveBytes(t, a)
	if _, err := ValidateArchive(bytes.NewReader(zipBytes), int64(len(zipBytes))); !errors.Is(err, errRetentionHistory) {
		t.Fatal("ZIP accepted missing parent after file hashes were regenerated", err)
	}
	a.Data["retention_reference_versions"] = data["retention_reference_versions"]
	a.Data["retention_source_values"] = nil
	zipBytes = archiveBytes(t, a)
	if _, err := ValidateArchive(bytes.NewReader(zipBytes), int64(len(zipBytes))); !errors.Is(err, errRetentionHistory) {
		t.Fatal("ZIP accepted missing original source value after file hashes were regenerated", err)
	}
	a.Data["retention_source_values"] = data["retention_source_values"]
	for _, spec := range tableCatalog {
		if spec.name == "generation_tasks" {
			spec.set(a.Data[spec.name][0], "frozen", "{}")
		}
	}
	zipBytes = archiveBytes(t, a)
	if _, err := ValidateArchive(bytes.NewReader(zipBytes), int64(len(zipBytes))); err == nil {
		t.Fatal("ZIP did not reject altered actual owner body", err)
	}
	for name, mutate := range map[string]func(Data){
		"missing source slot":    func(d Data) { d["retention_source_slots"] = nil },
		"missing original value": func(d Data) { d["retention_source_values"] = nil },
		"duplicate original value": func(d Data) {
			d["retention_source_values"] = append(d["retention_source_values"], d["retention_source_values"][0])
		},
		"source moved to another project": func(d Data) {
			for _, s := range retentionTableCatalog() {
				if s.name == "retention_source_slots" {
					s.set(d[s.name][0], "project_id", strings.Repeat("c", 32))
				}
			}
		},
		"null source substituted": func(d Data) {
			for _, s := range retentionTableCatalog() {
				if s.name == "retention_source_values" {
					s.set(d[s.name][0], "is_null", "1")
				}
			}
		},
		"original value age renewed": func(d Data) {
			for _, s := range retentionTableCatalog() {
				if s.name == "retention_source_values" {
					s.set(d[s.name][0], "first_verified_at", at.Format("2006-01-02 15:04:05.000000"))
				}
			}
		},
		"missing parent": func(d Data) { d["retention_reference_versions"] = d["retention_reference_versions"][1:] },
		"duplicate version": func(d Data) {
			d["retention_reference_versions"] = append(d["retention_reference_versions"], d["retention_reference_versions"][0])
		},
		"missing current":    func(d Data) { d["retention_references"] = nil },
		"missing evidence":   func(d Data) { d["retention_evidence"] = nil },
		"duplicate evidence": func(d Data) { d["retention_evidence"] = append(d["retention_evidence"], d["retention_evidence"][0]) },
		"short current":      func(d Data) { d["retention_references"][0] = d["retention_references"][0][:3] },
		"changed current class": func(d Data) {
			for _, s := range retentionTableCatalog() {
				if s.name == "retention_references" {
					s.set(d[s.name][0], "class", "CONTENT")
				}
			}
		},
		"rolled back current": func(d Data) {
			for _, s := range retentionTableCatalog() {
				if s.name == "retention_references" {
					s.set(d[s.name][0], "revision", "1")
				}
			}
		},
		"cross project evidence": func(d Data) {
			for _, s := range retentionTableCatalog() {
				if s.name == "retention_evidence" {
					s.set(d[s.name][0], "project_id", strings.Repeat("c", 32))
				}
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			fixture, at := retentionHistoryFixture(t)
			mutate(fixture)
			history, err := readArchiveReferenceHistory(fixture, at)
			if err == nil {
				err = validateArchiveSourceHistory(fixture, history, at)
			}
			if err == nil {
				t.Fatal("inconsistent archive history accepted")
			}
		})
	}
	if _, err := readArchiveReferenceHistory(data, at.Add(-2*time.Second)); err == nil {
		t.Fatal("history newer than snapshot accepted")
	}
}
