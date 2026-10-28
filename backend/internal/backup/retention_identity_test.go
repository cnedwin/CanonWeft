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

func archiveIdentityFixture(t *testing.T, table string) (*Archive, Data, Row) {
	t.Helper()
	a := emptyArchive(t)
	a.Manifest.SnapshotAt = time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	workspace, actor, project, source := strings.Repeat("a", 32), strings.Repeat("b", 32), strings.Repeat("c", 32), strings.Repeat("d", 32)
	testRow(a, "workspaces", map[string]string{"id": workspace})
	testRow(a, "users", map[string]string{"id": actor, "workspace_id": workspace, "enabled": "0"})
	testRow(a, "projects", map[string]string{"id": project, "workspace_id": workspace})
	testRow(a, "branches", map[string]string{"id": strings.Repeat("e", 32), "project_id": project})
	keyHash, requestHash := digest([]byte("原始用户请求")), digest([]byte("original request fingerprint"))
	values := map[string]string{"id": source, "actor_id": actor, "workspace_id": workspace, "project_id": project, "branch_id": strings.Repeat("e", 32), "operation_id": "retired-v1:" + keyHash, "request_hash": requestHash}
	kind := ""
	original, _ := retention.OwnerKey(source)
	if table == "operations" {
		kind, source = "project.create", project
		values["kind"], values["result_id"] = kind, source
		original, _ = retention.OwnerKey(actor, "sha256:"+keyHash)
	}
	if table == "model_calls" {
		values["input_hash"] = requestHash
	}
	if table == "semantic_review_runs" {
		values["target_kind"], values["target_id"] = "ip_candidate", strings.Repeat("f", 32)
		testRow(a, "ip_revisions", map[string]string{"id": values["target_id"], "branch_id": values["branch_id"]})
	}
	testRow(a, table, values)
	metadata := Data{"retention_identity_policies": nil, "retention_operation_keys": nil}
	retentionMetadataRow(t, metadata, "retention_identity_policies", map[string]string{
		"workspace_id": workspace, "legacy_writes_closed": "1", "revision": "2",
		"created_at": "2026-10-26 10:00:00", "closed_at": "2026-10-26 10:00:00.000002",
	})
	alias := retentionMetadataRow(t, metadata, "retention_operation_keys", map[string]string{
		"workspace_id": workspace, "actor_id": actor, "owner_table": table, "original_key_hash": keyHash,
		"original_owner_key": original, "anonymous_key": values["operation_id"], "source_id": source,
		"request_hash": requestHash, "operation_kind": kind, "created_at": "2026-10-26 10:00:00.000001",
	})
	return a, metadata, alias
}

func TestRetentionArchiveRequestIdentityPreservesOriginalResults(t *testing.T) {
	for _, table := range []string{"operations", "model_calls", "source_imports", "ingest_jobs", "generation_tasks", "recipe_runs", "semantic_review_runs"} {
		t.Run(table, func(t *testing.T) {
			a, metadata, _ := archiveIdentityFixture(t, table)
			if err := validateArchiveRequestIdentities(a.Data, metadata, a.Manifest.SnapshotAt); err != nil {
				t.Fatal("original identity rejected", err)
			}
			for _, change := range []struct{ field, value string }{
				{"workspace_id", strings.Repeat("e", 32)}, {"actor_id", strings.Repeat("f", 32)},
				{"original_key_hash", strings.Repeat("e", 64)}, {"anonymous_key", "retired-v1:" + strings.Repeat("e", 64)},
				{"original_owner_key", "[]"}, {"source_id", strings.Repeat("e", 32)}, {"request_hash", strings.Repeat("e", 64)},
				{"owner_table", "users"}, {"operation_kind", "different.operation"},
				{"created_at", "2026-10-27 00:00:00"}, {"created_at", "2026-10-25 00:00:00"},
			} {
				a, metadata, row := archiveIdentityFixture(t, table)
				for _, spec := range retentionTableCatalog() {
					if spec.name == "retention_operation_keys" {
						spec.set(row, change.field, change.value)
					}
				}
				if err := validateArchiveRequestIdentities(a.Data, metadata, a.Manifest.SnapshotAt); err == nil {
					t.Fatal("identity substitution accepted", change.field)
				}
			}
			metadata["retention_operation_keys"] = nil
			if err := validateArchiveRequestIdentities(a.Data, metadata, a.Manifest.SnapshotAt); err == nil {
				t.Fatal("anonymous source without replay mapping accepted")
			}
		})
	}
}

func TestRetentionArchiveRequestPolicyCannotReopenOrMove(t *testing.T) {
	for _, change := range []struct{ field, value string }{
		{"workspace_id", strings.Repeat("e", 32)}, {"legacy_writes_closed", "0"}, {"legacy_writes_closed", "2"},
		{"revision", "1"}, {"closed_at", ""}, {"closed_at", "2026-10-25 00:00:00"}, {"closed_at", "2026-10-27 00:00:00"},
	} {
		a, metadata, _ := archiveIdentityFixture(t, "operations")
		for _, spec := range retentionTableCatalog() {
			if spec.name == "retention_identity_policies" {
				spec.set(metadata[spec.name][0], change.field, change.value)
			}
		}
		if err := validateArchiveRequestIdentities(a.Data, metadata, a.Manifest.SnapshotAt); err == nil {
			t.Fatal("invalid policy accepted", change.field)
		}
	}
	for _, table := range []string{"retention_identity_policies", "retention_operation_keys"} {
		a, metadata, _ := archiveIdentityFixture(t, "operations")
		metadata[table] = append(metadata[table], metadata[table][0])
		if err := validateArchiveRequestIdentities(a.Data, metadata, a.Manifest.SnapshotAt); err == nil {
			t.Fatal("duplicate policy or replay identity accepted", table)
		}
	}
}

func TestRetentionArchiveRequestPolicyZIPGate(t *testing.T) {
	a := emptyArchive(t)
	a.Manifest.Schema = retentionSchemaVersion
	migration, _, err := retentionMigration()
	if err != nil {
		t.Fatal(err)
	}
	migration.Origin = "EXECUTED"
	a.Manifest.Migrations = append(a.Manifest.Migrations, migration)
	for _, spec := range retentionTableCatalog() {
		a.Data[spec.name] = nil
	}
	retentionMetadataRow(t, a.Data, "retention_identity_policies", map[string]string{
		"workspace_id": strings.Repeat("e", 32), "legacy_writes_closed": "0", "revision": "1",
		"created_at": a.Manifest.SnapshotAt.Add(-time.Hour).Format("2006-01-02 15:04:05.000000"),
	})
	raw := archiveBytes(t, a)
	if _, err := ValidateArchive(bytes.NewReader(raw), int64(len(raw))); !errors.Is(err, errRetentionIdentity) {
		t.Fatal("ZIP accepted policy for absent workspace", err)
	}
	valid, metadata, _ := archiveIdentityFixture(t, "operations")
	valid.Manifest.Schema = retentionSchemaVersion
	valid.Manifest.Migrations = append(valid.Manifest.Migrations, migration)
	for _, spec := range retentionTableCatalog() {
		valid.Data[spec.name] = nil
	}
	for name, rows := range metadata {
		valid.Data[name] = rows
	}
	raw = archiveBytes(t, valid)
	if _, err := ValidateArchive(bytes.NewReader(raw), int64(len(raw))); err == nil {
		t.Fatal("alias without original expiry history reached remaining archive gate", err)
	}
}

func TestRetentionArchiveRetiredRequestProofAndAliasBindTogether(t *testing.T) {
	a, metadata, alias := archiveIdentityFixture(t, "operations")
	a.Manifest.Schema = retentionSchemaVersion
	migration, _, err := retentionMigration()
	if err != nil {
		t.Fatal(err)
	}
	migration.Origin = "EXECUTED"
	a.Manifest.Migrations = append(a.Manifest.Migrations, migration)
	for _, spec := range retentionTableCatalog() {
		a.Data[spec.name] = nil
	}
	for name, rows := range metadata {
		a.Data[name] = rows
	}
	project, actor, workspace := strings.Repeat("c", 32), strings.Repeat("b", 32), strings.Repeat("a", 32)
	key, hash := "原始用户请求", digest([]byte("原始用户请求"))
	owner, _ := retention.OwnerKey(actor, "sha256:"+hash)
	id, _ := retention.ReferenceID(project, "operations", owner, "operation_id")
	born := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	deadline, _ := retention.Deadline(retention.RawRun, born, retention.Lifecycle{})
	at := time.Date(2026, 10, 26, 10, 0, 0, 0, time.UTC)
	prior := retention.ReferenceVersion{Schema: 1, ProjectID: project, Table: "operations", OwnerKey: owner, Field: "operation_id", Class: retention.RawRun, Storage: retention.Inline, SourceID: id, SourcePayloadHash: hash, ValueFirstVerifiedAt: born, LineageState: "PENDING", Deadline: deadline, RecordedAt: born,
		Reference: retention.Reference{ID: id, Rule: "operations.operation_id", PayloadHash: hash, CreatedAt: born, FirstVerifiedAt: born, TimeBasis: retention.LegacyRegistered, Revision: 1, State: retention.Retained}}
	parentRaw, _ := json.Marshal(prior)
	proof, err := retention.ProjectRetiredRequestKey(project, retention.Expiry{ReferenceID: id, Rule: prior.Reference.Rule, PayloadHash: hash, Revision: 1, Deadline: deadline, Storage: retention.Inline}, retention.RequestKeyRetirement{WorkspaceID: workspace, ActorID: actor, Table: "operations", OriginalKey: key, SourceID: project, RequestHash: digest([]byte("original request fingerprint")), Kind: "project.create", OriginalOwnerKey: owner}, at)
	if err != nil {
		t.Fatal(err)
	}
	proofRaw, _ := json.Marshal(proof)
	next := prior
	next.Schema, next.RecordedAt = 2, at
	next.Reference.State, next.Reference.Revision, next.Reference.ExpiredAt = retention.Expired, 2, at
	next.Expiration = &retention.ReferenceExpiration{PriorRevision: 1, PriorHash: digest(parentRaw), PriorRecordedAt: born, EvidenceHash: digest(proofRaw), BaseDeadline: deadline}
	nextRaw, _ := json.Marshal(next)
	if _, err := retention.ValidateReferenceVersionRecord(string(nextRaw), digest(nextRaw)); err != nil {
		t.Fatal(err)
	}
	stamp := func(at time.Time) string { return at.Format("2006-01-02 15:04:05.000000") }
	for i, raw := range [][]byte{parentRaw, nextRaw} {
		revision, clock := "1", born
		if i == 1 {
			revision, clock = "2", at
		}
		retentionMetadataRow(t, a.Data, "retention_reference_versions", map[string]string{"reference_id": id, "revision": revision, "project_id": project, "source_slot_id": id, "source_payload_hash": hash, "record_hash": digest(raw), "snapshot": string(raw), "recorded_at": stamp(clock)})
	}
	retentionMetadataRow(t, a.Data, "retention_references", map[string]string{"id": id, "project_id": project, "owner_table": "operations", "owner_key": owner, "field_key": "operation_id", "class": "RAW_RUN", "storage_kind": "INLINE", "payload_hash": hash, "source_created_at": stamp(born), "time_basis": "LEGACY_REGISTERED", "first_verified_at": stamp(born), "deadline": stamp(deadline), "state": "EXPIRED", "expired_at": stamp(at), "content_root": "0", "accounting_open": "0", "revision": "2", "updated_at": stamp(at)})
	evidenceID, _ := retention.EvidenceID(id, 2)
	retentionMetadataRow(t, a.Data, "retention_evidence", map[string]string{"id": evidenceID, "project_id": project, "reference_id": id, "reference_revision": "2", "original_payload_hash": hash, "projection_hash": digest(proofRaw), "projection": string(proofRaw), "expired_at": stamp(at)})
	retentionMetadataRow(t, a.Data, "retention_source_slots", map[string]string{"id": id, "project_id": project, "owner_table": "operations", "owner_key": owner, "field_key": "operation_id", "time_basis": "LEGACY_REGISTERED", "first_verified_at": stamp(born)})
	retentionMetadataRow(t, a.Data, "retention_source_values", map[string]string{"source_id": id, "payload_hash": hash, "is_null": "0", "first_verified_at": stamp(born)})
	retentionMetadataRow(t, a.Data, "project_lifecycle", map[string]string{"project_id": project, "source_ledger_id": a.Manifest.Authority.LedgerID, "time_basis": "SOURCE_UTC", "first_verified_at": stamp(born), "inventory_revision": "3", "catalog_version": "1", "state": "UNSCANNED", "updated_at": stamp(at)})
	raw := archiveBytes(t, a)
	if _, err := ValidateArchive(bytes.NewReader(raw), int64(len(raw))); err != nil {
		t.Fatal("complete anonymous source history failed validation", err)
	}
	a.Data["retention_operation_keys"] = nil
	raw = archiveBytes(t, a)
	if _, err := ValidateArchive(bytes.NewReader(raw), int64(len(raw))); err == nil {
		t.Fatal("missing independent alias accepted", err)
	}
	a.Data["retention_operation_keys"] = []Row{alias}
	for _, spec := range retentionTableCatalog() {
		if spec.name == "retention_operation_keys" {
			spec.set(alias, "operation_kind", "project.delete")
		}
	}
	for _, spec := range tableCatalog {
		if spec.name == "operations" {
			spec.set(a.Data[spec.name][0], "kind", "project.delete")
		}
	}
	raw = archiveBytes(t, a)
	if _, err := ValidateArchive(bytes.NewReader(raw), int64(len(raw))); err == nil {
		t.Fatal("consistent alias/source rewrite bypassed original proof", err)
	}
}

func TestRetentionArchiveRequestIdentityChecksActualScope(t *testing.T) {
	for _, table := range []string{"operations", "model_calls", "generation_tasks"} {
		a, metadata, _ := archiveIdentityFixture(t, table)
		other := strings.Repeat("9", 32)
		testRow(a, "workspaces", map[string]string{"id": other})
		for _, spec := range tableCatalog {
			if spec.name == "projects" {
				spec.set(a.Data[spec.name][0], "workspace_id", other)
			}
		}
		if err := validateArchiveRequestIdentities(a.Data, metadata, a.Manifest.SnapshotAt); err == nil {
			t.Fatal("actual result crossed into another existing workspace", table)
		}
	}
}

func TestRetentionArchiveRequestIdentityAllowsUnretiredLegacyRequests(t *testing.T) {
	a, metadata, _ := archiveIdentityFixture(t, "operations")
	metadata["retention_operation_keys"] = nil
	for _, spec := range retentionTableCatalog() {
		if spec.name == "retention_identity_policies" {
			row := metadata[spec.name][0]
			spec.set(row, "legacy_writes_closed", "0")
			spec.set(row, "revision", "1")
			row[spec.index("closed_at")] = nil
		}
	}
	for _, spec := range tableCatalog {
		if spec.name == "operations" {
			spec.set(a.Data[spec.name][0], "operation_id", "原始旧请求")
		}
	}
	if err := validateArchiveRequestIdentities(a.Data, metadata, a.Manifest.SnapshotAt); err != nil {
		t.Fatal("pre-retirement request became unusable", err)
	}
}
