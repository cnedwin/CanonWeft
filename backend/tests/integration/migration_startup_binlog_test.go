package integration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/go-sql-driver/mysql"
	"novelstudio/internal/domain"
	"novelstudio/internal/service"
	"novelstudio/internal/storage"
)

// Binlog coordinates are global: this test requires an exclusively owned,
// binlog-enabled instance, never the shared application/test server on 13306.
func migrationStartupDatabase(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("STUDIO_MIGRATION_TEST_ADMIN_DSN")
	if dsn == "" {
		t.Skip("exclusive binlog-enabled migration test instance required")
	}
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil || cfg.User != "root" || cfg.Net != "tcp" || cfg.Addr != "127.0.0.1:13316" || cfg.DBName != "" {
		t.Fatal("explicit isolated migration administrator required")
	}
	admin, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.Close() })
	var enabled bool
	if err = admin.QueryRow("SELECT @@log_bin").Scan(&enabled); err != nil || !enabled {
		t.Fatal("test requires actual binary logging", err)
	}
	name := "sm_" + service.NewID()
	if _, err = admin.Exec("CREATE DATABASE `" + name + "`"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec("DROP DATABASE `" + name + "`"); err != nil {
			t.Error(err)
		}
	})
	cfg.DBName = name
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func migrationStartupRows(t *testing.T, db *sql.DB, query string) string {
	t.Helper()
	rows, err := db.Query(query)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	var all [][]string
	for rows.Next() {
		values := make([]sql.NullString, len(cols))
		dest := make([]any, len(cols))
		for i := range values {
			dest[i] = &values[i]
		}
		if err = rows.Scan(dest...); err != nil {
			t.Fatal(err)
		}
		row := make([]string, len(cols))
		for i, value := range values {
			row[i] = value.String
		}
		all = append(all, row)
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(all)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func assertMigrationStartupReadOnly(t *testing.T, db *sql.DB, target int, want error) {
	t.Helper()
	queries := []string{
		"SHOW BINARY LOG STATUS",
		"SELECT * FROM schema_version ORDER BY id",
		"SELECT * FROM schema_migrations ORDER BY version",
		"SELECT * FROM workspaces ORDER BY id",
		"SELECT table_name,column_name,column_type,is_nullable,column_default,extra FROM information_schema.columns WHERE table_schema=DATABASE() ORDER BY table_name,ordinal_position",
	}
	before := make([]string, len(queries))
	for i, query := range queries {
		before[i] = migrationStartupRows(t, db, query)
	}
	for attempt := 0; attempt < 2; attempt++ {
		err := storage.RunMigrationsTo(context.Background(), db, target)
		if !errors.Is(err, want) {
			t.Fatalf("startup error: got %v want %v", err, want)
		}
	}
	for i, query := range queries {
		after := migrationStartupRows(t, db, query)
		if after != before[i] {
			if i == 0 {
				t.Errorf("repeated startup advanced binary log: before=%s after=%s", before[i], after)
			} else {
				t.Errorf("startup changed schema or rows for query %d", i)
			}
		}
	}
}

func TestMigrationStartupPreservesBinaryLogAndData(t *testing.T) {
	db := migrationStartupDatabase(t)
	if err := storage.RunMigrations(context.Background(), db); err != nil {
		t.Fatal("fresh install", err)
	}
	if err := storage.CheckVersion(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO workspaces(id,name) VALUES(?,?)", service.NewID(), "preserved fixture"); err != nil {
		t.Fatal(err)
	}
	assertMigrationStartupReadOnly(t, db, storage.SchemaVersion, nil)
}

func TestMigrationStartupLegacyHistoryAndUpgrade(t *testing.T) {
	db := migrationStartupDatabase(t)
	if err := storage.RunMigrationsTo(context.Background(), db, 1); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{"DROP TABLE schema_migrations", "ALTER TABLE schema_version DROP COLUMN history_initialized"} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := storage.RunMigrationsTo(context.Background(), db, 2); err != nil {
		t.Fatal("legacy adoption and real upgrade", err)
	}
	if got := migrationStartupRows(t, db, "SELECT version,origin FROM schema_migrations ORDER BY version"); got != `[["1","LEGACY_BASELINE"],["2","EXECUTED"]]` {
		t.Fatal("wrong provenance", got)
	}
	assertMigrationStartupReadOnly(t, db, 2, nil)
}

func TestMigrationStartupRejectsCorruptionWithoutWrites(t *testing.T) {
	for _, tc := range []struct {
		name, mutation string
		want           error
	}{
		{"history_checksum", "UPDATE schema_migrations SET checksum=REPEAT('0',64) WHERE version=1", domain.ErrNotReady},
		{"missing_history_row", "DELETE FROM schema_migrations WHERE version=1", domain.ErrNotReady},
		{"marker_checksum", "UPDATE schema_version SET checksum=REPEAT('0',64) WHERE id=1", domain.ErrNotReady},
		{"future_version", "UPDATE schema_version SET version=999 WHERE id=1", domain.ErrNewerSchema},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := migrationStartupDatabase(t)
			if err := storage.RunMigrationsTo(context.Background(), db, 2); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(tc.mutation); err != nil {
				t.Fatal(err)
			}
			assertMigrationStartupReadOnly(t, db, 2, tc.want)
		})
	}
}
