package backup_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"novelstudio/internal/backup"
	"novelstudio/internal/binlog"
)

type cancelAfterNewBaseline struct {
	context.Context
	stop      context.CancelFunc
	directory string
}

func (c *cancelAfterNewBaseline) Err() error {
	entries, _ := os.ReadDir(c.directory)
	count := 0
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".base") {
			count++
		}
	}
	if count > 1 {
		c.stop()
	}
	return c.Context.Err()
}

func TestLogRecoveryRolloverInterruptedAfterNewBaselineRecoversOverlap(t *testing.T) {
	f := isolated(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	root := filepath.Join(f.root, "rollover-journal")
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
	if err != nil {
		t.Fatal(err)
	}
	execute(t, f.db, "UPDATE projects SET name='日切期间的新标题',revision=revision+1 WHERE id=?", f.project)
	interrupt, stop := context.WithCancel(ctx)
	defer stop()
	boundary := &cancelAfterNewBaseline{Context: interrupt, stop: stop, directory: root}
	if _, err = runner.Tick(boundary, time.Now().UTC().Add(24*time.Hour)); !errors.Is(err, context.Canceled) {
		t.Fatal("did not interrupt after actual new-baseline publication", err)
	}
	if err = runner.Close(); err != nil {
		t.Fatal(err)
	}
	runner = nil
	runner, err = backup.NewLogRunner(ctx, f.db, f.blobs, f.config.FormatDSN(), root)
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := runner.Tick(ctx, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if restarted.BaselineID == first.BaselineID {
		t.Fatal("new baseline was not actually published")
	}
	records, err := runner.Store.Catalog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var current backup.LogBaselineRecord
	for _, record := range records {
		if record.ID == restarted.BaselineID {
			current = record
		}
	}
	old, err := runner.Store.Load(ctx, first.BaselineID, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	order, err := binlog.ComparePositions(old.Point().Position, current.Position)
	if err != nil || order < 0 {
		t.Fatal("restart hid unfinished rollover overlap", err)
	}
	if restarted.State != "CURRENT" || restarted.HistoryGaps != 0 {
		t.Fatal("recoverable rollover was not reconciled", restarted.State)
	}
}

func TestLogRecoveryStartupAuditsOlderRetainedChains(t *testing.T) {
	f := isolated(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	root := filepath.Join(f.root, "history-audit-journal")
	runner, err := backup.NewLogRunner(ctx, f.db, f.blobs, f.config.FormatDSN(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if runner != nil {
			runner.Close()
		}
	}()
	old, err := runner.Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	execute(t, f.db, "UPDATE projects SET name='历史修订',revision=revision+1 WHERE id=?", f.project)
	if _, err = runner.Collect(ctx); err != nil {
		t.Fatal(err)
	}
	fresh, err := runner.Store.CaptureBaseline(ctx, f.db, f.blobs)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	removed := false
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), old.BaselineID+".") && strings.HasSuffix(entry.Name(), ".seg") {
			if err = os.Remove(filepath.Join(root, entry.Name())); err != nil {
				t.Fatal(err)
			}
			removed = true
			break
		}
	}
	if !removed {
		t.Fatal("older chain has no published segment")
	}
	if err = runner.Close(); err != nil {
		t.Fatal(err)
	}
	runner = nil
	runner, err = backup.NewLogRunner(ctx, f.db, f.blobs, f.config.FormatDSN(), root)
	if err != nil {
		t.Fatal(err)
	}
	state, err := runner.Tick(ctx, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if state.BaselineID != fresh.ID || state.State != "CURRENT_WITH_GAPS" || state.HistoryGaps != 1 {
		t.Fatal("healthy latest baseline hid damaged retained history", state.State, state.HistoryGaps)
	}
}
