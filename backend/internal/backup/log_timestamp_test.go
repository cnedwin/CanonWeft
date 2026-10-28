package backup

import (
	"bytes"
	"context"
	"testing"
	"time"

	"novelstudio/internal/binlog"
)

func TestLogJournalSecondPrecisionCannotBackdateRecovery(t *testing.T) {
	r, b, _, _ := journalFixture(t)
	// MySQL's XID event header has second precision. A commit following the
	// microsecond baseline can therefore carry an apparently earlier time.
	b.ObservedAt = b.ObservedAt.Truncate(time.Second).Add(500 * time.Millisecond)
	r.point.ObservedAt = b.ObservedAt
	middle := binlog.Position{File: b.Position.File, Offset: 200}
	end := binlog.Position{File: b.Position.File, Offset: 300}
	txs := []binlog.Transaction{
		{Start: b.Position, End: middle, CommittedAt: b.ObservedAt.Truncate(time.Second)},
		{Start: middle, End: end, CommittedAt: b.ObservedAt.Add(time.Second).Truncate(time.Second)},
	}
	raw, err := r.PrepareSegment(context.Background(), nil, end, b.ObservedAt.Add(2*time.Second), txs)
	if err != nil {
		t.Fatal(err)
	}
	selected, err := r.ArchiveAt(raw, digest(raw), middle)
	if err != nil {
		t.Fatal(err)
	}
	a, err := ValidateArchive(bytes.NewReader(selected), int64(len(selected)))
	if err != nil {
		t.Fatal(err)
	}
	if a.Manifest.SnapshotAt.Before(b.ObservedAt) {
		t.Fatal("later log position was backdated before the already observed baseline")
	}
}

func TestLogJournalObservationCeilingAndThroughHaveOneTimestamp(t *testing.T) {
	for _, tail := range []bool{false, true} {
		t.Run(map[bool]string{false: "last commit", true: "metadata tail"}[tail], func(t *testing.T) {
			r, b, _, _ := journalFixture(t)
			commit := binlog.Position{File: b.Position.File, Offset: 200}
			through := commit
			if tail {
				through.Offset = 300
			}
			observed := b.ObservedAt.Add(time.Second)
			tx := binlog.Transaction{Start: b.Position, End: commit, CommittedAt: observed.Add(time.Second)}
			raw, err := r.PrepareSegment(context.Background(), nil, through, observed, []binlog.Transaction{tx})
			if err != nil {
				t.Fatal(err)
			}
			selected, err := r.ArchiveAt(raw, digest(raw), commit)
			if err != nil {
				t.Fatal(err)
			}
			a, err := ValidateArchive(bytes.NewReader(selected), int64(len(selected)))
			if err != nil {
				t.Fatal(err)
			}
			if a.Manifest.SnapshotAt.After(observed) {
				t.Fatal("commit label exceeds observed recovery time")
			}
			selected, err = r.ArchiveAt(raw, digest(raw), through)
			if err != nil {
				t.Fatal(err)
			}
			a, err = ValidateArchive(bytes.NewReader(selected), int64(len(selected)))
			if err != nil {
				t.Fatal(err)
			}
			if err = r.ApplySegment(raw, digest(raw)); err != nil {
				t.Fatal(err)
			}
			final, err := r.ArchiveBytes()
			if err != nil {
				t.Fatal(err)
			}
			last, err := ValidateArchive(bytes.NewReader(final), int64(len(final)))
			if err != nil {
				t.Fatal(err)
			}
			if !a.Manifest.SnapshotAt.Equal(last.Manifest.SnapshotAt) || !last.Manifest.SnapshotAt.Equal(observed) {
				t.Fatal("same through position has inconsistent timestamp")
			}
		})
	}
}
