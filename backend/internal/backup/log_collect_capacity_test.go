package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"novelstudio/internal/binlog"
	"novelstudio/internal/storage"
)

func captureObjectFixture(t *testing.T, count int) (*LogReplay, LogBaseline, []byte, storage.LocalBlobs, []binlog.Transaction) {
	t.Helper()
	a := emptyArchive(t)
	p, w, branch := strings.Repeat("a", 32), strings.Repeat("b", 32), strings.Repeat("c", 32)
	testRow(a, "workspaces", map[string]string{"id": w, "name": "workspace"})
	testRow(a, "projects", map[string]string{"id": p, "workspace_id": w, "name": "novel", "revision": "1"})
	testRow(a, "branches", map[string]string{"id": branch, "project_id": p, "name": "main", "revision": "1"})
	raw := archiveBytes(t, a)
	b := LogBaseline{Format: 1, ArchiveSHA256: digest(raw), Source: a.Manifest.Source, Schema: supportedSchemaVersion, ObservedAt: a.Manifest.SnapshotAt, Position: binlog.Position{File: "binlog.000001", Offset: 100}, Tables: rowContracts()}
	r, err := OpenLogReplay(b, raw)
	if err != nil {
		t.Fatal(err)
	}
	blobs := storage.LocalBlobs{Root: filepath.Join(t.TempDir(), "objects")}
	var transactions []binlog.Transaction
	position := b.Position
	for i := 0; i < count; i++ {
		body := bytes.Repeat([]byte{byte('a' + i)}, 4096)
		ref, err := blobs.PutObject(context.Background(), p, bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		rows := emptyArchive(t)
		testRow(rows, "resources", map[string]string{"id": fmt.Sprintf("%032x", i+1), "project_id": p, "branch_id": branch, "name": "reference", "kind": "source", "content_hash": ref.Hash, "revision": "1"})
		next := position
		next.Offset += 100
		transactions = append(transactions, binlog.Transaction{Start: position, End: next, CommittedAt: b.ObservedAt.Add(time.Duration(i+1) * time.Second), Changes: []binlog.Change{{Table: "resources", Kind: "insert", After: binlog.Row(rows.Data["resources"][0])}}})
		position = next
	}
	return r, b, raw, blobs, transactions
}

func TestLogCaptureBatchSplitsObjectAndEncodedCapacity(t *testing.T) {
	for _, kind := range []string{"object bytes", "encoded JSON"} {
		t.Run(kind, func(t *testing.T) {
			r, baseline, raw, blobs, transactions := captureObjectFixture(t, 4)
			if kind == "object bytes" {
				r.capacity.segmentObjects = 6000
			} else {
				r.capacity.segmentJSON = 10000
			}
			observed := baseline.ObservedAt.Add(time.Minute)
			through := transactions[3].End
			through.Offset += 10 // Metadata-only tail is certified only by finish.
			store, err := OpenLogStore(filepath.Join(t.TempDir(), "journal"), baseline.Source)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			id, err := store.CommitBaseline(context.Background(), baseline, raw, observed)
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			batch := newLogCaptureBatch(context.Background(), r, blobs, observed, func(raw []byte) (LogPoint, error) {
				calls++
				var segment LogSegment
				if err := json.Unmarshal(raw, &segment); err != nil {
					t.Fatal(err)
				}
				if len(segment.Transactions) != 1 || len(segment.Objects) != 1 {
					t.Fatal("capacity split did not preserve one complete transaction/object per part")
				}
				if calls < 4 && (segment.ObservedAt == observed || segment.Through != transactions[calls-1].End) {
					t.Fatal("prefix claimed unread tail coverage")
				}
				if kind == "encoded JSON" && len(raw) > 10000 {
					t.Fatal("encoded size budget bypassed")
				}
				return store.Append(context.Background(), id, raw, observed)
			})
			for _, tx := range transactions {
				if err := batch.consume(tx); err != nil {
					t.Fatal(err)
				}
			}
			point, err := batch.finish(through)
			if err != nil || calls != 4 || point.Position != through || point.ObservedAt != observed {
				t.Fatal("splittable object batch could not finish", err, calls, point)
			}
			archive, err := store.Archive(context.Background(), id, through, observed)
			if err != nil {
				t.Fatal(err)
			}
			restored, err := ValidateArchive(bytes.NewReader(archive), int64(len(archive)))
			if err != nil || len(restored.Data["resources"]) != 4 || len(restored.Blobs) != 4 {
				t.Fatal("split lost complete final recovery state", err)
			}
		})
	}
}

func TestLogCaptureBatchSplitFailureReopensAtAcknowledgedPrefix(t *testing.T) {
	r, baseline, raw, blobs, transactions := captureObjectFixture(t, 2)
	r.capacity.segmentObjects = 6000
	observed := baseline.ObservedAt.Add(time.Minute)
	root := filepath.Join(t.TempDir(), "journal")
	store, err := OpenLogStore(root, baseline.Source)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if store != nil {
			store.Close()
		}
	}()
	id, err := store.CommitBaseline(context.Background(), baseline, raw, observed)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	publicationFailure := errors.New("second part publication failed")
	batch := newLogCaptureBatch(context.Background(), r, blobs, observed, func(raw []byte) (LogPoint, error) {
		calls++
		if calls == 2 {
			return LogPoint{}, publicationFailure
		}
		return store.Append(context.Background(), id, raw, observed)
	})
	for _, tx := range transactions {
		if err := batch.consume(tx); err != nil {
			t.Fatal(err)
		}
	}
	point, err := batch.finish(transactions[1].End)
	if !errors.Is(err, publicationFailure) || calls != 2 || point.Position != transactions[0].End || point.ObservedAt == observed {
		t.Fatal("failed split did not retain only durable prefix", err, calls, point)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenLogStore(root, baseline.Source)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(context.Background(), id, observed)
	if err != nil || loaded.Point() != point || len(loaded.archive.Data["resources"]) != 1 {
		t.Fatal("durable split prefix could not reopen", err)
	}
	loaded.capacity.segmentObjects = 6000
	resumed := newLogCaptureBatch(context.Background(), loaded, blobs, observed, func(raw []byte) (LogPoint, error) {
		return store.Append(context.Background(), id, raw, observed)
	})
	if err := resumed.consume(transactions[1]); err != nil {
		t.Fatal(err)
	}
	if point, err := resumed.finish(transactions[1].End); err != nil || point.Position != transactions[1].End || len(loaded.archive.Data["resources"]) != 2 {
		t.Fatal("split tail could not resume", err)
	}
}

func TestLogCaptureBatchDoesNotSplitSingleTransactionOrPermanentFailure(t *testing.T) {
	for _, kind := range []string{"single transaction", "history capacity", "missing object", "invalid row"} {
		t.Run(kind, func(t *testing.T) {
			r, baseline, _, blobs, transactions := captureObjectFixture(t, 2)
			if kind == "single transaction" {
				r.capacity.segmentObjects = 6000
				transactions[0].Changes = append(transactions[0].Changes, transactions[1].Changes...)
				transactions[0].End = transactions[1].End
				transactions = transactions[:1]
			} else if kind == "history capacity" {
				r.capacity.entries = 3
			} else if kind == "missing object" {
				blobs.Root = filepath.Join(t.TempDir(), "missing")
			} else {
				transactions[1].Changes[0].Kind = "delete"
				transactions[1].Changes[0].Before = transactions[1].Changes[0].After
				transactions[1].Changes[0].After = nil
			}
			calls := 0
			batch := newLogCaptureBatch(context.Background(), r, blobs, baseline.ObservedAt.Add(time.Minute), func([]byte) (LogPoint, error) {
				calls++
				return LogPoint{}, errors.New("unexpected publication")
			})
			for _, tx := range transactions {
				if err := batch.consume(tx); err != nil {
					t.Fatal(err)
				}
			}
			point, err := batch.finish(transactions[len(transactions)-1].End)
			if err == nil || calls != 0 || point.Position != baseline.Position {
				t.Fatal("unsplittable failure was retried or advanced", err, calls)
			}
			if errors.Is(err, errLogSegmentCapacity) != (kind == "single transaction") {
				t.Fatal("permanent failure misclassified as splittable capacity", err)
			}
			if kind == "single transaction" && !strings.Contains(err.Error(), "single log transaction") {
				t.Fatal("single transaction capacity failure was not explicit", err)
			}
		})
	}
}

func TestLogCaptureBatchSplitCancellationAppliesAcknowledgedPrefix(t *testing.T) {
	r, baseline, raw, blobs, transactions := captureObjectFixture(t, 2)
	r.capacity.segmentObjects = 6000
	observed := baseline.ObservedAt.Add(time.Minute)
	store, err := OpenLogStore(filepath.Join(t.TempDir(), "journal"), baseline.Source)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	id, err := store.CommitBaseline(context.Background(), baseline, raw, observed)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	batch := newLogCaptureBatch(ctx, r, blobs, observed, func(raw []byte) (LogPoint, error) {
		calls++
		point, err := store.Append(context.Background(), id, raw, observed)
		cancel() // Publication is durable before cancellation reaches capture.
		return point, err
	})
	batch.limits.transactions = 2 // Split during a streaming prefix flush.
	if err := batch.consume(transactions[0]); err != nil {
		t.Fatal(err)
	}
	if err := batch.consume(transactions[1]); !errors.Is(err, context.Canceled) {
		t.Fatal("split ignored cancellation", err)
	}
	loaded, err := store.Load(context.Background(), id, observed)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || r.Point() != loaded.Point() || r.Point().Position != transactions[0].End || r.Point().ObservedAt == observed || len(r.archive.Data["resources"]) != 1 {
		t.Fatal("cancelled split did not apply exactly its acknowledged prefix")
	}
}
