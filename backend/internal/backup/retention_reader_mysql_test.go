package backup

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"novelstudio/internal/binlog"
	"novelstudio/internal/novel"
	"novelstudio/internal/readercredentials"
	"novelstudio/internal/retention"
	"novelstudio/internal/storage"
)

func TestRetentionReaderUpgradesExactGrantsAndCaptures(t *testing.T) {
	testReaderCatalogAndCapture(t, 36)
}

func TestNovelReaderUpgradesExactGrantsAndCaptures(t *testing.T) {
	testReaderCatalogAndCapture(t, 37)
}

func TestNovelSettingsReaderUpgradesExactGrantsAndCaptures(t *testing.T) {
	testReaderCatalogAndCapture(t, 38)
}

func testReaderCatalogAndCapture(t *testing.T, schema int) {
	db, cfg, admin := testDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	w, u, p := randomID(t), randomID(t), randomID(t)
	execTest(t, db, "INSERT INTO workspaces(id,name) VALUES(?,'reader upgrade')", w)
	execTest(t, db, "INSERT INTO users(id,workspace_id,username,password_hash,role) VALUES(?,?,'owner','test','owner')", u, w)
	execTest(t, db, "INSERT INTO projects(id,workspace_id,name) VALUES(?,?,'original')", p, w)
	ledger := provisionTestAuthority(t, db, admin)
	root := t.TempDir()
	options := ReaderProvisionOptions{Database: cfg.DBName, AuthoritySchema: ledger.Schema, Address: cfg.Addr, CredentialFile: filepath.Join(root, "reader.dpapi"), ReadyFile: filepath.Join(root, "reader.ready.json")}
	var uuid string
	if err := admin.QueryRowContext(ctx, "SELECT LOWER(@@server_uuid)").Scan(&uuid); err != nil {
		t.Fatal(err)
	}
	user, err := LogReaderAccountName(uuid, cfg.DBName)
	if err != nil {
		t.Fatal(err)
	}
	account := "'" + user + "'@'127.0.0.1'"
	t.Cleanup(func() { execTest(t, admin, "DROP USER IF EXISTS "+account) })
	before, err := ProvisionLogReader(ctx, admin, db, options)
	if err != nil {
		t.Fatal(err)
	}
	secret, err := readercredentials.Load(options.CredentialFile)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := sql.Open("mysql", secret.DSN())
	if err != nil {
		t.Fatal("reader connection unavailable")
	}
	defer reader.Close()
	installSnapshotRetentionFixture(t, db)
	if schema >= 37 {
		if err = storage.RunMigrationsTo(ctx, db, schema); err != nil {
			t.Fatal(err)
		}
	}
	if err = VerifyLogReaderScope(ctx, reader, ledger.Schema); err == nil {
		t.Fatal("old grants claimed full coverage after new metadata installed")
	}
	ready, err := ProvisionLogReader(ctx, admin, db, options)
	if err != nil {
		t.Fatal(err)
	}
	if ready.SchemaVersion != schema || ready.CredentialSHA256 != before.CredentialSHA256 || ready.TableFingerprint == before.TableFingerprint {
		t.Fatal("upgrade replaced credential or did not bind new catalog")
	}
	if err = VerifyLogReaderScope(ctx, reader, ledger.Schema); err != nil {
		t.Fatal(err)
	}
	provenance, err := ledger.ProvenanceTx(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	execTest(t, db, "INSERT INTO project_lifecycle(project_id,source_ledger_id,time_basis,first_verified_at) VALUES(?,?,'SOURCE_UTC',UTC_TIMESTAMP(6))", p, provenance.LedgerID)
	draftID, novelID, branchID := randomID(t), randomID(t), randomID(t)
	setup := novel.Defaults()
	setup.Title, setup.Genre, setup.Idea, setup.Conflict, setup.World = "日志小说", "悬疑", "守护雾城", "迷雾与真相", "雾城"
	setup.CharacterMode, setup.CreationMode = "architect", "manual"
	setupRaw, err := json.Marshal(setup)
	if err != nil {
		t.Fatal(err)
	}
	if schema >= 37 {
		execTest(t, db, "INSERT INTO branches(id,project_id,name) VALUES(?,?,'novel')", branchID, p)
		execTest(t, db, "INSERT INTO novel_setup_drafts(id,workspace_id,actor_id,revision,step,payload,content_hash) VALUES(?,?,?,1,5,?,?)", draftID, w, u, string(setupRaw), digest(setupRaw))
	}
	blobs := storage.LocalBlobs{Root: filepath.Join(root, "objects")}
	path := filepath.Join(root, "baseline.zip")
	base, _, err := CreateLogBaseline(ctx, reader, blobs, path)
	if err != nil {
		t.Fatal("restricted source baseline failed", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := OpenLogReplay(base, raw)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "UPDATE projects SET name='changed' WHERE id=?", p); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.ExecContext(ctx, "UPDATE project_lifecycle SET inventory_revision=inventory_revision+1,updated_at=UTC_TIMESTAMP(6) WHERE project_id=?", p); err != nil {
		t.Fatal(err)
	}
	if schema >= 37 {
		for _, statement := range []struct {
			query string
			args  []any
		}{
			{"UPDATE novel_setup_drafts SET confirmed_novel_id=?,updated_at=UTC_TIMESTAMP(6) WHERE id=?", []any{novelID, draftID}},
			{"INSERT INTO novels(id,project_id,branch_id,created_by,setup_id,settings_revision,stage) VALUES(?,?,?,?,?,1,'WORLD_PLANNING')", []any{novelID, p, branchID, u, draftID}},
			{"INSERT INTO novel_settings_versions(novel_id,revision,payload,content_hash,created_by) VALUES(?,1,?,?,?)", []any{novelID, string(setupRaw), digest(setupRaw), u}},
		} {
			if _, err = tx.ExecContext(ctx, statement.query, statement.args...); err != nil {
				t.Fatal(err)
			}
		}
		if err = retention.CaptureSourceValues(ctx, tx, ledger, p,
			retention.SourceKey{Table: "novel_setup_drafts", Primary: map[string]string{"id": draftID}},
			retention.SourceKey{Table: "novels", Primary: map[string]string{"id": novelID}},
			retention.SourceKey{Table: "novel_settings_versions", Primary: map[string]string{"novel_id": novelID, "revision": "1"}},
		); err != nil {
			t.Fatal(err)
		}
		if schema == 38 {
			if _, err = tx.ExecContext(ctx, "INSERT INTO novel_settings_drafts(novel_id,actor_id,revision,base_revision,payload,content_hash) VALUES(?,?,1,1,?,?)", novelID, u, string(setupRaw), digest(setupRaw)); err != nil {
				t.Fatal(err)
			}
			if err = retention.CaptureSourceValues(ctx, tx, ledger, p, retention.SourceKey{Table: "novel_settings_drafts", Primary: map[string]string{"novel_id": novelID, "actor_id": u}}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	stream, err := binlog.NewReader(secret.DSN(), uuid)
	if err != nil {
		t.Fatal("restricted protocol reader unavailable")
	}
	segment, err := replay.CaptureSegment(ctx, reader, stream, blobs)
	if err != nil {
		t.Fatal("restricted capture failed", err)
	}
	var decoded LogSegment
	if err = strictLogJSON(segment, &decoded); err != nil {
		t.Fatal(err)
	}
	archive, err := replay.ArchiveAt(segment, digest(segment), decoded.Through)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ValidateArchive(bytes.NewReader(archive), int64(len(archive)))
	if err != nil || memorySpec("projects").value(got.Data["projects"][0], "name") != "changed" || len(got.Data["project_lifecycle"]) != 1 {
		t.Fatal("restricted capture lost transaction", err)
	}
	if schema >= 37 {
		for _, spec := range novelCatalogForSchema(schema) {
			if len(got.Data[spec.name]) != 1 {
				t.Fatal("novel table omitted from replay", spec.name)
			}
			row := got.Data[spec.name][0]
			if spec.name == "novels" {
				if spec.value(row, "id") != novelID {
					t.Fatal("novel identity lost")
				}
			} else if spec.value(row, "payload") != string(setupRaw) {
				t.Fatal("novel settings bytes changed", spec.name)
			}
		}
		if _, err = reader.ExecContext(ctx, "UPDATE novels SET stage='FORBIDDEN'"); err == nil {
			t.Fatal("reader can modify novels")
		}
	}
	for _, query := range []string{
		"UPDATE project_lifecycle SET inventory_revision=inventory_revision+1",
		"DELETE FROM retention_source_values",
		"INSERT INTO retention_identity_policies(workspace_id) VALUES('forbidden')",
		"SELECT * FROM `" + ledger.Schema + "`.events",
		"SELECT * FROM mysql.user",
	} {
		_, err = reader.ExecContext(ctx, query)
		var denied *mysql.MySQLError
		if !errors.As(err, &denied) || denied.Number != 1142 {
			t.Fatal("expected table privilege denial", err)
		}
	}
	execTest(t, admin, "GRANT UPDATE ON `"+cfg.DBName+"`.project_lifecycle TO "+account)
	if err = VerifyLogReaderScope(ctx, reader, ledger.Schema); err == nil {
		t.Fatal("excess metadata write privilege accepted")
	}
	if _, err = ProvisionLogReader(ctx, admin, db, options); err == nil {
		t.Fatal("provisioning silently accepted excess authority")
	}
	if _, err = os.Stat(options.ReadyFile); !os.IsNotExist(err) {
		t.Fatal("failed re-audit left READY")
	}
}

func TestRetentionReaderRejectsUnqualifiedSourceBeforeProvisioning(t *testing.T) {
	db, cfg, admin := testDatabase(t)
	ctx := context.Background()
	ledger := provisionTestAuthority(t, db, admin)
	installSnapshotRetentionFixture(t, db)
	migration, _, err := retentionMigration()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, change, undo string
	}{
		{"obsolete version with new tables", "UPDATE schema_version SET version=35", "UPDATE schema_version SET version=36"},
		{"uninitialized history", "UPDATE schema_version SET history_initialized=FALSE", "UPDATE schema_version SET history_initialized=TRUE"},
		{"wrong header checksum", "UPDATE schema_version SET checksum=REPEAT('0',64)", "UPDATE schema_version SET checksum='" + migration.SHA256 + "'"},
		{"wrong migration checksum", "UPDATE schema_migrations SET checksum=REPEAT('0',64) WHERE version=36", "UPDATE schema_migrations SET checksum='" + migration.SHA256 + "' WHERE version=36"},
		{"legacy retention provenance", "UPDATE schema_migrations SET origin='LEGACY_BASELINE' WHERE version=36", "UPDATE schema_migrations SET origin='EXECUTED' WHERE version=36"},
		{"unexpected column", "ALTER TABLE project_lifecycle ADD COLUMN unexpected_column INT NULL", "ALTER TABLE project_lifecycle DROP COLUMN unexpected_column"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			execTest(t, db, tc.change)
			defer execTest(t, db, tc.undo)
			root := t.TempDir()
			options := ReaderProvisionOptions{Database: cfg.DBName, AuthoritySchema: ledger.Schema, Address: cfg.Addr, CredentialFile: filepath.Join(root, "reader.dpapi"), ReadyFile: filepath.Join(root, "reader.ready.json")}
			if _, err := ProvisionLogReader(ctx, admin, db, options); err == nil {
				t.Fatal("unqualified source was provisioned")
			}
			for _, path := range []string{options.CredentialFile, options.ReadyFile} {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatal("invalid source created protected credential or READY metadata")
				}
			}
		})
	}
	if version, err := qualifiedLogSourceSchema(ctx, db); err != nil || version != retentionSchemaVersion {
		t.Fatal("valid source was not restored after negative cases", err)
	}
}
