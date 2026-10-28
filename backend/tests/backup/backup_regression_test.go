package backup_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/go-sql-driver/mysql"
	ledgerstore "novelstudio/internal/authority"
	"novelstudio/internal/backup"
	"novelstudio/internal/domain"
	"novelstudio/internal/service"
	"novelstudio/internal/storage"
)

// These tests own only fresh, randomly named schemas. They never reset the
// shared integration schema or use a production source database.
type fixture struct {
	db, admin                *sql.DB
	config                   *mysql.Config
	workspace, user, project string
	root                     string
	blobs                    storage.LocalBlobs
	ledger                   ledgerstore.Ledger
}

func isolated(t *testing.T) *fixture {
	return isolatedSchema(t, storage.SchemaVersion)
}

func isolatedSchema(t *testing.T, version int) *fixture {
	t.Helper()
	dsn := os.Getenv("STUDIO_BACKUP_TEST_ADMIN_DSN")
	if dsn == "" {
		t.Skip("isolated MySQL test requires STUDIO_BACKUP_TEST_ADMIN_DSN")
	}
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		t.Fatal("invalid test configuration")
	}
	cfg.DBName = ""
	cfg.ParseTime = true
	admin, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.Close() })
	cfg.DBName = "studio_backup_test_" + service.NewID()
	if _, err = admin.Exec("CREATE DATABASE `" + cfg.DBName + "` CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci"); err != nil {
		t.Fatal(err)
	}
	cleanupSchema(t, admin, cfg.DBName)
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err = storage.RunMigrationsTo(context.Background(), db, version); err != nil {
		t.Fatal(err)
	}
	f := &fixture{db: db, admin: admin, config: cfg, workspace: service.NewID(), user: service.NewID(), project: service.NewID(), root: t.TempDir()}
	f.blobs = storage.LocalBlobs{Root: filepath.Join(f.root, "source")}
	execute(t, db, "INSERT INTO workspaces(id,name) VALUES(?,'backup regression')", f.workspace)
	execute(t, db, "INSERT INTO users(id,workspace_id,username,password_hash,role) VALUES(?,?,'owner','test-only','owner')", f.user, f.workspace)
	execute(t, db, "INSERT INTO projects(id,workspace_id,name) VALUES(?,?,'project')", f.project, f.workspace)
	schema := "studio_authority_test_" + service.NewID()
	t.Setenv("STUDIO_AUTHORITY_SCHEMA", schema)
	f.ledger, err = ledgerstore.Provision(context.Background(), admin, schema, cfg.DBName)
	if err != nil {
		t.Fatal(err)
	}
	cleanupSchema(t, admin, schema)
	return f
}
func cleanupSchema(t *testing.T, db *sql.DB, name string) {
	t.Helper()
	if !regexp.MustCompile(`^studio_(backup_test|authority_test|restore)_[a-f0-9]{32}$`).MatchString(name) {
		t.Fatal("unsafe test schema")
	}
	t.Cleanup(func() {
		if _, err := db.Exec("DROP DATABASE IF EXISTS `" + name + "`"); err != nil {
			t.Error(err)
		}
	})
}
func execute(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatal(err)
	}
}
func (f *fixture) put(t *testing.T, body string) string {
	t.Helper()
	ref, err := f.blobs.PutObject(context.Background(), f.project, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	return ref.Hash
}
func (f *fixture) options(t *testing.T, archive string, authority backup.CurrentAuthority) backup.RestoreOptions {
	t.Helper()
	cfg := f.config.Clone()
	cfg.DBName = "studio_restore_" + service.NewID()
	cleanupSchema(t, f.admin, cfg.DBName)
	return backup.RestoreOptions{ArchivePath: archive, TargetDSN: cfg.FormatDSN(), BlobRoot: filepath.Join(f.root, service.NewID()), Authority: authority}
}
func openTarget(t *testing.T, opt backup.RestoreOptions) *sql.DB {
	t.Helper()
	db, err := sql.Open("mysql", opt.TargetDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestReceiptOnlyObjectsSurviveCancelledLateAndConflictingSettlements(t *testing.T) {
	f := isolated(t)
	ctx := context.Background()
	late, conflict := service.NewID(), service.NewID()
	execute(t, f.db, "INSERT INTO model_budgets(workspace_id,currency,limit_micros,per_call_micros,reserved_micros,enabled,revision) VALUES(?,'USD',1000,100,200,1,1)", f.workspace)
	for i, id := range []string{late, conflict} {
		execute(t, f.db, "INSERT INTO model_calls(id,workspace_id,project_id,actor_id,operation_id,input_hash,reserved_micros,state,cancel_requested) VALUES(?,?,?,?,?, ?,100,'DISPATCHED',?)", id, f.workspace, f.project, f.user, id, strings.Repeat("a", 64), i == 0)
		execute(t, f.db, "INSERT INTO model_call_accounting(call_id,currency) VALUES(?,'USD')", id)
	}
	lateHash := f.put(t, "late cancelled result")
	firstHash := f.put(t, "accepted first result")
	conflictHash := f.put(t, "conflicting later result")
	budgets := service.Budgets{Workspace: &service.Workspace{DB: f.db}}
	if err := budgets.Settle(ctx, late, "SUCCEEDED", 80, "late-receipt", lateHash); err != nil {
		t.Fatal(err)
	}
	if err := budgets.Settle(ctx, conflict, "SUCCEEDED", 50, "first-receipt", firstHash); err != nil {
		t.Fatal(err)
	}
	if err := budgets.Settle(ctx, conflict, "SUCCEEDED", 60, "conflicting-receipt", conflictHash); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("real conflicting receipt behavior changed", err)
	}
	var state, hash string
	if err := f.db.QueryRow("SELECT state,output_hash FROM model_calls WHERE id=?", late).Scan(&state, &hash); err != nil || state != "CANCELLED" || hash != "" {
		t.Fatal("late result fixture is not receipt-only", state, hash, err)
	}
	if err := f.db.QueryRow("SELECT output_hash FROM model_calls WHERE id=?", conflict).Scan(&hash); err != nil || hash != firstHash {
		t.Fatal("conflicting result incorrectly replaced primary hash", err)
	}
	archive := filepath.Join(f.root, "receipts.zip")
	m, err := backup.Create(ctx, f.db, f.blobs, archive)
	if err != nil {
		t.Fatal(err)
	}
	if m.ObjectCount != 3 {
		t.Fatalf("receipt-only outputs omitted: got %d objects, want 3", m.ObjectCount)
	}
	opt := f.options(t, archive, backup.ProtectedAuthority{DB: f.admin, Ledger: f.ledger})
	if _, err = backup.Restore(ctx, opt); err != nil {
		t.Fatal(err)
	}
	for hash, want := range map[string]string{lateHash: "late cancelled result", firstHash: "accepted first result", conflictHash: "conflicting later result"} {
		body, err := (storage.LocalBlobs{Root: opt.BlobRoot}).ReadObject(ctx, f.project, hash)
		if err != nil || string(body) != want {
			t.Fatal("receipt object not restored", hash, err)
		}
	}
	target := openTarget(t, opt)
	var count, spent int
	if err = target.QueryRow("SELECT COUNT(*) FROM model_call_receipts").Scan(&count); err != nil || count != 3 {
		t.Fatal("receipt rows lost", count, err)
	}
	if err = target.QueryRow("SELECT spent_micros FROM model_budgets").Scan(&spent); err != nil || spent != 130 {
		t.Fatal("receipt accounting changed", spent, err)
	}
	for label, item := range map[string]struct{ hash, body string }{"late": {lateHash, "late cancelled result"}, "conflict": {conflictHash, "conflicting later result"}} {
		t.Run(label, func(t *testing.T) {
			path := filepath.Join(f.blobs.Root, f.project, item.hash)
			if err := os.WriteFile(path, []byte("corrupt receipt object"), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := backup.Create(ctx, f.db, f.blobs, filepath.Join(f.root, label+"-corrupt.zip")); err == nil {
				t.Fatal("corrupt receipt-only object accepted")
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if _, err := backup.Create(ctx, f.db, f.blobs, filepath.Join(f.root, label+"-missing.zip")); err == nil {
				t.Fatal("missing receipt-only object accepted")
			}
			f.put(t, item.body)
		})
	}
}

// The wrapper reads real current state, then changes the real source after the
// first authority read. The restore's second read must discover that change.
type changingAuthority struct {
	live       backup.ProtectedAuthority
	afterFirst func() error
	failSecond bool
	calls      int
}

func (a *changingAuthority) Current(ctx context.Context, source backup.Source) (backup.AuthorityState, error) {
	a.calls++
	if a.calls == 2 && a.failSecond {
		return backup.AuthorityState{}, errors.New("authority lost during restore")
	}
	state, err := a.live.Current(ctx, source)
	if err != nil {
		return state, err
	}
	if a.calls == 1 && a.afterFirst != nil {
		err = a.afterFirst()
	}
	return state, err
}
func authorityFixture(t *testing.T) (*fixture, string, string, string) {
	t.Helper()
	// Preserve the legacy archive fixture: its deliberately minimal capability
	// and skill reports predate schema36 source validation. Current-schema
	// revocation transport is covered by retention restore integration tests.
	f := isolatedSchema(t, 35)
	skill, capability := service.NewID(), service.NewID()
	hash := f.put(t, "revocable skill content")
	execute(t, f.db, "INSERT INTO skill_packages(id,project_id,bundle_hash,report,source,created_by) VALUES(?,?,?,'{}','{}',?)", skill, f.project, hash, f.user)
	execute(t, f.db, "INSERT INTO capability_versions(id,project_id,capability_key,kind,content_hash,definition,created_by) VALUES(?,?,'test','skill',?,'{}',?)", capability, f.project, strings.Repeat("a", 64), f.user)
	archive := filepath.Join(f.root, "authority.zip")
	if _, err := backup.Create(context.Background(), f.db, f.blobs, archive); err != nil {
		t.Fatal(err)
	}
	return f, archive, skill, capability
}
func TestRestoreRechecksRevocationsAddedAfterFirstAuthorityRead(t *testing.T) {
	f, archive, skill, capability := authorityFixture(t)
	authority := &changingAuthority{live: backup.ProtectedAuthority{DB: f.admin, Ledger: f.ledger}, afterFirst: func() error {
		tx, err := f.db.Begin()
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if _, err = tx.Exec("INSERT INTO skill_revocations(package_id,project_id,actor_id,reason) VALUES(?,?,?,'revoked during restore')", skill, f.project, f.user); err != nil {
			return err
		}
		if _, err = tx.Exec("INSERT INTO capability_revocations(version_id,project_id,actor_id,reason) VALUES(?,?,?,'revoked during restore')", capability, f.project, f.user); err != nil {
			return err
		}
		for _, event := range []ledgerstore.Event{
			{Kind: ledgerstore.SkillRevoked, ProjectID: f.project, ResourceID: skill, ActorID: f.user, Reason: "revoked during restore"},
			{Kind: ledgerstore.CapabilityRevoked, ProjectID: f.project, ResourceID: capability, ActorID: f.user, Reason: "revoked during restore"},
		} {
			if err = f.ledger.AppendTx(context.Background(), tx, event); err != nil {
				return err
			}
		}
		return tx.Commit()
	}}
	opt := f.options(t, archive, authority)
	if _, err := backup.Restore(context.Background(), opt); err != nil {
		t.Fatal(err)
	}
	target := openTarget(t, opt)
	for _, q := range []string{"SELECT COUNT(*) FROM skill_revocations", "SELECT COUNT(*) FROM capability_revocations"} {
		var count int
		if err := target.QueryRow(q).Scan(&count); err != nil || count != 1 {
			t.Fatal("late authority change was not replayed", q, count, err)
		}
	}
	var state string
	if err := target.QueryRow("SELECT state FROM skill_packages WHERE id=?", skill).Scan(&state); err != nil || state != "REVOKED" {
		t.Fatal("skill state was resurrected", state, err)
	}
}
func TestSecondAuthorityFailureRollsBackRestoredRows(t *testing.T) {
	f, archive, _, _ := authorityFixture(t)
	authority := &changingAuthority{live: backup.ProtectedAuthority{DB: f.admin, Ledger: f.ledger}, failSecond: true}
	opt := f.options(t, archive, authority)
	if _, err := backup.Restore(context.Background(), opt); err == nil || err.Error() != "final revocation authority unavailable" {
		t.Fatal("second authority failure was ignored", err)
	}
	target := openTarget(t, opt)
	for _, table := range []string{"workspaces", "users", "projects", "skill_packages", "capability_versions", "skill_revocations", "capability_revocations"} {
		var count int
		if err := target.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Fatal("partial rows committed after authority loss", table, count, err)
		}
	}
	if _, err := os.Stat(filepath.Join(opt.BlobRoot, "RESTORE_INCOMPLETE")); err != nil {
		t.Fatal("missing failure marker", err)
	}
	if _, err := os.Stat(filepath.Join(opt.BlobRoot, "RESTORED_ISOLATED_REBIND_REQUIRED")); !os.IsNotExist(err) {
		t.Fatal("failed restore marked successful")
	}
}
