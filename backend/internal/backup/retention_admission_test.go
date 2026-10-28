package backup

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"novelstudio/internal/memory"
)

func TestRetentionArchiveRunsBusinessAdmissionChecks(t *testing.T) {
	for _, change := range []string{"enabled identity", "password"} {
		t.Run(change, func(t *testing.T) {
			a, _ := archiveCleanupFixture(t)
			archiveQualificationCell(a, "users", 0, "enabled", "0")
			want := "archive identity enabled"
			if change == "enabled identity" {
				archiveQualificationCell(a, "users", 0, "enabled", "1")
			} else {
				archiveQualificationCell(a, "users", 0, "password_hash", strings.Repeat("a", 64))
				want = "secret reference in archive"
			}
			raw := archiveBytes(t, a)
			if _, err := ValidateArchive(bytes.NewReader(raw), int64(len(raw))); err == nil || !strings.Contains(err.Error(), want) {
				t.Fatal("retention path bypassed existing business admission", change, err)
			}
		})
	}
}

func TestRetentionArchiveKeepsExactMemorySourceAnchorChecks(t *testing.T) {
	for _, overflow := range []bool{false, true} {
		a := memoryArchiveFixture(t)
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
		if overflow {
			for _, table := range []string{"memory_items", "memory_revisions"} {
				spec := memorySpec(table)
				var item memory.Item
				if err := json.Unmarshal([]byte(spec.value(a.Data[table][0], "document")), &item); err != nil {
					t.Fatal(err)
				}
				item.Sources[0].End = 7
				raw, _ := json.Marshal(item)
				spec.set(a.Data[table][0], "document", string(raw))
			}
		}
		raw := archiveBytes(t, a)
		_, err = ValidateArchive(bytes.NewReader(raw), int64(len(raw)))
		if overflow && (err == nil || !strings.Contains(err.Error(), "memory source anchor exceeds exact published object")) || !overflow && err != nil {
			t.Fatal("retention memory object check", overflow, err)
		}
	}
}
