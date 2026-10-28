package backup_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"

	"novelstudio/internal/backup"
	"novelstudio/internal/binlog"
	"novelstudio/internal/service"
	"novelstudio/internal/storage"
)

func TestLogRecoveryRestoresExactMiddleCommitObjectsAndDisablesAuthority(t *testing.T) {
	f := isolated(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	branch, resource := service.NewID(), service.NewID()
	before, middle, later := f.put(t, "原稿"), f.put(t, "需要找回的中间修订"), f.put(t, "不应提前进入中间恢复点的后稿")
	execute(t, f.db, "INSERT INTO branches(id,project_id,name) VALUES(?,?,'main')", branch, f.project)
	execute(t, f.db, "INSERT INTO resources(id,project_id,branch_id,name,kind,content_hash) VALUES(?,?,?,'正文','document',?)", resource, f.project, branch, before)
	path := filepath.Join(f.root, "baseline.zip")
	baseline, _, err := backup.CreateLogBaseline(ctx, f.db, f.blobs, path)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := backup.OpenLogReplay(baseline, raw)
	if err != nil {
		t.Fatal(err)
	}
	execute(t, f.db, "UPDATE resources SET content_hash=?,revision=revision+1 WHERE id=?", middle, resource)
	var selected binlog.Position
	var included, excluded, gtids string
	if err := f.db.QueryRowContext(ctx, "SHOW BINARY LOG STATUS").Scan(&selected.File, &selected.Offset, &included, &excluded, &gtids); err != nil {
		t.Fatal(err)
	}
	execute(t, f.db, "UPDATE resources SET content_hash=?,revision=revision+1 WHERE id=?", later, resource)
	password := "PASSWORD_MUST_NOT_ENTER_PROJECT_LOG"
	execute(t, f.db, "UPDATE users SET password_hash=? WHERE id=?", password, f.user)
	execute(t, f.db, "INSERT INTO sessions(token_hash,user_id,expires_at) VALUES(?,?,UTC_TIMESTAMP(6)+INTERVAL 1 DAY)", service.NewID()+service.NewID(), f.user)
	reader, err := binlog.NewReader(f.config.FormatDSN(), baseline.Source.ServerUUID)
	if err != nil {
		t.Fatal(err)
	}
	segment, err := replay.CaptureSegment(ctx, f.db, reader, f.blobs)
	if err != nil {
		t.Fatal("actual log segment capture failed", err)
	}
	if bytes.Contains(segment, []byte(password)) || bytes.Contains(segment, []byte(base64.StdEncoding.EncodeToString([]byte(password)))) {
		t.Fatal("password leaked into sanitized journal")
	}
	hash := sha256.Sum256(segment)
	segmentHash := hex.EncodeToString(hash[:])
	journalPath := filepath.Join(f.root, "journal")
	journal, err := backup.OpenLogStore(journalPath, baseline.Source)
	if err != nil {
		t.Fatal(err)
	}
	baselineID, err := journal.CommitBaseline(ctx, baseline, raw, time.Now().UTC())
	if err != nil {
		journal.Close()
		t.Fatal(err)
	}
	if _, err = journal.Append(ctx, baselineID, segment, time.Now().UTC()); err != nil {
		journal.Close()
		t.Fatal(err)
	}
	if err = journal.Close(); err != nil {
		t.Fatal(err)
	}
	journal, err = backup.OpenLogStore(journalPath, baseline.Source)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	midArchive, err := journal.Archive(ctx, baselineID, selected, time.Now().UTC())
	if err != nil {
		t.Fatal("actual middle commit materialization failed", err)
	}
	midPath := filepath.Join(f.root, "selected.zip")
	if err := os.WriteFile(midPath, midArchive, 0600); err != nil {
		t.Fatal(err)
	}
	opt := f.options(t, midPath, backup.ProtectedAuthority{DB: f.admin, Ledger: f.ledger})
	if _, err := backup.Restore(ctx, opt); err != nil {
		t.Fatal("selected point restore failed", err)
	}
	target := openTarget(t, opt)
	var restoredHash string
	if err := target.QueryRowContext(ctx, "SELECT content_hash FROM resources WHERE id=?", resource).Scan(&restoredHash); err != nil || restoredHash != middle {
		t.Fatal("restore did not stop at selected content", err)
	}
	body, err := (storage.LocalBlobs{Root: opt.BlobRoot}).ReadObject(ctx, f.project, middle)
	if err != nil || string(body) != "需要找回的中间修订" {
		t.Fatal("selected immutable object not restored", err)
	}
	if _, err := os.Stat(filepath.Join(opt.BlobRoot, f.project, later)); !os.IsNotExist(err) {
		t.Fatal("later unselected private object exported")
	}
	var enabled, sessions, secrets int
	if err := target.QueryRow("SELECT COUNT(*) FROM users WHERE enabled<>0").Scan(&enabled); err != nil {
		t.Fatal(err)
	}
	if err := target.QueryRow("SELECT COUNT(*) FROM sessions").Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	if err := target.QueryRow("SELECT COUNT(*) FROM users WHERE password_hash<>''").Scan(&secrets); err != nil {
		t.Fatal(err)
	}
	if enabled != 0 || sessions != 0 || secrets != 0 {
		t.Fatal("restore reactivated old authority")
	}
	if err := replay.ApplySegment(segment, segmentHash); err != nil {
		t.Fatal(err)
	}
	final, err := replay.ArchiveBytes()
	if err != nil {
		t.Fatal(err)
	}
	a, err := backup.ValidateArchive(bytes.NewReader(final), int64(len(final)))
	if err != nil || string(a.Data["resources"][0][6]) != later {
		t.Fatal("full segment did not preserve later committed state", err)
	}
}
