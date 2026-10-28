package integration

import (
	"context"
	"encoding/json"
	"novelstudio/internal/models"
	"novelstudio/internal/service"
	"strings"
	"testing"
)

func TestGenerationRepairSavesAnchoredCandidateWithReviewLineage(t *testing.T) {
	transport := &generationTransport{response: generationSuccess}
	b, svc, a, pid, original := generationFixture(t, transport)
	ctx := context.Background()
	bid, root := original.BranchID, "/api/projects/"+pid
	installed := b.request("POST", root+"/capabilities/presets/continuity", map[string]any{}, 201)
	if again := b.request("POST", root+"/capabilities/presets/continuity", map[string]any{}, 201); again["root_id"] != installed["root_id"] {
		t.Fatal("preset install duplicated")
	}
	b.request("PUT", "/api/model-bindings/continuity_editor", map[string]any{"connection_id": original.Frozen.Config.ID, "connection_revision": 1, "revision": 0}, 200)
	base := propose(b, pid, bid, "", "repair-base", originalSnapshot())
	publish(b, pid, bid, base)
	id := resource(b, pid, bid, "角色初见")
	source := dependencyFromCatalog(t, b, pid, "entity", "lin-1")
	first := prepareRelease(b, pid, bid, base, "", "repair-original", []map[string]any{{"resource_id": id, "text": "🌙旧名推门而入。", "dependencies": map[string]any{"inputs": []any{map[string]any{"source": source, "kind": "representation", "reason": "角色称呼"}}}}})
	releaseAdopt(b, pid, first)
	releasePublish(b, pid, first)
	snapshot := originalSnapshot()
	snapshot.Entities[0].Description = "角色称呼调整，保留行为和主线"
	candidate := propose(b, pid, bid, base, "repair-ip", snapshot)
	plan := b.request("POST", root+"/changes", map[string]any{"operation_id": "repair-plan", "branch_id": bid, "candidate_id": candidate}, 201)
	r, err := svc.IP.ReadResource(ctx, a, pid, id)
	if err != nil {
		t.Fatal(err)
	}
	output, _ := json.Marshal(map[string]any{"schema": 1, "status": "CANDIDATE", "plan_id": plan["id"], "summary": "只修正称呼，保留推门行动。", "patches": []any{map[string]any{"resource_id": id, "base_revision": r.Head, "base_hash": r.Hash, "start_rune": 1, "end_rune": 3, "expected_text": "旧名", "replacement": "林舟", "reason": "称呼与候选设定一致"}}})
	provider, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]any{"content": string(output)}}}, "usage": map[string]any{"prompt_tokens": 20, "completion_tokens": 10}})
	transport.response = string(provider)
	g, err := svc.Prepare(ctx, a, pid, service.GenerationInput{BranchID: bid, ExpectedIPHead: base, Role: "continuity_editor", PriceID: original.Frozen.Price.ID, OperationID: "repair-generation", ChangePlanID: plan["id"].(string), Request: models.Request{Prompt: "调整冲突称呼，保留其他内容", MaxOutputTokens: 1000}})
	if err != nil {
		t.Fatal(err)
	}
	path := root + "/generations/" + g.ID + "/patches"
	b.request("GET", path, nil, 409)
	if _, err = svc.Start(ctx, a, pid, g.ID); err != nil {
		t.Fatal(err)
	}
	if state, e := svc.Execute(ctx, g.ID); e != nil || state != "SUCCEEDED" {
		t.Fatal(state, e)
	}
	if !strings.Contains(transport.payload, "continuity-repair") || !strings.Contains(transport.payload, "body_hash") {
		t.Fatal("preset and actual anchors not frozen")
	}
	preview := b.request("GET", path, nil, 200)
	if len(preview["items"].([]any)) != 1 {
		t.Fatal(preview)
	}
	if preview["save_blocked_reason"] == nil {
		t.Fatal("unadopted IP prerequisite missing")
	}
	b.request("POST", path, map[string]any{}, 400)
	b.request("POST", root+"/branches/"+bid+"/candidates/"+candidate+"/adopt", map[string]any{}, 200)
	preview = b.request("GET", path, nil, 200)
	if preview["save_blocked_reason"] != nil {
		t.Fatal("confirmed IP remains blocked", preview)
	}
	prepared := b.request("POST", path, map[string]any{}, 201)
	b.request("POST", path, map[string]any{"approved": true}, 400)
	releaseID := prepared["id"].(string)
	manifest := prepared["manifest"].(map[string]any)
	if manifest["ip_candidate_id"] != candidate {
		t.Fatal("joint IP candidate lost", manifest)
	}
	origin := manifest["artifacts"].([]any)[0].(map[string]any)["origin"].(map[string]any)
	if origin["kind"] != "derived" || origin["review_required"] != true || origin["generation"].(map[string]any)["generation_id"] != g.ID {
		t.Fatal("AI lineage missing", origin)
	}
	if again := b.request("POST", path, map[string]any{}, 201); again["id"] != releaseID {
		t.Fatal("duplicate repair", again)
	}
	current, err := svc.IP.ReadResource(ctx, a, pid, id)
	if err != nil || current.Text != r.Text || current.Head != r.Head {
		t.Fatal("candidate changed original", current, err)
	}
	// The candidate cannot bypass the existing independent-review publication gate.
	if _, err = svc.IP.AdoptRelease(ctx, a, pid, releaseID); err == nil {
		t.Fatal("unreviewed AI repair adopted")
	}
	// An unrelated material changes the graph. Old preview is no longer usable;
	// an acknowledged saved candidate is still returned as the same receipt.
	other := resource(b, pid, bid, "后续材料")
	changed := prepareRelease(b, pid, bid, base, "", "repair-later", []map[string]any{{"resource_id": other, "text": "后续正式材料"}})
	releaseAdopt(b, pid, changed)
	releasePublish(b, pid, changed)
	b.request("GET", path, nil, 409)
	if again := b.request("POST", path, map[string]any{}, 201); again["id"] != releaseID {
		t.Fatal("saved receipt lost after head change")
	}
	if transport.calls.Load() != 1 {
		t.Fatal("repair triggered extra model calls")
	}
	// A published upstream change is repaired against the new authoritative IP,
	// without trying to adopt its already-published candidate a second time.
	publish(b, pid, bid, candidate)
	propagated, err := svc.IP.PropagatePublication(ctx, publicationEvent(t, svc.IP.Workspace.DB, candidate))
	if err != nil {
		t.Fatal(err)
	}
	var next map[string]any
	_ = json.Unmarshal(output, &next)
	next["plan_id"] = propagated.PlanID
	output, _ = json.Marshal(next)
	provider, _ = json.Marshal(map[string]any{"choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]any{"content": string(output)}}}, "usage": map[string]any{"prompt_tokens": 20, "completion_tokens": 10}})
	transport.response = string(provider)
	after, err := svc.Prepare(ctx, a, pid, service.GenerationInput{BranchID: bid, ExpectedIPHead: candidate, Role: "continuity_editor", PriceID: original.Frozen.Price.ID, OperationID: "repair-published", ChangePlanID: propagated.PlanID, Request: models.Request{Prompt: "修订已发布设定造成的冲突", MaxOutputTokens: 1000}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Start(ctx, a, pid, after.ID); err != nil {
		t.Fatal(err)
	}
	if state, e := svc.Execute(ctx, after.ID); e != nil || state != "SUCCEEDED" {
		t.Fatal(state, e)
	}
	post, err := svc.PrepareRepair(ctx, a, pid, after.ID)
	if err != nil {
		t.Fatal("published change repair", err)
	}
	if post.Manifest.IPCandidateID != "" || post.Manifest.Artifacts[0].Dependencies.Inputs[0].Source.Revision != candidate {
		t.Fatal("published dependency not refreshed", post.Manifest)
	}
}
