package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"novelstudio/internal/binlog"
)

type cancelAfterJournalDelete struct {
	context.Context
	path string
}

type cancelAfterJournalPublish struct {
	context.Context
	path string
}

func (c *cancelAfterJournalPublish) Err() error {
	if _, err := os.Stat(c.path); err == nil {
		return context.Canceled
	}
	return c.Context.Err()
}
func TestLogStoreCrashBetweenPayloadAndReceiptRepairsOnlyValidatedChain(t *testing.T) {
	r, b, baseline, _ := journalFixture(t)
	root := filepath.Join(t.TempDir(), "journal")
	ctx := context.Background()
	s, err := OpenLogStore(root, b.Source)
	if err != nil {
		t.Fatal(err)
	}
	now := b.ObservedAt.Add(time.Minute)
	id, err := s.CommitBaseline(ctx, b, baseline, now)
	if err != nil {
		s.Close()
		t.Fatal(err)
	}
	raw, err := r.PrepareSegment(ctx, nil, b.Position, now, nil)
	if err != nil {
		s.Close()
		t.Fatal(err)
	}
	path := filepath.Join(root, id+"."+digest(raw)+".seg")
	stopped := &cancelAfterJournalPublish{Context: ctx, path: path}
	if _, err = s.Append(stopped, id, raw, now); !errors.Is(err, context.Canceled) {
		s.Close()
		t.Fatal("did not stop between payload and receipt", err)
	}
	if _, err = os.Stat(path + ".commit"); !os.IsNotExist(err) {
		s.Close()
		t.Fatal("receipt was published after cancellation", err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenLogStore(root, b.Source)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	loaded, err := s.Load(ctx, id, now)
	if err != nil || loaded.Point().Hash != digest(raw) {
		t.Fatal("valid orphan not recovered", err)
	}
	if _, err = os.Stat(path + ".commit"); err != nil {
		t.Fatal("recovered tail lacks durable receipt", err)
	}
	if err = os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Load(ctx, id, now); err == nil {
		t.Fatal("recovered tail loss went undetected")
	}
}

func (c *cancelAfterJournalDelete) Err() error {
	if _, err := os.Stat(c.path); os.IsNotExist(err) {
		return context.Canceled
	}
	return c.Context.Err()
}
func TestLogStorePruneCancellationPreservesUnfinishedOwner(t *testing.T) {
	r, b, baseline, _ := journalFixture(t)
	root := filepath.Join(t.TempDir(), "journal")
	ctx := context.Background()
	s, err := OpenLogStore(root, b.Source)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := b.ObservedAt.Add(time.Minute)
	id, err := s.CommitBaseline(ctx, b, baseline, now)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for i := 0; i < 2; i++ {
		raw, err := r.PrepareSegment(ctx, nil, b.Position, now.Add(time.Duration(i)*time.Second), nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = s.Append(ctx, id, raw, now.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		if err = r.ApplySegment(raw, digest(raw)); err != nil {
			t.Fatal(err)
		}
		names = append(names, id+"."+digest(raw)+".seg")
	}
	if names[0] > names[1] {
		names[0], names[1] = names[1], names[0]
	}
	stopped := &cancelAfterJournalDelete{Context: ctx, path: filepath.Join(root, names[0])}
	if err = s.Prune(stopped, b.ObservedAt.Add(ProtectionRetention)); !errors.Is(err, context.Canceled) {
		t.Fatal("delete cancellation was ignored", err)
	}
	if _, err = os.Stat(filepath.Join(root, id+".base")); err != nil {
		t.Fatal("cancelled incomplete owner lost baseline", err)
	}
	if _, err = os.Stat(filepath.Join(root, names[1])); err != nil {
		t.Fatal("cancelled sweep continued into next segment", err)
	}
}

func TestLogStoreRejectsForkAndDisconnectedSegment(t *testing.T) {
	for _, kind := range []string{"fork", "disconnected"} {
		t.Run(kind, func(t *testing.T) {
			r, b, baseline, _ := journalFixture(t)
			ctx := context.Background()
			root := filepath.Join(t.TempDir(), "journal")
			s, err := OpenLogStore(root, b.Source)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			now := b.ObservedAt.Add(time.Minute)
			id, err := s.CommitBaseline(ctx, b, baseline, now)
			if err != nil {
				t.Fatal(err)
			}
			segment, err := r.PrepareSegment(ctx, nil, b.Position, now, nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.Append(ctx, id, segment, now); err != nil {
				t.Fatal(err)
			}
			var tampered LogSegment
			if err = json.Unmarshal(segment, &tampered); err != nil {
				t.Fatal(err)
			}
			if kind == "fork" {
				tampered.ObservedAt = now.Add(time.Second)
			} else {
				tampered.PreviousSHA256 = strings.Repeat("f", 64)
			}
			raw, err := json.Marshal(tampered)
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(filepath.Join(root, id+"."+digest(raw)+".seg"), raw, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err = s.Load(ctx, id, now.Add(time.Second)); err == nil {
				t.Fatal("damaged history silently skipped")
			}
		})
	}
}

func TestLogStoreMissingCommittedTailNeverSilentlyRewinds(t *testing.T) {
	for _, kind := range []string{"last", "all"} {
		t.Run(kind, func(t *testing.T) {
			r, b, baseline, _ := journalFixture(t)
			ctx := context.Background()
			root := filepath.Join(t.TempDir(), "journal")
			s, err := OpenLogStore(root, b.Source)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			now := b.ObservedAt.Add(time.Minute)
			id, err := s.CommitBaseline(ctx, b, baseline, now)
			if err != nil {
				t.Fatal(err)
			}
			var names []string
			var last []byte
			for i := 0; i < 2; i++ {
				last, err = r.PrepareSegment(ctx, nil, b.Position, now.Add(time.Duration(i)*time.Second), nil)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = s.Append(ctx, id, last, now.Add(time.Second)); err != nil {
					t.Fatal(err)
				}
				if err = r.ApplySegment(last, digest(last)); err != nil {
					t.Fatal(err)
				}
				names = append(names, id+"."+digest(last)+".seg")
			}
			if kind == "last" {
				names = names[1:]
			}
			for _, name := range names {
				if err = os.Remove(filepath.Join(root, name)); err != nil {
					t.Fatal(err)
				}
			}
			if _, err = s.Load(ctx, id, now.Add(time.Second)); err == nil {
				t.Fatal("missing committed tail silently rewound")
			}
			if _, err = s.Archive(ctx, id, b.Position, now.Add(time.Second)); err == nil {
				t.Fatal("damaged committed history allowed archive")
			}
			if _, err = s.Append(ctx, id, last, now.Add(time.Second)); err == nil {
				t.Fatal("damaged committed history allowed append")
			}
		})
	}
}

func TestLogStoreCapacityReservesReceiptAndKeepsCommittedRecoveryUsable(t *testing.T) {
	r, b, baseline, _ := journalFixture(t)
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "journal")
	s, err := OpenLogStore(root, b.Source)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := b.ObservedAt.Add(time.Minute)
	id, err := s.CommitBaseline(ctx, b, baseline, now)
	if err != nil {
		t.Fatal(err)
	}
	// Three identity files + one baseline. The same production budget path is
	// scaled so one more payload fits but the former shared receipt gate did not.
	s.inventoryLimit = 7
	segment, err := r.PrepareSegment(ctx, nil, b.Position, now, nil)
	if err != nil {
		t.Fatal(err)
	}
	point, err := s.Append(ctx, id, segment, now)
	if err != nil {
		t.Fatal("published payload could not reserve its receipt", err)
	}
	if err = r.ApplySegment(segment, digest(segment)); err != nil {
		t.Fatal(err)
	}
	next, err := r.PrepareSegment(ctx, nil, b.Position, now.Add(time.Second), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Append(ctx, id, next, now.Add(time.Second)); err == nil {
		t.Fatal("inventory capacity not enforced before another payload")
	}
	loaded, err := s.Load(ctx, id, now.Add(time.Second))
	if err != nil || loaded.Point() != point {
		t.Fatal("capacity refusal broke prior committed recovery", err)
	}
	if _, err = s.Archive(ctx, id, point.Position, now.Add(time.Second)); err != nil {
		t.Fatal("capacity refusal blocked recovery export", err)
	}
}

func TestLogStoreCorruptOwnerDoesNotBlockOtherExpiryOrTemps(t *testing.T) {
	_, b, baseline, _ := journalFixture(t)
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "journal")
	s, err := OpenLogStore(root, b.Source)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	first, err := s.CommitBaseline(ctx, b, baseline, b.ObservedAt)
	if err != nil {
		t.Fatal(err)
	}
	secondBaseline := b
	secondBaseline.Position.Offset++
	second, err := s.CommitBaseline(ctx, secondBaseline, baseline, b.ObservedAt)
	if err != nil {
		t.Fatal(err)
	}
	// Directory iteration order cannot decide which unrelated owner is cleaned.
	bad, good := first, second
	if bad > good {
		bad, good = good, bad
	}
	if err = os.WriteFile(filepath.Join(root, bad+".base"), []byte("corrupt baseline"), 0600); err != nil {
		t.Fatal(err)
	}
	temp := filepath.Join(root, strings.Repeat("e", 64)+".base.tmp")
	if err = os.WriteFile(temp, []byte("interrupted"), 0600); err != nil {
		t.Fatal(err)
	}
	expired := b.ObservedAt.Add(ProtectionRetention)
	if err = os.Chtimes(temp, expired.Add(-25*time.Hour), expired.Add(-25*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err = s.Prune(ctx, expired); err == nil {
		t.Fatal("corruption was not reported")
	}
	if _, err = os.Stat(filepath.Join(root, good+".base")); !os.IsNotExist(err) {
		t.Fatal("bad owner blocked unrelated expiry", err)
	}
	if _, err = os.Stat(temp); !os.IsNotExist(err) {
		t.Fatal("bad owner blocked stale temporary cleanup", err)
	}
	if _, err = os.Stat(filepath.Join(root, bad+".base")); err != nil {
		t.Fatal("untrusted damaged baseline removed", err)
	}
}

func TestLogStoreCrashWriterChild(t *testing.T) {
	root := os.Getenv("STUDIO_TEST_LOG_CRASH_ROOT")
	if root == "" {
		return
	}
	r, b, baseline, _ := journalFixture(t)
	s, err := OpenLogStore(root, b.Source)
	if err != nil {
		t.Fatal(err)
	}
	now := b.ObservedAt.Add(time.Minute)
	id, err := s.CommitBaseline(context.Background(), b, baseline, now)
	if err != nil {
		t.Fatal(err)
	}
	segment, err := r.PrepareSegment(context.Background(), nil, b.Position, now, nil)
	if err != nil {
		t.Fatal(err)
	}
	point, err := s.Append(context.Background(), id, segment, now)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(os.Getenv("STUDIO_TEST_LOG_CRASH_SIGNAL"), []byte(id+":"+point.Hash), 0600); err != nil {
		t.Fatal(err)
	}
	// Deliberately skip every defer and Close after durable file publication.
	os.Exit(23)
}

func TestLogStoreProcessCrashReleasesLockAndRecoversPublishedPoint(t *testing.T) {
	_, b, _, _ := journalFixture(t)
	parent := t.TempDir()
	root, signal := filepath.Join(parent, "journal"), filepath.Join(parent, "committed")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, exe, "-test.run=^TestLogStoreCrashWriterChild$", "-test.timeout=12s")
	child.Env = append(os.Environ(), "STUDIO_TEST_LOG_CRASH_ROOT="+root, "STUDIO_TEST_LOG_CRASH_SIGNAL="+signal)
	if output, err := child.CombinedOutput(); err == nil || child.ProcessState == nil || child.ProcessState.ExitCode() != 23 {
		t.Fatalf("child did not exit at committed boundary: %v %s", err, output)
	}
	committed, err := os.ReadFile(signal)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(string(committed), ":")
	if len(parts) != 2 {
		t.Fatal("invalid child receipt")
	}
	s, err := OpenLogStore(root, b.Source)
	if err != nil {
		t.Fatal("crashed process retained repository lock", err)
	}
	defer s.Close()
	r, err := s.Load(context.Background(), parts[0], b.ObservedAt.Add(time.Minute))
	if err != nil || r.Point().Hash != parts[1] {
		t.Fatal("published point lost at process exit", err)
	}
}

func TestLogStoreRestartRetainsExactCommitAndIdempotentAppend(t *testing.T) {
	r, b, baseline, w := journalFixture(t)
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "journal")
	s, err := OpenLogStore(root, b.Source)
	if err != nil {
		t.Fatal(err)
	}
	now := b.ObservedAt.Add(3 * time.Second)
	id, err := s.CommitBaseline(ctx, b, baseline, now)
	if err != nil {
		t.Fatal(err)
	}
	end := binlog.Position{File: b.Position.File, Offset: 200}
	tx := binlog.Transaction{Start: b.Position, End: end, CommittedAt: now.Add(-time.Second), Changes: []binlog.Change{{Table: "workspaces", Kind: "update", Before: logRow(w, "before", "2026-10-25 12:00:00"), After: logRow(w, "durable", "2026-10-25 12:00:00")}}}
	segment, err := r.PrepareSegment(ctx, nil, end, now, []binlog.Transaction{tx})
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.Append(ctx, id, segment, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenLogStore(root, b.Source)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	loaded, err := s.Load(ctx, id, now)
	if err != nil || loaded.Point() != first {
		t.Fatal("durable cursor not recovered", err)
	}
	if retry, err := s.Append(ctx, id, segment, now); err != nil || retry != first {
		t.Fatal("committed retry not idempotent", err)
	}
	raw, err := s.Archive(ctx, id, end, now)
	if err != nil {
		t.Fatal(err)
	}
	a, err := ValidateArchive(bytes.NewReader(raw), int64(len(raw)))
	if err != nil || string(a.Data["workspaces"][0][1]) != "durable" {
		t.Fatal("wrong selected content", err)
	}
	old, err := s.Archive(ctx, id, b.Position, now)
	if err != nil {
		t.Fatal(err)
	}
	a, err = ValidateArchive(bytes.NewReader(old), int64(len(old)))
	if err != nil || string(a.Data["workspaces"][0][1]) != "before" {
		t.Fatal("baseline not immutable", err)
	}
}

func TestLogStoreInterruptedPayloadIsInvisibleAndRecoveryDetectsTampering(t *testing.T) {
	r, b, baseline, _ := journalFixture(t)
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "journal")
	s, err := OpenLogStore(root, b.Source)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := b.ObservedAt.Add(time.Minute)
	id, err := s.CommitBaseline(ctx, b, baseline, now)
	if err != nil {
		t.Fatal(err)
	}
	segment, err := r.PrepareSegment(ctx, nil, b.Position, now, nil)
	if err != nil {
		t.Fatal(err)
	}
	name := id + "." + digest(segment) + ".seg"
	if err := os.WriteFile(filepath.Join(root, name+".tmp"), []byte("interrupted partial write"), 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := s.Load(ctx, id, now)
	if err != nil || loaded.Point().Hash != b.ArchiveSHA256 {
		t.Fatal("uncommitted bytes became visible", err)
	}
	if _, err := s.Append(ctx, id, segment, now); err != nil {
		t.Fatal("same payload retry cannot replace partial temporary", err)
	}
	if err := os.WriteFile(filepath.Join(root, name), []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Load(ctx, id, now); err == nil {
		t.Fatal("corrupt durable segment accepted")
	}
}

func TestLogStoreIsolationAndHardExpiryIncludeLastBaseline(t *testing.T) {
	_, b, baseline, _ := journalFixture(t)
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "journal")
	s, err := OpenLogStore(root, b.Source)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if duplicate, err := OpenLogStore(root, b.Source); err == nil {
		duplicate.Close()
		t.Fatal("second writer accepted")
	}
	id, err := s.CommitBaseline(ctx, b, baseline, b.ObservedAt)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "leave-me.txt"), []byte("unowned"), 0600); err != nil {
		t.Fatal(err)
	}
	expired := b.ObservedAt.Add(ProtectionRetention)
	if _, err := s.Load(ctx, id, expired); err == nil {
		t.Fatal("expired last baseline still usable")
	}
	if err := s.Prune(ctx, expired); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, id+".base")); !os.IsNotExist(err) {
		t.Fatal("expired baseline not removed", err)
	}
	if _, err := os.Stat(filepath.Join(root, "leave-me.txt")); err != nil {
		t.Fatal("unowned file removed", err)
	}
	if _, err := s.CommitBaseline(ctx, b, baseline, expired); err == nil {
		t.Fatal("copy refreshed expired baseline")
	}
	if _, err := s.Load(ctx, strings.Repeat("../", 22), expired); err == nil {
		t.Fatal("unsafe identifier accepted")
	}
	other := filepath.Join(t.TempDir(), "snapshots")
	p, err := OpenProtection(other, b.Source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(other, "checkpoint.json"), []byte("existing snapshot"), 0600); err != nil {
		t.Fatal(err)
	}
	p.Close()
	if mixed, err := OpenLogStore(other, b.Source); err == nil {
		mixed.Close()
		t.Fatal("snapshot repository repurposed")
	}
}
