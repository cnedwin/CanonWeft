package backup

import (
	"strings"
	"testing"
	"time"

	"novelstudio/internal/retention"
)

func retentionMetadataRow(t *testing.T, data Data, name string, values map[string]string) Row {
	t.Helper()
	for _, spec := range retentionTableCatalog() {
		if spec.name != name {
			continue
		}
		row := make(Row, len(spec.cols()))
		for key, value := range values {
			if spec.index(key) < 0 {
				t.Fatal("unknown metadata column", key)
			}
			spec.set(row, key, value)
		}
		data[name] = append(data[name], row)
		return row
	}
	t.Fatal("unknown metadata table", name)
	return nil
}

func retentionStateFixture(t *testing.T) (*Archive, Data, Row) {
	t.Helper()
	a := emptyArchive(t)
	a.Manifest.SnapshotAt = time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	testRow(a, "projects", map[string]string{"id": strings.Repeat("a", 32)})
	metadata := Data{"project_lifecycle": nil, "retention_objects": nil, "retention_source_slots": nil}
	row := retentionMetadataRow(t, metadata, "project_lifecycle", map[string]string{
		"project_id": strings.Repeat("a", 32), "source_ledger_id": a.Manifest.Authority.LedgerID,
		"time_basis": "SOURCE_UTC", "first_verified_at": "2026-10-01 00:00:00.000000",
		"inventory_revision": "1", "catalog_version": "1", "state": "UNSCANNED", "updated_at": "2026-10-26 11:00:00.000000",
	})
	return a, metadata, row
}

func TestRetentionArchiveLifecycleClocksAndProvenance(t *testing.T) {
	a, metadata, _ := retentionStateFixture(t)
	states, err := readArchiveLifecycles(a.Data, metadata, a.Manifest)
	if err != nil || len(states) != 1 {
		t.Fatal("active original lifecycle rejected", err)
	}
	for _, change := range []struct{ field, value string }{
		{"source_ledger_id", strings.Repeat("e", 32)}, {"project_id", strings.Repeat("b", 32)},
		{"inventory_revision", "0"}, {"inventory_revision", "9223372036854775808"},
		{"catalog_version", "2"}, {"state", "UNKNOWN"}, {"state", "CURRENT"}, {"state", "PURGED"},
		{"first_verified_at", "2026-10-27 00:00:00"}, {"updated_at", "2026-09-01 00:00:00"},
		{"updated_at", "2026-10-27 00:00:00"}, {"scanned_at", "2026-10-26 10:00:00"},
		{"delete_sequence", "0"}, {"deleted_at", ""}, {"recover_until", "2026-11-01 00:00:00"},
	} {
		t.Run(change.field+"/"+change.value, func(t *testing.T) {
			a, metadata, row := retentionStateFixture(t)
			for _, spec := range retentionTableCatalog() {
				if spec.name == "project_lifecycle" {
					spec.set(row, change.field, change.value)
				}
			}
			if _, err := readArchiveLifecycles(a.Data, metadata, a.Manifest); err == nil {
				t.Fatal("invalid archived lifecycle accepted")
			}
		})
	}
	metadata["project_lifecycle"] = append(metadata["project_lifecycle"], metadata["project_lifecycle"][0])
	if _, err := readArchiveLifecycles(a.Data, metadata, a.Manifest); err == nil {
		t.Fatal("duplicate lifecycle accepted")
	}
}

func TestNovelArchiveCatalogRequiresSchema37(t *testing.T) {
	for _, schema := range []int{36, 37, 38} {
		for _, catalog := range []string{"1", "2", "3", "4"} {
			a, metadata, row := retentionStateFixture(t)
			a.Manifest.Schema = schema
			for _, spec := range retentionTableCatalog() {
				if spec.name == "project_lifecycle" {
					spec.set(row, "catalog_version", catalog)
				}
			}
			_, err := readArchiveLifecycles(a.Data, metadata, a.Manifest)
			want := catalog == "1" || (schema >= 37 && catalog == "2") || (schema == 38 && catalog == "3")
			if (err == nil) != want {
				t.Fatalf("schema=%d catalog=%s accepted=%v", schema, catalog, err == nil)
			}
		}
	}
}

func TestRetentionArchiveDeletedLifecycleCannotRenewRecovery(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		a, metadata, row := retentionStateFixture(t)
		at := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
		want, _ := retention.DeletedLifecycle(a.Manifest.Authority.LedgerID, 17, at)
		if legacy {
			want, _ = retention.LegacyDeletedLifecycle(a.Manifest.Authority.LedgerID, 17, at.Add(-time.Hour), at)
		}
		for _, spec := range tableCatalog {
			if spec.name == "projects" {
				spec.set(a.Data["projects"][0], "deleted_at", "2026-10-10 00:00:00.000001")
			}
		}
		var lifecycleSpec tableSpec
		for _, spec := range retentionTableCatalog() {
			if spec.name == "project_lifecycle" {
				lifecycleSpec = spec
			}
		}
		lifecycleSpec.set(row, "delete_sequence", "17")
		lifecycleSpec.set(row, "time_basis", string(want.TimeBasis))
		for name, clock := range map[string]time.Time{
			"source_deleted_at": want.SourceDeletedAt, "deleted_at": want.DeletedAt,
			"recover_until": want.RecoverUntil, "cleanup_eligible_at": want.CleanupEligibleAt,
			"online_deadline": want.OnlineDeadline, "backup_deadline": want.BackupDeadline,
		} {
			if !clock.IsZero() {
				lifecycleSpec.set(row, name, clock.Format("2006-01-02 15:04:05.000000"))
			}
		}
		got, err := readArchiveLifecycles(a.Data, metadata, a.Manifest)
		if err != nil || got[strings.Repeat("a", 32)].Lifecycle != want {
			t.Fatal("original deleted lifecycle rejected", legacy, err)
		}
		lifecycleSpec.set(row, "recover_until", "2026-11-02 00:00:00")
		if _, err := readArchiveLifecycles(a.Data, metadata, a.Manifest); err == nil {
			t.Fatal("recovery window renewed by archive", legacy)
		}
	}
}

func TestRetentionArchiveLifecycleMustMatchHistoricalDeletion(t *testing.T) {
	a, metadata, _ := retentionStateFixture(t)
	states, err := readArchiveLifecycles(a.Data, metadata, a.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	project := strings.Repeat("a", 32)
	deleted, _ := retention.DeletedLifecycle(a.Manifest.Authority.LedgerID, 17, a.Manifest.SnapshotAt.Add(-24*time.Hour))
	history := &archiveReferenceHistory{Versions: map[string]retention.ReferenceVersion{
		"historical": {ProjectID: project, Lifecycle: deleted},
	}}
	// Even a superseded historical record binds the original deletion event;
	// changing the archive's current lifecycle cannot silently renew its age.
	if err := validateArchiveLifecycleHistory(metadata, history, states); err == nil {
		t.Fatal("historical deletion disappeared from current lifecycle")
	}
	state := states[project]
	state.Lifecycle = deleted
	states[project] = state
	if err := validateArchiveLifecycleHistory(metadata, history, states); err != nil {
		t.Fatal("same original deletion rejected", err)
	}
	state.Lifecycle, _ = retention.DeletedLifecycle(a.Manifest.Authority.LedgerID, 18, deleted.DeletedAt.Add(time.Hour))
	states[project] = state
	if err := validateArchiveLifecycleHistory(metadata, history, states); err == nil {
		t.Fatal("deletion identity or original deadline replaced")
	}
}

func TestRetentionArchiveObjectStateCannotHideRetainedMaterial(t *testing.T) {
	a, metadata, _ := retentionStateFixture(t)
	project, hash := strings.Repeat("a", 32), strings.Repeat("b", 64)
	row := retentionMetadataRow(t, metadata, "retention_objects", map[string]string{
		"project_id": project, "object_hash": hash, "first_seen_at": "2026-10-26 11:00:00",
		"lease_until": "2026-10-26 12:10:00", "state": "PRESENT", "revision": "2",
	})
	materials := map[string][]retention.MaterialReference{project: {{Storage: retention.Blob, ObjectHash: hash}}}
	if err := validateArchiveObjects(a.Data, metadata, materials, a.Manifest.SnapshotAt); err != nil {
		t.Fatal("original future lease rejected", err)
	}
	var spec tableSpec
	for _, s := range retentionTableCatalog() {
		if s.name == "retention_objects" {
			spec = s
		}
	}
	for _, change := range []struct{ field, value string }{
		{"project_id", strings.Repeat("c", 32)}, {"object_hash", "invalid"}, {"revision", "0"},
		{"first_seen_at", "2026-10-27 00:00:00"}, {"lease_until", "2026-10-01 00:00:00"},
		{"state", "DELETING"}, {"state", "DELETED"}, {"deleted_at", "2026-10-26 11:30:00"},
	} {
		original := row[spec.index(change.field)]
		spec.set(row, change.field, change.value)
		if err := validateArchiveObjects(a.Data, metadata, materials, a.Manifest.SnapshotAt); err == nil {
			t.Fatal("invalid object accepted", change.field, change.value)
		}
		row[spec.index(change.field)] = original
	}
	// An expired object's tombstone can remain without requiring erased bytes.
	metadata["retention_objects"][0][spec.index("lease_until")] = nil
	spec.set(row, "state", "DELETED")
	spec.set(row, "deleted_at", "2026-10-26 11:30:00")
	if err := validateArchiveObjects(a.Data, metadata, nil, a.Manifest.SnapshotAt); err != nil {
		t.Fatal("unreferenced deleted object rejected", err)
	}
}
