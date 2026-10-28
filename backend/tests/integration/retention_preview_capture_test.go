package integration

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"novelstudio/internal/authority"
	"novelstudio/internal/models"
	"novelstudio/internal/retention"
)

func TestRetentionStreamCheckpointsBoundHistoryAndKeepOriginalAge(t *testing.T) {
	b, svc, actor, project, original := generationFixture(t, &generationTransport{})
	db := svc.IP.Workspace.DB
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	steps := make(chan struct{})
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		for i := 1; i <= 5; i++ {
			fmt.Fprint(w, "data: {\"id\":\"retained-stream\",\"choices\":[{\"delta\":{\"content\":\"雨\"}}]}\n\n")
			w.(http.Flusher).Flush()
			select {
			case <-steps:
			case <-r.Context().Done():
				return
			}
		}
		fmt.Fprint(w, "data: {\"id\":\"retained-stream\",\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":20,\"completion_tokens\":10,\"prompt_cache_hit_tokens\":10,\"prompt_cache_miss_tokens\":10,\"total_tokens\":30}}\n\ndata: [DONE]\n\n")
	}))
	defer func() { cancel(); server.Close() }()
	target, _ := url.Parse(server.URL)
	svc.Adapter = models.New(localStreamTransport{target})
	prepared := b.request("POST", "/api/projects/"+project+"/generations", map[string]any{"branch_id": original.BranchID, "role": "writer", "price_id": original.Frozen.Price.ID, "operation_id": "retention-stream", "stream": true, "request": map[string]any{"prompt": "本地流式保存测试", "max_output_tokens": 100}}, 201)
	id := prepared["id"].(string)
	installSourceRegistrationFixture(t, db)
	if _, err := svc.Start(ctx, actor, project, id); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := svc.Execute(ctx, id); done <- err }()
	var first retention.ReferenceVersion
	var previousRevision int64
	read := func() retention.ReferenceVersion {
		t.Helper()
		key, _ := retention.RowOwnerKey("generation_previews", map[string]string{"generation_id": id})
		var raw string
		if err := db.QueryRow("SELECT v.snapshot FROM retention_references r JOIN retention_reference_versions v ON v.reference_id=r.id AND v.revision=r.revision WHERE r.project_id=? AND r.owner_table='generation_previews' AND r.owner_key=?", project, key).Scan(&raw); err != nil {
			t.Fatal("preview has no original observation", err)
		}
		var ref retention.ReferenceVersion
		if err := json.Unmarshal([]byte(raw), &ref); err != nil {
			t.Fatal(err)
		}
		return ref
	}
	for n := 1; n <= 5; n++ {
		deadline := time.Now().Add(4 * time.Second)
		var sequence int
		for time.Now().Before(deadline) {
			_ = db.QueryRow("SELECT sequence FROM generation_previews WHERE generation_id=?", id).Scan(&sequence)
			if sequence == n {
				break
			}
			select {
			case err := <-done:
				t.Fatal("stream terminated before checkpoint", n, err)
			default:
			}
			time.Sleep(30 * time.Millisecond)
		}
		if sequence != n {
			t.Fatal("missing checkpoint", n, sequence)
		}
		version := read()
		if n == 1 {
			first = version
		}
		if version.Reference.Revision != first.Reference.Revision {
			t.Fatal("journal grows with each transient frame")
		}
		var count int
		if err := db.QueryRow("SELECT COUNT(*) FROM retention_source_values v JOIN retention_source_slots s ON s.id=v.source_id WHERE s.project_id=? AND s.owner_table='generation_previews'", project).Scan(&count); err != nil || count != 1 {
			t.Fatal("transient frames accumulated", count, err)
		}
		var currentRevision int64
		if err := db.QueryRow("SELECT inventory_revision FROM project_lifecycle WHERE project_id=?", project).Scan(&currentRevision); err != nil || currentRevision <= previousRevision {
			t.Fatal("checkpoint left old cleanup plan valid", currentRevision, previousRevision, err)
		}
		previousRevision = currentRevision
		select {
		case steps <- struct{}{}:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	final := read()
	if final.Reference.Revision != first.Reference.Revision+1 || !final.Reference.FirstVerifiedAt.Equal(first.Reference.FirstVerifiedAt) || final.Reference.ContentRoot {
		t.Fatal("final preview age/authority changed", final)
	}
	result, err := svc.Read(ctx, actor, project, id)
	if err != nil || result.State != "SUCCEEDED" || result.Output == nil || result.Output.Text != strings.Repeat("雨", 5) {
		t.Fatal("final preview/result", result, err)
	}
	if requests.Load() != 1 {
		t.Fatal("provider request repeated")
	}
}

func TestRetentionInterruptedPreviewScanAndCheckpointFailure(t *testing.T) {
	_, svc, actor, project, generation := generationFixture(t, &generationTransport{})
	db, ctx := svc.IP.Workspace.DB, context.Background()
	installSourceRegistrationFixture(t, db)
	write := func(sequence int) error {
		t.Helper()
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		content := strings.Repeat("未完成的预览。", sequence)
		hash := fmt.Sprintf("%x", sha256.Sum256([]byte(content)))
		if _, err := tx.Exec("INSERT INTO generation_previews(generation_id,content,sequence,content_hash) VALUES(?,?,?,?) ON DUPLICATE KEY UPDATE content=VALUES(content),sequence=VALUES(sequence),content_hash=VALUES(content_hash)", generation.ID, content, sequence, hash); err != nil {
			return err
		}
		if err := retention.CheckpointGenerationPreview(ctx, tx, authority.Ledger{Schema: os.Getenv("STUDIO_AUTHORITY_SCHEMA")}, project, generation.ID, false); err != nil {
			return err
		}
		return tx.Commit()
	}
	if err := write(1); err != nil {
		t.Fatal(err)
	}
	var firstAge string
	if err := db.QueryRow("SELECT CAST(first_verified_at AS CHAR) FROM retention_references WHERE project_id=? AND owner_table='generation_previews'", project).Scan(&firstAge); err != nil {
		t.Fatal(err)
	}
	if err := write(2); err != nil {
		t.Fatal(err)
	}
	// No final flush, as after a process interruption. The ordinary inventory
	// must bind the actual latest row using the first persisted checkpoint age.
	if report, err := svc.IP.Workspace.RegisterRetentionSources(ctx, actor.ID, project); err != nil || report.UnboundSources != 0 {
		t.Fatal("interrupted preview inventory", report, err)
	}
	var age, hash string
	if err := db.QueryRow("SELECT CAST(first_verified_at AS CHAR),payload_hash FROM retention_references WHERE project_id=? AND owner_table='generation_previews'", project).Scan(&age, &hash); err != nil || age != firstAge || hash != fmt.Sprintf("%x", sha256.Sum256([]byte(strings.Repeat("未完成的预览。", 2)))) {
		t.Fatal("scan reset age or kept obsolete preview", age, hash, err)
	}
	if _, err := db.Exec("CREATE TRIGGER reject_preview_dirty BEFORE UPDATE ON project_lifecycle FOR EACH ROW BEGIN IF NEW.inventory_revision>OLD.inventory_revision THEN SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='test rejected dirty inventory'; END IF; END"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.Exec("DROP TRIGGER IF EXISTS reject_preview_dirty") })
	if err := write(3); err == nil {
		t.Fatal("checkpoint committed while old cleanup scan stayed valid")
	}
	var sequence int
	if err := db.QueryRow("SELECT sequence FROM generation_previews WHERE generation_id=?", generation.ID).Scan(&sequence); err != nil || sequence != 2 {
		t.Fatal("checkpoint failure partially saved preview", sequence, err)
	}
	if _, err := db.Exec("DROP TRIGGER reject_preview_dirty"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE retention_references SET state='EXPIRED' WHERE project_id=? AND owner_table='generation_previews'", project); err != nil {
		t.Fatal(err)
	}
	if err := write(3); err == nil {
		t.Fatal("expired preview resurrected")
	}
	if err := db.QueryRow("SELECT sequence FROM generation_previews WHERE generation_id=?", generation.ID).Scan(&sequence); err != nil || sequence != 2 {
		t.Fatal("expired preview changed", sequence, err)
	}
}
