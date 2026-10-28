package backup

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"novelstudio/internal/binlog"
)

// observedErrContext preserves a real cancellation context. Its second Err
// call pauses after observing the underlying error, before returning it. This
// models a scheduler pause between an ordinary Err read and its return: real
// cancellation may happen meanwhile, so that one already-observed nil is stale.
// Every later Err call observes the underlying cancellation normally.
type observedErrContext struct {
	context.Context
	calls   atomic.Int32
	reached chan struct{}
	release chan struct{}
}

func (c *observedErrContext) Err() error {
	err := c.Context.Err()
	if c.calls.Add(1) == 2 {
		close(c.reached)
		<-c.release
	}
	return err
}

func TestPrepareSegmentCancellationAfterObservedCheck(t *testing.T) {
	replay, baseline, _, _ := journalFixture(t)
	before := replay.Point()
	position := baseline.Position
	var transactions []binlog.Transaction
	for i := 0; i < 3; i++ {
		next := position
		next.Offset++
		transactions = append(transactions, binlog.Transaction{
			Start:       position,
			End:         next,
			CommittedAt: baseline.ObservedAt.Add(time.Duration(i+1) * time.Second),
		})
		position = next
	}

	underlying, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := &observedErrContext{
		Context: underlying,
		reached: make(chan struct{}),
		release: make(chan struct{}),
	}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(ctx.release) }) }
	defer release()
	type preparation struct {
		raw []byte
		err error
	}
	done := make(chan preparation, 1)
	go func() {
		raw, err := replay.PrepareSegment(ctx, nil, position, baseline.ObservedAt.Add(time.Minute), transactions)
		done <- preparation{raw: raw, err: err}
	}()

	// This timeout detects a broken test handshake; it never schedules cancel.
	select {
	case <-ctx.reached:
	case result := <-done:
		t.Fatalf("preparation returned before cancellation barrier: %v", result.err)
	case <-time.After(10 * time.Second):
		t.Fatal("preparation did not reach cancellation barrier")
	}
	cancel()
	<-underlying.Done()
	release()

	select {
	case result := <-done:
		if replay.Point() != before {
			t.Fatal("cancelled preparation advanced the recovery point")
		}
		if !errors.Is(result.err, context.Canceled) {
			t.Fatalf("cancellation during final validation ignored: want context.Canceled, got %v (prepared bytes=%d)", result.err, len(result.raw))
		}
		if len(result.raw) != 0 {
			t.Fatal("cancelled preparation returned a publishable segment")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("preparation did not return after cancellation")
	}
}

func TestLogCaptureBatchRejectsPublisherMutationAfterAcknowledgement(t *testing.T) {
	replay, baseline, archive, blobs, transactions := captureObjectFixture(t, 1)
	before := replay.Point()
	observed := baseline.ObservedAt.Add(time.Minute)
	store, err := OpenLogStore(filepath.Join(t.TempDir(), "journal"), baseline.Source)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	id, err := store.CommitBaseline(context.Background(), baseline, archive, observed)
	if err != nil {
		t.Fatal(err)
	}

	var acknowledged LogPoint
	publications := 0
	batch := newLogCaptureBatch(context.Background(), replay, blobs, observed, func(raw []byte) (LogPoint, error) {
		point, err := store.Append(context.Background(), id, raw, observed)
		if err != nil {
			return point, err
		}
		publications++
		acknowledged = point
		// The actual durable payload and receipt already exist. Changing the
		// caller-owned bytes must invalidate its private prepared state, not
		// silently install state from an acknowledgement of different bytes.
		raw[0] ^= 1
		return point, nil
	})
	batch.limits.transactions = 1
	err = batch.consume(transactions[0])
	if err == nil || !strings.Contains(err.Error(), "prepared log segment changed") {
		t.Fatalf("publisher mutation was not rejected by prepared-state binding: %v", err)
	}
	if publications != 1 || replay.Point() != before || len(replay.archive.Data["resources"]) != 0 {
		t.Fatal("changed publication advanced in-memory recovery state")
	}
	loaded, err := store.Load(context.Background(), id, observed)
	if err != nil {
		t.Fatal("original acknowledged payload could not be read from the repository", err)
	}
	if loaded.Point() != acknowledged || loaded.Point().Position != transactions[0].End || len(loaded.archive.Data["resources"]) != 1 || len(loaded.archive.Blobs) != 1 {
		t.Fatal("durable acknowledged state was lost after rejecting mutated callback bytes")
	}
}
