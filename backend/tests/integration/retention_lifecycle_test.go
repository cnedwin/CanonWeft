package integration

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"novelstudio/internal/authority"
	"novelstudio/internal/retention"
	"novelstudio/internal/service"
	"novelstudio/internal/storage"
)

func installLifecycleFixture(t *testing.T, db *sql.DB) {
	t.Helper()
	var version int
	if err := db.QueryRow("SELECT version FROM schema_version WHERE id=1").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version == storage.SchemaVersion {
		if err := storage.CheckVersion(context.Background(), db); err != nil {
			t.Fatal(err)
		}
		return
	}
	for _, file := range []string{"retention.sql.pending", "retention_identity.sql.pending"} {
		raw, err := os.ReadFile("../../migrations/" + file)
		if err != nil {
			t.Fatal(err)
		}
		for _, statement := range strings.Split(string(raw), ";") {
			if strings.TrimSpace(statement) != "" {
				if _, err := db.Exec(statement); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	if _, err := db.Exec("INSERT INTO schema_migrations(version,name,checksum,origin) VALUES(36,?,?, 'EXECUTED')", retention.MigrationName, strings.Repeat("0", 64)); err != nil {
		t.Fatal(err)
	}
}

func TestRetentionLifecycleUTCAndImmutableReplay(t *testing.T) {
	b, db := setup(t)
	project := createProject(b, "期限项目", "clock-project")
	installLifecycleFixture(t, db)
	actor := b.request("GET", "/api/me", nil, 200)["id"].(string)
	ctx := context.Background()
	ledger := authority.Ledger{Schema: os.Getenv("STUDIO_AUTHORITY_SCHEMA")}
	// The writer must declare UTC even when a caller's session is +08:00.
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec("SET time_zone='+08:00'"); err != nil {
		t.Fatal(err)
	}
	if err := ledger.AppendTx(ctx, tx, authority.Event{Kind: authority.ProjectDeleted, ProjectID: project, ResourceID: project, ActorID: actor}); err != nil {
		t.Fatal(err)
	}
	evidence, err := ledger.DeletionEvidenceTx(ctx, tx, project)
	if err != nil || !evidence.VerifiedUTC || evidence.Sequence == 0 || evidence.CheckedAt.Sub(evidence.DeletedAt) < 0 || evidence.CheckedAt.Sub(evidence.DeletedAt) > time.Minute {
		t.Fatal("UTC evidence missing", evidence, err)
	}
	first, err := retention.RegisterProjectLifecycle(ctx, tx, ledger, project)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Lifecycle.DeletedAt.Equal(evidence.DeletedAt) || !first.Lifecycle.RecoverUntil.Equal(evidence.DeletedAt.Add(7*24*time.Hour)) {
		t.Fatal("recovery clock derived from wrong source", first)
	}
	if _, err := tx.Exec("SET time_zone='+00:00'"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	tx, err = db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := ledger.AppendTx(ctx, tx, authority.Event{Kind: authority.ProjectDeleted, ProjectID: project, ResourceID: project, ActorID: actor}); err != nil {
		t.Fatal(err)
	}
	again, err := retention.RegisterProjectLifecycle(ctx, tx, ledger, project)
	if err != nil || first != again {
		t.Fatal("replay reset lifecycle", first, again, err)
	}
	active, err := ledger.ProjectActiveTx(ctx, tx, project)
	if err != nil || active {
		t.Fatal("business deleted_at absence revived a deleted project", err)
	}
}

func TestRetentionLifecycleLegacyRegistrationDoesNotRestoreAccess(t *testing.T) {
	b, db := setup(t)
	project := createProject(b, "历史删除", "legacy-clock-project")
	installLifecycleFixture(t, db)
	ledger := authority.Ledger{Schema: os.Getenv("STUDIO_AUTHORITY_SCHEMA")}
	var ledgerID string
	if err := db.QueryRow("SELECT ledger_id FROM `" + ledger.Schema + "`.sources WHERE source_database=DATABASE()").Scan(&ledgerID); err != nil {
		t.Fatal(err)
	}
	// Simulate an old application: it cannot assert the new UTC basis by default.
	if _, err := db.Exec("INSERT INTO `"+ledger.Schema+"`.events(ledger_id,kind,project_id,resource_id,actor_id,reason,created_at) VALUES(?,'project_deleted',?,?,'','old local-time deletion','2020-01-02 08:00:00')", ledgerID, project, project); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	register := func() retention.RegisteredLifecycle {
		t.Helper()
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		result, err := retention.RegisterProjectLifecycle(ctx, tx, ledger, project)
		if err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		return result
	}
	first := register()
	if first.Lifecycle.TimeBasis != retention.LegacyRegistered || !first.Lifecycle.RecoverUntil.IsZero() || first.Lifecycle.SourceDeletedAt.Year() != 2020 || first.Lifecycle.DeletedAt.Year() == 2020 {
		t.Fatal("legacy evidence incorrectly granted recovery", first)
	}
	if again := register(); first != again {
		t.Fatal("first registration was reset", first, again)
	}
	if _, err := db.Exec("UPDATE project_lifecycle SET source_deleted_at='2021-01-01' WHERE project_id=?", project); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := retention.RegisterProjectLifecycle(ctx, tx, ledger, project); err == nil {
		t.Fatal("mismatched registry overwritten")
	}
}

func TestRetentionLifecycleDeletionServiceCommitsAndRollsBackTogether(t *testing.T) {
	b, db := setup(t)
	project := createProject(b, "事务删除", "atomic-clock-project")
	other := createProject(b, "失败保留", "atomic-clock-other")
	installLifecycleFixture(t, db)
	actor := b.request("GET", "/api/me", nil, 200)["id"].(string)
	svc := service.Workspace{DB: db}
	ctx := context.Background()
	ledger := authority.Ledger{Schema: os.Getenv("STUDIO_AUTHORITY_SCHEMA")}
	initialTx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer initialTx.Rollback()
	initial, err := retention.RegisterProjectLifecycle(ctx, initialTx, ledger, project)
	if err != nil || initial.Revision != 1 || initial.Lifecycle != (retention.Lifecycle{}) {
		t.Fatal("active registration failed", initial, err)
	}
	if err := initialTx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteProject(ctx, actor, project, "事务删除", 1, "delete-clock"); err != nil {
		t.Fatal(err)
	}
	var sequence uint64
	var first string
	var revision int64
	if err := db.QueryRow("SELECT delete_sequence,CAST(first_verified_at AS CHAR),inventory_revision FROM project_lifecycle WHERE project_id=?", project).Scan(&sequence, &first, &revision); err != nil || sequence == 0 || revision != 2 || first != initial.FirstVerifiedAt.Format("2006-01-02 15:04:05.000000") {
		t.Fatal("T0 omitted lifecycle", err)
	}
	if err := svc.DeleteProject(ctx, actor, project, "事务删除", 1, "delete-clock"); err != nil {
		t.Fatal(err)
	}
	var replay string
	if err := db.QueryRow("SELECT CAST(first_verified_at AS CHAR) FROM project_lifecycle WHERE project_id=?", project).Scan(&replay); err != nil || replay != first {
		t.Fatal("retry extended deletion", err)
	}
	// Missing installed lifecycle storage must roll back both business and ledger.
	if _, err := db.Exec("RENAME TABLE project_lifecycle TO test_missing_lifecycle"); err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteProject(ctx, actor, other, "失败保留", 1, "delete-failing-clock"); err == nil {
		t.Fatal("partial retention installation accepted")
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM projects WHERE id=? AND deleted_at IS NULL AND revision=1", other).Scan(&count); err != nil || count != 1 {
		t.Fatal("failed deletion changed project", err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	active, err := ledger.ProjectActiveTx(ctx, tx, other)
	if err != nil || !active {
		t.Fatal("failed registration left protected tombstone", err)
	}
}

func TestRetentionLifecycleReadsCurrentDeletionBeyondOldSnapshot(t *testing.T) {
	b, db := setup(t)
	project := createProject(b, "旧快照", "old-clock-snapshot")
	installLifecycleFixture(t, db)
	actor := b.request("GET", "/api/me", nil, 200)["id"].(string)
	ctx := context.Background()
	ledger := authority.Ledger{Schema: os.Getenv("STUDIO_AUTHORITY_SCHEMA")}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var oldCount int
	if err := tx.QueryRow("SELECT COUNT(*) FROM `"+ledger.Schema+"`.events WHERE project_id=? AND kind='project_deleted'", project).Scan(&oldCount); err != nil || oldCount != 0 {
		t.Fatal(err)
	}
	svc := service.Workspace{DB: db}
	if err := svc.DeleteProject(ctx, actor, project, "旧快照", 1, "delete-old-snapshot"); err != nil {
		t.Fatal(err)
	}
	registered, err := retention.RegisterProjectLifecycle(ctx, tx, ledger, project)
	if err != nil || registered.Lifecycle.DeleteSequence == 0 {
		t.Fatal("registration read stale protected authority", registered, err)
	}
}

func TestRetentionLifecycleRequiresExactMigrationMarker(t *testing.T) {
	b, db := setup(t)
	project := createProject(b, "未启用清理", "clock-inactive")
	installLifecycleFixture(t, db)
	if _, err := db.Exec("UPDATE schema_migrations SET name='unrelated_retention_experiment.sql' WHERE version=36"); err != nil {
		t.Fatal(err)
	}
	actor := b.request("GET", "/api/me", nil, 200)["id"].(string)
	svc := service.Workspace{DB: db}
	if err := svc.DeleteProject(context.Background(), actor, project, "未启用清理", 1, "delete-clock-inactive"); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM project_lifecycle").Scan(&count); err != nil || count != 0 {
		t.Fatal("unrelated migration activated lifecycle registration", count, err)
	}
}

func TestRetentionLifecycleClockUpgradeDoesNotRewriteLegacyEvidence(t *testing.T) {
	b, db := setup(t)
	ledger := authority.Ledger{Schema: os.Getenv("STUDIO_AUTHORITY_SCHEMA")}
	if _, err := db.Exec("ALTER TABLE `" + ledger.Schema + "`.events DROP COLUMN clock_basis"); err != nil {
		t.Fatal(err)
	}
	project := createProject(b, "旧账本兼容", "clock-old-schema")
	actor := b.request("GET", "/api/me", nil, 200)["id"].(string)
	ctx := context.Background()
	svc := service.Workspace{DB: db}
	if err := svc.DeleteProject(ctx, actor, project, "旧账本兼容", 1, "clock-old-delete"); err != nil {
		t.Fatal(err)
	}
	read := func() authority.DeletionEvidence {
		t.Helper()
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		evidence, err := ledger.DeletionEvidenceTx(ctx, tx, project)
		if err != nil {
			t.Fatal(err)
		}
		return evidence
	}
	before := read()
	if before.VerifiedUTC || before.Sequence == 0 {
		t.Fatal("old schema invented UTC evidence")
	}
	var business string
	if err := db.QueryRow("SELECT DATABASE()").Scan(&business); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.Provision(ctx, db, ledger.Schema, business); err != nil {
		t.Fatal(err)
	}
	after := read()
	if after.VerifiedUTC || after.Sequence != before.Sequence || !after.DeletedAt.Equal(before.DeletedAt) {
		t.Fatal("upgrade rewrote legacy evidence")
	}
	if err := svc.DeleteProject(ctx, actor, project, "旧账本兼容", 1, "clock-old-delete"); err != nil {
		t.Fatal(err)
	}
	if replay := read(); replay.VerifiedUTC || replay.Sequence != before.Sequence {
		t.Fatal("retry promoted old clock")
	}
}

func TestRetentionLifecycleDoesNotShiftWhenDriverLocationIsLocal(t *testing.T) {
	b, db := setup(t)
	project := createProject(b, "本地驱动时区", "clock-driver-location")
	installLifecycleFixture(t, db)
	actor := b.request("GET", "/api/me", nil, 200)["id"].(string)
	var business string
	if err := db.QueryRow("SELECT DATABASE()").Scan(&business); err != nil {
		t.Fatal(err)
	}
	cfg, err := mysql.ParseDSN(os.Getenv("STUDIO_INTEGRATION_TEST_ADMIN_DSN"))
	if err != nil {
		t.Fatal("invalid fixture connection")
	}
	cfg.DBName, cfg.ParseTime, cfg.Loc = business, true, time.FixedZone("fixture-local", 8*3600)
	connector, err := mysql.NewConnector(cfg)
	if err != nil {
		t.Fatal(err)
	}
	localDB := sql.OpenDB(connector)
	defer localDB.Close()
	svc := service.Workspace{DB: localDB}
	ctx := context.Background()
	if err := svc.DeleteProject(ctx, actor, project, "本地驱动时区", 1, "delete-driver-location"); err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteProject(ctx, actor, project, "本地驱动时区", 1, "delete-driver-location"); err != nil {
		t.Fatal("driver shifted stored UTC lifecycle", err)
	}
	var gap int
	if err := db.QueryRow("SELECT TIMESTAMPDIFF(SECOND,deleted_at,recover_until) FROM project_lifecycle WHERE project_id=?", project).Scan(&gap); err != nil || gap != 7*24*3600 {
		t.Fatal("wrong recovery interval", gap, err)
	}
}
