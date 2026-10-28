package qualification_test

import (
	"context"
	"novelstudio/internal/storage"
	"testing"
)

func TestQualificationMigrationResumesAfterCommittedDDL(t *testing.T) {
	f := isolated(t)
	ctx := context.Background()
	var checksum string
	if err := f.db.QueryRow("SELECT checksum FROM schema_migrations WHERE version=27").Scan(&checksum); err != nil {
		t.Fatal(err)
	}
	tx, err := f.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.Exec("DELETE FROM schema_migrations WHERE version>=28"); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec("UPDATE schema_version SET version=27,checksum=? WHERE id=1", checksum); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err = storage.RunMigrations(ctx, f.db); err != nil {
		t.Fatal("committed qualification DDL did not resume", err)
	}
	if err = storage.CheckVersion(ctx, f.db); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = f.db.QueryRow("SELECT COUNT(*) FROM schema_migrations WHERE version=28").Scan(&count); err != nil || count != 1 {
		t.Fatal("migration journal duplicated or missing", count, err)
	}
}

func TestWeeklyBudgetMigrationFromApplied28PreservesHistoricalChecksum(t *testing.T) {
	f := isolated(t)
	ctx := context.Background()
	var checksum string
	if err := f.db.QueryRow("SELECT checksum FROM schema_migrations WHERE version=28").Scan(&checksum); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec("DELETE FROM schema_migrations WHERE version>=29"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec("UPDATE schema_version SET version=28,checksum=? WHERE id=1", checksum); err != nil {
		t.Fatal(err)
	}
	if err := storage.RunMigrations(ctx, f.db); err != nil {
		t.Fatal(err)
	}
	var after string
	if err := f.db.QueryRow("SELECT checksum FROM schema_migrations WHERE version=28").Scan(&after); err != nil || after != checksum {
		t.Fatal("applied28 history changed", err)
	}
	if err := storage.CheckVersion(ctx, f.db); err != nil {
		t.Fatal(err)
	}
}
