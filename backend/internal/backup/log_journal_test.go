package backup

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"novelstudio/internal/binlog"
	"novelstudio/internal/storage"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func journalFixture(t *testing.T) (*LogReplay, LogBaseline, []byte, string) {
	t.Helper()
	a := emptyArchive(t)
	w := strings.Repeat("a", 32)
	a.Data["workspaces"] = []Row{Row(logRow(w, "before", "2026-10-25 12:00:00"))}
	raw := archiveBytes(t, a)
	b := LogBaseline{Format: 1, ArchiveSHA256: digest(raw), Source: a.Manifest.Source, Schema: supportedSchemaVersion, ObservedAt: a.Manifest.SnapshotAt, Position: binlog.Position{File: "binlog.000001", Offset: 100}, Tables: rowContracts()}
	r, err := OpenLogReplay(b, raw)
	if err != nil {
		t.Fatal(err)
	}
	return r, b, raw, w
}

func TestLogJournalHashChainAndMiddleCommitArchive(t *testing.T) {
	r, b, raw, w := journalFixture(t)
	middle, end := binlog.Position{File: "binlog.000001", Offset: 200}, binlog.Position{File: "binlog.000001", Offset: 300}
	stamp := b.ObservedAt.Add(time.Second)
	first := binlog.Transaction{Start: b.Position, End: middle, CommittedAt: stamp, Changes: []binlog.Change{{Table: "workspaces", Kind: "update", Before: logRow(w, "before", "2026-10-25 12:00:00"), After: logRow(w, "middle", "2026-10-25 12:00:00")}}}
	second := binlog.Transaction{Start: middle, End: end, CommittedAt: stamp.Add(time.Second), Changes: []binlog.Change{{Table: "workspaces", Kind: "update", Before: logRow(w, "middle", "2026-10-25 12:00:00"), After: logRow(w, "after", "2026-10-25 12:00:00")}}}
	segment, err := r.PrepareSegment(context.Background(), nil, end, stamp.Add(3*time.Second), []binlog.Transaction{first, second})
	if err != nil {
		t.Fatal("segment preparation failed", err)
	}
	if r.Point().Position != b.Position {
		t.Fatal("preparation advanced durable replay")
	}
	selected, err := r.ArchiveAt(segment, digest(segment), middle)
	if err != nil {
		t.Fatal("middle commit unavailable", err)
	}
	a, err := ValidateArchive(bytes.NewReader(selected), int64(len(selected)))
	if err != nil || string(a.Data["workspaces"][0][1]) != "middle" {
		t.Fatal("selected recovery point includes later commit", err)
	}
	if err := r.ApplySegment(segment, digest(segment)); err != nil {
		t.Fatal(err)
	}
	if r.Point().Position != end || r.Point().Hash != digest(segment) {
		t.Fatal("applied segment did not advance verified cursor")
	}
	if err := r.ApplySegment(segment, digest(segment)); err == nil {
		t.Fatal("duplicate segment replay accepted")
	}
	if r.Point().Position != end {
		t.Fatal("failed replay changed verified cursor")
	}
	base, err := ValidateArchive(bytes.NewReader(raw), int64(len(raw)))
	if err != nil || string(base.Data["workspaces"][0][1]) != "before" {
		t.Fatal("immutable baseline changed", err)
	}
}

func TestLogJournalRefusesCorruptHashScopeAndGapWithoutAdvancing(t *testing.T) {
	r, b, _, w := journalFixture(t)
	end := binlog.Position{File: b.Position.File, Offset: 200}
	tx := binlog.Transaction{Start: b.Position, End: end, CommittedAt: b.ObservedAt.Add(time.Second), Changes: []binlog.Change{{Table: "workspaces", Kind: "update", Before: logRow(w, "before", "2026-10-25 12:00:00"), After: logRow(w, "after", "2026-10-25 12:00:00")}}}
	raw, err := r.PrepareSegment(context.Background(), nil, end, b.ObservedAt.Add(2*time.Second), []binlog.Transaction{tx})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.ApplySegment(raw, strings.Repeat("0", 64)); err == nil {
		t.Fatal("corrupt segment hash accepted")
	}
	for _, mutate := range []func(*LogSegment){
		func(s *LogSegment) { s.PreviousSHA256 = strings.Repeat("0", 64) },
		func(s *LogSegment) { s.Source.Database = "other_database" },
		func(s *LogSegment) { s.From.Offset++ },
		func(s *LogSegment) { s.Transactions[0].Changes[0].Before[1] = []byte("different") },
		func(s *LogSegment) {
			s.Objects = map[string][]byte{"blobs/unsafe/private": []byte("unreferenced private marker")}
		},
	} {
		var segment LogSegment
		if err := json.Unmarshal(raw, &segment); err != nil {
			t.Fatal(err)
		}
		mutate(&segment)
		changed, err := json.Marshal(segment)
		if err != nil {
			t.Fatal(err)
		}
		if err := r.ApplySegment(changed, digest(changed)); err == nil {
			t.Fatal("invalid log chain accepted")
		}
		if r.Point().Position != b.Position || r.Point().Hash != b.ArchiveSHA256 {
			t.Fatal("rejected segment changed recovery state")
		}
	}
}

func TestLogJournalRetainsIntermediateObjectsButExportsOnlySelectedReferences(t *testing.T) {
	a := emptyArchive(t)
	p, w, branch, resource := strings.Repeat("a", 32), strings.Repeat("b", 32), strings.Repeat("c", 32), strings.Repeat("d", 32)
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
	ref, err := blobs.PutObject(context.Background(), p, strings.NewReader("中途存在而后删除的正文"))
	if err != nil {
		t.Fatal(err)
	}
	rows := emptyArchive(t)
	testRow(rows, "resources", map[string]string{"id": resource, "project_id": p, "branch_id": branch, "name": "正文", "kind": "document", "content_hash": ref.Hash, "revision": "1"})
	row := binlog.Row(rows.Data["resources"][0])
	middle, end := binlog.Position{File: b.Position.File, Offset: 200}, binlog.Position{File: b.Position.File, Offset: 300}
	first := binlog.Transaction{Start: b.Position, End: middle, CommittedAt: b.ObservedAt.Add(time.Second), Changes: []binlog.Change{{Table: "resources", Kind: "insert", After: row}}}
	second := binlog.Transaction{Start: middle, End: end, CommittedAt: b.ObservedAt.Add(2 * time.Second), Changes: []binlog.Change{{Table: "resources", Kind: "delete", Before: row}}}
	segment, err := r.PrepareSegment(context.Background(), blobs, end, b.ObservedAt.Add(3*time.Second), []binlog.Transaction{first, second})
	if err != nil {
		t.Fatal(err)
	}
	var decoded LogSegment
	if err := json.Unmarshal(segment, &decoded); err != nil || len(decoded.Objects) != 1 {
		t.Fatal("intermediate recovery object not retained", err)
	}
	selected, err := r.ArchiveAt(segment, digest(segment), middle)
	if err != nil {
		t.Fatal(err)
	}
	mid, err := ValidateArchive(bytes.NewReader(selected), int64(len(selected)))
	if err != nil || len(mid.Blobs) != 1 || len(mid.Data["resources"]) != 1 {
		t.Fatal("intermediate object missing in selected archive", err)
	}
	if err := r.ApplySegment(segment, digest(segment)); err != nil {
		t.Fatal(err)
	}
	final, err := r.ArchiveBytes()
	if err != nil {
		t.Fatal(err)
	}
	last, err := ValidateArchive(bytes.NewReader(final), int64(len(final)))
	if err != nil || len(last.Blobs) != 0 || len(last.Data["resources"]) != 0 {
		t.Fatal("later deletion did not exclude old content from selected export", err)
	}
	// Each new state fits one object plus data/manifest, but retained history
	// must also be bounded even when an additional object's body is empty.
	r.capacity.entries = 3
	empty, err := blobs.PutObject(context.Background(), p, strings.NewReader(""))
	if err != nil {
		t.Fatal(err)
	}
	rows = emptyArchive(t)
	testRow(rows, "resources", map[string]string{"id": resource, "project_id": p, "branch_id": branch, "name": "空附件", "kind": "document", "content_hash": empty.Hash, "revision": "1"})
	row = binlog.Row(rows.Data["resources"][0])
	nextMiddle, nextEnd := binlog.Position{File: b.Position.File, Offset: 400}, binlog.Position{File: b.Position.File, Offset: 500}
	nextFirst := binlog.Transaction{Start: end, End: nextMiddle, CommittedAt: b.ObservedAt.Add(4 * time.Second), Changes: []binlog.Change{{Table: "resources", Kind: "insert", After: row}}}
	nextSecond := binlog.Transaction{Start: nextMiddle, End: nextEnd, CommittedAt: b.ObservedAt.Add(5 * time.Second), Changes: []binlog.Change{{Table: "resources", Kind: "delete", Before: row}}}
	point := r.Point()
	if _, err := r.PrepareSegment(context.Background(), blobs, nextEnd, b.ObservedAt.Add(6*time.Second), []binlog.Transaction{nextFirst, nextSecond}); err == nil {
		t.Fatal("retained historical object entry capacity was not enforced")
	}
	crafted := LogSegment{Format: 1, BaselineSHA256: b.ArchiveSHA256, PreviousSHA256: point.Hash, Source: b.Source, Schema: b.Schema, From: end, Through: nextEnd, ObservedAt: b.ObservedAt.Add(6 * time.Second), Transactions: []binlog.Transaction{nextFirst, nextSecond}, Objects: map[string][]byte{"blobs/" + p + "/" + empty.Hash: {}}}
	encoded, err := json.Marshal(crafted)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.ApplySegment(encoded, digest(encoded)); err == nil || r.Point() != point {
		t.Fatal("history capacity bypassed or advanced rejected point", err)
	}
}

func TestLogJournalCompleteCapacityBudgetsAreIndependent(t *testing.T) {
	for _, kind := range []string{"archive", "manifest", "entries"} {
		t.Run(kind, func(t *testing.T) {
			r, b, _, _ := journalFixture(t)
			good, err := r.PrepareSegment(context.Background(), nil, b.Position, b.ObservedAt.Add(time.Second), nil)
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "archive":
				r.capacity.archive = 1
			case "manifest":
				r.capacity.manifest = 1
			case "entries":
				r.capacity.entries = 1
			}
			point := r.Point()
			if _, err := r.PrepareSegment(context.Background(), nil, b.Position, b.ObservedAt.Add(time.Second), nil); err == nil {
				t.Fatal("complete capacity not checked during preparation")
			}
			if err := r.ApplySegment(good, digest(good)); err == nil || point != r.Point() {
				t.Fatal("complete capacity bypassed during replay", err)
			}
		})
	}
}

func TestLogJournalDoesNotSerializeSecretsAndRefusesDuplicateJSONKeys(t *testing.T) {
	r, b, _, w := journalFixture(t)
	end := binlog.Position{File: b.Position.File, Offset: 200}
	tx := binlog.Transaction{Start: b.Position, End: end, CommittedAt: b.ObservedAt.Add(time.Second), Changes: []binlog.Change{
		{Table: "users", Kind: "insert", After: logRow(strings.Repeat("b", 32), w, "owner", "PRIVATE_PASSWORD", "owner", "1", "2026-10-25 12:00:00")},
		{Table: "sessions", Kind: "insert", After: logRow("PRIVATE_SESSION", strings.Repeat("b", 32), "2026-10-26 12:00:00")},
	}}
	raw, err := r.PrepareSegment(context.Background(), nil, end, b.ObservedAt.Add(2*time.Second), []binlog.Transaction{tx})
	if err != nil {
		t.Fatal(err)
	}
	var segment LogSegment
	if err := json.Unmarshal(raw, &segment); err != nil {
		t.Fatal(err)
	}
	if len(segment.Transactions[0].Changes) != 1 || len(segment.Transactions[0].Changes[0].After[3]) != 0 || string(segment.Transactions[0].Changes[0].After[5]) != "0" {
		t.Fatal("password/session/authority persisted")
	}
	duplicate := append([]byte(`{"format":0,`), raw[1:]...)
	if err := r.ApplySegment(duplicate, digest(duplicate)); err == nil {
		t.Fatal("duplicate JSON keys bypassed log validation")
	}
	if _, err := r.ArchiveAt(raw, digest(raw), binlog.Position{File: end.File, Offset: end.Offset - 1}); err == nil {
		t.Fatal("mid-event position was accepted for restoration")
	}
	if err := r.ApplySegment(raw, digest(raw)); err != nil {
		t.Fatal("safe journal refused", err)
	}
}

func TestLogJournalCaptureRejectsReaderForDifferentSourceBeforeAnyIO(t *testing.T) {
	r, _, _, _ := journalFixture(t)
	reader, err := binlog.NewReader("reader:unused@tcp(127.0.0.1:1)/", "22222222-2222-2222-2222-222222222222")
	if err != nil {
		t.Fatal(err)
	}
	// No opened DB is necessary: mismatched source must fail before SQL IO.
	_, err = r.CaptureSegment(context.Background(), new(sql.DB), reader, nil)
	if err == nil || err.Error() != "log reader belongs to a different source" {
		t.Fatal("reader identity was not bound before IO", err)
	}
}

func TestLogJournalCumulativeAndIntermediateStatesMustRemainMaterializable(t *testing.T) {
	r, b, _, w := journalFixture(t)
	data, _ := json.Marshal(r.archive.Data)
	r.capacity.data = int64(len(data) + 200)
	middle, end := binlog.Position{File: b.Position.File, Offset: 200}, binlog.Position{File: b.Position.File, Offset: 300}
	first := binlog.Transaction{Start: b.Position, End: middle, CommittedAt: b.ObservedAt.Add(time.Second), Changes: []binlog.Change{{Table: "workspaces", Kind: "update", Before: logRow(w, "before", "2026-10-25 12:00:00"), After: logRow(w, strings.Repeat("x", 100), "2026-10-25 12:00:00")}}}
	raw, err := r.PrepareSegment(context.Background(), nil, middle, b.ObservedAt.Add(2*time.Second), []binlog.Transaction{first})
	if err != nil {
		t.Fatal("small first segment rejected", err)
	}
	if err := r.ApplySegment(raw, digest(raw)); err != nil {
		t.Fatal(err)
	}
	second := binlog.Transaction{Start: middle, End: end, CommittedAt: b.ObservedAt.Add(3 * time.Second), Changes: []binlog.Change{{Table: "workspaces", Kind: "update", Before: first.Changes[0].After, After: logRow(w, strings.Repeat("y", 200), "2026-10-25 12:00:00")}}}
	if _, err := r.PrepareSegment(context.Background(), nil, end, b.ObservedAt.Add(4*time.Second), []binlog.Transaction{second}); err == nil {
		t.Fatal("small segment pushed cumulative state beyond archive data budget")
	}
	crafted := LogSegment{Format: 1, BaselineSHA256: b.ArchiveSHA256, PreviousSHA256: r.Point().Hash, Source: b.Source, Schema: b.Schema, From: middle, Through: end, ObservedAt: b.ObservedAt.Add(4 * time.Second), Transactions: []binlog.Transaction{second}, Objects: map[string][]byte{}}
	encoded, _ := json.Marshal(crafted)
	if err := r.ApplySegment(encoded, digest(encoded)); err == nil {
		t.Fatal("unmaterializable committed state advanced recovery point")
	}
	if r.Point().Position != middle {
		t.Fatal("capacity failure changed verified point")
	}
	// The final point fits again, but the first complete intermediate point
	// must not silently become an offered recovery position that cannot export.
	r2, b2, _, w2 := journalFixture(t)
	baseData, _ := json.Marshal(r2.archive.Data)
	r2.capacity.data = int64(len(baseData) + 200)
	large := binlog.Transaction{Start: b2.Position, End: middle, CommittedAt: b2.ObservedAt.Add(time.Second), Changes: []binlog.Change{{Table: "workspaces", Kind: "update", Before: logRow(w2, "before", "2026-10-25 12:00:00"), After: logRow(w2, strings.Repeat("z", 200), "2026-10-25 12:00:00")}}}
	shrink := binlog.Transaction{Start: middle, End: end, CommittedAt: b2.ObservedAt.Add(2 * time.Second), Changes: []binlog.Change{{Table: "workspaces", Kind: "update", Before: large.Changes[0].After, After: large.Changes[0].Before}}}
	if _, err := r2.PrepareSegment(context.Background(), nil, end, b2.ObservedAt.Add(3*time.Second), []binlog.Transaction{large, shrink}); err == nil {
		t.Fatal("later deletion/shrink hid unmaterializable intermediate state")
	}
}
