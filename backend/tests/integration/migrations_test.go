package integration

import (
	"context"
	"novelstudio/internal/storage"
	"strings"
	"testing"
)

func TestAllMigrationChecksumsGuardStartupAndWrites(t *testing.T) {
	b, db := setup(t)
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM schema_migrations").Scan(&count); err != nil || count != storage.SchemaVersion {
		t.Fatal("complete migration journal missing", count, err)
	}
	var original string
	if err := db.QueryRow("SELECT checksum FROM schema_migrations WHERE version=1").Scan(&original); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.Exec("UPDATE schema_migrations SET checksum=? WHERE version=1", original) })
	if _, err := db.Exec("UPDATE schema_migrations SET checksum=? WHERE version=1", strings.Repeat("0", 64)); err != nil {
		t.Fatal(err)
	}
	if err := storage.RunMigrations(context.Background(), db); err == nil {
		t.Fatal("changed old migration was accepted")
	}
	if err := storage.CheckVersion(context.Background(), db); err == nil {
		t.Fatal("write guard ignored old migration")
	}
	b.request("POST", "/api/projects", map[string]any{"name": "unsafe", "operation_id": "old-migration"}, 503)
	if _, err := db.Exec("UPDATE schema_migrations SET checksum=? WHERE version=1", original); err != nil {
		t.Fatal(err)
	}
	if err := storage.RunMigrations(context.Background(), db); err != nil {
		t.Fatal("valid history could not recover", err)
	}
}

func TestPrivateMigrationRecoversCommittedDDL(t *testing.T) {
	db := database(t)
	// Simulate a crash after MySQL commits 0027 DDL but before its journal commit.
	var checksum string
	if err := db.QueryRow("SELECT checksum FROM schema_migrations WHERE version=26").Scan(&checksum); err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec("DELETE FROM schema_migrations WHERE version>=27"); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if _, err = tx.Exec("UPDATE schema_version SET version=26,checksum=? WHERE id=1", checksum); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err = storage.RunMigrations(context.Background(), db); err != nil {
		t.Fatal("committed DDL cannot recover", err)
	}
	if err = storage.CheckVersion(context.Background(), db); err != nil {
		t.Fatal(err)
	}
}
