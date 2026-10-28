package integration

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"novelstudio/internal/authority"
	"novelstudio/internal/domain"
	"novelstudio/internal/models"
	"novelstudio/internal/objectwriter"
	"novelstudio/internal/retention"
	"novelstudio/internal/service"
	"novelstudio/internal/storage"
)

func TestRetentionGenerationInputUsesProtectedWriter(t *testing.T) {
	transport := &generationTransport{response: generationSuccess}
	_, svc, actor, project, original := generationFixture(t, transport)
	db := svc.IP.Workspace.DB
	installSourceRegistrationFixture(t, db)
	input := service.GenerationInput{BranchID: original.BranchID, Role: "writer", PriceID: original.Frozen.Price.ID, OperationID: "protected-generation", Request: models.Request{Prompt: "新章节生成所需的完整输入", MaxOutputTokens: 100}}
	created, err := svc.Prepare(context.Background(), actor, project, input)
	if err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM generation_tasks g JOIN retention_objects o ON o.project_id=g.project_id AND o.object_hash=g.input_hash WHERE g.id=? AND o.state='PRESENT' AND o.lease_until>UTC_TIMESTAMP(6)", created.ID).Scan(&count); err != nil || count != 1 {
		t.Fatal("generation input not protected", count, err)
	}
	owner, err := retention.RowOwnerKey("generation_tasks", map[string]string{"id": created.ID})
	if err != nil {
		t.Fatal(err)
	}
	var first string
	if err := db.QueryRow("SELECT CAST(first_verified_at AS CHAR) FROM retention_source_slots WHERE project_id=? AND owner_table='generation_tasks' AND owner_key=? AND field_key='frozen'", project, owner).Scan(&first); err != nil {
		t.Fatal("preparation did not capture original input age", err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM retention_references WHERE project_id=? AND owner_table='generation_tasks' AND owner_key=? AND field_key='frozen'", project, owner).Scan(&count); err != nil || count != 1 {
		t.Fatal("ordinary generation preparation lacks complete frozen source reference", count, err)
	}
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	observation := retention.SourceKey{Table: "generation_tasks", Primary: map[string]string{"id": created.ID}, ObserveOnly: true}
	full := observation
	full.ObserveOnly = false
	if err := retention.CaptureSourceValuesWithObjects(context.Background(), tx, authority.Ledger{Schema: os.Getenv("STUDIO_AUTHORITY_SCHEMA")}, project, svc.IP.Blobs.ReadObject, observation, full); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var recorded string
	if err := db.QueryRow("SELECT CAST(first_verified_at AS CHAR) FROM retention_references WHERE project_id=? AND owner_table='generation_tasks' AND owner_key=? AND field_key='frozen'", project, owner).Scan(&recorded); err != nil || recorded != first {
		t.Fatal("later consumer capture changed age or was suppressed by observation", recorded, first, err)
	}
	if _, err := db.Exec("UPDATE retention_objects SET state='BLOCKED' WHERE project_id=? AND object_hash=?", project, created.Frozen.InputHash); err != nil {
		t.Fatal(err)
	}
	// Retrying an already saved operation returns its original identity without
	// rewriting the file. This does not grant dispatch or repair its blocked file.
	replay, err := svc.Prepare(context.Background(), actor, project, input)
	if err != nil || replay.ID != created.ID {
		t.Fatal(replay, err)
	}
	input.OperationID = "new-operation-same-blocked-file"
	if _, err := svc.Prepare(context.Background(), actor, project, input); err == nil {
		t.Fatal("new generation adopted unavailable object")
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM generation_tasks WHERE project_id=?", project).Scan(&count); err != nil || count != 2 || transport.calls.Load() != 0 {
		t.Fatal("failed prepare wrote a task or called provider", count, err)
	}
}

func TestRetentionImportWritesReferenceAndLeaseTogether(t *testing.T) {
	b, db := setup(t)
	project := createProject(b, "资料导入保护", "import-writer")
	actor := domain.Actor{ID: b.request("GET", "/api/me", nil, 200)["id"].(string)}
	bid := branch(b, project, "正史")
	installSourceRegistrationFixture(t, db)
	store := storage.LocalBlobs{Root: t.TempDir()}
	ip := &service.IP{Workspace: &service.Workspace{DB: db}, Blobs: store}
	ctx := context.Background()
	data := []byte("港口贸易资料：记录潮汐、航线与税制。")
	result, err := ip.ImportSource(ctx, actor, project, bid, "资料.txt", "import-once", data)
	if err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM source_imports s JOIN retention_objects o ON o.project_id=s.project_id AND o.object_hash=s.source_hash WHERE s.id=? AND o.state='PRESENT' AND o.lease_until>UTC_TIMESTAMP(6)", result.ID).Scan(&count); err != nil || count != 1 {
		t.Fatal("import lacks committed protection", count, err)
	}
	var revision int64
	if err := db.QueryRow("SELECT revision FROM retention_objects WHERE project_id=? AND object_hash=?", project, result.Hash).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	replayed, err := ip.ImportSource(ctx, actor, project, bid, "资料.txt", "import-once", data)
	if err != nil || replayed.ID != result.ID {
		t.Fatal(replayed, err)
	}
	var replayRevision int64
	if err := db.QueryRow("SELECT revision FROM retention_objects WHERE project_id=? AND object_hash=?", project, result.Hash).Scan(&replayRevision); err != nil || replayRevision != revision {
		t.Fatal("replay renewed file lease", replayRevision, revision, err)
	}
	// A physical failure after lease insertion must roll back both SQL records.
	badRoot := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(badRoot, []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	ip.Blobs = storage.LocalBlobs{Root: badRoot}
	if _, err := ip.ImportSource(ctx, actor, project, bid, "失败.txt", "failed-import", []byte("different content")); err == nil {
		t.Fatal("failed physical write was adopted")
	}
	for _, table := range []string{"source_imports", "retention_objects"} {
		if err := db.QueryRow("SELECT COUNT(*) FROM "+table+" WHERE project_id=?", project).Scan(&count); err != nil || count != 1 {
			t.Fatal("failed write left committed records", table, count, err)
		}
	}
}

func TestRetentionObjectWriterLocksAndPreservesAge(t *testing.T) {
	b, db := setup(t)
	project := createProject(b, "写入保护", "writer-project")
	installSourceRegistrationFixture(t, db)
	store := storage.LocalBlobs{Root: t.TempDir()}
	ctx := context.Background()
	data := []byte("用于长篇写作的研究资料")
	hash := fmt.Sprintf("%x", sha256.Sum256(data))
	write := func() *sql.Tx {
		t.Helper()
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { tx.Rollback() })
		ref, err := objectwriter.Write(ctx, tx, store, project, data)
		if err != nil || ref.Hash != hash {
			t.Fatal(ref, err)
		}
		return tx
	}
	tx := write()
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var first string
	var leased int
	if err := db.QueryRow("SELECT CAST(first_seen_at AS CHAR),lease_until>UTC_TIMESTAMP(6) FROM retention_objects WHERE project_id=? AND object_hash=?", project, hash).Scan(&first, &leased); err != nil || leased != 1 {
		t.Fatal(first, leased, err)
	}
	if _, err := db.Exec("UPDATE retention_objects SET first_seen_at='2020-01-02 03:04:05.000006',lease_until='2090-01-01' WHERE project_id=? AND object_hash=?", project, hash); err != nil {
		t.Fatal(err)
	}
	tx = write()
	// A cleaner must lock this same row through physical deletion. It cannot
	// claim the object between filesystem write and business reference commit.
	wait, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()
	other, err := db.BeginTx(wait, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Rollback()
	var state string
	err = other.QueryRowContext(wait, "SELECT state FROM retention_objects WHERE project_id=? AND object_hash=? FOR UPDATE", project, hash).Scan(&state)
	if err == nil || wait.Err() == nil {
		t.Fatal("concurrent cleanup acquired writer lock", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var lease string
	if err := db.QueryRow("SELECT CAST(first_seen_at AS CHAR),CAST(lease_until AS CHAR) FROM retention_objects WHERE project_id=? AND object_hash=?", project, hash).Scan(&first, &lease); err != nil || first != "2020-01-02 03:04:05.000006" || lease != "2090-01-01 00:00:00.000000" {
		t.Fatal("rewrite refreshed age or shortened lease", first, lease, err)
	}
	for _, state := range []string{"DELETING", "DELETED", "BLOCKED"} {
		if _, err := db.Exec("UPDATE retention_objects SET state=? WHERE project_id=? AND object_hash=?", state, project, hash); err != nil {
			t.Fatal(err)
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		_, err = objectwriter.Write(ctx, tx, store, project, data)
		tx.Rollback()
		if err == nil {
			t.Fatal("writer revived unavailable object", state)
		}
	}
}

func TestRetentionObjectWriterRollbackAndPartialInstall(t *testing.T) {
	b, db := setup(t)
	project := createProject(b, "事务回滚", "writer-rollback")
	store := storage.LocalBlobs{Root: t.TempDir()}
	ctx := context.Background()
	write := func(data string) error {
		t.Helper()
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		_, err = objectwriter.Write(ctx, tx, store, project, []byte(data))
		return err
	}
	if err := write("schema35 compatibility"); err != nil {
		t.Fatal(err)
	}
	installSourceRegistrationFixture(t, db)
	if err := write("uncommitted source"); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM retention_objects").Scan(&count); err != nil || count != 0 {
		t.Fatal("rollback left registered object", count, err)
	}
	if _, err := db.Exec("DELETE FROM schema_migrations WHERE name=?", retention.MigrationName); err != nil {
		t.Fatal(err)
	}
	if err := write("partial metadata must fail"); err == nil {
		t.Fatal("partial install silently disabled protection")
	}
}
