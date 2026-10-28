package integration

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"

	"novelstudio/internal/domain"
	"novelstudio/internal/retention"
	"novelstudio/internal/service"
)

func TestRetentionPrivateIndexPinsDraftBusinessVersion(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(map[bool]string{false: "recorded", true: "unobserved-version"}[missing], func(t *testing.T) {
			b, db := setup(t)
			project := createProject(b, "私人来源业务版本", "private-origin-version")
			bid := branch(b, project, "正史")
			rid := resource(b, project, bid, "草稿资料")
			b.request("PUT", "/api/projects/"+project+"/resources/"+rid+"/draft", map[string]any{"revision": 0, "base_head": "", "text": "旧草稿的药师等待故人。"}, 200)
			job := b.request("POST", "/api/projects/"+project+"/branches/"+bid+"/knowledge/jobs", map[string]any{"operation_id": "draft-origin-index", "kind": "working"}, 202)
			if state, err := privateJobs(t, db).Execute(context.Background(), job["id"].(string)); err != nil || state != "READY" {
				t.Fatal(state, err)
			}
			actor := b.request("GET", "/api/me", nil, 200)["id"].(string)
			installSourceRegistrationFixture(t, db)
			if missing {
				// Same bytes, a different business revision. Hash equality cannot
				// establish that this was the original version used by the index.
				if _, err := db.Exec("UPDATE resource_drafts SET revision=2 WHERE resource_id=?", rid); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec("UPDATE resource_policy_drafts SET revision=2 WHERE resource_id=?", rid); err != nil {
					t.Fatal(err)
				}
			}
			w := &service.Workspace{DB: db}
			report, err := w.RegisterRetentionSources(context.Background(), actor, project)
			if err != nil {
				t.Fatal(err)
			}
			if missing {
				if report.UnboundSources == 0 {
					t.Fatal("unobserved old draft rebound to same bytes in new revision", report)
				}
				return
			}
			var raw string
			if err := db.QueryRow("SELECT v.snapshot FROM retention_references r JOIN retention_reference_versions v ON v.reference_id=r.id AND v.revision=r.revision WHERE r.project_id=? AND r.owner_table='working_indexes'", project).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			var v retention.ReferenceVersion
			if err := json.Unmarshal([]byte(raw), &v); err != nil || len(v.Reference.Sources) != 1 {
				t.Fatal("manifest lacks exact draft origin", v, err)
			}
			var table, field string
			if err := db.QueryRow("SELECT owner_table,field_key FROM retention_references WHERE id=?", v.Reference.Sources[0].ReferenceID).Scan(&table, &field); err != nil || table != "resource_drafts" || field != "body" {
				t.Fatal("incorrect draft parent", table, field, err)
			}
		})
	}
}

func TestRetentionPrivateIndexResolvesOriginalDraftFromHistory(t *testing.T) {
	b, db := setup(t)
	project := createProject(b, "旧索引来源历史", "private-origin-history")
	bid := branch(b, project, "正史")
	rid := resource(b, project, bid, "草稿资料")
	b.request("PUT", "/api/projects/"+project+"/resources/"+rid+"/draft", map[string]any{"revision": 0, "base_head": "", "text": "旧草稿的药师等待故人。", "policy": domain.DefaultKnowledgePolicy()}, 200)
	job := b.request("POST", "/api/projects/"+project+"/branches/"+bid+"/knowledge/jobs", map[string]any{"operation_id": "historical-draft-index", "kind": "working"}, 202)
	if state, err := privateJobs(t, db).Execute(context.Background(), job["id"].(string)); err != nil || state != "READY" {
		t.Fatal(state, err)
	}
	actor := b.request("GET", "/api/me", nil, 200)["id"].(string)
	var index string
	if err := db.QueryRow("SELECT id FROM working_indexes WHERE project_id=?", project).Scan(&index); err != nil {
		t.Fatal(err)
	}
	// Hold only these test index rows aside so source v1 can be registered
	// before a later first observation of the old index.
	restoreIndex := holdRetentionIndexRows(t, db, "working_indexes", "id", index)
	restoreChunks := holdRetentionIndexRows(t, db, "working_chunks", "index_id", index)
	if _, err := db.Exec("DELETE FROM working_chunks WHERE index_id=?", index); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("DELETE FROM working_indexes WHERE id=?", index); err != nil {
		t.Fatal(err)
	}
	installSourceRegistrationFixture(t, db)
	w, ctx := &service.Workspace{DB: db}, context.Background()
	if _, err := w.RegisterRetentionSources(ctx, actor, project); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE resource_drafts SET revision=2,body='新版正文已经变化' WHERE resource_id=?", rid); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE resource_policy_drafts SET revision=2,payload=JSON_OBJECT('space','research','visibility','author','reveal_at',0) WHERE resource_id=?", rid); err != nil {
		t.Fatal(err)
	}
	if _, err := w.RegisterRetentionSources(ctx, actor, project); err != nil {
		t.Fatal(err)
	}
	restoreIndex()
	restoreChunks()
	var policyID, policyRecord, policyHash, policyClock string
	if err := db.QueryRow("SELECT v.reference_id,v.snapshot,v.record_hash,CAST(v.recorded_at AS CHAR) FROM retention_reference_versions v JOIN retention_references r ON r.id=v.reference_id WHERE r.project_id=? AND r.owner_table='resource_policy_drafts' AND v.revision=1", project).Scan(&policyID, &policyRecord, &policyHash, &policyClock); err != nil {
		t.Fatal(err)
	}
	var policyVersion retention.ReferenceVersion
	if err := json.Unmarshal([]byte(policyRecord), &policyVersion); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("DELETE FROM retention_reference_versions WHERE reference_id=? AND revision=1", policyID); err != nil {
		t.Fatal(err)
	}
	partial, err := w.RegisterRetentionSources(ctx, actor, project)
	if err != nil || partial.UnboundSources != 1 {
		t.Fatal("missing policy history was guessed", partial, err)
	}
	if repeat, err := w.RegisterRetentionSources(ctx, actor, project); err != nil || repeat.UpdatedReferences != 0 || repeat.UnboundSources != 1 {
		t.Fatal("partial proof replay was unstable", repeat, err)
	}
	if _, err := db.Exec("INSERT INTO retention_reference_versions(reference_id,revision,project_id,source_slot_id,source_payload_hash,record_hash,snapshot,recorded_at) VALUES(?,1,?,?,?,?,?,?)", policyID, project, policyVersion.SourceID, policyVersion.SourcePayloadHash, policyHash, policyRecord, policyClock); err != nil {
		t.Fatal(err)
	}
	first, err := w.RegisterRetentionSources(ctx, actor, project)
	if err != nil || first.UnboundSources != 0 {
		t.Fatal(first, err)
	}
	var raw string
	if err := db.QueryRow("SELECT v.snapshot FROM retention_references r JOIN retention_reference_versions v ON v.reference_id=r.id AND v.revision=r.revision WHERE r.project_id=? AND r.owner_table='working_indexes'", project).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var v retention.ReferenceVersion
	if err := json.Unmarshal([]byte(raw), &v); err != nil || len(v.Reference.Sources) != 2 {
		t.Fatal("body and policy history not bound", v, err)
	}
	for _, pin := range v.Reference.Sources {
		var current int64
		if err := db.QueryRow("SELECT revision FROM retention_references WHERE id=?", pin.ReferenceID).Scan(&current); err != nil || current != 2 || pin.Revision != 1 {
			t.Fatal("old copy bound to latest version", current, pin, err)
		}
	}
	if repeat, err := w.RegisterRetentionSources(ctx, actor, project); err != nil || repeat.NewReferences != 0 || repeat.UpdatedReferences != 0 || repeat.UnboundSources != 0 {
		t.Fatal("replay rewrote historical pins", repeat, err)
	}
}

func holdRetentionIndexRows(t *testing.T, db *sql.DB, table, column, id string) func() {
	t.Helper()
	if (table != "working_indexes" || column != "id") && (table != "working_chunks" || column != "index_id") {
		t.Fatal("invalid test table")
	}
	rows, err := db.Query("SELECT * FROM "+table+" WHERE "+column+"=?", id)
	if err != nil {
		t.Fatal(err)
	}
	columns, err := rows.Columns()
	if err != nil {
		rows.Close()
		t.Fatal(err)
	}
	var saved [][]any
	for rows.Next() {
		values := make([]any, len(columns))
		pointers := make([]any, len(columns))
		for i := range values {
			pointers[i] = &values[i]
		}
		if err := rows.Scan(pointers...); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		for i, value := range values {
			if bytes, ok := value.([]byte); ok {
				values[i] = append([]byte(nil), bytes...)
			}
		}
		saved = append(saved, values)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		t.Fatal(err)
	}
	return func() {
		for _, values := range saved {
			if _, err := db.Exec("INSERT INTO "+table+" (`"+strings.Join(columns, "`,`")+"`) VALUES ("+strings.TrimSuffix(strings.Repeat("?,", len(columns)), ",")+")", values...); err != nil {
				t.Fatal(err)
			}
		}
	}
}
