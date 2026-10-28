package backup_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"novelstudio/internal/backup"
	"novelstudio/internal/domain"
	"novelstudio/internal/service"
	"novelstudio/internal/storage"
)

func TestIngestRestorePreservesCompletedReportAndPrivateEdits(t *testing.T) {
	f := isolated(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	branch := service.NewID()
	execute(t, f.db, "INSERT INTO branches(id,project_id,name) VALUES(?,?,'main')", branch, f.project)
	ip := &service.IP{Workspace: &service.Workspace{DB: f.db}, Blobs: f.blobs}
	jobs := &service.IngestJobs{IP: ip}
	actor := domain.Actor{ID: f.user}
	completed, err := jobs.Create(ctx, actor, f.project, branch, "original.md", "backup-import", []byte("原始世界观记录🌙"))
	if err != nil {
		t.Fatal(err)
	}
	if state, err := jobs.Advance(ctx, completed.ID); err != nil || state != "COMPLETED" {
		t.Fatal(state, err)
	}
	draft, err := ip.ReadImportDraft(ctx, actor, f.project, branch, completed.ID)
	if err != nil {
		t.Fatal(err)
	}
	draft.Content.Text = "作者另行修改的私人记录"
	draft.Content.Policy = &domain.KnowledgePolicy{Space: "methods", Visibility: "author"}
	draft, err = ip.SaveImportDraft(ctx, actor, f.project, branch, completed.ID, draft)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := jobs.Create(ctx, actor, f.project, branch, "later.md", "backup-pending", []byte("尚未处理"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(f.root, "ingestion.zip")
	if _, err = backup.Create(ctx, f.db, f.blobs, path); err != nil {
		t.Fatal(err)
	}
	opt := f.options(t, path, backup.ProtectedAuthority{DB: f.admin, Ledger: f.ledger})
	if _, err = backup.Restore(ctx, opt); err != nil {
		t.Fatal(err)
	}
	restored := openTarget(t, opt)
	var reportText, draftText, state string
	var paused, cancelled bool
	if err = restored.QueryRow("SELECT JSON_UNQUOTE(JSON_EXTRACT(report,'$.text')) FROM source_imports WHERE id=?", completed.ID).Scan(&reportText); err != nil || reportText != "原始世界观记录🌙" {
		t.Fatal("original report changed", err)
	}
	if err = restored.QueryRow("SELECT JSON_UNQUOTE(JSON_EXTRACT(payload,'$.text')) FROM import_drafts WHERE import_id=? AND actor_id=?", completed.ID, f.user).Scan(&draftText); err != nil || draftText != draft.Content.Text {
		t.Fatal("private edit lost", err)
	}
	var space string
	if err = restored.QueryRow("SELECT JSON_UNQUOTE(JSON_EXTRACT(payload,'$.policy.space')) FROM import_drafts WHERE import_id=? AND actor_id=?", completed.ID, f.user).Scan(&space); err != nil || space != "methods" {
		t.Fatal("private knowledge use lost", err)
	}
	for id, hash := range map[string]string{completed.ID: completed.Hash, pending.ID: pending.Hash} {
		original, err := (storage.LocalBlobs{Root: opt.BlobRoot}).ReadObject(ctx, f.project, hash)
		expected := "尚未处理"
		if id == completed.ID {
			expected = "原始世界观记录🌙"
		}
		if err != nil || string(original) != expected {
			t.Fatal("raw file missing or changed after restore", err)
		}
	}
	if err = restored.QueryRow("SELECT state,paused,cancel_requested FROM ingest_jobs WHERE id=?", pending.ID).Scan(&state, &paused, &cancelled); err != nil || state != "CANCELLED" || !paused || !cancelled {
		t.Fatal("restored upload can execute", err)
	}
}
