package storage

import (
	"context"
	"database/sql"
	"fmt"
	"novelstudio/internal/domain"
	"strings"
)

const SchemaVersion = 38

// RunMigrations pins a connection because advisory locks are connection-scoped.
func RunMigrations(ctx context.Context, db *sql.DB) error {
	return RunMigrationsTo(ctx, db, SchemaVersion)
}

// RunMigrationsTo installs only compiled migrations, never downgrades a database.
// Isolated legacy archive recovery uses its independently qualified baseline;
// normal application startup always calls RunMigrations for the latest version.
func RunMigrationsTo(ctx context.Context, db *sql.DB, target int) error {
	if target < 1 || target > SchemaVersion {
		return domain.ErrValidation
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	var lock int
	if err = conn.QueryRowContext(ctx, "SELECT GET_LOCK(CONCAT(DATABASE(), ':schema'),10)").Scan(&lock); err != nil {
		return err
	}
	if lock != 1 {
		return domain.ErrNotReady
	}
	defer conn.ExecContext(context.Background(), "SELECT RELEASE_LOCK(CONCAT(DATABASE(), ':schema'))")
	if err = ensureMigrationTable(ctx, conn, "schema_version", `CREATE TABLE IF NOT EXISTS schema_version(id INT PRIMARY KEY, version INT NOT NULL, checksum CHAR(64) NOT NULL) ENGINE=InnoDB`); err != nil {
		return err
	}
	var version int
	var stored string
	err = conn.QueryRowContext(ctx, "SELECT version,checksum FROM schema_version WHERE id=1").Scan(&version, &stored)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	if version > target {
		return domain.ErrNewerSchema
	}
	catalog, err := migrationCatalog()
	if err != nil {
		return err
	}
	if version < 0 || (version > 0 && stored != catalog[version-1].checksum) {
		return domain.ErrNotReady
	}
	if err = initializeHistory(ctx, conn, version, catalog); err != nil {
		return err
	}
	if err = checkHistory(ctx, conn, version, catalog); err != nil {
		return err
	}
	for index, item := range catalog {
		if index+1 > target {
			break
		}
		if index+1 <= version {
			continue
		}
		// DDL implicitly commits. Statements are resumable; marker advances last.
		for _, statement := range strings.Split(item.text, ";") {
			if strings.TrimSpace(statement) == "" {
				continue
			}
			if _, err = conn.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("migration %s: %w", item.name, err)
			}
		}
		if index+1 == 30 {
			if err = backfillLegacyQualificationCalls(ctx, conn); err != nil {
				return fmt.Errorf("migration %s legacy receipts: %w", item.name, err)
			}
		}
		if err = recordMigration(ctx, conn, index+1, item); err != nil {
			return err
		}
	}
	return nil
}

// Call only while holding the schema advisory lock. Even a no-op CREATE TABLE
// IF NOT EXISTS enters MySQL's binary log, so ordinary startup must first check
// the catalog without issuing DDL against an existing migration table.
func ensureMigrationTable(ctx context.Context, conn *sql.Conn, name, statement string) error {
	var count int
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name=?", name).Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return nil
	}
	_, err := conn.ExecContext(ctx, statement)
	return err
}

func CheckVersion(ctx context.Context, db *sql.DB) error {
	var version int
	if err := db.QueryRowContext(ctx, "SELECT version FROM schema_version WHERE id=1").Scan(&version); err != nil {
		return err
	}
	if version != SchemaVersion {
		return domain.ErrNewerSchema
	}
	catalog, err := migrationCatalog()
	if err != nil {
		return err
	}
	return checkHistory(ctx, db, version, catalog)
}
