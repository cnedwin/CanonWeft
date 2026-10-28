package integration

import (
	"context"
	"encoding/json"
	"novelstudio/internal/domain"
	"novelstudio/internal/memory"
	"novelstudio/internal/service"
	"testing"
)

func TestMemorySummaryRecipeProducesTypedCandidate(t *testing.T) {
	transport := &generationTransport{}
	b, gen, actor, pid, seed := generationFixture(t, transport)
	ctx := context.Background()
	if _, err := (&service.Budgets{Workspace: gen.IP.Workspace}).Configure(ctx, actor.ID, service.Budget{Currency: "USD", Limit: 5000000, PerCall: 1000000, Enabled: true, Revision: 1}); err != nil {
		t.Fatal(err)
	}
	bid := seed.BranchID
	ip := propose(b, pid, bid, "", "summary-recipe-ip", originalSnapshot())
	publish(b, pid, bid, ip)
	rid := resource(b, pid, bid, "第一章")
	text := "林舟准备返回雾城，救出同伴。"
	rel := prepareRelease(b, pid, bid, ip, "", "summary-recipe-source", []map[string]any{{"resource_id": rid, "text": text}})
	releaseAdopt(b, pid, rel)
	releasePublish(b, pid, rel)
	src := b.request("GET", "/api/projects/"+pid+"/resources/"+rid, nil, 200)
	root := "/api/projects/" + pid + "/branches/" + bid + "/memory"
	sid := b.request("POST", root+"/sessions", service.MemorySessionInput{OperationID: "summary-recipe-session", Document: service.MemorySessionDocument{Title: "分卷摘要"}}, 200)["id"].(string)
	chapter := memory.Item{Kind: "summary", Visibility: "author", Session: sid, Control: true, Text: text, Summary: &memory.Summary{Level: "chapter", UnitID: service.NewID(), Title: "第一章"}, Sources: []memory.Source{{Resource: rid, Revision: rel, Hash: src["hash"].(string), Start: 0, End: len([]rune(text))}}}
	chapter = decodeMemory(t, b.request("POST", root+"/items", service.MemoryInput{OperationID: "summary-recipe-save", Item: chapter}, 200))
	chapter = decodeMemory(t, b.request("POST", root+"/items/"+chapter.ID+"/adopt", service.MemoryAction{ExpectedRevision: chapter.Revision, OperationID: "summary-recipe-adopt"}, 200))
	target := &service.SummaryTarget{Level: "volume", UnitID: service.NewID(), Title: "第一卷", Timeline: "main"}
	doc := b.request("POST", root+"/extraction-input", service.ExtractionSourceInput{MemoryIDs: []string{chapter.ID}, SummaryTarget: target, ExpectedIPHead: ip, StoryTime: 3, Focus: []string{"summaries"}}, 200)
	raw, _ := json.Marshal(doc)
	installed, err := (&service.Capabilities{IP: gen.IP}).InstallNarrative(ctx, actor, pid, "memory-consolidation")
	if err != nil {
		t.Fatal(err)
	}
	b.request("PUT", "/api/model-bindings/bookkeeper", map[string]any{"connection_id": seed.Frozen.Config.ID, "connection_revision": 1, "revision": 0}, 200)
	contextPack := b.request("POST", "/api/projects/"+pid+"/branches/"+bid+"/knowledge/search", map[string]any{"query": "分卷摘要", "purpose": "critic", "story_time": 3, "limit": 3}, 200)
	in := service.RecipeExecutionInput{RecipeVersionID: installed.RootID, BranchID: bid, ExpectedIPHead: ip, OperationID: "summary-recipe-run", Trial: true, Input: raw, Currency: "USD", BudgetLimitMicros: 1000000, Steps: []service.RecipeStepInput{{Key: "memory-consolidation", PriceID: seed.Frozen.Price.ID, MaxOutputTokens: 2000, ContextID: contextPack["id"].(string), Input: raw}}}
	recipes := &service.RecipeExecutions{Generations: gen}
	run, err := recipes.Prepare(ctx, actor, pid, in)
	if err != nil {
		t.Fatal(err)
	}
	anchor := map[string]any{"resource_id": chapter.ID, "revision": chapter.Revision, "hash": memory.SummaryTextHash(chapter.Text), "start_rune": 0, "end_rune": len([]rune(chapter.Text)), "quote": chapter.Text}
	candidate := map[string]any{"id": "summary-1", "kind": "summary", "layer": "episodic", "subject_id": nil, "holder_id": nil, "object_id": nil, "predicate": "概括", "statement": "第一卷将讲述重返雾城救出同伴。", "context": "plan", "summary_level": "volume", "narrative_role": "none", "scope": doc["scope"], "evidence": []any{anchor}, "disposition": "needs_confirmation", "related_ids": []string{}, "check_result": "not_applicable"}
	out, _ := json.Marshal(map[string]any{"schema": 1, "project_id": pid, "base_revision": ip, "scope": doc["scope"], "status": "CANDIDATE", "candidates": []any{candidate}, "findings": []any{}, "limitations": []string{"仅概括选中的章节摘要"}})
	var response map[string]any
	json.Unmarshal([]byte(generationSuccess), &response)
	response["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)["content"] = string(out)
	wire, _ := json.Marshal(response)
	transport.response = string(wire)
	run, err = recipes.Start(ctx, actor, pid, run.ID, run.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = recipes.Advance(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	run, err = recipes.Read(ctx, actor, pid, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state, err := gen.Execute(ctx, run.Steps[0].GenerationID); err != nil || state != "SUCCEEDED" {
		t.Fatal(state, err)
	}
	if state, err := recipes.Advance(ctx, run.ID); err != nil || state != "WAITING_CONFIRMATION" {
		t.Fatal(state, err)
	}
	run, err = recipes.Read(ctx, actor, pid, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	req := service.MemoryImportInput{RunID: run.ID, StepID: run.Steps[0].ID, ExpectedRevision: run.Revision, OutputHash: run.Steps[0].OutputHash, SessionID: sid, Timeline: "other", CandidateIDs: []string{"summary-1"}}
	b.request("POST", root+"/import-extraction", req, 400)
	req.Timeline = "main"
	imported := b.request("POST", root+"/import-extraction", req, 200)
	item := decodeMemory(t, imported["items"].([]any)[0].(map[string]any))
	if item.Summary == nil || item.Summary.Level != "volume" || item.Summary.UnitID != target.UnitID || len(item.Summary.Inputs) != 1 || len(item.Sources) != 0 || !item.Control || item.State != "candidate" {
		t.Fatal("lost typed hierarchy", item)
	}
	if item.Summary.Inputs[0].Revision != chapter.Revision {
		t.Fatal("lost exact pin")
	}
	adopted := decodeMemory(t, b.request("POST", root+"/items/"+item.ID+"/adopt", service.MemoryAction{ExpectedRevision: item.Revision, OperationID: "summary-volume-adopt"}, 200))
	if adopted.State != "adopted_plan" {
		t.Fatal("plan promoted to fact")
	}
	if transport.calls.Load() != 1 {
		t.Fatal("unexpected paid-dispatch substitute count")
	}
}

func TestMemorySummaryHierarchyInvalidationAndHistoricalProvenance(t *testing.T) {
	testMemorySummaryHierarchy(t, false)
}
func TestMemorySummaryRawSourceInvalidatesAncestorsBeforeCache(t *testing.T) {
	testMemorySummaryHierarchy(t, true)
}
func testMemorySummaryHierarchy(t *testing.T, changeRawSource bool) {
	b, _ := setup(t)
	pid := createProject(b, "分层摘要", "summary-project")
	bid := branch(b, pid, "正史")
	ip := propose(b, pid, bid, "", "summary-ip", originalSnapshot())
	publish(b, pid, bid, ip)
	rid := resource(b, pid, bid, "章节原文")
	text := "林舟救出同伴，放下复仇。"
	rel := prepareRelease(b, pid, bid, ip, "", "summary-source", []map[string]any{{"resource_id": rid, "text": text, "policy": domain.KnowledgePolicy{Space: "evidence", Visibility: "public"}}})
	releaseAdopt(b, pid, rel)
	releasePublish(b, pid, rel)
	src := b.request("GET", "/api/projects/"+pid+"/resources/"+rid, nil, 200)
	root := "/api/projects/" + pid + "/branches/" + bid + "/memory"
	sid := b.request("POST", root+"/sessions", service.MemorySessionInput{OperationID: "summary-session", Document: service.MemorySessionDocument{Title: "摘要整理"}}, 200)["id"].(string)
	var items []memory.Item
	for _, level := range []string{"chapter", "volume", "book"} {
		item := memory.Item{Kind: "summary", Visibility: "author", Timeline: "main", Session: sid, From: 3, RevealAt: 3, Text: level + "：放下复仇，守护同伴。", Summary: &memory.Summary{Level: level, UnitID: service.NewID(), Title: level}}
		if len(items) == 0 {
			item.Sources = []memory.Source{{Resource: rid, Revision: rel, Hash: src["hash"].(string), Start: 0, End: len([]rune(text))}}
		} else {
			child := items[len(items)-1]
			item.Summary.Inputs = []memory.SummaryPin{{ID: child.ID, Revision: child.Revision, Hash: memory.SummaryTextHash(child.Text)}}
		}
		saved := decodeMemory(t, b.request("POST", root+"/items", service.MemoryInput{OperationID: "save-" + level, Item: item}, 200))
		adopted := decodeMemory(t, b.request("POST", root+"/items/"+saved.ID+"/adopt", service.MemoryAction{ExpectedRevision: saved.Revision, OperationID: "adopt-" + level}, 200))
		items = append(items, adopted)
	}
	input := service.ExtractionSourceInput{MemoryIDs: []string{items[0].ID}, SummaryTarget: &service.SummaryTarget{Level: "volume", UnitID: service.NewID(), Title: "第一卷", Timeline: "main"}, ExpectedIPHead: ip, StoryTime: 3, Focus: []string{"summaries"}}
	doc := b.request("POST", root+"/extraction-input", input, 200)
	childSource := doc["sources"].([]any)[0].(map[string]any)
	if childSource["kind"] != "memory" || childSource["revision"] != items[0].Revision || childSource["text"] != items[0].Text {
		t.Fatal("summary not frozen", doc)
	}
	input.SummaryTarget.Level = "book"
	b.request("POST", root+"/extraction-input", input, 400)
	input.SummaryTarget.Level = "volume"
	input.SummaryTarget.Timeline = "other"
	b.request("POST", root+"/extraction-input", input, 400)
	input.SummaryTarget.Timeline = "main"
	// Public history may be inspected, but original private drafts stay private.
	member := b.request("POST", "/api/members", map[string]any{"username": "summary-editor", "password": "summary-editor-password-123", "role": "editor"}, 201)
	b.request("PUT", "/api/projects/"+pid+"/members/"+member["id"].(string), map[string]any{"role": "editor"}, 200)
	other := &browser{t: t, handler: b.handler}
	other.request("POST", "/api/login", map[string]any{"username": "summary-editor", "password": "summary-editor-password-123"}, 200)
	path := root + "/items/" + items[0].ID + "/revisions/" + items[0].Revision
	other.request("GET", path, nil, 200)
	compile := service.MemoryCompileInput{Purpose: "author", Perspective: "lin-1", Timeline: "main", StoryTime: 3, Budget: 65536, RequiredIDs: []string{items[2].ID}}
	b.request("POST", root+"/compile", compile, 200)
	if changeRawSource {
		next := prepareRelease(b, pid, bid, ip, "", "summary-raw-change", []map[string]any{{"resource_id": rid, "expected_head": rel, "text": "正文已经改成其他结局。", "policy": domain.KnowledgePolicy{Space: "evidence", Visibility: "public"}}})
		releaseAdopt(b, pid, next)
		releasePublish(b, pid, next)
		b.request("POST", root+"/compile", compile, 409)
		b.request("POST", root+"/extraction-input", input, 409)
		list := b.request("GET", root, nil, 200)
		for _, raw := range list["items"].([]any) {
			if raw.(map[string]any)["state"] != "stale" {
				t.Fatal("raw source did not invalidate every ancestor")
			}
		}
		other.request("GET", path, nil, 200)
		return
	}
	edit := items[0]
	edit.Session = sid
	edit.Text = "不同的章节结局"
	edited := decodeMemory(t, b.request("POST", root+"/items", service.MemoryInput{OperationID: "summary-change", ExpectedRevision: edit.Revision, Item: edit}, 200))
	b.request("POST", root+"/extraction-input", input, 403)
	b.request("POST", root+"/compile", compile, 409)
	list := b.request("GET", root, nil, 200)
	stale := 0
	for _, raw := range list["items"].([]any) {
		row := raw.(map[string]any)
		if row["state"] == "stale" {
			stale++
		}
	}
	if stale != 2 {
		t.Fatalf("expected two stale ancestors, got %d", stale)
	}
	b.request("GET", path, nil, 200)
	other.request("GET", path, nil, 200)
	other.request("GET", root+"/items/"+edited.ID+"/revisions/"+edited.Revision, nil, 403)
	// Re-adoption has a new exact revision; old parents cannot silently use it.
	b.request("POST", root+"/items/"+edited.ID+"/adopt", service.MemoryAction{ExpectedRevision: edited.Revision, OperationID: "readopt-chapter"}, 200)
	b.request("POST", root+"/compile", compile, 409)
	b.request("POST", root+"/extraction-input", input, 200)
}
