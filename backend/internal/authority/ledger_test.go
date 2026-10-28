package authority

import (
	"bufio"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/go-sql-driver/mysql"
)

func testID(t *testing.T) string {
	t.Helper()
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}

func isolatedLedger(t *testing.T) (*sql.DB, *sql.DB, string, Source) {
	t.Helper()
	dsn := os.Getenv("STUDIO_AUTHORITY_TEST_ADMIN_DSN")
	if dsn == "" {
		t.Skip("STUDIO_AUTHORITY_TEST_ADMIN_DSN required for isolated databases")
	}
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		t.Fatal("invalid isolated test configuration")
	}
	cfg.DBName = ""
	cfg.ParseTime = true
	admin, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.Close() })
	schema := "studio_authority_test_" + testID(t)
	sourceDB := "studio_source_test_" + testID(t)
	for _, name := range []string{schema, sourceDB} {
		if _, err := admin.Exec("CREATE DATABASE `" + name + "` CHARACTER SET utf8mb4"); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			prefix := "studio_authority_test_"
			if strings.HasPrefix(name, "studio_source_test_") {
				prefix = "studio_source_test_"
			}
			if len(strings.TrimPrefix(name, prefix)) != 32 {
				t.Fatal("unsafe cleanup name")
			}
			if _, err := hex.DecodeString(strings.TrimPrefix(name, prefix)); err != nil {
				t.Fatal("unsafe cleanup suffix")
			}
			if _, err := admin.Exec("DROP DATABASE `" + name + "`"); err != nil {
				t.Error(err)
			}
		})
	}
	cfg.DBName = sourceDB
	business, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { business.Close() })
	for _, stmt := range []string{
		"CREATE TABLE projects(id CHAR(32) PRIMARY KEY, deleted_at DATETIME(6) NULL) ENGINE=InnoDB",
		"CREATE TABLE skill_revocations(package_id CHAR(32) PRIMARY KEY,project_id CHAR(32),actor_id CHAR(32),reason TEXT) ENGINE=InnoDB",
		"CREATE TABLE capability_revocations(version_id CHAR(32) PRIMARY KEY,project_id CHAR(32),actor_id CHAR(32),reason TEXT) ENGINE=InnoDB",
	} {
		if _, err := business.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	var source Source
	if err := business.QueryRow("SELECT @@server_uuid,DATABASE()").Scan(&source.ServerUUID, &source.Database); err != nil {
		t.Fatal(err)
	}
	return admin, business, schema, source
}

func TestCrossSchemaRollbackAndOfflineBusinessAuthority(t *testing.T) {
	ctx := context.Background()
	admin, db, schema, source := isolatedLedger(t)
	ledger, err := Provision(ctx, admin, schema, source.Database)
	if err != nil {
		t.Fatal(err)
	}
	project, actor := testID(t), testID(t)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.Exec("INSERT INTO projects(id) VALUES(?)", project); err != nil {
		t.Fatal(err)
	}
	if err = ledger.AppendTx(ctx, tx, Event{Kind: ProjectCreated, ProjectID: project, ResourceID: project, ActorID: actor}); err != nil {
		t.Fatal(err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	state, err := ledger.Current(ctx, admin, source)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Projects) != 0 {
		t.Fatal("rolled-back project survived in ledger")
	}
	var count int
	if err = db.QueryRow("SELECT COUNT(*) FROM projects").Scan(&count); err != nil || count != 0 {
		t.Fatal("business rollback failed", count, err)
	}
	tx, err = db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec("INSERT INTO projects(id) VALUES(?)", project); err != nil {
		t.Fatal(err)
	}
	if err = ledger.AppendTx(ctx, tx, Event{Kind: ProjectCreated, ProjectID: project, ResourceID: project, ActorID: actor}); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	revoked := Event{Kind: SkillRevoked, ProjectID: project, ResourceID: testID(t), ActorID: actor, Reason: "withdrawn after snapshot"}
	tx, err = db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec("INSERT INTO skill_revocations VALUES(?,?,?,?)", revoked.ResourceID, project, actor, revoked.Reason); err != nil {
		t.Fatal(err)
	}
	if err = ledger.AppendTx(ctx, tx, revoked); err != nil {
		t.Fatal(err)
	}
	if err = ledger.AppendTx(ctx, tx, revoked); err != nil {
		t.Fatal("duplicate should be idempotent", err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	state, err = ledger.Current(ctx, admin, source)
	if err != nil {
		t.Fatal(err)
	}
	if !state.Projects[project] || len(state.Skills) != 1 || state.Skills[0].ResourceID != revoked.ResourceID {
		t.Fatalf("lost current authority: %+v", state)
	}
	cfg, _ := mysql.ParseDSN(os.Getenv("STUDIO_AUTHORITY_TEST_ADMIN_DSN"))
	cfg.DBName = ""
	cfg.ParseTime = false
	textDB, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	defer textDB.Close()
	if _, err = ledger.Current(ctx, textDB, source); err != nil {
		t.Fatal("archive connection without parseTime must work", err)
	}
	wrong := source
	wrong.Database = "not_original"
	if _, err = ledger.Current(ctx, admin, wrong); err == nil {
		t.Fatal("wrong source accepted")
	}
}

func TestProtectedPrivilegesBaselineDeletionAndAncestor(t *testing.T) {
	ctx := context.Background()
	admin, db, schema, source := isolatedLedger(t)
	project, actor, ancestor := testID(t), testID(t), testID(t)
	for _, stmt := range []struct {
		q    string
		args []any
	}{
		{"INSERT INTO projects(id) VALUES(?)", []any{project}},
		{"INSERT INTO skill_revocations VALUES(?,?,?,?)", []any{ancestor, project, actor, "baseline ancestor withdrawn"}},
	} {
		if _, err := db.Exec(stmt.q, stmt.args...); err != nil {
			t.Fatal(err)
		}
	}
	ledger, err := Provision(ctx, admin, schema, source.Database)
	if err != nil {
		t.Fatal(err)
	}
	before, err := ledger.Current(ctx, admin, source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = Provision(ctx, admin, schema, source.Database); err != nil {
		t.Fatal(err)
	}
	after, err := ledger.Current(ctx, admin, source)
	if err != nil || before.LedgerID != after.LedgerID || len(after.Skills) != 1 {
		t.Fatal("reprovision replaced evidence", err)
	}
	cfg, err := mysql.ParseDSN(os.Getenv("STUDIO_AUTHORITY_TEST_ADMIN_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	user := "auth_" + testID(t)[:24]
	password := testID(t) + testID(t)
	if _, err = admin.Exec("CREATE USER '" + user + "'@'127.0.0.1' IDENTIFIED BY '" + password + "'"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec("DROP USER '" + user + "'@'127.0.0.1'"); err != nil {
			t.Error(err)
		}
	})
	for _, grant := range []string{
		"GRANT SELECT,INSERT,UPDATE,DELETE ON `" + source.Database + "`.* TO '" + user + "'@'127.0.0.1'",
		"GRANT SELECT ON `" + schema + "`.sources TO '" + user + "'@'127.0.0.1'",
		"GRANT SELECT,INSERT ON `" + schema + "`.events TO '" + user + "'@'127.0.0.1'",
	} {
		if _, err = admin.Exec(grant); err != nil {
			t.Fatal(err)
		}
	}
	cfg.User = user
	cfg.Passwd = password
	cfg.DBName = source.Database
	cfg.ParseTime = true
	app, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	for _, statement := range []string{
		"UPDATE `" + schema + "`.events SET reason='rewrite'",
		"DELETE FROM `" + schema + "`.events",
		"DROP TABLE `" + schema + "`.events",
		"INSERT INTO `" + schema + "`.sources SELECT * FROM `" + schema + "`.sources",
		"UPDATE `" + schema + "`.sources SET protocol=2",
	} {
		if _, err = app.Exec(statement); err == nil {
			t.Fatal("protected operation permitted", statement)
		}
	}
	tx, err := app.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	revoked, err := ledger.RevokedTx(ctx, tx, project, SkillRevoked, []string{testID(t), ancestor})
	if err != nil || !revoked {
		t.Fatal("revoked ancestor not enforced", revoked, err)
	}
	if _, err = tx.Exec("UPDATE projects SET deleted_at=UTC_TIMESTAMP(6) WHERE id=?", project); err != nil {
		t.Fatal(err)
	}
	if err = ledger.AppendTx(ctx, tx, Event{Kind: ProjectDeleted, ProjectID: project, ResourceID: project, ActorID: actor}); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	// Simulate reverting old business data: the external tombstone still wins.
	if _, err = app.Exec("UPDATE projects SET deleted_at=NULL WHERE id=?", project); err != nil {
		t.Fatal(err)
	}
	tx, err = app.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	active, err := ledger.ProjectActiveTx(ctx, tx, project)
	if err != nil || active {
		t.Fatal("old business state reopened deleted project", active, err)
	}
	evidence, err := ledger.DeletionEvidenceTx(ctx, tx, project)
	if err != nil || !evidence.VerifiedUTC || evidence.Sequence == 0 {
		t.Fatal("restricted ledger account cannot verify UTC evidence", evidence, err)
	}
	if err = ledger.AppendTx(ctx, tx, Event{Kind: CapabilityRevoked, ProjectID: project, ResourceID: testID(t), ActorID: actor, Reason: "late"}); err == nil {
		t.Fatal("deleted project accepted relevant write")
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	state, err := ledger.Current(ctx, app, source)
	if err != nil || state.Projects[project] {
		t.Fatal("tombstone lost", err)
	}
	missing := Ledger{Schema: "studio_authority_test_" + testID(t)}
	if _, err = missing.Current(ctx, app, source); err == nil {
		t.Fatal("missing ledger accepted")
	}
}

func TestAuthorityTransactionProcessKillAndRestart(t *testing.T) {
	ctx := context.Background()
	admin, db, schema, source := isolatedLedger(t)
	ledger, err := Provision(ctx, admin, schema, source.Database)
	if err != nil {
		t.Fatal(err)
	}
	project := testID(t)
	cmd := exec.Command(os.Args[0], "-test.run=^TestAuthorityCrashHelper$")
	cmd.Env = append(os.Environ(), "AUTHORITY_CRASH_CHILD=1", "AUTHORITY_CRASH_SCHEMA="+schema, "AUTHORITY_CRASH_DATABASE="+source.Database, "AUTHORITY_CRASH_PROJECT="+project)
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	scanner := bufio.NewScanner(pipe)
	if !scanner.Scan() || scanner.Text() != "transaction staged" {
		t.Fatal("child did not stage transaction")
	}
	if err = cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	state, err := ledger.Current(ctx, admin, source)
	if err != nil || state.Projects[project] {
		t.Fatal("killed uncommitted transaction leaked ledger", err)
	}
	var count int
	if err = db.QueryRow("SELECT COUNT(*) FROM projects WHERE id=?", project).Scan(&count); err != nil || count != 0 {
		t.Fatal("killed transaction leaked business", count, err)
	}
	// A new process/connection can commit exactly the same logical operation.
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.Exec("INSERT INTO projects(id) VALUES(?)", project); err != nil {
		t.Fatal(err)
	}
	if err = ledger.AppendTx(ctx, tx, Event{Kind: ProjectCreated, ProjectID: project, ResourceID: project, ActorID: testID(t)}); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	state, err = ledger.Current(ctx, admin, source)
	if err != nil || !state.Projects[project] {
		t.Fatal("committed evidence lost after reconnect", err)
	}
}

func TestAuthorityCrashHelper(t *testing.T) {
	if os.Getenv("AUTHORITY_CRASH_CHILD") != "1" {
		t.Skip("isolated child helper")
	}
	cfg, err := mysql.ParseDSN(os.Getenv("STUDIO_AUTHORITY_TEST_ADMIN_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.DBName = os.Getenv("AUTHORITY_CRASH_DATABASE")
	cfg.ParseTime = true
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	project := os.Getenv("AUTHORITY_CRASH_PROJECT")
	if _, err = tx.Exec("INSERT INTO projects(id) VALUES(?)", project); err != nil {
		t.Fatal(err)
	}
	ledger := Ledger{Schema: os.Getenv("AUTHORITY_CRASH_SCHEMA")}
	if err = ledger.AppendTx(context.Background(), tx, Event{Kind: ProjectCreated, ProjectID: project, ResourceID: project, ActorID: testID(t)}); err != nil {
		t.Fatal(err)
	}
	os.Stdout.WriteString("transaction staged\n")
	select {}
}
