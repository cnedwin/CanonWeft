package backup

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"novelstudio/internal/domain"
	"novelstudio/internal/storage"
)

func installSnapshotRetentionFixture(t *testing.T, db *sql.DB) {
	t.Helper()
	migration, ddl, err := retentionMigration()
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range strings.Split(ddl, ";") {
		if strings.TrimSpace(statement) != "" {
			execTest(t, db, statement)
		}
	}
	execTest(t, db, "INSERT INTO schema_migrations(version,name,checksum,origin) VALUES(?,?,?,'EXECUTED')", migration.Version, migration.Name, migration.SHA256)
	execTest(t, db, "UPDATE schema_version SET version=?,checksum=? WHERE id=1", retentionSchemaVersion, migration.SHA256)
}

func readSnapshotFixture(t *testing.T, path string) *Archive {
	t.Helper()
	zr, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	a := &Archive{Blobs: map[string][]byte{}}
	for _, file := range zr.File {
		r, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		raw, err := io.ReadAll(r)
		r.Close()
		if err != nil {
			t.Fatal(err)
		}
		switch file.Name {
		case "manifest.json":
			err = json.Unmarshal(raw, &a.Manifest)
		case "data.json":
			err = json.Unmarshal(raw, &a.Data)
		default:
			a.Blobs[file.Name] = raw
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	return a
}

func TestRetentionSnapshotCapturesExactMetadata(t *testing.T) {
	db, _, admin := testDatabase(t)
	w, u, p := randomID(t), randomID(t), randomID(t)
	execTest(t, db, "INSERT INTO workspaces(id,name) VALUES(?,'retention snapshot')", w)
	execTest(t, db, "INSERT INTO users(id,workspace_id,username,password_hash,role) VALUES(?,?,'owner','secret-password','owner')", u, w)
	execTest(t, db, "INSERT INTO projects(id,workspace_id,name) VALUES(?,?,'original project')", p, w)
	ledger := provisionTestAuthority(t, db, admin)
	provenance, err := ledger.ProvenanceTx(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	installSnapshotRetentionFixture(t, db)
	var at string
	if err := db.QueryRow("SELECT CAST(UTC_TIMESTAMP(6) AS CHAR)").Scan(&at); err != nil {
		t.Fatal(err)
	}
	execTest(t, db, "INSERT INTO project_lifecycle(project_id,source_ledger_id,time_basis,first_verified_at,updated_at) VALUES(?,?,'SOURCE_UTC',?,?)", p, provenance.LedgerID, at, at)
	execTest(t, db, "INSERT INTO retention_identity_policies(workspace_id,legacy_writes_closed,revision,created_at,closed_at) VALUES(?,TRUE,2,?,?)", w, at, at)
	root := t.TempDir()
	path := filepath.Join(root, "retention.zip")
	blobs := storage.LocalBlobs{Root: filepath.Join(root, "blobs")}
	body := []byte("原文必须在快照中保留")
	object, err := blobs.PutObject(context.Background(), p, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	branch := randomID(t)
	execTest(t, db, "INSERT INTO branches(id,project_id,name) VALUES(?,?,'main')", branch, p)
	resource, release := randomID(t), randomID(t)
	manifest := domain.Manifest{Schema: 1, ReleaseID: release, BranchID: branch, Artifacts: []domain.ArtifactRef{{ResourceID: resource, Hash: object.Hash, Size: int64(len(body))}}}
	manifestRaw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	manifestObject, err := blobs.PutObject(context.Background(), p, bytes.NewReader(manifestRaw))
	if err != nil {
		t.Fatal(err)
	}
	execTest(t, db, "INSERT INTO resources(id,project_id,branch_id,name,kind,content_hash,head_revision) VALUES(?,?,?,'source','document',?,?)", resource, p, branch, object.Hash, release)
	execTest(t, db, "INSERT INTO artifact_releases(id,project_id,branch_id,manifest,content_hash,status,created_by,adopted_by,adopted_hash) VALUES(?,?,?,?,?,'PUBLISHED',?,?,?)", release, p, branch, string(manifestRaw), manifestObject.Hash, u, u, manifestObject.Hash)
	metrics, err := Create(context.Background(), db, blobs, path)
	if err != nil {
		t.Fatal("schema36 capture failed", err)
	}
	a := readSnapshotFixture(t, path)
	if metrics.ObjectCount != 2 || !bytes.Equal(a.Blobs["blobs/"+p+"/"+object.Hash], body) {
		t.Fatal("retained object not captured")
	}
	if a.Manifest.Schema != retentionSchemaVersion || len(a.Manifest.Migrations) != 36 {
		t.Fatal("snapshot claimed wrong schema")
	}
	if err := validateManifest(a.Manifest); err != nil {
		t.Fatal(err)
	}
	business, metadata, err := splitRetentionData(a.Manifest.Schema, a.Data)
	if err != nil || len(metadata) != 11 {
		t.Fatal("missing retention tables", len(metadata), err)
	}
	for _, spec := range retentionTableCatalog() {
		actual, err := readRows(context.Background(), db, spec)
		if err != nil {
			t.Fatal(err)
		}
		left, _ := json.Marshal(actual)
		right, _ := json.Marshal(metadata[spec.name])
		if !bytes.Equal(left, right) {
			t.Fatal("changed original metadata", spec.name)
		}
	}
	users := memorySpec("users")
	if users.value(business["users"][0], "enabled") != "0" || users.value(business["users"][0], "password_hash") != "" {
		t.Fatal("snapshot identity or password active")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateArchive(bytes.NewReader(raw), int64(len(raw))); err != nil {
		t.Fatal("snapshot failed public validation", err)
	}
	var password string
	var enabled bool
	if err := db.QueryRow("SELECT password_hash,enabled FROM users WHERE id=?", u).Scan(&password, &enabled); err != nil || password != "secret-password" || !enabled {
		t.Fatal("capture modified source identity", err)
	}
}

func TestRetentionSnapshotRefusesUnqualifiedCatalogBeforeOutput(t *testing.T) {
	db, _, admin := testDatabase(t)
	provisionTestAuthority(t, db, admin)
	installSnapshotRetentionFixture(t, db)
	migration, _, err := retentionMigration()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	for _, change := range []string{"checksum", "unexecuted", "partial install", "column drift"} {
		t.Run(change, func(t *testing.T) {
			switch change {
			case "checksum":
				execTest(t, db, "UPDATE schema_migrations SET checksum=? WHERE version=36", strings.Repeat("0", 64))
			case "unexecuted":
				execTest(t, db, "UPDATE schema_migrations SET origin='LEGACY_BASELINE' WHERE version=36")
			case "partial install":
				execTest(t, db, "UPDATE schema_version SET version=35 WHERE id=1")
			case "column drift":
				execTest(t, db, "ALTER TABLE retention_receipts ADD COLUMN extra_field INT NULL")
			}
			path := filepath.Join(root, strings.ReplaceAll(change, " ", "-")+".zip")
			_, captureErr := Create(context.Background(), db, storage.LocalBlobs{Root: filepath.Join(root, "objects")}, path)
			if captureErr == nil {
				t.Error("unqualified source accepted", change)
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Error("invalid source created an output file", err)
			}
			switch change {
			case "checksum":
				execTest(t, db, "UPDATE schema_migrations SET checksum=? WHERE version=36", migration.SHA256)
			case "unexecuted":
				execTest(t, db, "UPDATE schema_migrations SET origin='EXECUTED' WHERE version=36")
			case "partial install":
				execTest(t, db, "UPDATE schema_version SET version=36 WHERE id=1")
			case "column drift":
				execTest(t, db, "ALTER TABLE retention_receipts DROP COLUMN extra_field")
			}
		})
	}
}
