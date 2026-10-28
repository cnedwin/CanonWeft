package backup

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"novelstudio/internal/binlog"
)

func TestLogCaptureBatchPublishesContiguousThousandTransactionPrefixes(t *testing.T) {
	r, b, rawBaseline, _ := journalFixture(t)
	durable, err := OpenLogReplay(b, rawBaseline)
	if err != nil {
		t.Fatal(err)
	}
	var segments []LogSegment
	observed := b.ObservedAt.Add(time.Hour)
	batch := newLogCaptureBatch(context.Background(), r, nil, observed, func(raw []byte) (LogPoint, error) {
		var segment LogSegment
		if err := json.Unmarshal(raw, &segment); err != nil {
			t.Fatal(err)
		}
		if r.Point() != durable.Point() {
			t.Fatal("memory advanced before publication")
		}
		if err := durable.ApplySegment(raw, digest(raw)); err != nil {
			return LogPoint{}, err
		}
		segments = append(segments, segment)
		return durable.Point(), nil
	})
	position := b.Position
	for i := 0; i < 2001; i++ {
		next := position
		next.Offset++
		tx := binlog.Transaction{Start: position, End: next, CommittedAt: b.ObservedAt.Add(time.Duration(i+1) * time.Second)}
		if err := batch.consume(tx); err != nil {
			t.Fatal(err)
		}
		position = next
	}
	if len(segments) != 2 || r.Point().Position.Offset != b.Position.Offset+2000 {
		t.Fatal("empty transactions were not bounded and durably published")
	}
	if segments[0].ObservedAt != b.ObservedAt.Add(1000*time.Second) || segments[0].ObservedAt == observed {
		t.Fatal("prefix falsely claimed final observation")
	}
	point, err := batch.finish(position)
	if err != nil || point != durable.Point() || point.Position != position || point.ObservedAt != observed || len(segments) != 3 {
		t.Fatal("final coverage or continuity lost", err)
	}
	for _, segment := range segments {
		if len(segment.Transactions) > 1000 {
			t.Fatal("transaction cap bypassed")
		}
	}
}

func TestLogCaptureBatchFailureAndCancellationKeepOnlyAcknowledgedPrefix(t *testing.T) {
	for _, kind := range []string{"publish failure", "cancel", "receipt mismatch"} {
		t.Run(kind, func(t *testing.T) {
			r, b, rawBaseline, _ := journalFixture(t)
			durable, err := OpenLogReplay(b, rawBaseline)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			batch := newLogCaptureBatch(ctx, r, nil, b.ObservedAt.Add(time.Minute), func(raw []byte) (LogPoint, error) {
				calls++
				if calls == 2 {
					if kind == "receipt mismatch" {
						return LogPoint{}, nil
					}
					return LogPoint{}, errors.New("publication failed")
				}
				if err := durable.ApplySegment(raw, digest(raw)); err != nil {
					return LogPoint{}, err
				}
				return durable.Point(), nil
			})
			batch.limits.transactions = 2
			position := b.Position
			for i := 0; i < 2; i++ {
				next := position
				next.Offset++
				if err := batch.consume(binlog.Transaction{Start: position, End: next, CommittedAt: b.ObservedAt.Add(time.Second)}); err != nil {
					t.Fatal(err)
				}
				position = next
			}
			prefix := r.Point()
			if kind == "cancel" {
				cancel()
			}
			next := position
			next.Offset++
			err = batch.consume(binlog.Transaction{Start: position, End: next, CommittedAt: b.ObservedAt.Add(2 * time.Second)})
			if kind == "cancel" {
				if !errors.Is(err, context.Canceled) {
					t.Fatal("cancel ignored", err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				_, err = batch.finish(next)
				if err == nil {
					t.Fatal("failed or mismatched publication accepted")
				}
			}
			if r.Point() != prefix || r.Point() != durable.Point() || r.Point().ObservedAt == batch.observed {
				t.Fatal("unpublished tail advanced capture")
			}
		})
	}
}

func TestLogCaptureBatchBoundsRowsBytesAndKeepsCoarseTimeMonotonic(t *testing.T) {
	for _, kind := range []string{"rows", "bytes", "oversized", "future commit time"} {
		t.Run(kind, func(t *testing.T) {
			r, b, rawBaseline, w := journalFixture(t)
			durable, err := OpenLogReplay(b, rawBaseline)
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			batch := newLogCaptureBatch(context.Background(), r, nil, b.ObservedAt.Add(time.Second), func(raw []byte) (LogPoint, error) {
				calls++
				if err := durable.ApplySegment(raw, digest(raw)); err != nil {
					return LogPoint{}, err
				}
				return durable.Point(), nil
			})
			tx := binlog.Transaction{Start: b.Position, End: binlog.Position{File: b.Position.File, Offset: b.Position.Offset + 1}, CommittedAt: b.ObservedAt.Truncate(time.Second), Changes: []binlog.Change{{Table: "workspaces", Kind: "update", Before: logRow(w, "before", "2026-10-25 12:00:00"), After: logRow(w, "after", "2026-10-25 12:00:00")}}}
			if kind == "future commit time" {
				tx.CommittedAt = b.ObservedAt.Add(10 * time.Second)
				batch.limits.rows = 1
			} else if kind == "rows" {
				batch.limits.rows = 1
			} else if kind == "bytes" {
				// Two three-cell images: 2*32 ID bytes + 6+5 name bytes +
				// 2*19 timestamp bytes + 6*32 allowance = 305.
				batch.limits.bytes = 305
			} else {
				batch.limits.bytes = 1
			}
			err = batch.consume(tx)
			if kind == "oversized" {
				if err == nil || calls != 0 || r.Point().Position != b.Position {
					t.Fatal("oversized transaction published", err)
				}
				return
			}
			wantObservation := b.ObservedAt
			if kind == "future commit time" {
				wantObservation = batch.observed
			}
			if err != nil || calls != 1 || r.Point().ObservedAt != wantObservation {
				t.Fatal("row/byte split or coarse timestamp clamp failed", err, calls)
			}
			if _, err := batch.finish(tx.End); err != nil {
				t.Fatal(err)
			}
			wantCalls := 2
			if kind == "future commit time" {
				wantCalls = 1
			}
			if calls != wantCalls || r.Point().ObservedAt != batch.observed {
				t.Fatal("successful range did not certify final observation")
			}
		})
	}
}

func TestLogCaptureBatchFlushesBeforeNextTransactionWouldOverflow(t *testing.T) {
	r, b, rawBaseline, w := journalFixture(t)
	durable, err := OpenLogReplay(b, rawBaseline)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	batch := newLogCaptureBatch(context.Background(), r, nil, b.ObservedAt.Add(time.Minute), func(raw []byte) (LogPoint, error) {
		calls++
		if err := durable.ApplySegment(raw, digest(raw)); err != nil {
			return LogPoint{}, err
		}
		return durable.Point(), nil
	})
	batch.limits.bytes = 500 // Each update is 305 bytes; two cannot share a segment.
	before, after := logRow(w, "before", "2026-10-25 12:00:00"), logRow(w, "after", "2026-10-25 12:00:00")
	middle, end := b.Position, b.Position
	middle.Offset++
	end.Offset += 2
	first := binlog.Transaction{Start: b.Position, End: middle, CommittedAt: b.ObservedAt.Add(time.Second), Changes: []binlog.Change{{Table: "workspaces", Kind: "update", Before: before, After: after}}}
	second := binlog.Transaction{Start: middle, End: end, CommittedAt: b.ObservedAt.Add(2 * time.Second), Changes: []binlog.Change{{Table: "workspaces", Kind: "update", Before: after, After: before}}}
	if err := batch.consume(first); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatal("small first transaction flushed prematurely")
	}
	if err := batch.consume(second); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || r.Point().Position != middle {
		t.Fatal("overflow did not publish only previous complete transaction")
	}
	if _, err := batch.finish(end); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || r.Point().Position != end {
		t.Fatal("overflowing next transaction lost")
	}
}

func TestLogCaptureBatchExcludedRowsStillConsumeTransactionBudget(t *testing.T) {
	r, b, rawBaseline, _ := journalFixture(t)
	durable, err := OpenLogReplay(b, rawBaseline)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	batch := newLogCaptureBatch(context.Background(), r, nil, b.ObservedAt.Add(time.Minute), func(raw []byte) (LogPoint, error) {
		calls++
		var segment LogSegment
		if err := json.Unmarshal(raw, &segment); err != nil {
			t.Fatal(err)
		}
		for _, tx := range segment.Transactions {
			if len(tx.Changes) != 0 {
				t.Fatal("excluded session persisted")
			}
		}
		if err := durable.ApplySegment(raw, digest(raw)); err != nil {
			return LogPoint{}, err
		}
		return durable.Point(), nil
	})
	batch.limits.transactions = 2
	position := b.Position
	for i := 0; i < 3; i++ {
		next := position
		next.Offset++
		tx := binlog.Transaction{Start: position, End: next, CommittedAt: b.ObservedAt.Add(time.Second), Changes: []binlog.Change{{Table: "sessions", Kind: "insert", After: logRow("PRIVATE_SESSION", "user", "2026-10-26 00:00:00")}}}
		if err := batch.consume(tx); err != nil {
			t.Fatal(err)
		}
		position = next
	}
	if calls != 1 {
		t.Fatal("excluded transactions bypassed count budget")
	}
	if _, err := batch.finish(position); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || r.Point().Position != position {
		t.Fatal("excluded transaction coverage lost")
	}
}
