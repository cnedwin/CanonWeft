package backup_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"novelstudio/internal/backup"
	"novelstudio/internal/binlog"
)

func TestLogBatchActualThousandTransactionsKeepDurablePrefixAndResume(t *testing.T) {
	f := isolated(t)
	ctx, stop := context.WithTimeout(context.Background(), 2*time.Minute)
	defer stop()
	path := filepath.Join(f.root, "batch-baseline.zip")
	baseline, _, err := backup.CreateLogBaseline(ctx, f.db, f.blobs, path)
	if err != nil {
		t.Fatal(err)
	}
	rawBaseline, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Each Exec is a real autocommitted source transaction. Reusing one row
	// keeps the fixture's state size small while exceeding the old range cap.
	for i := 1; i <= 1005; i++ {
		if _, err := f.db.ExecContext(ctx, "UPDATE workspaces SET name=? WHERE id=?", fmt.Sprintf("batch-%04d", i), f.workspace); err != nil {
			t.Fatal(err)
		}
	}
	reader, err := binlog.NewReader(f.config.FormatDSN(), baseline.Source.ServerUUID)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"complete range", "publication failure", "cancel after durable prefix"} {
		t.Run(mode, func(t *testing.T) {
			journalPath := filepath.Join(t.TempDir(), "journal")
			journal, err := backup.OpenLogStore(journalPath, baseline.Source)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if journal != nil {
					journal.Close()
				}
			}()
			id, err := journal.CommitBaseline(ctx, baseline, rawBaseline, time.Now().UTC())
			if err != nil {
				t.Fatal(err)
			}
			replay, err := journal.Load(ctx, id, time.Now().UTC())
			if err != nil {
				t.Fatal(err)
			}
			capture, cancel := context.WithCancel(ctx)
			defer cancel()
			calls := 0
			var first backup.LogPoint
			var expectedPrefix []byte
			intentional := errors.New("intentional second publication failure")
			point, err := replay.CaptureSegments(capture, f.db, reader, f.blobs, func(raw []byte) (backup.LogPoint, error) {
				calls++
				var segment backup.LogSegment
				if err := json.Unmarshal(raw, &segment); err != nil {
					t.Fatal(err)
				}
				if len(segment.Transactions) > 1000 {
					t.Fatal("durable batch exceeded transaction cap")
				}
				if mode == "complete range" {
					return journal.Append(capture, id, raw, time.Now().UTC())
				}
				if calls == 2 {
					return backup.LogPoint{}, intentional
				}
				if len(segment.Transactions) != 1000 {
					t.Fatal("first prefix did not use bounded complete transactions")
				}
				for _, tx := range segment.Transactions {
					for _, change := range tx.Changes {
						if change.Table == "workspaces" {
							expectedPrefix = append([]byte{}, change.After[1]...)
						}
					}
				}
				first, err = journal.Append(capture, id, raw, time.Now().UTC())
				if err == nil && mode == "cancel after durable prefix" {
					cancel()
				}
				return first, err
			})
			if mode == "complete range" {
				if err != nil || calls < 2 || point != replay.Point() {
					t.Fatal("large range did not complete in bounded segments", err, calls)
				}
				archive, err := journal.Archive(ctx, id, point.Position, time.Now().UTC())
				if err != nil {
					t.Fatal(err)
				}
				final, err := backup.ValidateArchive(bytes.NewReader(archive), int64(len(archive)))
				if err != nil || string(final.Data["workspaces"][0][1]) != "batch-1005" {
					t.Fatal("large range lost committed source changes", err)
				}
				return
			}
			if mode == "publication failure" {
				if !errors.Is(err, intentional) || calls != 2 {
					t.Fatal("second publication did not fail as intended", err, calls)
				}
			} else if !errors.Is(err, context.Canceled) || calls != 1 {
				t.Fatal("cancellation did not stop unpublished tail", err, calls)
			}
			if point != first || replay.Point() != first || first.Position == baseline.Position {
				t.Fatal("capture lost or overstated acknowledged progress")
			}
			if err := journal.Close(); err != nil {
				t.Fatal(err)
			}
			journal = nil
			journal, err = backup.OpenLogStore(journalPath, baseline.Source)
			if err != nil {
				t.Fatal(err)
			}
			recovered, err := journal.Load(ctx, id, time.Now().UTC())
			if err != nil || recovered.Point() != first {
				t.Fatal("durable prefix did not reopen", err)
			}
			prefixArchive, err := journal.Archive(ctx, id, first.Position, time.Now().UTC())
			if err != nil {
				t.Fatal(err)
			}
			prefix, err := backup.ValidateArchive(bytes.NewReader(prefixArchive), int64(len(prefixArchive)))
			if err != nil {
				t.Fatal(err)
			}
			if expectedPrefix != nil && !bytes.Equal(prefix.Data["workspaces"][0][1], expectedPrefix) {
				t.Fatal("prefix includes unpublished source changes")
			}
			resumed, err := recovered.CaptureSegments(ctx, f.db, reader, f.blobs, func(raw []byte) (backup.LogPoint, error) { return journal.Append(ctx, id, raw, time.Now().UTC()) })
			if err != nil {
				t.Fatal("resume after durable prefix failed", err)
			}
			if resumed.Position == first.Position || resumed.ObservedAt.Before(first.ObservedAt) {
				t.Fatal("resume did not advance safely")
			}
			archive, err := journal.Archive(ctx, id, resumed.Position, time.Now().UTC())
			if err != nil {
				t.Fatal(err)
			}
			final, err := backup.ValidateArchive(bytes.NewReader(archive), int64(len(archive)))
			if err != nil || string(final.Data["workspaces"][0][1]) != "batch-1005" {
				t.Fatal("resumed capture did not preserve all source commits", err)
			}
		})
	}
}
