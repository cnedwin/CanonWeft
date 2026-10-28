package integration

import (
	"context"
	"database/sql"
	"testing"

	"novelstudio/internal/storage"
)

// A fixture for guards that previously ran against the pre-activation schema.
// Only the migration marker is rolled back in this disposable database; compiled
// application gates remain unchanged and must refuse writes until repaired.
func interruptRetentionMigrationMarker(t *testing.T, db *sql.DB) {
	t.Helper()
	var previous string
	if err := db.QueryRow("SELECT checksum FROM schema_migrations WHERE version=35").Scan(&previous); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("DELETE FROM schema_migrations WHERE version=36"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE schema_version SET version=35,checksum=? WHERE id=1", previous); err != nil {
		t.Fatal(err)
	}
}

func TestRetentionMigrationActivatesHTTPAndResumesCommittedDDL(t *testing.T) {
	b, db := setup(t)
	var version int
	if err := db.QueryRow("SELECT version FROM schema_version WHERE id=1").Scan(&version); err != nil || version != 36 {
		t.Fatal("normal startup did not install retention schema", version, err)
	}
	project := createProject(b, "正式迁移后的小说", "retention-migration-project")
	path := "/api/projects/" + project + "/retention-inventory/scan"
	if result := b.request("POST", path, nil, 200); result["complete"] != true {
		t.Fatal("installed HTTP scan unavailable", result)
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM retention_references WHERE project_id=?", project).Scan(&count); err != nil || count == 0 {
		t.Fatal("live writers did not register sources", count, err)
	}
	var previous string
	if err := db.QueryRow("SELECT checksum FROM schema_migrations WHERE version=35").Scan(&previous); err != nil {
		t.Fatal(err)
	}
	// Exact state after all additive DDL committed but before its journal marker.
	if _, err := db.Exec("DELETE FROM schema_migrations WHERE version=36"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE schema_version SET version=35,checksum=? WHERE id=1", previous); err != nil {
		t.Fatal(err)
	}
	b.request("POST", path, nil, 503)
	if err := storage.RunMigrations(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	if err := storage.CheckVersion(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	var retained int
	if err := db.QueryRow("SELECT COUNT(*) FROM retention_references WHERE project_id=?", project).Scan(&retained); err != nil || retained != count {
		t.Fatal("migration retry changed retained references", retained, count, err)
	}
	b.request("POST", path, nil, 200)
	if err := storage.RunMigrations(context.Background(), db); err != nil {
		t.Fatal("repeated startup", err)
	}
}
