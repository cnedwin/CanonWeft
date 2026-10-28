package integration

import (
	"context"
	"encoding/json"
	"errors"
	"novelstudio/internal/domain"
	"novelstudio/internal/memory"
	"novelstudio/internal/models"
	"novelstudio/internal/service"
	"testing"
)

func TestMemoryRefreshRejectsSourcePolicyWideningBeforePreparation(t *testing.T) {
	b, _ := setup(t)
	pid := createProject(b, "重整权限", "refresh-policy-project")
	bid := branch(b, pid, "正史")
	ip := propose(b, pid, bid, "", "refresh-policy-ip", originalSnapshot())
	publish(b, pid, bid, ip)
	rid := resource(b, pid, bid, "原始依据")
	rel := prepareRelease(b, pid, bid, ip, "", "refresh-policy-source", []map[string]any{{"resource_id": rid, "text": "公开的章节原文。", "policy": domain.KnowledgePolicy{Space: "evidence", Visibility: "public"}}})
	releaseAdopt(b, pid, rel)
	releasePublish(b, pid, rel)
	src := b.request("GET", "/api/projects/"+pid+"/resources/"+rid, nil, 200)
	root := "/api/projects/" + pid + "/branches/" + bid + "/memory"
	sid := b.request("POST", root+"/sessions", service.MemorySessionInput{OperationID: "policy-session", Document: service.MemorySessionDocument{Title: "修改"}}, 200)["id"].(string)
	item := memory.Item{Kind: "summary", Session: sid, Visibility: "public", Text: "公开摘要", Summary: &memory.Summary{Level: "chapter", UnitID: service.NewID(), Title: "第一章"}, Sources: []memory.Source{{Resource: rid, Revision: rel, Hash: src["hash"].(string), Start: 0, End: 4}}}
	item = decodeMemory(t, b.request("POST", root+"/items", service.MemoryInput{OperationID: "policy-save", Item: item}, 200))
	item = decodeMemory(t, b.request("POST", root+"/items/"+item.ID+"/adopt", service.MemoryAction{OperationID: "policy-adopt", ExpectedRevision: item.Revision}, 200))
	input := service.ExtractionSourceInput{ExpectedIPHead: ip, ResourceIDs: []string{rid}, StoryTime: 0, Focus: []string{"summaries"}, SummaryTarget: &service.SummaryTarget{ItemID: item.ID, Revision: item.Revision, Level: "chapter", UnitID: item.Summary.UnitID, Title: item.Summary.Title, Timeline: "main"}}
	b.request("POST", root+"/extraction-input", input, 200)
	next := prepareRelease(b, pid, bid, ip, "", "refresh-policy-change", []map[string]any{{"resource_id": rid, "expected_head": rel, "text": "现在包含作者秘密。", "policy": domain.KnowledgePolicy{Space: "evidence", Visibility: "author"}}})
	releaseAdopt(b, pid, next)
	releasePublish(b, pid, next)
	b.request("POST", root+"/extraction-input", input, 400)
	input.SummaryTarget.Title = "换一个标题绕过"
	b.request("POST", root+"/extraction-input", input, 400)
}

func TestMemoryRefreshRepairsPinnedHierarchyWithoutDuplicateIdentity(t *testing.T) {
	transport := &generationTransport{}
	b, gen, actor, pid, seed := generationFixture(t, transport)
	ctx := context.Background()
	if _, err := (&service.Budgets{Workspace: gen.IP.Workspace}).Configure(ctx, actor.ID, service.Budget{Currency: "USD", Limit: 5000000, PerCall: 1000000, Enabled: true, Revision: 1}); err != nil {
		t.Fatal(err)
	}
	bid := seed.BranchID
	ip := propose(b, pid, bid, "", "refresh-ip", originalSnapshot())
	publish(b, pid, bid, ip)
	rid := resource(b, pid, bid, "第一章")
	text := "林舟救出同伴。"
	rel := prepareRelease(b, pid, bid, ip, "", "refresh-initial", []map[string]any{{"resource_id": rid, "text": text}})
	releaseAdopt(b, pid, rel)
	releasePublish(b, pid, rel)
	src := b.request("GET", "/api/projects/"+pid+"/resources/"+rid, nil, 200)
	root := "/api/projects/" + pid + "/branches/" + bid + "/memory"
	sid := b.request("POST", root+"/sessions", service.MemorySessionInput{OperationID: "refresh-session", Document: service.MemorySessionDocument{Title: "增量整理"}}, 200)["id"].(string)
	var chain []memory.Item
	for _, level := range []string{"chapter", "volume"} {
		item := memory.Item{Kind: "summary", Pinned: true, Control: true, Visibility: "author", Session: sid, Text: text, Summary: &memory.Summary{Level: level, UnitID: service.NewID(), Title: level}}
		if len(chain) == 0 {
			item.Sources = []memory.Source{{Resource: rid, Revision: rel, Hash: src["hash"].(string), Start: 0, End: len([]rune(text))}}
		} else {
			child := chain[0]
			item.Summary.Inputs = []memory.SummaryPin{{ID: child.ID, Revision: child.Revision, Hash: memory.SummaryTextHash(child.Text)}}
		}
		item = decodeMemory(t, b.request("POST", root+"/items", service.MemoryInput{OperationID: "refresh-save-" + level, Item: item}, 200))
		item = decodeMemory(t, b.request("POST", root+"/items/"+item.ID+"/adopt", service.MemoryAction{ExpectedRevision: item.Revision, OperationID: "refresh-adopt-" + level}, 200))
		chain = append(chain, item)
	}
	next := prepareRelease(b, pid, bid, ip, "", "refresh-changed", []map[string]any{{"resource_id": rid, "expected_head": rel, "text": "林舟放下复仇，救出同伴。"}})
	releaseAdopt(b, pid, next)
	releasePublish(b, pid, next)
	installed, err := (&service.Capabilities{IP: gen.IP}).InstallNarrative(ctx, actor, pid, "memory-consolidation")
	if err != nil {
		t.Fatal(err)
	}
	b.request("PUT", "/api/model-bindings/bookkeeper", map[string]any{"connection_id": seed.Frozen.Config.ID, "connection_revision": 1, "revision": 0}, 200)
	recipes := &service.RecipeExecutions{Generations: gen}
	member := b.request("POST", "/api/members", map[string]any{"username": "refresh-editor", "password": "refresh-editor-password-123", "role": "editor"}, 201)
	b.request("PUT", "/api/projects/"+pid+"/members/"+member["id"].(string), map[string]any{"role": "editor"}, 200)
	other := &browser{t: t, handler: b.handler}
	other.request("POST", "/api/login", map[string]any{"username": "refresh-editor", "password": "refresh-editor-password-123"}, 200)
	for index, old := range chain {
		list := b.request("GET", root, nil, 200)
		tasks := list["refresh_tasks"].([]any)
		var task map[string]any
		for _, raw := range tasks {
			v := raw.(map[string]any)
			if v["input"].(map[string]any)["summary_target"].(map[string]any)["item_id"] == old.ID {
				task = v
			}
		}
		if task == nil || task["state"] != "READY" {
			t.Fatal("next level not ready", list)
		}
		if index == 0 && tasks[1].(map[string]any)["state"] != "WAITING_INPUT" {
			t.Fatal("parent did not wait")
		}
		input := task["input"].(map[string]any)
		input["expected_ip_head"] = ip
		doc := b.request("POST", root+"/extraction-input", input, 200)
		raw, _ := json.Marshal(doc)
		knowledge := "/api/projects/" + pid + "/branches/" + bid + "/knowledge/search"
		b.request("POST", knowledge, map[string]any{"query": "重整摘要", "purpose": "critic", "story_time": 3, "limit": 3}, 409)
		cp := b.request("POST", knowledge, map[string]any{"query": "重整摘要", "purpose": "critic", "story_time": 3, "limit": 3, "memory": map[string]any{"refresh": memory.RefreshScope{ID: old.ID, Revision: old.Revision}}}, 200)
		if _, err := gen.Prepare(ctx, actor, pid, service.GenerationInput{BranchID: bid, ExpectedIPHead: ip, Role: "bookkeeper", PriceID: seed.Frozen.Price.ID, ContextID: cp["id"].(string), OperationID: "generic-refresh-" + old.ID, Request: models.Request{Prompt: "绕过必需摘要", MaxOutputTokens: 100}}); !errors.Is(err, domain.ErrValidation) {
			t.Fatal("generic task consumed repair context", err)
		}
		ri := service.RecipeExecutionInput{RecipeVersionID: installed.RootID, BranchID: bid, ExpectedIPHead: ip, OperationID: "refresh-run-" + old.ID, Trial: true, Input: raw, Currency: "USD", BudgetLimitMicros: 1000000, Steps: []service.RecipeStepInput{{Key: "memory-consolidation", PriceID: seed.Frozen.Price.ID, MaxOutputTokens: 2000, ContextID: cp["id"].(string), Input: raw}}}
		run, err := recipes.Prepare(ctx, actor, pid, ri)
		if err != nil {
			t.Fatal(err)
		}
		ri.OperationID += "-concurrent"
		concurrent, err := recipes.Prepare(ctx, actor, pid, ri)
		if err != nil {
			t.Fatal(err)
		}
		var anchors []any
		for _, raw := range doc["sources"].([]any) {
			source := raw.(map[string]any)
			body := source["text"].(string)
			anchors = append(anchors, map[string]any{"resource_id": source["resource_id"], "revision": source["revision"], "hash": source["hash"], "start_rune": 0, "end_rune": len([]rune(body)), "quote": body})
		}
		candidate := map[string]any{"id": "replacement", "kind": "summary", "layer": "narrative", "subject_id": nil, "holder_id": nil, "object_id": nil, "predicate": "概括", "statement": "林舟选择守护同伴而非复仇。", "context": "plan", "summary_level": old.Summary.Level, "narrative_role": "none", "scope": doc["scope"], "evidence": anchors, "disposition": "needs_confirmation", "related_ids": []string{}, "check_result": "not_applicable"}
		out, _ := json.Marshal(map[string]any{"schema": 1, "project_id": pid, "base_revision": ip, "scope": doc["scope"], "status": "CANDIDATE", "candidates": []any{candidate}, "findings": []any{}, "limitations": []string{"只概括所选范围"}})
		var wire map[string]any
		json.Unmarshal([]byte(generationSuccess), &wire)
		wire["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)["content"] = string(out)
		response, _ := json.Marshal(wire)
		transport.response = string(response)
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
		if state, err := gen.Execute(ctx, run.Steps[0].GenerationID); state != "SUCCEEDED" || err != nil {
			t.Fatal(state, err)
		}
		if state, err := recipes.Advance(ctx, run.ID); state != "WAITING_CONFIRMATION" || err != nil {
			t.Fatal(state, err)
		}
		run, err = recipes.Read(ctx, actor, pid, run.ID)
		if err != nil {
			t.Fatal(err)
		}
		req := service.MemoryImportInput{RunID: run.ID, StepID: run.Steps[0].ID, ExpectedRevision: run.Revision, OutputHash: run.Steps[0].OutputHash, SessionID: sid, Timeline: "main", CandidateIDs: []string{"replacement", "other"}}
		b.request("POST", root+"/import-extraction", req, 400)
		req.CandidateIDs = []string{"replacement"}
		result := b.request("POST", root+"/import-extraction", req, 200)
		item := decodeMemory(t, result["items"].([]any)[0].(map[string]any))
		if item.ID != old.ID || item.Revision == old.Revision || !item.Pinned || !item.Control || item.State != "candidate" {
			t.Fatal("replacement lost identity or policy", item)
		}
		other.request("POST", root+"/extraction-input", input, 403)
		if _, err = recipes.Start(ctx, actor, pid, concurrent.ID, concurrent.Revision); !errors.Is(err, domain.ErrConflict) {
			t.Fatal("concurrent old target ran", err)
		}
		item.Text += "作者精修。"
		edited := decodeMemory(t, b.request("POST", root+"/items", service.MemoryInput{OperationID: "refresh-edit-" + old.ID, Item: item, ExpectedRevision: item.Revision}, 200))
		retry := b.request("POST", root+"/import-extraction", req, 200)
		if retry["items"].([]any)[0].(map[string]any)["revision"] != edited.Revision {
			t.Fatal("retry clobbered edit")
		}
		if _, err = recipes.Confirm(ctx, actor, pid, run.ID, run.Steps[0].Key, run.Revision, run.Steps[0].OutputHash); err != nil {
			t.Fatal("imported refresh cannot finish review", err)
		}
		if state, err := recipes.Advance(ctx, run.ID); err != nil || state != "COMPLETED" {
			t.Fatal("imported refresh not completed", state, err)
		}
		b.request("POST", root+"/items/"+item.ID+"/adopt", service.MemoryAction{OperationID: "refresh-final-" + old.ID, ExpectedRevision: edited.Revision}, 200)
	}
	list := b.request("GET", root, nil, 200)
	if len(list["items"].([]any)) != 2 || list["refresh_tasks"] != nil {
		t.Fatal("refresh duplicated identity or left stale task", list)
	}
	b.request("POST", root+"/compile", service.MemoryCompileInput{Purpose: "writer", Perspective: "lin-1", Timeline: "main", StoryTime: 3, Budget: 65536}, 200)
	if transport.calls.Load() != 2 {
		t.Fatal("unexpected model attempts", transport.calls.Load())
	}
}
