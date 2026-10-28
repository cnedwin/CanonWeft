package backup_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"novelstudio/internal/backup"
	"novelstudio/internal/service"
)

func TestLogRecoveryRunnerCollectsResumesAndMakesLostTailVisible(t *testing.T) {
	f := isolated(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	branch, resource := service.NewID(), service.NewID()
	before, after := f.put(t, "自动保护之前"), f.put(t, "自动保护之后")
	execute(t, f.db, "INSERT INTO branches(id,project_id,name) VALUES(?,?,'main')", branch, f.project)
	execute(t, f.db, "INSERT INTO resources(id,project_id,branch_id,name,kind,content_hash) VALUES(?,?,?,'正文','document',?)", resource, f.project, branch, before)
	root := filepath.Join(f.root, "automatic-journal")
	runner, err := backup.NewLogRunner(ctx, f.db, f.blobs, f.config.FormatDSN(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if runner != nil {
			runner.Close()
		}
	}()
	first, err := runner.Collect(ctx)
	if err != nil || first.State != "CURRENT" || first.BaselineID == "" {
		t.Fatal("automatic baseline unavailable", err, first.State)
	}
	execute(t, f.db, "UPDATE resources SET content_hash=?,revision=revision+1 WHERE id=?", after, resource)
	second, err := runner.Collect(ctx)
	if err != nil || second.State != "CURRENT" || second.Latest.Hash == first.Latest.Hash {
		t.Fatal("automatic segment not committed", err, second.State)
	}
	raw, err := runner.Store.Archive(ctx, second.BaselineID, second.Latest.Position, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	a, err := backup.ValidateArchive(bytes.NewReader(raw), int64(len(raw)))
	if err != nil || string(a.Data["resources"][0][6]) != after {
		t.Fatal("automatic archive has wrong content", err)
	}
	if err = runner.Close(); err != nil {
		t.Fatal(err)
	}
	runner = nil
	runner, err = backup.NewLogRunner(ctx, f.db, f.blobs, f.config.FormatDSN(), root)
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := runner.Tick(ctx, time.Now().UTC())
	if err != nil || resumed.Latest != second.Latest || resumed.BaselineID != second.BaselineID {
		t.Fatal("restart did not resume committed cursor", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	removed := false
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".seg") {
			if err = os.Remove(filepath.Join(root, entry.Name())); err != nil {
				t.Fatal(err)
			}
			removed = true
			break
		}
	}
	if !removed {
		t.Fatal("no actual committed segment to remove")
	}
	failed, err := runner.Collect(ctx)
	if err == nil || failed.State != "CAPTURE_FAILED" {
		t.Fatal("lost committed tail silently accepted", err, failed.State)
	}
	fresh, err := runner.Collect(ctx)
	if err != nil || fresh.State != "CURRENT_WITH_GAPS" || fresh.HistoryGaps != 1 || fresh.BaselineID == second.BaselineID {
		t.Fatal("fresh coverage hid historical loss", err, fresh.State)
	}
	if _, err = runner.Store.Load(ctx, second.BaselineID, time.Now().UTC()); err == nil {
		t.Fatal("damaged historical chain re-enabled")
	}
	if err = runner.Close(); err != nil {
		t.Fatal(err)
	}
	runner = nil
	runner, err = backup.NewLogRunner(ctx, f.db, f.blobs, f.config.FormatDSN(), root)
	if err != nil {
		t.Fatal(err)
	}
	last, err := runner.Tick(ctx, time.Now().UTC())
	if err != nil || last.State != "CURRENT_WITH_GAPS" || last.HistoryGaps != 1 {
		t.Fatal("restart erased history-gap warning", err, last.State)
	}
	running, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { runner.Run(running, func(_ backup.LogRecoveryView, _ error) { stop() }); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("automatic collector failed to stop")
	}
}
