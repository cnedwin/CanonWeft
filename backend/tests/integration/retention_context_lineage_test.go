package integration

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"

	"novelstudio/internal/authority"
	"novelstudio/internal/domain"
	"novelstudio/internal/memory"
	"novelstudio/internal/models"
	"novelstudio/internal/retention"
	"novelstudio/internal/service"
	"novelstudio/internal/storage"
)

func TestRetentionContextBindsExactSourcesAndRejectsAlteredCopy(t *testing.T) {
	b, db := setup(t)
	project := createProject(b, "上下文来源", "context-lineage")
	bid := branch(b, project, "正史")
	ip := propose(b, project, bid, "", "context-ip", originalSnapshot())
	publish(b, project, bid, ip)
	rid := resource(b, project, bid, "药师记录")
	release := prepareRelease(b, project, bid, ip, "", "context-release", []map[string]any{{"resource_id": rid, "expected_head": "", "text": "药师沿河寻找故人，记下渡口相遇的细节。"}})
	releaseAdopt(b, project, release)
	releasePublish(b, project, release)
	draft := resource(b, project, bid, "未确认的药方")
	b.request("PUT", "/api/projects/"+project+"/resources/"+draft+"/draft", map[string]any{"revision": 0, "base_head": "", "text": "药方草稿中仍有未证实的配比。"}, 200)
	unused := resource(b, project, bid, "远方的沙漠")
	b.request("PUT", "/api/projects/"+project+"/resources/"+unused+"/draft", map[string]any{"revision": 0, "base_head": "", "text": "沙漠商队穿过高山，寻找一座无人城堡。"}, 200)
	path := "/api/projects/" + project + "/branches/" + bid + "/knowledge"
	b.request("POST", path+"/index", map[string]any{}, 200)
	job := b.request("POST", path+"/jobs", map[string]any{"operation_id": "context-private-index", "kind": "working"}, 202)
	if state, err := privateJobs(t, db).Execute(context.Background(), job["id"].(string)); err != nil || state != "READY" {
		t.Fatal(state, err)
	}
	result := b.request("POST", path+"/search", map[string]any{"query": "药师药方", "purpose": "author", "limit": 5, "include_working": true, "include_private_working": true, "required_resource_ids": []string{rid}, "working_refs": []map[string]string{{"kind": "resource_draft", "id": draft, "version": "1"}}}, 200)
	contextID := result["id"].(string)
	otherProject := createProject(b, "另一部作品", "context-other-project")
	otherBranch := branch(b, otherProject, "其他正史")
	otherIndex := b.request("POST", "/api/projects/"+otherProject+"/branches/"+otherBranch+"/knowledge/index", map[string]any{}, 200)["id"].(string)
	actor := b.request("GET", "/api/me", nil, 200)["id"].(string)
	installSourceRegistrationFixture(t, db)
	w, ctx := &service.Workspace{DB: db}, context.Background()
	// Capture public hits, private hits and required full text through the
	// actual save path before any full-project source registration.
	caller := domain.Actor{ID: actor}
	if err := db.QueryRow("SELECT workspace_id,role FROM users WHERE id=?", actor).Scan(&caller.WorkspaceID, &caller.Role); err != nil {
		t.Fatal(err)
	}
	saved, err := privateJobs(t, db).Knowledge.Search(ctx, caller, project, bid, service.SearchInput{Query: "药师药方", Purpose: "author", Limit: 1, IncludeWorking: true, IncludePrivateWorking: true, RequiredResourceIDs: []string{rid}, WorkingRefs: []domain.WorkingRef{{Kind: "resource_draft", ID: draft, Version: "1"}}})
	if err != nil {
		t.Fatal("capture complete context on save", err)
	}
	contextID = saved.ID
	var raw string
	if err := db.QueryRow("SELECT v.snapshot FROM retention_references r JOIN retention_reference_versions v ON v.reference_id=r.id AND v.revision=r.revision WHERE r.project_id=? AND r.owner_table='context_packages' AND r.field_key='payload'", project).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var version retention.ReferenceVersion
	if json.Unmarshal([]byte(raw), &version) != nil || len(version.Reference.Sources) < 6 {
		t.Fatal("context source closure absent", version.Reference.Sources)
	}
	var pkg service.ContextPackage
	var original, originalHash string
	if err := db.QueryRow("SELECT payload,content_hash FROM context_packages WHERE id=?", contextID).Scan(&original, &originalHash); err != nil || json.Unmarshal([]byte(original), &pkg) != nil || len(pkg.Evidence) == 0 || len(pkg.PrivateHits) == 0 || len(pkg.Unconfirmed) == 0 || len(pkg.Required) == 0 {
		t.Fatal("test did not produce all expected inputs", err)
	}
	originalIndex := pkg.IndexID
	for _, kind := range []string{"evidence", "private", "required", "ip", "draft", "foreign-index"} {
		if err := json.Unmarshal([]byte(original), &pkg); err != nil {
			t.Fatal(err)
		}
		switch kind {
		case "evidence":
			pkg.Evidence[0].Text = "伪造的河岸记录"
		case "private":
			pkg.PrivateHits[0].Text = "伪造的药方片段"
		case "required":
			pkg.Required[0].Text = "伪造的必要全文"
		case "ip":
			pkg.Entities[0].Name = "伪造的角色名字"
		case "draft":
			pkg.Unconfirmed[0].Parts[0].Text = "伪造的草稿正文"
		case "foreign-index":
			pkg.IndexID = otherIndex
		}
		changed, _ := json.Marshal(pkg)
		if _, err := db.Exec("UPDATE context_packages SET payload=?,content_hash=SHA2(?,256),index_id=? WHERE id=?", string(changed), changed, pkg.IndexID, contextID); err != nil {
			t.Fatal(err)
		}
		if kind == "draft" {
			var before, after int
			if err := db.QueryRow("SELECT COUNT(*) FROM retention_reference_versions WHERE project_id=?", project).Scan(&before); err != nil {
				t.Fatal(err)
			}
			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			err = retention.CaptureSourceValuesWithObjects(ctx, tx, authority.Ledger{Schema: os.Getenv("STUDIO_AUTHORITY_SCHEMA")}, project, storage.LocalBlobs{Root: os.Getenv("STUDIO_BLOB_ROOT")}.ReadObject, retention.SourceKey{Table: "context_packages", Primary: map[string]string{"id": contextID}})
			if rollbackErr := tx.Rollback(); rollbackErr != nil {
				t.Fatal(rollbackErr)
			}
			if err == nil {
				t.Fatal("live capture accepted an unproved source copy")
			}
			if err := db.QueryRow("SELECT COUNT(*) FROM retention_reference_versions WHERE project_id=?", project).Scan(&after); err != nil || after != before {
				t.Fatal("failed capture committed partial source history", before, after, err)
			}
		}
		report, err := w.RegisterRetentionSources(ctx, actor, project)
		// A missing immutable old draft proof is allowed only as explicitly
		// unbound; immutable published/chunk mismatches must abort registration.
		if kind == "draft" {
			if err == nil && report.UnboundSources == 0 {
				t.Fatal("unproved draft text accepted", report)
			}
		} else if !errors.Is(err, domain.ErrNotReady) {
			t.Fatal("changed context copy accepted", kind, err)
		}
		if _, err := db.Exec("UPDATE context_packages SET payload=?,content_hash=?,index_id=? WHERE id=?", original, originalHash, originalIndex, contextID); err != nil {
			t.Fatal(err)
		}
	}
	if report, err := w.RegisterRetentionSources(ctx, actor, project); err != nil || report.UnboundSources != 0 {
		t.Fatal("original context could not resume", report, err)
	}
}

func TestRetentionContextKeepsHistoricalMemorySessionAndPrivateDraft(t *testing.T) {
	b, db := setup(t)
	project := createProject(b, "上下文跨编辑历史", "context-history")
	bid := branch(b, project, "正史")
	rid := resource(b, project, bid, "主线记录")
	body := "她沿着河岸寻找故人，最后决定守护药师。"
	release := prepareRelease(b, project, bid, "", "", "memory-context-source", []map[string]any{{"resource_id": rid, "text": body}})
	releaseAdopt(b, project, release)
	releasePublish(b, project, release)
	resourceInfo := b.request("GET", "/api/projects/"+project+"/resources/"+rid, nil, 200)
	root := "/api/projects/" + project + "/branches/" + bid + "/memory"
	sid := b.request("POST", root+"/sessions", service.MemorySessionInput{OperationID: "context-session", Document: service.MemorySessionDocument{Title: "第一轮规划", Notes: "这段私有笔记不应被复制进正文。"}}, 200)["id"].(string)
	input := service.MemoryInput{OperationID: "context-memory", Item: memory.Item{Session: sid, Timeline: "main", Kind: "mainline", Visibility: "author", Text: "主线是寻人和守护。", Sources: []memory.Source{{Resource: rid, Revision: release, Hash: resourceInfo["hash"].(string), Start: 0, End: len([]rune(body))}}}}
	item := decodeMemory(t, b.request("POST", root+"/items", input, 200))
	draft := resource(b, project, bid, "私人草稿")
	b.request("PUT", "/api/projects/"+project+"/resources/"+draft+"/draft", map[string]any{"revision": 0, "base_head": "", "text": "旧版草稿仍在思考人物的去向。"}, 200)
	path := "/api/projects/" + project + "/branches/" + bid + "/knowledge"
	b.request("POST", path+"/index", map[string]any{}, 200)
	result := b.request("POST", path+"/search", map[string]any{"query": "主线守护", "purpose": "author", "limit": 2, "include_working": true, "working_refs": []map[string]string{{"kind": "resource_draft", "id": draft, "version": "1"}}, "memory": service.MemoryCompileInput{Session: sid, Query: "主线守护", RequiredIDs: []string{item.ID}}}, 200)
	if result["session_source"] == nil || len(result["memory"].(map[string]any)["items"].([]any)) != 1 {
		t.Fatal("session or memory not captured")
	}
	b.request("GET", path+"/contexts/"+result["id"].(string), nil, 200)
	actor := b.request("GET", "/api/me", nil, 200)["id"].(string)
	installSourceRegistrationFixture(t, db)
	w, ctx := &service.Workspace{DB: db}, context.Background()
	caller := domain.Actor{ID: actor}
	if err := db.QueryRow("SELECT workspace_id,role FROM users WHERE id=?", actor).Scan(&caller.WorkspaceID, &caller.Role); err != nil {
		t.Fatal(err)
	}
	saved, err := privateJobs(t, db).Knowledge.Search(ctx, caller, project, bid, service.SearchInput{Query: "主线守护", Purpose: "author", Limit: 2, IncludeWorking: true, WorkingRefs: []domain.WorkingRef{{Kind: "resource_draft", ID: draft, Version: "1"}}, Memory: &service.MemoryCompileInput{Session: sid, Query: "主线守护", RequiredIDs: []string{item.ID}}})
	if err != nil {
		t.Fatal("capture original memory on save", err)
	}
	result["id"] = saved.ID
	var original string
	if err := db.QueryRow("SELECT v.snapshot FROM retention_references r JOIN retention_reference_versions v ON v.reference_id=r.id AND v.revision=r.revision WHERE r.project_id=? AND r.owner_table='context_packages' AND r.field_key='payload'", project).Scan(&original); err != nil {
		t.Fatal(err)
	}
	var initial retention.ReferenceVersion
	if err := json.Unmarshal([]byte(original), &initial); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, pin := range initial.Reference.Sources {
		var table string
		if err := db.QueryRow("SELECT owner_table FROM retention_references WHERE id=?", pin.ReferenceID).Scan(&table); err != nil {
			t.Fatal(err)
		}
		seen[table] = true
	}
	for _, table := range []string{"memory_revisions", "memory_sessions", "resource_drafts"} {
		if !seen[table] {
			t.Fatal("missing context source", table)
		}
	}
	// The reserved migration marker intentionally blocks HTTP readiness. Call
	// the same authorized business services directly inside this isolated fixture.
	ipService := &service.IP{Workspace: w, Blobs: storage.LocalBlobs{Root: os.Getenv("STUDIO_BLOB_ROOT")}}
	mem := &service.Memory{IP: ipService}
	if _, err := mem.SaveSession(ctx, domain.Actor{ID: actor}, project, bid, service.MemorySessionInput{OperationID: "context-session-edit", ID: sid, ExpectedRevision: 1, Document: service.MemorySessionDocument{Title: "第二轮规划", Notes: "新的私人思路。"}}); err != nil {
		t.Fatal(err)
	}
	input.Item = item
	input.Item.Text = "新版主线候选。"
	input.ExpectedRevision, input.OperationID = item.Revision, "context-memory-edit"
	if _, err := mem.Save(ctx, domain.Actor{ID: actor}, project, bid, input); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE resource_drafts SET revision=2,body='新版草稿改变了人物去向。' WHERE resource_id=?", draft); err != nil {
		t.Fatal(err)
	}
	if report, err := w.RegisterRetentionSources(ctx, actor, project); err != nil || report.UnboundSources != 0 {
		t.Fatal("old context lost version evidence", report, err)
	}
	var after string
	if err := db.QueryRow("SELECT v.snapshot FROM retention_references r JOIN retention_reference_versions v ON v.reference_id=r.id AND v.revision=r.revision WHERE r.id=?", initial.Reference.ID).Scan(&after); err != nil || after != original {
		t.Fatal("context pins were rebound on source edit", err)
	}
	if _, err := (&service.Knowledge{IP: ipService}).ReadContext(ctx, domain.Actor{ID: actor}, project, bid, result["id"].(string)); !errors.Is(err, domain.ErrConflict) && !errors.Is(err, domain.ErrNotReady) {
		t.Fatal("old context became executable after source edits", err)
	}
	if report, err := w.RegisterRetentionSources(ctx, actor, project); err != nil || report.UpdatedReferences != 0 || report.UnboundSources != 0 {
		t.Fatal("historical context replay was unstable", report, err)
	}
}

func TestRetentionGenerationPinsActualFrozenContextWithoutDispatch(t *testing.T) {
	transport := &generationTransport{response: generationSuccess}
	b, svc, actor, project, first := generationFixture(t, transport)
	db, ctx := svc.IP.Workspace.DB, context.Background()
	bid := first.BranchID
	head := propose(b, project, bid, "", "generation-context-ip", originalSnapshot())
	publish(b, project, bid, head)
	path := "/api/projects/" + project + "/branches/" + bid + "/knowledge"
	b.request("POST", path+"/index", map[string]any{}, 200)
	writer := b.request("POST", path+"/search", map[string]any{"query": "继续写作", "purpose": "writer", "perspective": "lin-1", "limit": 2}, 200)
	other := b.request("POST", path+"/search", map[string]any{"query": "另一个查询", "purpose": "writer", "perspective": "lin-1", "limit": 2}, 200)
	generation, err := svc.Prepare(ctx, actor, project, service.GenerationInput{BranchID: bid, ExpectedIPHead: head, Role: "writer", ContextID: writer["id"].(string), PriceID: first.Frozen.Price.ID, OperationID: "frozen-context-proof", Request: models.Request{Prompt: "依据本次上下文提出候选。", MaxOutputTokens: 100}})
	if err != nil {
		t.Fatal(err)
	}
	installSourceRegistrationFixture(t, db)
	w := svc.IP.Workspace
	if _, err := w.RegisterRetentionSources(ctx, actor.ID, project); err != nil {
		t.Fatal(err)
	}
	rows, err := db.Query("SELECT v.snapshot FROM retention_references r JOIN retention_reference_versions v ON v.reference_id=r.id AND v.revision=r.revision WHERE r.project_id=? AND r.owner_table='generation_tasks' AND r.field_key='frozen.input_hash'", project)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for rows.Next() {
		var raw string
		var v retention.ReferenceVersion
		if err := rows.Scan(&raw); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		if json.Unmarshal([]byte(raw), &v) != nil {
			rows.Close()
			t.Fatal("invalid reference")
		}
		if v.Reference.ObjectHash == generation.Frozen.InputHash {
			found = len(v.Reference.Sources) == 1
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil || !found {
		t.Fatal("frozen input has no context origin", err)
	}
	var original string
	if err := db.QueryRow("SELECT frozen FROM generation_tasks WHERE id=?", generation.ID).Scan(&original); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE generation_tasks SET frozen=JSON_SET(frozen,'$.context_id',?) WHERE id=?", other["id"].(string), generation.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := w.RegisterRetentionSources(ctx, actor.ID, project); !errors.Is(err, domain.ErrNotReady) {
		t.Fatal("declared context not present in frozen request was accepted", err)
	}
	if _, err := db.Exec("UPDATE generation_tasks SET frozen=? WHERE id=?", original, generation.ID); err != nil {
		t.Fatal(err)
	}
	if report, err := w.RegisterRetentionSources(ctx, actor.ID, project); err != nil || report.UpdatedReferences != 0 {
		t.Fatal("failure left partial pins", report, err)
	}
	if transport.calls.Load() != 0 {
		t.Fatal("provenance registration dispatched a provider call")
	}
}
