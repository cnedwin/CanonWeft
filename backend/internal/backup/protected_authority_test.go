package backup

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"novelstudio/internal/authority"
	"novelstudio/internal/storage"
)

func provisionTestAuthority(t *testing.T, db, admin *sql.DB) authority.Ledger {
	t.Helper()
	schema := "studio_authority_test_" + randomID(t)
	t.Setenv("STUDIO_AUTHORITY_SCHEMA", schema)
	var business string
	if err := db.QueryRow("SELECT DATABASE()").Scan(&business); err != nil {
		t.Fatal(err)
	}
	ledger, err := authority.Provision(context.Background(), admin, schema, business)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if strings.HasPrefix(schema, "studio_authority_test_") && hex32.MatchString(strings.TrimPrefix(schema, "studio_authority_test_")) {
			if _, err := admin.Exec("DROP DATABASE `" + schema + "`"); err != nil {
				t.Error(err)
			}
		}
	})
	return ledger
}

func TestProtectedAuthorityRestoresWithoutBusinessDatabase(t *testing.T) {
	db, cfg, admin := testDatabase(t)
	ctx := context.Background()
	root := t.TempDir()
	w, p, u, skill, capability := randomID(t), randomID(t), randomID(t), randomID(t), randomID(t)
	blobs := storage.LocalBlobs{Root: filepath.Join(root, "source-blobs")}
	bundle, err := blobs.PutObject(ctx, p, strings.NewReader(`{"files":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	execTest(t, db, "INSERT INTO workspaces(id,name) VALUES(?,'offline source')", w)
	execTest(t, db, "INSERT INTO users(id,workspace_id,username,password_hash,role) VALUES(?,?,'offline-user','never copied','owner')", u, w)
	execTest(t, db, "INSERT INTO projects(id,workspace_id,name) VALUES(?,?,'offline project')", p, w)
	execTest(t, db, "INSERT INTO skill_packages(id,project_id,bundle_hash,report,source,created_by) VALUES(?,?,?,'{}','{}',?)", skill, p, bundle.Hash, u)
	execTest(t, db, "INSERT INTO capability_versions(id,project_id,capability_key,kind,content_hash,definition,created_by) VALUES(?,?,'test','skill',?,'{}',?)", capability, p, strings.Repeat("b", 64), u)
	ledger := provisionTestAuthority(t, db, admin)
	archive := filepath.Join(root, "before-revoke.zip")
	if _, err := Create(ctx, db, blobs, archive); err != nil {
		t.Fatal(err)
	}
	for _, entry := range []struct {
		table, column, id string
		kind              authority.Kind
	}{{"skill_revocations", "package_id", skill, authority.SkillRevoked}, {"capability_revocations", "version_id", capability, authority.CapabilityRevoked}} {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec("INSERT INTO "+entry.table+"("+entry.column+",project_id,actor_id,reason) VALUES(?,?,?,'after snapshot')", entry.id, p, u); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if err = ledger.AppendTx(ctx, tx, authority.Event{Kind: entry.kind, ProjectID: p, ResourceID: entry.id, ActorID: u, Reason: "after snapshot"}); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if err = tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	// Destroy only this generated temporary business schema. The restore path
	// must work when opening the original DSN is no longer possible.
	execTest(t, admin, "DROP DATABASE `"+cfg.DBName+"`")
	opt := RestoreOptions{ArchivePath: archive, TargetDSN: targetDSN(t, cfg, admin), BlobRoot: filepath.Join(root, "restored"), Authority: ProtectedAuthority{DB: admin, Ledger: ledger}}
	if _, err := Restore(ctx, opt); err != nil {
		t.Fatal(err)
	}
	target, err := sql.Open("mysql", opt.TargetDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	for _, table := range []string{"skill_revocations", "capability_revocations"} {
		var count int
		if err = target.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil || count != 1 {
			t.Fatal("post-snapshot revocation missing", table, count, err)
		}
	}
	cliTarget := targetDSN(t, cfg, admin)
	command := exec.Command("go", "run", "../../cmd/backup", "restore", "-archive", archive, "-source-env", "STUDIO_PROTECTED_SOURCE_DSN", "-target-env", "STUDIO_PROTECTED_TARGET_DSN", "-blobs", filepath.Join(root, "cli-restored"))
	command.Env = append(os.Environ(), "STUDIO_PROTECTED_SOURCE_DSN="+cfg.FormatDSN(), "STUDIO_PROTECTED_TARGET_DSN="+cliTarget)
	authorityConfig := cfg.Clone()
	authorityConfig.DBName = ledger.Schema
	command.Env = append(command.Env, "STUDIO_AUTHORITY_DSN="+authorityConfig.FormatDSN())
	output, err := command.CombinedOutput()
	if err != nil || !bytes.Contains(output, []byte(`"authority_checked_at"`)) {
		t.Fatal("CLI failed without original business DB", string(output), err)
	}
	var protectedSource authority.Source
	protectedSource.Database = cfg.DBName
	if err = admin.QueryRow("SELECT @@server_uuid").Scan(&protectedSource.ServerUUID); err != nil {
		t.Fatal(err)
	}
	current, err := ledger.Current(ctx, admin, protectedSource)
	if err != nil || len(current.Skills) != 1 || len(current.Capabilities) != 1 {
		t.Fatal("restore overwrote protected ledger", err)
	}
	var protectedTables int
	if err = target.QueryRow("SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name IN ('sources','events')").Scan(&protectedTables); err != nil || protectedTables != 0 {
		t.Fatal("protected tables included in archive", protectedTables, err)
	}
	file, err := os.Open(archive)
	if err != nil {
		t.Fatal(err)
	}
	stat, _ := file.Stat()
	legacy, err := ValidateArchive(file, stat.Size())
	file.Close()
	if err != nil {
		t.Fatal(err)
	}
	beforePrivateIndexes(legacy)
	legacy.Manifest.Schema = 26
	legacy.Manifest.Migrations = legacy.Manifest.Migrations[:26]
	legacy26Path := filepath.Join(root, "legacy26.zip")
	if err = os.WriteFile(legacy26Path, archiveBytes(t, legacy), 0600); err != nil {
		t.Fatal(err)
	}
	opt.ArchivePath = legacy26Path
	opt.TargetDSN = targetDSN(t, cfg, admin)
	opt.BlobRoot = filepath.Join(root, "legacy26-restored")
	if _, err = Restore(ctx, opt); err != nil {
		t.Fatal("schema26 restore failed", err)
	}
	delete(legacy.Data, "generation_previews")
	legacy.Manifest.Schema = 25
	legacy.Manifest.Migrations = legacy.Manifest.Migrations[:25]
	legacy25Path := filepath.Join(root, "legacy25.zip")
	if err = os.WriteFile(legacy25Path, archiveBytes(t, legacy), 0600); err != nil {
		t.Fatal(err)
	}
	opt.ArchivePath = legacy25Path
	opt.TargetDSN = targetDSN(t, cfg, admin)
	opt.BlobRoot = filepath.Join(root, "legacy25-restored")
	if _, err = Restore(ctx, opt); err != nil {
		t.Fatal("schema25 restore failed", err)
	}
	legacy.Manifest.Schema = 24
	legacy.Manifest.Authority = nil
	legacy.Manifest.Migrations = legacy.Manifest.Migrations[:24]
	for i, row := range legacy.Data["projects"] {
		legacy.Data["projects"][i] = row[:5]
	}
	legacyPath := filepath.Join(root, "legacy24.zip")
	if err = os.WriteFile(legacyPath, archiveBytes(t, legacy), 0600); err != nil {
		t.Fatal(err)
	}
	opt.ArchivePath = legacyPath
	opt.TargetDSN = targetDSN(t, cfg, admin)
	opt.BlobRoot = filepath.Join(root, "legacy-restored")
	if _, err = Restore(ctx, opt); err != nil {
		t.Fatal("schema24 archive could not restore through trusted upgrade", err)
	}
}

func TestSchema24ArchiveHasExplicitTrustedUpgrade(t *testing.T) {
	a := emptyArchive(t)
	a.Manifest.Schema = 24
	beforePrivateIndexes(a)
	delete(a.Data, "generation_previews")
	a.Manifest.Migrations = a.Manifest.Migrations[:24]
	a.Manifest.Authority = nil
	project := strings.Repeat("a", 32)
	workspace := strings.Repeat("b", 32)
	testRow(a, "workspaces", map[string]string{"id": workspace, "name": "legacy"})
	testRow(a, "projects", map[string]string{"id": project, "workspace_id": workspace, "name": "legacy", "revision": "1"})
	a.Data["projects"][0] = a.Data["projects"][0][:5]
	raw := archiveBytes(t, a)
	upgraded, err := ValidateArchive(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	if len(upgraded.Data["projects"][0]) != 6 || upgraded.Data["projects"][0][5] != nil {
		t.Fatal("legacy project row not safely upgraded")
	}
	a.Data["projects"][0] = append(a.Data["projects"][0], []byte("2020-01-01"))
	raw = archiveBytes(t, a)
	if _, err = ValidateArchive(bytes.NewReader(raw), int64(len(raw))); err == nil {
		t.Fatal("schema24 hidden extra column accepted")
	}
}

func TestProtectedRestoreRejectsChangedLedgerProvenance(t *testing.T) {
	a := emptyArchive(t)
	state := AuthorityState{Source: a.Manifest.Source, CheckedAt: time.Now().UTC(), Projects: map[string]bool{}, LedgerID: strings.Repeat("e", 32), Protocol: 1}
	if err := checkAuthority(a, state); err == nil {
		t.Fatal("replacement ledger accepted for schema25 snapshot")
	}
	state.LedgerID = a.Manifest.Authority.LedgerID
	if err := checkAuthority(a, state); err != nil {
		t.Fatal("matching live provenance refused", err)
	}
	state.Protocol = 2
	if err := checkAuthority(a, state); err == nil {
		t.Fatal("unsupported authority protocol accepted")
	}
	a.Manifest.Authority = nil
	if err := validateManifest(a.Manifest); err == nil {
		t.Fatal("schema25 archive accepted without ledger binding")
	}
}
