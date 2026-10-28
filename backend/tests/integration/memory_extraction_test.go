package integration

import (
	"context"
	"encoding/json"
	"errors"
	"novelstudio/internal/domain"
	"novelstudio/internal/service"
	"strings"
	"testing"
)

func TestMemoryExtractionBuildsAuthoritativeInput(t *testing.T) {
	b, _ := setup(t)
	pid := createProject(b, "记忆提炼", "extraction-project")
	bid := branch(b, pid, "正史")
	ip := propose(b, pid, bid, "", "extraction-ip", originalSnapshot())
	publish(b, pid, bid, ip)
	rid := resource(b, pid, bid, "章节依据")
	release := prepareRelease(b, pid, bid, ip, "", "extraction-source", []map[string]any{{"resource_id": rid, "text": "🌙林舟藏起钥匙，妹妹尚不知情。"}})
	releaseAdopt(b, pid, release)
	releasePublish(b, pid, release)
	root := "/api/projects/" + pid + "/branches/" + bid + "/memory"
	input := map[string]any{"resource_ids": []string{rid}, "expected_ip_head": ip, "story_time": 3, "focus": []string{"facts", "summaries"}, "instructions": "保留人物的信息差"}
	result := b.request("POST", root+"/extraction-input", input, 200)
	if result["project_id"] != pid || result["base_revision"] != ip {
		t.Fatal("input identity", result)
	}
	sources := result["sources"].([]any)
	source := sources[0].(map[string]any)
	if source["revision"] != release || source["text"] != "🌙林舟藏起钥匙，妹妹尚不知情。" {
		t.Fatal("source not resolved", source)
	}
	other := branch(b, pid, "改编")
	b.request("POST", "/api/projects/"+pid+"/branches/"+other+"/memory/extraction-input", input, 409)
	input["resource_ids"] = []string{rid, rid}
	b.request("POST", root+"/extraction-input", input, 400)
	input["resource_ids"] = []string{rid}
	input["expected_ip_head"] = "old-head"
	b.request("POST", root+"/extraction-input", input, 409)
}

func TestMemoryExtractionRecipeImportAndStaleDispatch(t *testing.T) {
	transport := &generationTransport{}
	b, gen, actor, pid, seed := generationFixture(t, transport)
	ctx := context.Background()
	// The complete preset includes its full schemas and examples in the prompt.
	// This test-only allowance exercises the existing gate without a real API.
	if _, err := (&service.Budgets{Workspace: gen.IP.Workspace}).Configure(ctx, actor.ID, service.Budget{Currency: "USD", Limit: 5000000, PerCall: 1000000, Enabled: true, Revision: 1}); err != nil {
		t.Fatal(err)
	}
	bid := seed.BranchID
	ip := propose(b, pid, bid, "", "extract-ip", originalSnapshot())
	publish(b, pid, bid, ip)
	rid := resource(b, pid, bid, "章节")
	text := "🌙林舟藏起钥匙，妹妹尚不知情。"
	rel := prepareRelease(b, pid, bid, ip, "", "extract-publish", []map[string]any{{"resource_id": rid, "text": text}})
	releaseAdopt(b, pid, rel)
	releasePublish(b, pid, rel)
	root := "/api/projects/" + pid + "/branches/" + bid + "/memory"
	doc := b.request("POST", root+"/extraction-input", map[string]any{"resource_ids": []string{rid}, "expected_ip_head": ip, "story_time": 3, "focus": []string{"facts", "summaries"}, "instructions": "保留信息差"}, 200)
	raw, _ := json.Marshal(doc)
	installed, err := (&service.Capabilities{IP: gen.IP}).InstallNarrative(ctx, actor, pid, "memory-consolidation")
	if err != nil {
		t.Fatal(err)
	}
	b.request("PUT", "/api/model-bindings/bookkeeper", map[string]any{"connection_id": seed.Frozen.Config.ID, "connection_revision": 1, "revision": 0}, 200)
	in := service.RecipeExecutionInput{RecipeVersionID: installed.RootID, BranchID: bid, ExpectedIPHead: ip, OperationID: "extract-recipe", Trial: true, Input: raw, Currency: "USD", BudgetLimitMicros: 1000000, Steps: []service.RecipeStepInput{{Key: "memory-consolidation", PriceID: seed.Frozen.Price.ID, MaxOutputTokens: 2000, Input: raw}}}
	recipes := &service.RecipeExecutions{Generations: gen}
	bad := in
	bad.OperationID = "forged-extract"
	bad.Steps = append([]service.RecipeStepInput{}, in.Steps...)
	bad.Steps[0].Input = json.RawMessage(strings.Replace(string(raw), text, "伪造来源", 1))
	if _, err = recipes.Prepare(ctx, actor, pid, bad); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("forged source accepted", err)
	}
	run, err := recipes.Prepare(ctx, actor, pid, in)
	if err != nil {
		t.Fatal(err)
	}
	source := doc["sources"].([]any)[0].(map[string]any)
	candidate := map[string]any{"id": "c1", "kind": "belief", "layer": "episodic", "subject_id": "lin-1", "holder_id": "lin-1", "object_id": "key", "predicate": "藏匿", "statement": "林舟认为钥匙安全。", "context": "dream", "summary_level": "none", "narrative_role": "none", "scope": doc["scope"], "evidence": []any{map[string]any{"resource_id": rid, "revision": rel, "hash": source["hash"], "start_rune": 0, "end_rune": len([]rune(text)), "quote": text}}, "disposition": "needs_confirmation", "related_ids": []string{}, "check_result": "not_applicable"}
	output := map[string]any{"schema": 1, "project_id": pid, "base_revision": ip, "scope": doc["scope"], "status": "CANDIDATE", "candidates": []any{candidate},
		"findings":    []any{map[string]any{"id": "finding-1", "kind": "pending_confirmation", "description": "梦境与现实不能合并", "question": "人物是否已经醒来？", "candidate_ids": []string{"c1"}, "evidence": candidate["evidence"]}},
		"limitations": []string{"只检查当前章节，未验证全书"}}
	out, _ := json.Marshal(output)
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
		t.Fatal(state, err, "run", run.State, run.Reason)
	}
	if state, err := recipes.Advance(ctx, run.ID); err != nil || state != "WAITING_CONFIRMATION" {
		t.Fatal(state, err, "run", run.State, run.Reason)
	}
	run, err = recipes.Read(ctx, actor, pid, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	session := b.request("POST", root+"/sessions", service.MemorySessionInput{OperationID: "extract-session", Document: service.MemorySessionDocument{Title: "提炼结果"}}, 200)
	req := service.MemoryImportInput{RunID: run.ID, StepID: run.Steps[0].ID, ExpectedRevision: run.Revision, OutputHash: run.Steps[0].OutputHash, SessionID: session["id"].(string), Timeline: "main", CandidateIDs: []string{"c1", "missing"}}
	b.request("POST", root+"/import-extraction", req, 400)
	list := b.request("GET", root, nil, 200)
	if len(list["items"].([]any)) != 0 {
		t.Fatal("batch partially committed")
	}
	req.CandidateIDs = []string{"c1"}
	wrong := req
	wrong.OutputHash = strings.Repeat("0", 64)
	b.request("POST", root+"/import-extraction", wrong, 409)
	member := b.request("POST", "/api/members", map[string]any{"username": "extraction-editor", "password": "extraction-password-123", "role": "editor"}, 201)
	b.request("PUT", "/api/projects/"+pid+"/members/"+member["id"].(string), map[string]any{"role": "editor"}, 200)
	other := &browser{t: t, handler: b.handler}
	other.request("POST", "/api/login", map[string]any{"username": "extraction-editor", "password": "extraction-password-123"}, 200)
	other.request("POST", root+"/import-extraction", req, 403)
	imported := b.request("POST", root+"/import-extraction", req, 200)
	item := decodeMemory(t, imported["items"].([]any)[0].(map[string]any))
	if item.Kind != "hypothesis" || item.State != "candidate" || item.Actor == "" || !strings.Contains(item.Text, "梦境") || !strings.Contains(item.Text, "lin-1") {
		t.Fatal("lost candidate meaning", item)
	}
	if !strings.Contains(item.Text, "梦境与现实不能合并") || !strings.Contains(item.Text, "未验证全书") || len(item.Sources) != 2 {
		t.Fatal("lost review or its evidence", item)
	}
	retry := b.request("POST", root+"/import-extraction", req, 200)
	if retry["items"].([]any)[0].(map[string]any)["id"] != item.ID {
		t.Fatal("duplicate import")
	}
	item.Text = "作者修改的梦境推测"
	edited := decodeMemory(t, b.request("POST", root+"/items", service.MemoryInput{Item: item, ExpectedRevision: item.Revision, OperationID: "edit-import"}, 200))
	retry = b.request("POST", root+"/import-extraction", req, 200)
	if retry["items"].([]any)[0].(map[string]any)["revision"] != edited.Revision {
		t.Fatal("retry clobbered edit")
	}
	b.request("POST", root+"/items/"+edited.ID+"/adopt", service.MemoryAction{ExpectedRevision: edited.Revision, OperationID: "adopt-import"}, 200)
	// Prepare a second run, then update the authoritative source before Start.
	in.OperationID = "stale-extract"
	contextPack := b.request("POST", "/api/projects/"+pid+"/branches/"+bid+"/knowledge/search", map[string]any{"query": "人物信息差", "purpose": "critic", "story_time": 3, "limit": 3}, 200)
	in.Steps[0].ContextID = contextPack["id"].(string)
	stale, err := recipes.Prepare(ctx, actor, pid, in)
	if err != nil {
		t.Fatal(err)
	}
	in.OperationID = "queued-extract"
	queued, err := recipes.Prepare(ctx, actor, pid, in)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = recipes.Start(ctx, actor, pid, queued.ID, queued.Revision); err != nil {
		t.Fatal(err)
	}
	if state, err := recipes.Advance(ctx, queued.ID); err != nil || state != "RUNNING" {
		t.Fatal(state, err)
	}
	queued, err = recipes.Read(ctx, actor, pid, queued.ID)
	if err != nil {
		t.Fatal(err)
	}
	rel2 := prepareRelease(b, pid, bid, ip, "", "extract-source-update", []map[string]any{{"resource_id": rid, "expected_head": rel, "text": text + "钥匙后来丢失。"}})
	releaseAdopt(b, pid, rel2)
	releasePublish(b, pid, rel2)
	if _, err = recipes.Start(ctx, actor, pid, stale.ID, stale.Revision); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("stale source started", err)
	}
	b.request("POST", root+"/import-extraction", req, 409)
	if state, err := gen.Execute(ctx, queued.Steps[0].GenerationID); state != "BLOCKED" || !errors.Is(err, domain.ErrConflict) {
		t.Fatal("stale queued extraction dispatched", state, err)
	}
	if transport.calls.Load() != 1 {
		t.Fatal("unexpected model dispatch", transport.calls.Load())
	}
}
