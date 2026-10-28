package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"novelstudio/internal/domain"
	"novelstudio/migrations"
	"strings"
)

type migrationFile struct{ name, checksum, text string }

func migrationCatalog() ([]migrationFile, error) {
	names := []string{"0001_workspace.sql", "0002_ip_versions.sql", "0003_ip_collaboration.sql", "0004_artifact_releases.sql", "0005_resource_drafts.sql", "0006_ip_amendments.sql", "0007_model_connections.sql", "0008_model_budget.sql", "0009_ip_promotions.sql", "0010_review_tasks.sql", "0011_knowledge.sql", "0012_source_imports.sql", "0013_generation.sql", "0014_resource_policy_drafts.sql", "0015_skill_packages.sql"}
	names = append(names, "0016_model_reconciliation.sql")
	names = append(names, "0017_change_plans.sql")
	names = append(names, "0018_change_plan_releases.sql")
	names = append(names, "0019_index_jobs.sql")
	names = append(names, "0020_capabilities.sql")
	names = append(names, "0021_preset_installations.sql")
	names = append(names, "0022_capability_exact_json.sql")
	names = append(names, "0023_skill_discovery.sql")
	names = append(names, "0024_tool_trials.sql")
	names = append(names, "0025_project_deletion.sql")
	names = append(names, "0026_generation_previews.sql")
	names = append(names, "0027_private_working_index.sql")
	names = append(names, "0028_capability_qualification.sql")
	names = append(names, "0029_qualification_budget.sql")
	names = append(names, "0030_qualification_legacy_provenance.sql")
	names = append(names, "0031_semantic_reviews.sql")
	names = append(names, "0032_patch_drafts.sql")
	names = append(names, "0033_resumable_ingestion.sql")
	names = append(names, "0034_recipe_execution.sql")
	names = append(names, "0035_memory.sql")
	if len(names) != 35 {
		return nil, domain.ErrNotReady
	}
	result := make([]migrationFile, 0, len(names))
	for _, name := range names {
		raw, err := migrations.Files.ReadFile(name)
		if err != nil {
			return nil, err
		}
		text := strings.ReplaceAll(string(raw), "\r\n", "\n")
		sum := sha256.Sum256([]byte(text))
		result = append(result, migrationFile{name: name, checksum: hex.EncodeToString(sum[:]), text: text})
	}
	// Preserve the exact previously qualified archive/log contract and checksum.
	// Its split SQL files retain their historical names; catalog membership here
	// activates the complete migration, never an individual partial file.
	text, checksum, err := migrations.PendingRetentionSQL()
	if err != nil {
		return nil, err
	}
	result = append(result, migrationFile{name: "0036_retention.sql", checksum: checksum, text: text})
	text, checksum, err = migrations.NovelEntrySQL()
	if err != nil {
		return nil, err
	}
	result = append(result, migrationFile{name: migrations.NovelEntryName, checksum: checksum, text: text})
	text, checksum, err = migrations.NovelSettingsSQL()
	if err != nil {
		return nil, err
	}
	result = append(result, migrationFile{name: migrations.NovelSettingsName, checksum: checksum, text: text})
	if len(result) != SchemaVersion {
		return nil, domain.ErrNotReady
	}
	return result, nil
}

type historyReader interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func checkHistory(ctx context.Context, db historyReader, version int, catalog []migrationFile) error {
	rows, err := db.QueryContext(ctx, "SELECT version,name,checksum FROM schema_migrations ORDER BY version")
	if err != nil {
		return err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var n int
		var name, checksum string
		if err = rows.Scan(&n, &name, &checksum); err != nil {
			return err
		}
		count++
		if n != count || n > version || n > len(catalog) || name != catalog[n-1].name || checksum != catalog[n-1].checksum {
			return fmt.Errorf("migration journal mismatch at %d: %w", n, domain.ErrNotReady)
		}
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if count != version {
		return domain.ErrNotReady
	}
	return nil
}

// A pre-journal database only retained the latest checksum. Record that boundary
// explicitly; earlier execution cannot be proved retroactively by backfilling.
func initializeHistory(ctx context.Context, conn *sql.Conn, version int, catalog []migrationFile) error {
	var column int
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name='schema_version' AND column_name='history_initialized'").Scan(&column); err != nil {
		return err
	}
	if column == 0 {
		if _, err := conn.ExecContext(ctx, "ALTER TABLE schema_version ADD COLUMN history_initialized BOOLEAN NOT NULL DEFAULT FALSE"); err != nil {
			return err
		}
	}
	if _, err := conn.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations(version INT PRIMARY KEY,name VARCHAR(120) NOT NULL,checksum CHAR(64) NOT NULL,origin VARCHAR(24) NOT NULL,recorded_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6)) ENGINE=InnoDB`); err != nil {
		return err
	}
	var initialized bool
	err := conn.QueryRowContext(ctx, "SELECT history_initialized FROM schema_version WHERE id=1").Scan(&initialized)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	if initialized || version == 0 {
		return nil
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var count int
	if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations").Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return domain.ErrNotReady
	}
	for n := 1; n <= version; n++ {
		item := catalog[n-1]
		if _, err = tx.ExecContext(ctx, "INSERT INTO schema_migrations(version,name,checksum,origin) VALUES(?,?,?,'LEGACY_BASELINE')", n, item.name, item.checksum); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, "UPDATE schema_version SET history_initialized=TRUE WHERE id=1"); err != nil {
		return err
	}
	return tx.Commit()
}

func recordMigration(ctx context.Context, conn *sql.Conn, version int, item migrationFile) error {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "INSERT INTO schema_migrations(version,name,checksum,origin) VALUES(?,?,?,'EXECUTED')", version, item.name, item.checksum); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO schema_version(id,version,checksum,history_initialized) VALUES(1,?,?,TRUE) ON DUPLICATE KEY UPDATE version=VALUES(version),checksum=VALUES(checksum),history_initialized=TRUE", version, item.checksum); err != nil {
		return err
	}
	return tx.Commit()
}
