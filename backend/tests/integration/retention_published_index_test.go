package integration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"novelstudio/internal/domain"
	"novelstudio/internal/retention"
	"novelstudio/internal/service"
)

func TestRetentionPublishedIndexPreservesHistoryAndRejectsChangedChunks(t *testing.T) {
	b, db := setup(t)
	project := createProject(b, "发布索引保留", "retention-published-index")
	bid := branch(b, project, "正史")
	rid := resource(b, project, bid, "背景资料")
	first := prepareRelease(b, project, bid, "", "", "published-one", []map[string]any{{"resource_id": rid, "expected_head": "", "text": "药师在雨夜等候故人。"}})
	releaseAdopt(b, project, first)
	releasePublish(b, project, first)
	second := prepareRelease(b, project, bid, "", "", "published-two", []map[string]any{{"resource_id": rid, "expected_head": first, "text": "药师在雪夜找到了旧日友人的信。"}})
	releaseAdopt(b, project, second)
	releasePublish(b, project, second)
	actor := b.request("GET", "/api/me", nil, 200)["id"].(string)
	installSourceRegistrationFixture(t, db)
	w, ctx := &service.Workspace{DB: db}, context.Background()
	registered, err := w.RegisterRetentionSources(ctx, actor, project)
	if err != nil || registered.DeletionReady {
		t.Fatal(registered, err)
	}
	rows, err := db.Query("SELECT owner_table,field_key,content_root,CAST(deadline AS CHAR) FROM retention_references WHERE project_id=? AND owner_table IN ('knowledge_indexes','knowledge_chunks')", project)
	if err != nil {
		t.Fatal(err)
	}
	indexes, chunks := 0, 0
	for rows.Next() {
		var table, field string
		var root bool
		var deadline sql.NullString
		if err := rows.Scan(&table, &field, &root, &deadline); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		if table == "knowledge_indexes" {
			indexes++
		}
		if field == "content" {
			chunks++
		}
		if deadline.Valid || (field != "resource_name" && !root) {
			rows.Close()
			t.Fatalf("published material still expires: %s.%s root=%v", table, field, root)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		t.Fatal(err)
	}
	rows.Close()
	if indexes != 2 || chunks != 2 {
		t.Fatal("historical published index was omitted", indexes, chunks)
	}
	if replay, err := w.RegisterRetentionSources(ctx, actor, project); err != nil || replay.NewReferences != 0 || replay.UpdatedReferences != 0 {
		t.Fatal("replay changed publication roots", replay, err)
	}
	var ref, owner string
	if err := db.QueryRow("SELECT id,owner_key FROM retention_references WHERE project_id=? AND owner_table='knowledge_chunks' AND field_key='content' ORDER BY id LIMIT 1", project).Scan(&ref, &owner); err != nil {
		t.Fatal(err)
	}
	var ids []string
	if err := json.Unmarshal([]byte(owner), &ids); err != nil || len(ids) != 1 {
		t.Fatal("invalid fixture identity", err)
	}
	var original string
	if err := db.QueryRow("SELECT content FROM knowledge_chunks WHERE id=?", ids[0]).Scan(&original); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE knowledge_chunks SET content='与已发布原文不同的内容' WHERE id=?", ids[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := w.RegisterRetentionSources(ctx, actor, project); !errors.Is(err, domain.ErrNotReady) {
		t.Fatal("changed chunk promoted to content root", err)
	}
	var values int
	if err := db.QueryRow("SELECT COUNT(*) FROM retention_source_values WHERE source_id=?", ref).Scan(&values); err != nil || values != 1 {
		t.Fatal("failed body proof leaked observations", values, err)
	}
	if _, err := db.Exec("UPDATE knowledge_chunks SET content=? WHERE id=?", original, ids[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE knowledge_chunks SET vector_hash=REPEAT('0',64) WHERE id=?", ids[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := w.RegisterRetentionSources(ctx, actor, project); !errors.Is(err, domain.ErrNotReady) {
		t.Fatal("invalid vector identity accepted", err)
	}
	if _, err := db.Exec("UPDATE knowledge_chunks SET vector_hash=SHA2(vector,256) WHERE id=?", ids[0]); err != nil {
		t.Fatal(err)
	}
	if err := w.DeleteProject(ctx, actor, project, "发布索引保留", 1, "delete-published-index-project"); err != nil {
		t.Fatal(err)
	}
	if _, err := w.RegisterRetentionSources(ctx, actor, project); err != nil {
		t.Fatal(err)
	}
	var raw string
	if err := db.QueryRow("SELECT v.snapshot FROM retention_references r JOIN retention_reference_versions v ON v.reference_id=r.id AND v.revision=r.revision WHERE r.id=?", ref).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var deleted retention.ReferenceVersion
	if err := json.Unmarshal([]byte(raw), &deleted); err != nil || !deleted.Reference.ContentRoot || deleted.Reference.Revision != 2 || len(deleted.Reference.Sources) != 0 || deleted.Lifecycle.DeletedAt.IsZero() || !deleted.Deadline.Equal(deleted.Lifecycle.DeletedAt.Add(7*24*time.Hour)) {
		t.Fatal("publication root bypassed project deletion policy", deleted, err)
	}
}
