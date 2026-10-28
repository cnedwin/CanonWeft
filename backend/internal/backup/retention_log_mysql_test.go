package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"novelstudio/internal/binlog"
	"novelstudio/internal/domain"
	"novelstudio/internal/retention"
	"novelstudio/internal/storage"
)

func TestRetentionLogCapturesRealTransactionsAndReloads(t *testing.T) {
	db, cfg, admin := testDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	w, u, p := randomID(t), randomID(t), randomID(t)
	execTest(t, db, "INSERT INTO workspaces(id,name) VALUES(?,'retention log')", w)
	execTest(t, db, "INSERT INTO users(id,workspace_id,username,password_hash,role) VALUES(?,?,'owner','private-password','owner')", u, w)
	execTest(t, db, "INSERT INTO projects(id,workspace_id,name) VALUES(?,?,'before')", p, w)
	ledger := provisionTestAuthority(t, db, admin)
	provenance, err := ledger.ProvenanceTx(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	installSnapshotRetentionFixture(t, db)
	execTest(t, db, "INSERT INTO project_lifecycle(project_id,source_ledger_id,time_basis,first_verified_at) VALUES(?,?,'SOURCE_UTC',UTC_TIMESTAMP(6))", p, provenance.LedgerID)
	branch := randomID(t)
	execTest(t, db, "INSERT INTO branches(id,project_id,name) VALUES(?,?,'main')", branch, p)
	draft := domain.StoredIPDraft{DraftSchema: 2, AmendmentReason: "原稿：保留中文与 JSON 原始格式"}
	confidence := 0.000015
	draft.Snapshot = domain.Snapshot{
		Entities:   []domain.Entity{{ID: "hero", Kind: "character", Name: "林青"}},
		Assertions: []domain.Assertion{{ID: "claim", Subject: "hero", Predicate: "计划", Value: "前往北方", Kind: "intention", EffectiveFrom: 9007199254740993, Confidence: &confidence}},
	}
	draftRaw, err := json.Marshal(draft)
	if err != nil {
		t.Fatal(err)
	}
	execTest(t, db, "INSERT INTO ip_drafts(branch_id,actor_id,revision,base_head,payload) VALUES(?,?,1,'',?)", branch, u, string(draftRaw))
	target := retention.SourceKey{Table: "ip_drafts", Primary: map[string]string{"branch_id": branch, "actor_id": u}}
	initial, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer initial.Rollback()
	if err = retention.CaptureSourceValues(ctx, initial, ledger, p, target); err != nil {
		t.Fatal(err)
	}
	if err = initial.Commit(); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	blobs := storage.LocalBlobs{Root: filepath.Join(root, "objects")}
	path := filepath.Join(root, "baseline.zip")
	base, metrics, err := CreateLogBaseline(ctx, db, blobs, path)
	if err != nil {
		t.Fatal(err)
	}
	if base.Schema != retentionSchemaVersion || !base.ObservedAt.Equal(metrics.SnapshotAt) {
		t.Fatal("baseline schema/clock disagrees with snapshot")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := OpenLogReplay(base, raw)
	if err != nil {
		t.Fatal(err)
	}
	keys := map[string][]string{
		"retention_objects":            {"project_id", "object_hash"},
		"retention_operation_keys":     {"actor_id", "owner_table", "original_key_hash"},
		"retention_reference_versions": {"reference_id", "revision"},
		"retention_source_values":      {"source_id", "payload_hash", "is_null"},
		"retention_receipts":           {"job_id", "item_key", "phase"},
		"retention_recovery_attempts":  {"target_project_id"},
	}
	for _, table := range base.Tables {
		if want, ok := keys[table.Layout.Name]; ok {
			var got []string
			for _, index := range table.Layout.PrimaryKey {
				got = append(got, table.Layout.Columns[index])
			}
			if !slices.Equal(got, want) {
				t.Fatalf("incorrect composite key for %s: %v", table.Layout.Name, got)
			}
			delete(keys, table.Layout.Name)
		}
	}
	if len(keys) != 0 {
		t.Fatal("retention tables omitted from log contract")
	}
	// Two objects share the first primary-key column. Replay must retain both.
	hashes := []string{digest([]byte("first orphan")), digest([]byte("second orphan"))}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	event := randomID(t)
	if _, err = blobs.PutObject(ctx, p, bytes.NewReader([]byte("{}"))); err != nil {
		t.Fatal(err)
	}
	eventRevision := randomID(t)
	if _, err = tx.ExecContext(ctx, "INSERT INTO ip_revisions(id,branch_id,base_head,payload,content_hash,created_by) VALUES(?,?,'','{}',?,?)", eventRevision, branch, digest([]byte("{}")), u); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO outbox(id,project_id,kind,resource_id,payload) VALUES(?,?,'ip.published',?,'{\"head\":\"logged-event\"}')", event, p, eventRevision); err != nil {
		t.Fatal(err)
	}
	if err = retention.CaptureSourceValues(ctx, tx, ledger, p, retention.SourceKey{Table: "outbox", Primary: map[string]string{"id": event}}); err != nil {
		t.Fatal(err)
	}
	draft.AmendmentReason = "修改稿：对白与人物关系保持原意"
	draftRaw, err = json.Marshal(draft)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.ExecContext(ctx, "UPDATE ip_drafts SET payload=?,revision=2,updated_at=UTC_TIMESTAMP(6) WHERE branch_id=? AND actor_id=?", string(draftRaw), branch, u); err != nil {
		t.Fatal(err)
	}
	if err = retention.CaptureSourceValues(ctx, tx, ledger, p, target); err != nil {
		t.Fatal(err)
	}
	for _, hash := range hashes {
		if _, err = tx.ExecContext(ctx, "INSERT INTO retention_objects(project_id,object_hash,first_seen_at) VALUES(?,?,UTC_TIMESTAMP(6))", p, hash); err != nil {
			t.Fatal(err)
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	reader, err := binlog.NewReader(cfg.FormatDSN(), base.Source.ServerUUID)
	if err != nil {
		t.Fatal(err)
	}
	segment, err := replay.CaptureSegment(ctx, db, reader, blobs)
	if err != nil {
		t.Fatal(err)
	}
	var decoded LogSegment
	if err = strictLogJSON(segment, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Transactions) != 1 {
		t.Fatalf("expected one atomic source transaction, got %d", len(decoded.Transactions))
	}
	repository := filepath.Join(root, "journal")
	store, err := OpenLogStore(repository, base.Source)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	id, err := store.CommitBaseline(ctx, base, raw, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	point, err := store.Append(ctx, id, segment, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenLogStore(repository, base.Source)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(ctx, id, time.Now().UTC())
	if err != nil || loaded.Point() != point {
		t.Fatal("durable replay failed", err)
	}
	archive, err := store.Archive(ctx, id, point.Position, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	got, err := validateArchiveContents(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		t.Fatal(err)
	}
	var actualJSON string
	if err = db.QueryRowContext(ctx, "SELECT CAST(payload AS CHAR) FROM ip_drafts WHERE branch_id=? AND actor_id=?", branch, u).Scan(&actualJSON); err != nil {
		t.Fatal(err)
	}
	if memorySpec("ip_drafts").value(got.Data["ip_drafts"][0], "payload") != actualJSON || len(got.Data["retention_objects"]) != 2 {
		t.Fatal("business JSON bytes or metadata changes lost")
	}
	if len(got.Data["retention_reference_versions"]) < 2 {
		t.Fatal("source history absent from actual replay")
	}
	if len(got.Data["outbox"]) != 1 || memorySpec("outbox").value(got.Data["outbox"][0], "processed_at") == "" {
		t.Fatal("logged event history omitted or delivery enabled")
	}
	for _, spec := range retentionTableCatalog() {
		rows, err := readRows(ctx, db, spec)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != len(got.Data[spec.name]) {
			t.Fatalf("metadata row count differs: %s", spec.name)
		}
		for _, expected := range rows {
			found := false
			for _, row := range got.Data[spec.name] {
				if equalLogRow(binlog.Row(row), binlog.Row(expected)) {
					found = true
				}
			}
			if !found {
				t.Fatalf("source metadata changed during replay: %s", spec.name)
			}
		}
	}
	if memorySpec("users").value(got.Data["users"][0], "password_hash") != "" || memorySpec("users").value(got.Data["users"][0], "enabled") != "0" {
		t.Fatal("restored identity enabled or contains source password")
	}
	if _, err = ValidateArchive(bytes.NewReader(archive), int64(len(archive))); err != nil {
		t.Fatal("replayed archive failed public validation", err)
	}
	// Restart the actual coordinator against this journal. A schema36 chain
	// must be continued, not mistaken for historical schema35 and replaced.
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	next, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer next.Rollback()
	if _, err = next.ExecContext(ctx, "UPDATE outbox SET processed_at=UTC_TIMESTAMP(6) WHERE id=?", event); err != nil {
		t.Fatal(err)
	}
	if _, err = next.ExecContext(ctx, "UPDATE ip_drafts SET revision=3,payload=JSON_SET(payload,'$.amendment_reason',?,'$.snapshot.assertions[0].confidence',CAST(1 AS DOUBLE)),updated_at=UTC_TIMESTAMP(6) WHERE branch_id=? AND actor_id=?", "第三稿 <人物> & 对话\n行间\u2028\u2029", branch, u); err != nil {
		t.Fatal(err)
	}
	if err = retention.CaptureSourceValues(ctx, next, ledger, p, target); err != nil {
		t.Fatal(err)
	}
	if err = next.Commit(); err != nil {
		t.Fatal(err)
	}
	runner, err := NewLogRunner(ctx, db, blobs, cfg.FormatDSN(), repository)
	if err != nil {
		t.Fatal(err)
	}
	defer runner.Close()
	view, err := runner.Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if view.BaselineID != id || view.State != "CURRENT" || view.HistoryGaps != 0 || view.Latest.Position == point.Position {
		t.Fatal("coordinator did not continue actual retention chain", view.State, view.HistoryGaps)
	}
	latest, err := runner.Store.Archive(ctx, id, view.Latest.Position, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	latestArchive, err := validateArchiveContents(bytes.NewReader(latest), int64(len(latest)))
	if err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRowContext(ctx, "SELECT CAST(payload AS CHAR) FROM ip_drafts WHERE branch_id=? AND actor_id=?", branch, u).Scan(&actualJSON); err != nil {
		t.Fatal(err)
	}
	if memorySpec("ip_drafts").value(latestArchive.Data["ip_drafts"][0], "payload") != actualJSON || memorySpec("ip_drafts").value(latestArchive.Data["ip_drafts"][0], "revision") != "3" {
		t.Fatal("batched live capture changed JSON or lost revision")
	}
}
