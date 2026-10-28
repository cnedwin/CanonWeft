package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"novelstudio/internal/binlog"
)

func noopLogSegment(r *LogReplay, count int) LogSegment {
	s := LogSegment{Format: 1, BaselineSHA256: r.baseline.ArchiveSHA256, PreviousSHA256: r.point.Hash, Source: r.baseline.Source, Schema: r.baseline.Schema, From: r.point.Position, ObservedAt: r.point.ObservedAt.Add(time.Minute)}
	cursor := s.From
	for i := 0; i < count; i++ {
		end := binlog.Position{File: cursor.File, Offset: cursor.Offset + 100}
		s.Transactions = append(s.Transactions, binlog.Transaction{Start: cursor, End: end, CommittedAt: s.ObservedAt})
		cursor = end
	}
	s.Through = cursor
	return s
}

func TestLogReplayNoopReusesOnlyIdenticalValidatedState(t *testing.T) {
	r, _, _ := retentionLogFixture(t)
	s := noopLogSegment(r, 10)
	checks := 0
	data, refs, err := r.segmentRows(context.Background(), s, func(data Data, at time.Time) error {
		checks++
		_, err := r.materializationFiles(data, r.archive.Blobs, at)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	// Initial state, first transaction (including row normalization), and final
	// observation each require a full predicate. The other nine inputs are equal.
	if checks != 3 {
		t.Fatalf("identical empty transaction states repeated full validation: got %d calls, want 3", checks)
	}
	want, err := r.applyRows(r.archive.Data, s.From, s.Through, s.Transactions, s.ObservedAt)
	if err != nil {
		t.Fatal(err)
	}
	gotJSON, _ := json.Marshal(data)
	wantJSON, _ := json.Marshal(want)
	wantRefs, err := r.stateReferences(want, s.ObservedAt)
	if err != nil || !bytes.Equal(gotJSON, wantJSON) || len(refs) != len(wantRefs) {
		t.Fatal("empty transaction replay changed state or references", err)
	}
	for ref := range wantRefs {
		if !refs[ref] {
			t.Fatal("lost recovery object")
		}
	}
}

func TestLogReplayNoopStillValidatesEveryTransactionBoundary(t *testing.T) {
	r, _, _, _ := journalFixture(t)
	for name, mutate := range map[string]func(*LogSegment){
		"gap":               func(s *LogSegment) { s.Transactions[1].Start.Offset++ },
		"backwards":         func(s *LogSegment) { s.Transactions[1].End.Offset = s.Transactions[1].Start.Offset - 1 },
		"equal":             func(s *LogSegment) { s.Transactions[1].End = s.Transactions[1].Start },
		"overrun":           func(s *LogSegment) { s.Transactions[1].End.Offset = s.Through.Offset + 1 },
		"zero commit":       func(s *LogSegment) { s.Transactions[1].CommittedAt = time.Time{} },
		"foreign file":      func(s *LogSegment) { s.Transactions[1].Start.File = "other.000001" },
		"transaction limit": func(s *LogSegment) { *s = noopLogSegment(r, 1001) },
		"changed before image": func(s *LogSegment) {
			s.Transactions[1].Changes = []binlog.Change{{Table: "workspaces", Kind: "update", Before: logRow(strings.Repeat("a", 32), "wrong", "2026-10-25 12:00:00"), After: logRow(strings.Repeat("a", 32), "after", "2026-10-25 12:00:00")}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			s := noopLogSegment(r, 3)
			mutate(&s)
			if _, _, err := r.segmentRows(context.Background(), s, nil); err == nil {
				t.Fatal("invalid transaction accepted")
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	checks := 0
	_, _, err := r.segmentRows(ctx, noopLogSegment(r, 10), func(Data, time.Time) error {
		checks++
		if checks == 2 {
			cancel()
		}
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatal("empty transaction suffix ignored cancellation", err)
	}
}

func TestLogReplayNoopPreservesSortingChangesAndLegacyClocks(t *testing.T) {
	r, _, _, w := journalFixture(t)
	// The first empty transaction must still canonicalize an unsorted baseline.
	r.archive.Data["workspaces"] = append([]Row{Row(logRow(strings.Repeat("b", 32), "second", "2026-10-25 12:00:00"))}, r.archive.Data["workspaces"]...)
	s := noopLogSegment(r, 4)
	s.Transactions[2].Changes = []binlog.Change{{Table: "workspaces", Kind: "update", Before: logRow(w, "before", "2026-10-25 12:00:00"), After: logRow(w, "after", "2026-10-25 12:00:00")}}
	checks := 0
	data, _, err := r.segmentRows(context.Background(), s, func(Data, time.Time) error { checks++; return nil })
	if err != nil || checks != 4 || string(data["workspaces"][0][0]) != w || string(data["workspaces"][0][1]) != "after" {
		t.Fatal("first empty normalization or nonempty mutation skipped", checks, err)
	}
	if string(r.archive.Data["workspaces"][0][0]) != strings.Repeat("b", 32) || string(r.archive.Data["workspaces"][1][1]) != "before" {
		t.Fatal("replay mutated its baseline")
	}
	s = noopLogSegment(r, 4)
	for i := range s.Transactions {
		s.Transactions[i].CommittedAt = r.point.ObservedAt.Add(time.Duration(i+1) * time.Second)
	}
	checks = 0
	if _, _, err := r.segmentRows(context.Background(), s, func(Data, time.Time) error { checks++; return nil }); err != nil || checks != 6 {
		t.Fatal("different legacy recovery clocks skipped validation", checks, err)
	}
}
