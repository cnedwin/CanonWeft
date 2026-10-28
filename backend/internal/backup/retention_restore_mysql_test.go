package backup

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"

	"novelstudio/internal/authority"
	"novelstudio/internal/binlog"
	"novelstudio/internal/domain"
	"novelstudio/internal/retention"
	"novelstudio/internal/storage"
)

func TestRetentionRestorePreservesHistoryAndDisablesExecution(t *testing.T) {
	db, cfg, admin := testDatabase(t)
	ctx := context.Background()
	w, u, p, branch, capability := randomID(t), randomID(t), randomID(t), randomID(t), randomID(t)
	execTest(t, db, "INSERT INTO workspaces(id,name) VALUES(?,'retention restore')", w)
	execTest(t, db, "INSERT INTO users(id,workspace_id,username,password_hash,role) VALUES(?,?,'owner','never restore this','owner')", u, w)
	execTest(t, db, "INSERT INTO projects(id,workspace_id,name) VALUES(?,?,'original project')", p, w)
	execTest(t, db, "INSERT INTO branches(id,project_id,name) VALUES(?,?,'main')", branch, p)
	execTest(t, db, "INSERT INTO ip_drafts(branch_id,actor_id,revision,base_head,payload) VALUES(?,?,1,'','{}')", branch, u)
	definition := domain.CapabilityDefinition{Kind: "agent", Key: "example-agent", Name: "写作助手", Description: "生成待确认的文字", Instructions: "保留来源并生成候选。", Role: "writer", InputSchema: json.RawMessage(`{"type":"object"}`), OutputSchema: json.RawMessage(`{"type":"object"}`)}
	if err := definition.Validate(); err != nil {
		t.Fatal(err)
	}
	definitionRaw, _ := json.Marshal(definition)
	definitionHashRaw, _ := json.Marshal([]any{"", definition})
	execTest(t, db, "INSERT INTO capability_versions(id,project_id,capability_key,kind,content_hash,definition,created_by) VALUES(?,?,'example-agent','agent',?,?,?)", capability, p, recipeJSONDigest(definitionHashRaw), string(definitionRaw), u)
	previouslyRevoked := randomID(t)
	execTest(t, db, "INSERT INTO capability_versions(id,project_id,capability_key,kind,content_hash,definition,created_by) VALUES(?,?,'example-agent','agent',?,?,?)", previouslyRevoked, p, recipeJSONDigest(definitionHashRaw), string(definitionRaw), u)
	execTest(t, db, "INSERT INTO capability_revocations(version_id,project_id,actor_id,reason) VALUES(?,?,?,'original authority reason')", previouslyRevoked, p, u)
	ledger := provisionTestAuthority(t, db, admin)
	installSnapshotRetentionFixture(t, db)
	event := randomID(t)
	eventRevision := randomID(t)
	execTest(t, db, "INSERT INTO ip_revisions(id,branch_id,base_head,payload,content_hash,created_by) VALUES(?,?,'','{}',?,?)", eventRevision, branch, digest([]byte("{}")), u)
	execTest(t, db, "INSERT INTO outbox(id,project_id,kind,resource_id,payload) VALUES(?,?,'ip.published',?,'{\"head\":\"original-event\"}')", event, p, eventRevision)
	// A historical business explanation can differ from the immutable ledger's
	// original reason. Recovery must preserve its independently bound bytes.
	execTest(t, db, "UPDATE capability_revocations SET reason='historical display note' WHERE version_id=?", previouslyRevoked)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err = retention.CaptureSourceValues(ctx, tx, ledger, p,
		retention.SourceKey{Table: "outbox", Primary: map[string]string{"id": event}},
		retention.SourceKey{Table: "ip_drafts", Primary: map[string]string{"branch_id": branch, "actor_id": u}},
		retention.SourceKey{Table: "capability_revocations", Primary: map[string]string{"version_id": previouslyRevoked}}); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var clock string
	var revision int64
	if err = db.QueryRow("SELECT CAST(UTC_TIMESTAMP(6) AS CHAR),inventory_revision FROM project_lifecycle WHERE project_id=?", p).Scan(&clock, &revision); err != nil {
		t.Fatal(err)
	}
	at, err := time.ParseInLocation("2006-01-02 15:04:05.999999", clock, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	// This fixture's old scan is not a production completeness claim. Restore
	// must invalidate it even though all metadata/plan fields are consistent.
	execTest(t, db, "UPDATE project_lifecycle SET state='CURRENT',scanned_at=?,updated_at=? WHERE project_id=?", clock, clock, p)
	plan, err := retention.BuildPlan(retention.Inventory{ProjectID: p, Revision: revision, CatalogVersion: 1, CoveredTables: retention.KnownTables(), CapturedAt: at, Complete: true}, at)
	if err != nil {
		t.Fatal(err)
	}
	planRaw, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	job := randomID(t)
	execTest(t, db, "INSERT INTO retention_jobs(id,project_id,actor_id,operation_id,request_hash,inventory_revision,inventory_hash,plan_hash,plan,created_at,updated_at) VALUES(?,?,?,'old-cleanup',?,?,?,?,?,?,?)", job, p, u, digest([]byte("request")), revision, plan.InventoryHash, plan.Hash, string(planRaw), clock, clock)
	execTest(t, db, "INSERT INTO retention_identity_policies(workspace_id,legacy_writes_closed,revision,created_at,closed_at) VALUES(?,TRUE,2,?,?)", w, clock, clock)
	root := t.TempDir()
	archivePath := filepath.Join(root, "retention.zip")
	blobs := storage.LocalBlobs{Root: filepath.Join(root, "source")}
	if _, err = blobs.PutObject(ctx, p, strings.NewReader("{}")); err != nil {
		t.Fatal(err)
	}
	if _, err = Create(ctx, db, blobs, archivePath); err != nil {
		t.Fatal(err)
	}
	original := readSnapshotFixture(t, archivePath)
	// A new protected revocation after the snapshot must still reach targets.
	revoke, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer revoke.Rollback()
	if err = ledger.AppendTx(ctx, revoke, authority.Event{Kind: authority.CapabilityRevoked, ProjectID: p, ResourceID: capability, ActorID: u, Reason: "revoked after snapshot"}); err != nil {
		t.Fatal(err)
	}
	if err = revoke.Commit(); err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		name := "first"
		if attempt == 1 {
			name = "second"
		}
		opt := RestoreOptions{ArchivePath: archivePath, TargetDSN: targetDSN(t, cfg, admin), BlobRoot: filepath.Join(root, name), Authority: ProtectedAuthority{DB: admin, Ledger: ledger}}
		if attempt == 1 {
			requested, err := mysql.ParseDSN(opt.TargetDSN)
			if err != nil {
				t.Fatal("invalid test target configuration")
			}
			requested.Params["time_zone"] = "'+08:00'"
			opt.TargetDSN = requested.FormatDSN()
		}
		metrics, err := Restore(ctx, opt)
		if err != nil {
			t.Fatal(err)
		}
		target, err := sql.Open("mysql", opt.TargetDSN)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { target.Close() })
		if !metrics.SnapshotAt.Equal(original.Manifest.SnapshotAt) {
			t.Fatal("restore reset original snapshot clock")
		}
		if err = checkTables(ctx, target, retentionSchemaVersion); err != nil {
			t.Fatal(err)
		}
		var pending int
		if err = target.QueryRow("SELECT COUNT(*) FROM outbox WHERE processed_at IS NULL").Scan(&pending); err != nil || pending != 0 {
			t.Fatal("restored event can be delivered", err)
		}
		var eventBody string
		if err = target.QueryRow("SELECT JSON_UNQUOTE(JSON_EXTRACT(payload,'$.head')) FROM outbox WHERE id=?", event).Scan(&eventBody); err != nil || eventBody != "original-event" {
			t.Fatal("restored event history lost", err)
		}
		history, err := readHistory(ctx, target, retentionSchemaVersion)
		if err != nil || len(history) != retentionSchemaVersion || history[35].Origin != "EXECUTED" {
			t.Fatal("target retention migration not actually installed", err)
		}
		for _, spec := range retentionTableCatalog() {
			actual, err := readRows(ctx, target, spec)
			if err != nil || len(actual) != len(original.Data[spec.name]) {
				t.Fatal("restored metadata count differs", spec.name, err)
			}
			for _, old := range original.Data[spec.name] {
				expected := make(Row, len(old))
				copy(expected, old)
				if spec.name == "project_lifecycle" {
					spec.set(expected, "state", "UNSCANNED")
					expected[spec.index("scanned_at")] = nil
				}
				if spec.name == "retention_jobs" || spec.name == "retention_recovery_attempts" {
					spec.set(expected, "restored_disabled", "1")
				}
				found := false
				for _, row := range actual {
					if equalLogRow(binlog.Row(row), binlog.Row(expected)) {
						found = true
					}
				}
				if !found {
					t.Fatal("restore changed original history, age, identity or plan", spec.name)
				}
			}
		}
		var reason, password string
		var enabled bool
		if err = target.QueryRow("SELECT reason FROM capability_revocations WHERE version_id=?", capability).Scan(&reason); err != nil || reason != "revoked after snapshot" {
			t.Fatal("current revocation missing", err)
		}
		var utcClock bool
		if err = target.QueryRow("SELECT ABS(TIMESTAMPDIFF(SECOND,created_at,UTC_TIMESTAMP()))<60 FROM capability_revocations WHERE version_id=?", capability).Scan(&utcClock); err != nil || !utcClock {
			t.Fatal("new recovery evidence used a local wall clock", err)
		}
		if err = target.QueryRow("SELECT reason FROM capability_revocations WHERE version_id=?", previouslyRevoked).Scan(&reason); err != nil || reason != "historical display note" {
			t.Fatal("historical revocation source overwritten", err)
		}
		if err = target.QueryRow("SELECT password_hash,enabled FROM users WHERE id=?", u).Scan(&password, &enabled); err != nil || password != "" || enabled {
			t.Fatal("restored account active", err)
		}
		if _, err = os.Stat(filepath.Join(opt.BlobRoot, "RESTORED_ISOLATED_REBIND_REQUIRED")); err != nil {
			t.Fatal(err)
		}
	}
	var unchanged bool
	var sourcePending int
	if err = db.QueryRow("SELECT COUNT(*) FROM outbox WHERE id=? AND processed_at IS NULL", event).Scan(&sourcePending); err != nil || sourcePending != 1 {
		t.Fatal("backup suppressed live source delivery", err)
	}
	if err = db.QueryRow("SELECT enabled FROM users WHERE id=?", u).Scan(&unchanged); err != nil || !unchanged {
		t.Fatal("restore changed source account", err)
	}
	// A later source deletion denies another normal restore before a target
	// is created. Seven-day recovery requires the separate copy/adoption flow.
	deleted, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer deleted.Rollback()
	if err = ledger.AppendTx(ctx, deleted, authority.Event{Kind: authority.ProjectDeleted, ProjectID: p, ResourceID: p, ActorID: u}); err != nil {
		t.Fatal(err)
	}
	if err = deleted.Commit(); err != nil {
		t.Fatal(err)
	}
	denied := RestoreOptions{ArchivePath: archivePath, TargetDSN: targetDSN(t, cfg, admin), BlobRoot: filepath.Join(root, "denied"), Authority: ProtectedAuthority{DB: admin, Ledger: ledger}}
	if _, err = Restore(ctx, denied); err == nil {
		t.Fatal("deleted source resurrected")
	}
	if _, err = os.Stat(denied.BlobRoot); !os.IsNotExist(err) {
		t.Fatal("denied restore created output")
	}
	targetCfg, err := targetConfig(denied.TargetDSN)
	if err != nil {
		t.Fatal(err)
	}
	var databases int
	if err = admin.QueryRow("SELECT COUNT(*) FROM information_schema.schemata WHERE schema_name=?", targetCfg.DBName).Scan(&databases); err != nil || databases != 0 {
		t.Fatal("denied restore created database", err)
	}
}
