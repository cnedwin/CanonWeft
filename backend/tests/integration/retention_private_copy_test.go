package integration

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"novelstudio/internal/domain"
	"novelstudio/internal/retention"
	"novelstudio/internal/retrieval"
	"novelstudio/internal/service"
)

func TestRetentionPrivateCopyRejectsChangedTextRangesAndMissingChunks(t *testing.T) {
	b, db := setup(t)
	project := createProject(b, "检索副本校验", "private-copy-verification")
	bid := branch(b, project, "正史")
	rid := resource(b, project, bid, "私人长草稿")
	body := strings.Repeat("药师🌙在雨夜等候故人，记下沿途细节。", 60)
	b.request("PUT", "/api/projects/"+project+"/resources/"+rid+"/draft", map[string]any{"revision": 0, "base_head": "", "text": body}, 200)
	job := b.request("POST", "/api/projects/"+project+"/branches/"+bid+"/knowledge/jobs", map[string]any{"operation_id": "copy-proof-index", "kind": "working"}, 202)
	if state, err := privateJobs(t, db).Execute(context.Background(), job["id"].(string)); err != nil || state != "READY" {
		t.Fatal(state, err)
	}
	actor := b.request("GET", "/api/me", nil, 200)["id"].(string)
	installSourceRegistrationFixture(t, db)
	w, ctx := &service.Workspace{DB: db}, context.Background()
	if _, err := w.RegisterRetentionSources(ctx, actor, project); err != nil {
		t.Fatal(err)
	}
	var id, index, text, name, hash string
	var start, end int
	var vector []byte
	if err := db.QueryRow("SELECT id,index_id,content,source_name,start_rune,end_rune,vector,vector_hash FROM working_chunks ORDER BY start_rune LIMIT 1").Scan(&id, &index, &text, &name, &start, &end, &vector, &hash); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"text", "range", "name", "vector"} {
		switch mode {
		case "text":
			_, err := db.Exec("UPDATE working_chunks SET content=CONCAT('伪',SUBSTRING(content,2)) WHERE id=?", id)
			if err != nil {
				t.Fatal(err)
			}
		case "range":
			_, err := db.Exec("UPDATE working_chunks SET start_rune=start_rune+1,end_rune=end_rune+1 WHERE id=?", id)
			if err != nil {
				t.Fatal(err)
			}
		case "name":
			_, err := db.Exec("UPDATE working_chunks SET source_name='另一份来源' WHERE id=?", id)
			if err != nil {
				t.Fatal(err)
			}
		case "vector":
			_, err := db.Exec("UPDATE working_chunks SET vector=?,vector_hash=SHA2(?,256) WHERE id=?", make([]byte, retrieval.Dimension*4), make([]byte, retrieval.Dimension*4), id)
			if err != nil {
				t.Fatal(err)
			}
		}
		if _, err := w.RegisterRetentionSources(ctx, actor, project); !errors.Is(err, domain.ErrNotReady) {
			t.Fatal("invalid private copy accepted", mode, err)
		}
		if _, err := db.Exec("UPDATE working_chunks SET content=?,source_name=?,start_rune=?,end_rune=?,vector=?,vector_hash=? WHERE id=?", text, name, start, end, vector, hash, id); err != nil {
			t.Fatal(err)
		}
	}
	restore := holdRetentionIndexRows(t, db, "working_chunks", "index_id", index)
	if _, err := db.Exec("DELETE FROM working_chunks WHERE id=?", id); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE working_indexes SET chunks=chunks-1 WHERE id=?", index); err != nil {
		t.Fatal(err)
	}
	if _, err := w.RegisterRetentionSources(ctx, actor, project); !errors.Is(err, domain.ErrNotReady) {
		t.Fatal("incomplete source accepted after declared count was changed", err)
	}
	if _, err := db.Exec("DELETE FROM working_chunks WHERE index_id=?", index); err != nil {
		t.Fatal(err)
	}
	restore()
	if _, err := db.Exec("UPDATE working_indexes SET chunks=chunks+1 WHERE id=?", index); err != nil {
		t.Fatal(err)
	}
	if report, err := w.RegisterRetentionSources(ctx, actor, project); err != nil || report.NewValues != 0 || report.NewReferences != 0 || report.UpdatedReferences != 0 {
		t.Fatal("failed copy checks left journal changes", report, err)
	}
}

func TestRetentionPrivateCopyLegacyProofNeverRebindsOriginalVersion(t *testing.T) {
	b, db := setup(t)
	project := createProject(b, "旧副本内容证明", "private-copy-legacy")
	bid := branch(b, project, "正史")
	rid := resource(b, project, bid, "旧草稿")
	body := "药师仍在旧渡口等待故人。"
	b.request("PUT", "/api/projects/"+project+"/resources/"+rid+"/draft", map[string]any{"revision": 0, "base_head": "", "text": body}, 200)
	job := b.request("POST", "/api/projects/"+project+"/branches/"+bid+"/knowledge/jobs", map[string]any{"operation_id": "legacy-copy-index", "kind": "working"}, 202)
	if state, err := privateJobs(t, db).Execute(context.Background(), job["id"].(string)); err != nil || state != "READY" {
		t.Fatal(state, err)
	}
	actor := b.request("GET", "/api/me", nil, 200)["id"].(string)
	installSourceRegistrationFixture(t, db)
	w, ctx := &service.Workspace{DB: db}, context.Background()
	if _, err := w.RegisterRetentionSources(ctx, actor, project); err != nil {
		t.Fatal(err)
	}
	var raw string
	if err := db.QueryRow("SELECT v.snapshot FROM retention_references r JOIN retention_reference_versions v ON v.reference_id=r.id AND v.revision=r.revision WHERE r.project_id=? AND r.owner_table='resource_drafts' AND r.field_key='body'", project).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var original retention.ReferenceVersion
	if err := json.Unmarshal([]byte(raw), &original); err != nil || original.TextProof == nil {
		t.Fatal("source split proof not persisted", err)
	}
	// Simulate a valid pre-upgrade record with version evidence but no split
	// certificate. Only this isolated fixture removes the optional field.
	original.TextProof = nil
	legacy, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE retention_reference_versions SET snapshot=?,record_hash=SHA2(?,256) WHERE reference_id=? AND revision=1", string(legacy), legacy, original.Reference.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE resource_drafts SET body='新正文已没有旧渡口',revision=2 WHERE resource_id=?", rid); err != nil {
		t.Fatal(err)
	}
	if report, err := w.RegisterRetentionSources(ctx, actor, project); err != nil || report.UnboundSources != 1 {
		t.Fatal("lost old text proof was guessed", report, err)
	}
	if report, err := w.RegisterRetentionSources(ctx, actor, project); err != nil || report.UnboundSources != 1 || report.UpdatedReferences != 0 {
		t.Fatal("missing proof replay changed history", report, err)
	}
	// Same bytes may supply literal verification, but revision 3 must never
	// replace the business/retention version 1 pinned by the old index.
	if _, err := db.Exec("UPDATE resource_drafts SET body=?,revision=3 WHERE resource_id=?", body, rid); err != nil {
		t.Fatal(err)
	}
	if report, err := w.RegisterRetentionSources(ctx, actor, project); err != nil || report.UnboundSources != 0 {
		t.Fatal("verified identical text did not supply literal proof", report, err)
	}
	if err := db.QueryRow("SELECT v.snapshot FROM retention_references r JOIN retention_reference_versions v ON v.reference_id=r.id AND v.revision=r.revision WHERE r.project_id=? AND r.owner_table='working_indexes'", project).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var copy retention.ReferenceVersion
	if err := json.Unmarshal([]byte(raw), &copy); err != nil || len(copy.Reference.Sources) != 1 || copy.Reference.Sources[0].ReferenceID != original.Reference.ID || copy.Reference.Sources[0].Revision != 1 {
		t.Fatal("literal proof rebound original business version", copy.Reference.Sources, err)
	}
	if err := db.QueryRow("SELECT v.snapshot FROM retention_references r JOIN retention_reference_versions v ON v.reference_id=r.id AND v.revision=r.revision WHERE r.id=?", original.Reference.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var current retention.ReferenceVersion
	if err := json.Unmarshal([]byte(raw), &current); err != nil || !current.Reference.FirstVerifiedAt.Equal(original.Reference.FirstVerifiedAt) || !current.Deadline.Equal(original.Deadline) || current.DraftVersion.Version != "3" {
		t.Fatal("literal proof changed source age", err)
	}
	if report, err := w.RegisterRetentionSources(ctx, actor, project); err != nil || report.UnboundSources != 0 || report.UpdatedReferences != 0 {
		t.Fatal("supplemental proof replay changed history", report, err)
	}
}
