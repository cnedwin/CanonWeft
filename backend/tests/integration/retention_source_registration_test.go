package integration

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"novelstudio/internal/authority"
	"novelstudio/internal/domain"
	"novelstudio/internal/retention"
	"novelstudio/internal/service"
	"novelstudio/internal/storage"
)

func installSourceRegistrationFixture(t *testing.T, db *sql.DB) {
	t.Helper()
	installLifecycleFixture(t, db)
	raw, err := os.ReadFile("../../migrations/retention_sources.sql.pending")
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

// Seed historical observations before the first reference version exists;
// rewriting clocks after registration would corrupt immutable age evidence.
func seedProjectNameObservation(t *testing.T, db *sql.DB, project string) {
	t.Helper()
	var name string
	if err := db.QueryRow("SELECT name FROM projects WHERE id=?", project).Scan(&name); err != nil {
		t.Fatal(err)
	}
	owner, err := retention.RowOwnerKey("projects", map[string]string{"id": project})
	if err != nil {
		t.Fatal(err)
	}
	id, err := retention.ReferenceID(project, "projects", owner, "name")
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256([]byte(name))
	if _, err := db.Exec("INSERT INTO retention_source_slots(id,project_id,owner_table,owner_key,field_key,time_basis,first_verified_at) VALUES(?,?,'projects',?,'name','LEGACY_REGISTERED','2020-01-02 03:04:05.000006')", id, project, owner); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO retention_source_values(source_id,payload_hash,is_null,first_verified_at) VALUES(?,?,FALSE,'2020-01-02 03:04:05.000006')", id, hex.EncodeToString(hash[:])); err != nil {
		t.Fatal(err)
	}
}

func TestRetentionSourceRegistrationHTTPGateAndServicePermissions(t *testing.T) {
	b, db := setup(t)
	project := createProject(b, "SOURCE_CANARY_PRIVATE", "registration-permission")
	path := "/api/projects/" + project + "/retention-sources/register"
	interruptRetentionMigrationMarker(t, db)
	b.request("POST", path, nil, 503)
	if err := storage.RunMigrations(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	anonymous := &browser{t: t, handler: b.handler}
	anonymous.request("POST", path, nil, 401)
	b.request("POST", "/api/members", map[string]any{"username": "source-observer-editor", "password": "source-observer-password", "role": "editor"}, 201)
	editor := &browser{t: t, handler: b.handler}
	editor.request("POST", "/api/login", map[string]any{"username": "source-observer-editor", "password": "source-observer-password"}, 200)
	editor.request("POST", path, nil, 403)
	actor := b.request("GET", "/api/me", nil, 200)["id"].(string)
	installSourceRegistrationFixture(t, db)
	foreignWorkspace, foreignActor := service.NewID(), service.NewID()
	if _, err := db.Exec("INSERT INTO workspaces(id,name) VALUES(?,'foreign registration workspace')", foreignWorkspace); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO users(id,workspace_id,username,password_hash,role,enabled) VALUES(?,?,'foreign-observer','unused-test-credential','owner',TRUE)", foreignActor, foreignWorkspace); err != nil {
		t.Fatal(err)
	}
	w := &service.Workspace{DB: db}
	var before int
	if err := db.QueryRow("SELECT COUNT(*) FROM retention_source_slots").Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err := w.RegisterRetentionSources(context.Background(), foreignActor, project); err != domain.ErrForbidden {
		t.Fatal("foreign owner registered another workspace", err)
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM retention_source_slots").Scan(&count); err != nil || count != before {
		t.Fatal("denied request changed registration", count, err)
	}
	report, err := w.RegisterRetentionSources(context.Background(), actor, project)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(report)
	for _, private := range []string{"SOURCE_CANARY_PRIVATE", project, "payload_hash", "owner_key", "source-observer-editor", "password_hash"} {
		if strings.Contains(string(raw), private) {
			t.Fatal("registration response contains private source identities")
		}
	}
	if report.DeletionReady {
		t.Fatal("invalid registration readiness", report)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM retention_source_slots WHERE owner_table IN ('users','sessions','workspaces','model_connection_versions')").Scan(&count); err != nil || count != 0 {
		t.Fatal("shared identities or credentials were journaled", count, err)
	}
	// Complete installation admits the same authorized operation through HTTP.
	b.request("POST", path, nil, 200)
}

func TestRetentionSourceRegistrationSerializesConcurrentFirstObservation(t *testing.T) {
	b, db := setup(t)
	project := createProject(b, "并发来源登记", "registration-concurrent")
	actor := b.request("GET", "/api/me", nil, 200)["id"].(string)
	installSourceRegistrationFixture(t, db)
	w := &service.Workspace{DB: db}
	type outcome struct {
		report retention.SourceRegistrationReport
		err    error
	}
	start := make(chan struct{})
	results := make(chan outcome, 2)
	for i := 0; i < 2; i++ {
		go func() {
			<-start
			report, err := w.RegisterRetentionSources(context.Background(), actor, project)
			results <- outcome{report, err}
		}()
	}
	close(start)
	a, z := <-results, <-results
	if a.err != nil || z.err != nil || a.report.Sources != z.report.Sources || a.report.NewSources+z.report.NewSources != a.report.Sources || a.report.NewValues+z.report.NewValues != a.report.Sources {
		t.Fatal("concurrent registration changed first observations", a, z)
	}
	if a.report.NewReferences+z.report.NewReferences != a.report.MaterialReferences || a.report.UpdatedReferences+z.report.UpdatedReferences != 0 {
		t.Fatal("concurrent registration duplicated reference revisions", a, z)
	}
	var versions int
	if err := db.QueryRow("SELECT COUNT(*) FROM retention_reference_versions WHERE project_id=?", project).Scan(&versions); err != nil || versions != a.report.MaterialReferences {
		t.Fatal("concurrent version history differs from current references", versions, err)
	}
}

func TestRetentionSourceRegistrationAcrossDatabaseBatches(t *testing.T) {
	b, db := setup(t)
	project := createProject(b, "长篇资料登记", "registration-batch")
	branchID := branch(b, project, "正史")
	actor := b.request("GET", "/api/me", nil, 200)["id"].(string)
	var values []string
	var args []any
	for i := 0; i < 601; i++ {
		values = append(values, "(?,?,?,'note','批量资料')")
		args = append(args, service.NewID(), project, branchID)
	}
	if _, err := db.Exec("INSERT INTO resources(id,project_id,branch_id,kind,name) VALUES "+strings.Join(values, ","), args...); err != nil {
		t.Fatal(err)
	}
	installSourceRegistrationFixture(t, db)
	w := &service.Workspace{DB: db}
	first, err := w.RegisterRetentionSources(context.Background(), actor, project)
	if err != nil || first.Sources < 1202 || first.NewSources != first.Sources || first.NewValues != first.Sources || first.NewReferences < 601 || first.NewReferences != first.MaterialReferences {
		t.Fatal("multi-batch sources were lost", first, err)
	}
	second, err := w.RegisterRetentionSources(context.Background(), actor, project)
	if err != nil || second.NewValues != 0 || second.NewSources != 0 || second.NewReferences != 0 || second.UpdatedReferences != 0 || second.Sources != first.Sources {
		t.Fatal("multi-batch replay renewed sources", second, err)
	}
	var versions int
	if err := db.QueryRow("SELECT COUNT(*) FROM retention_reference_versions WHERE project_id=?", project).Scan(&versions); err != nil || versions != first.NewReferences {
		t.Fatal("multi-batch history omitted or duplicated references", versions, err)
	}
}

func TestRetentionSourceRegistrationRollsBackAndRejectsCorruptClock(t *testing.T) {
	b, db := setup(t)
	project := createProject(b, "登记回滚", "registration-rollback")
	installSourceRegistrationFixture(t, db)
	actor := b.request("GET", "/api/me", nil, 200)["id"].(string)
	w := &service.Workspace{DB: db}
	if _, err := db.Exec("CREATE TRIGGER source_registration_failure BEFORE INSERT ON retention_source_values FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='injected source registration failure'"); err != nil {
		t.Fatal(err)
	}
	if _, err := w.RegisterRetentionSources(context.Background(), actor, project); err != domain.ErrNotReady {
		t.Fatal("injected write failure did not reject registration", err)
	}
	for _, table := range []string{"retention_source_slots", "retention_source_values", "project_lifecycle"} {
		var count int
		if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Fatal("partial registration committed", table, count, err)
		}
	}
	if _, err := db.Exec("DROP TRIGGER source_registration_failure"); err != nil {
		t.Fatal(err)
	}
	if _, err := w.RegisterRetentionSources(context.Background(), actor, project); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE retention_source_slots SET first_verified_at='2099-01-01' WHERE project_id=?", project); err != nil {
		t.Fatal(err)
	}
	if _, err := w.RegisterRetentionSources(context.Background(), actor, project); err != domain.ErrNotReady {
		t.Fatal("corrupt clock was silently reset", err)
	}
}

func TestRetentionSourceRegistrationCurrentReadAfterOlderSnapshot(t *testing.T) {
	b, db := setup(t)
	project := createProject(b, "登记前旧内容", "registration-current-read")
	installSourceRegistrationFixture(t, db)
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var old string
	if err := tx.QueryRow("SELECT name FROM projects WHERE id=?", project).Scan(&old); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE projects SET name='登记时真实内容' WHERE id=?", project); err != nil {
		t.Fatal(err)
	}
	ledger := authority.Ledger{Schema: os.Getenv("STUDIO_AUTHORITY_SCHEMA")}
	if _, err := retention.RegisterSourceObservations(ctx, tx, ledger, project, nil); err != nil {
		t.Fatal(err)
	}
	var hash string
	if err := tx.QueryRow("SELECT v.payload_hash FROM retention_source_values v JOIN retention_source_slots s ON s.id=v.source_id WHERE s.project_id=? AND s.owner_table='projects' AND s.field_key='name'", project).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	want := sha256.Sum256([]byte("登记时真实内容"))
	if hash != hex.EncodeToString(want[:]) {
		t.Fatal("old consistent snapshot was registered as current source")
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestRetentionSourceRegistrationRejectsMetadataDrift(t *testing.T) {
	for _, mode := range []string{"missing-table", "extra-column", "timestamp-clock", "missing-key", "nontransactional-table", "unknown-table", "experimental-marker"} {
		t.Run(mode, func(t *testing.T) {
			b, db := setup(t)
			project := createProject(b, "登记目录", "registration-catalog")
			installSourceRegistrationFixture(t, db)
			actor := b.request("GET", "/api/me", nil, 200)["id"].(string)
			commands := map[string]string{
				"missing-table":          "RENAME TABLE retention_source_values TO unrecognized_source_values",
				"extra-column":           "ALTER TABLE retention_source_slots ADD private_text MEDIUMTEXT",
				"timestamp-clock":        "ALTER TABLE retention_source_slots MODIFY first_verified_at TIMESTAMP(6) NOT NULL",
				"missing-key":            "ALTER TABLE retention_source_values DROP PRIMARY KEY",
				"nontransactional-table": "ALTER TABLE retention_source_values ENGINE=MyISAM",
				"unknown-table":          "CREATE TABLE unclassified_private_table(id INT PRIMARY KEY,body TEXT)",
				"experimental-marker":    "UPDATE schema_migrations SET name='0036_retention_experiment.sql' WHERE version=36",
			}
			if _, err := db.Exec(commands[mode]); err != nil {
				t.Fatal(err)
			}
			w := &service.Workspace{DB: db}
			if _, err := w.RegisterRetentionSources(context.Background(), actor, project); err != domain.ErrNotReady {
				t.Fatal("incomplete/changed registration catalog accepted", err)
			}
			if mode == "nontransactional-table" {
				tx, err := db.Begin()
				if err != nil {
					t.Fatal(err)
				}
				_, rejected := retention.RegisterSourceObservations(context.Background(), tx, authority.Ledger{Schema: os.Getenv("STUDIO_AUTHORITY_SCHEMA")}, project, nil)
				_ = tx.Rollback()
				if !errors.Is(rejected, retention.ErrSourceOwnership) {
					t.Fatal("nontransactional engine reached a write instead of the catalog gate", rejected)
				}
			}
			var count int
			if err := db.QueryRow("SELECT COUNT(*) FROM retention_source_slots").Scan(&count); err != nil || count != 0 {
				t.Fatal("invalid catalog changed source observations", count, err)
			}
		})
	}
}

func TestRetentionSourceRegistrationUsesUTCWithLocalDriver(t *testing.T) {
	b, db := setup(t)
	project := createProject(b, "来源登记时区", "registration-timezone")
	installSourceRegistrationFixture(t, db)
	actor := b.request("GET", "/api/me", nil, 200)["id"].(string)
	var database string
	if err := db.QueryRow("SELECT DATABASE()").Scan(&database); err != nil {
		t.Fatal(err)
	}
	cfg, err := mysql.ParseDSN(os.Getenv("STUDIO_INTEGRATION_TEST_ADMIN_DSN"))
	if err != nil {
		t.Fatal("invalid fixture connection")
	}
	cfg.DBName, cfg.ParseTime, cfg.Loc = database, true, time.FixedZone("source-test-local", 8*3600)
	cfg.Params["time_zone"] = "'+08:00'"
	connector, err := mysql.NewConnector(cfg)
	if err != nil {
		t.Fatal(err)
	}
	local := sql.OpenDB(connector)
	defer local.Close()
	w := &service.Workspace{DB: local}
	report, err := w.RegisterRetentionSources(context.Background(), actor, project)
	if err != nil {
		t.Fatal(err)
	}
	var stored string
	if err := db.QueryRow("SELECT CAST(first_verified_at AS CHAR) FROM retention_source_slots WHERE project_id=? LIMIT 1", project).Scan(&stored); err != nil || stored != report.CapturedAt.UTC().Format("2006-01-02 15:04:05.000000") {
		t.Fatal("source clock shifted to driver timezone", stored, err)
	}
	if repeat, err := w.RegisterRetentionSources(context.Background(), actor, project); err != nil || repeat.NewValues != 0 || repeat.NewSources != 0 {
		t.Fatal("local clock caused replay to renew sources", repeat, err)
	}
}

func TestRetentionSourceRegistrationKeepsFirstObservationAcrossEdits(t *testing.T) {
	b, db := setup(t)
	project := createProject(b, "来源年龄原始私密文本", "source-register-project")
	other := createProject(b, "另一项目不可写入登记", "source-register-other")
	actor := b.request("GET", "/api/me", nil, 200)["id"].(string)
	installSourceRegistrationFixture(t, db)
	seedProjectNameObservation(t, db, project)
	w := &service.Workspace{DB: db}
	first, err := w.RegisterRetentionSources(context.Background(), actor, project)
	if err != nil || first.Sources == 0 || first.NewValues == 0 || first.DeletionReady {
		t.Fatal("actual sources not registered", first, err)
	}
	var sourceID, observed string
	if err := db.QueryRow("SELECT id,CAST(first_verified_at AS CHAR) FROM retention_source_slots WHERE project_id=? AND owner_table='projects' AND field_key='name'", project).Scan(&sourceID, &observed); err != nil {
		t.Fatal(err)
	}
	second, err := w.RegisterRetentionSources(context.Background(), actor, project)
	if err != nil || second.NewSources != 0 || second.NewValues != 0 {
		t.Fatal("replay recreated observations", second, err)
	}
	if _, err := db.Exec("UPDATE projects SET name='编辑后的私密文本' WHERE id=?", project); err != nil {
		t.Fatal(err)
	}
	third, err := w.RegisterRetentionSources(context.Background(), actor, project)
	if err != nil || third.NewSources != 0 || third.NewValues != 1 {
		t.Fatal("edited source lost identity", third, err)
	}
	if err := db.QueryRow("SELECT CAST(first_verified_at AS CHAR) FROM retention_source_slots WHERE id=?", sourceID).Scan(&observed); err != nil || observed != "2020-01-02 03:04:05.000006" {
		t.Fatal("editing renewed original age", observed, err)
	}
	if _, err := db.Exec("UPDATE projects SET name='来源年龄原始私密文本' WHERE id=?", project); err != nil {
		t.Fatal(err)
	}
	fourth, err := w.RegisterRetentionSources(context.Background(), actor, project)
	if err != nil || fourth.NewValues != 0 {
		t.Fatal("returning to old contents reset their version age", fourth, err)
	}
	hash := sha256.Sum256([]byte("来源年龄原始私密文本"))
	if err := db.QueryRow("SELECT CAST(first_verified_at AS CHAR) FROM retention_source_values WHERE source_id=? AND payload_hash=? AND is_null=FALSE", sourceID, hex.EncodeToString(hash[:])).Scan(&observed); err != nil || observed != "2020-01-02 03:04:05.000006" {
		t.Fatal("original value observation changed", observed, err)
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM retention_source_slots WHERE project_id=?", other).Scan(&count); err != nil || count != 0 {
		t.Fatal("registered a foreign project's private material", count, err)
	}
}
