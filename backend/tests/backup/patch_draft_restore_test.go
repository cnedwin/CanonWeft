package backup_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"novelstudio/internal/backup"
	"novelstudio/internal/domain"
	"novelstudio/internal/service"
)

func TestPrivatePatchDraftRestoresExactUnpublishedWorkWithoutAuthority(t *testing.T) {
	f := isolated(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	branch := service.NewID()
	execute(t, f.db, "INSERT INTO branches(id,project_id,name) VALUES(?,?,'main')", branch, f.project)
	content := domain.PatchDraftContent{OperationID: "still-unpublished", Group: domain.TextPatchGroup{ID: service.NewID(), Members: []string{}, Patches: []domain.TextPatch{}}, Snippet: "还没改完的原文🌙", Replacement: "新的角色动机", Reason: "正在核对"}
	raw, _ := json.Marshal(content)
	sum := sha256.Sum256(raw)
	hash := hex.EncodeToString(sum[:])
	execute(t, f.db, "INSERT INTO patch_drafts(branch_id,actor_id,project_id,revision,payload,content_hash) VALUES(?,?,?,7,?,?)", branch, f.user, f.project, string(raw), hash)
	path := filepath.Join(f.root, "private-draft.zip")
	if _, err := backup.Create(ctx, f.db, f.blobs, path); err != nil {
		t.Fatal(err)
	}
	opt := f.options(t, path, backup.ProtectedAuthority{DB: f.admin, Ledger: f.ledger})
	if _, err := backup.Restore(ctx, opt); err != nil {
		t.Fatal(err)
	}
	restored := openTarget(t, opt)
	var revision int64
	var saved, savedHash string
	if err := restored.QueryRow("SELECT revision,payload,content_hash FROM patch_drafts WHERE branch_id=? AND actor_id=? AND project_id=?", branch, f.user, f.project).Scan(&revision, &saved, &savedHash); err != nil || revision != 7 || saved != string(raw) || savedHash != hash {
		t.Fatal("unpublished private work changed", err)
	}
	var enabled int
	if err := restored.QueryRow("SELECT enabled FROM users WHERE id=?", f.user).Scan(&enabled); err != nil || enabled != 0 {
		t.Fatal("restore granted identity authority", err)
	}
	var releases int
	if err := restored.QueryRow("SELECT COUNT(*) FROM artifact_releases").Scan(&releases); err != nil || releases != 0 {
		t.Fatal("private draft became a candidate", err)
	}
}
