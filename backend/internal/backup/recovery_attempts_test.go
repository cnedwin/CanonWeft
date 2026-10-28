package backup

import (
	"strings"
	"testing"
)

func TestRecoveryJournalArchiveValidationAndRestoreDisable(t *testing.T) {
	a, _ := archiveCleanupFixture(t)
	row := retentionMetadataRow(t, a.Data, "retention_recovery_attempts", map[string]string{
		"target_project_id": strings.Repeat("d", 32), "project_id": strings.Repeat("a", 32), "actor_id": strings.Repeat("c", 32), "storage_namespace": strings.Repeat("e", 64), "objects": "[]", "state": "WRITING", "restored_disabled": "0", "created_at": "2026-10-26 11:00:00", "updated_at": "2026-10-26 11:00:00",
	})
	spec, err := archiveStateTable(a.Data, "retention_recovery_attempts")
	if err != nil {
		t.Fatal(err)
	}
	if err := validateArchiveRecoveryAttempts(a.Data, a.Data, a.Manifest.SnapshotAt); err != nil {
		t.Fatal("valid unfinished recovery rejected", err)
	}
	for _, bad := range []struct{ field, value string }{
		{"objects", `[{"prompt":"private author text"}]`}, {"actor_id", strings.Repeat("f", 32)}, {"state", "COMMITTED"}, {"storage_namespace", "other-store"}, {"restored_disabled", "2"},
	} {
		before := spec.value(row, bad.field)
		spec.set(row, bad.field, bad.value)
		if err := validateArchiveRecoveryAttempts(a.Data, a.Data, a.Manifest.SnapshotAt); err == nil {
			t.Fatal("invalid recovery archive accepted", bad.field)
		}
		spec.set(row, bad.field, before)
	}
	sanitizeWithSourceHistory(a.Data, true)
	if spec.value(row, "restored_disabled") != "1" || spec.value(row, "state") != "WRITING" || spec.value(row, "objects") != "[]" {
		t.Fatal("restore enabled cleanup or rewrote evidence")
	}
}
