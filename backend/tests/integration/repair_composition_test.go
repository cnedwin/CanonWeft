package integration

import (
	"context"
	"encoding/json"
	"novelstudio/internal/models"
	"novelstudio/internal/service"
	"testing"
)

func TestRepairCompositionPreservesOriginalNewResourcesAndConflicts(t *testing.T) {
	transport := &generationTransport{response: generationSuccess}
	b, svc, a, pid, initial := generationFixture(t, transport)
	ctx := context.Background()
	bid, root := initial.BranchID, "/api/projects/"+pid
	b.request("PUT", "/api/model-bindings/continuity_editor", map[string]any{"connection_id": initial.Frozen.Config.ID, "connection_revision": 1, "revision": 0}, 200)
	sourceID, targetID, newID := resource(b, pid, bid, "地点资料"), resource(b, pid, bid, "初见章节"), resource(b, pid, bid, "新增作者笔记")
	first := prepareRelease(b, pid, bid, "", "", "composition-base", []map[string]any{{"resource_id": sourceID, "text": "故事发生于旧城"}})
	releaseAdopt(b, pid, first)
	releasePublish(b, pid, first)
	source := dependencyFromCatalog(t, b, pid, "resource", sourceID)
	baseText := "🌙旧城。人物低声说话。"
	second := prepareRelease(b, pid, bid, "", "", "composition-chapter", []map[string]any{{"resource_id": targetID, "text": baseText, "dependencies": map[string]any{"inputs": []any{map[string]any{"source": source, "kind": "representation", "reason": "地点影响场景"}}}}})
	releaseAdopt(b, pid, second)
	releasePublish(b, pid, second)
	prospective := map[string]any{"kind": "resource", "branch_id": bid, "id": sourceID, "revision": "$release", "hash": ""}
	makeOriginal := func(operation, text string) string {
		return prepareRelease(b, pid, bid, "", "", operation, []map[string]any{
			{"resource_id": sourceID, "expected_head": first, "text": "故事改为新城"},
			{"resource_id": targetID, "expected_head": second, "text": text, "dependencies": map[string]any{"inputs": []any{map[string]any{"source": prospective, "kind": "representation", "reason": "保留场景依赖"}}}},
			{"resource_id": newID, "text": "作者新增的后续章节笔记，必须保留。"},
		})
	}
	originalID := makeOriginal("composition-original", baseText+"雨下得更大了。")
	conflictID := makeOriginal("composition-conflict", "🌙海港。人物低声说话。")
	r, err := svc.IP.ReadResource(ctx, a, pid, targetID)
	if err != nil {
		t.Fatal(err)
	}
	run := func(sourceRelease, operation string) string {
		plan := b.request("POST", root+"/changes", map[string]any{"operation_id": operation + "-plan", "branch_id": bid, "release_id": sourceRelease}, 201)
		output, _ := json.Marshal(map[string]any{"schema": 1, "status": "CANDIDATE", "plan_id": plan["id"], "summary": "修订冲突地点，保留人物动作。", "patches": []any{map[string]any{"resource_id": targetID, "base_revision": r.Head, "base_hash": r.Hash, "start_rune": 1, "end_rune": 3, "expected_text": "旧城", "replacement": "新城", "reason": "与新地点设定一致"}}})
		response, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]any{"content": string(output)}}}, "usage": map[string]any{"prompt_tokens": 20, "completion_tokens": 10}})
		transport.response = string(response)
		g, e := svc.Prepare(ctx, a, pid, service.GenerationInput{BranchID: bid, Role: "continuity_editor", PriceID: initial.Frozen.Price.ID, OperationID: operation, ChangePlanID: plan["id"].(string), Request: models.Request{Prompt: "保留原候选所有修改，修订地点", MaxOutputTokens: 1000}})
		if e != nil {
			t.Fatal(e)
		}
		if _, e = svc.Start(ctx, a, pid, g.ID); e != nil {
			t.Fatal(e)
		}
		if state, e := svc.Execute(ctx, g.ID); e != nil || state != "SUCCEEDED" {
			t.Fatal(state, e)
		}
		return g.ID
	}
	conflictGeneration := run(conflictID, "composition-conflicting-generation")
	conflictPath := root + "/generations/" + conflictGeneration + "/patches"
	conflict := b.request("GET", conflictPath, nil, 200)
	if conflict["save_blocked_reason"] == nil || len(conflict["conflicts"].([]any)) != 1 {
		t.Fatal("overlap not shown", conflict)
	}
	b.request("POST", conflictPath, map[string]any{}, 409)
	generation := run(originalID, "composition-safe-generation")
	path := root + "/generations/" + generation + "/patches"
	preview := b.request("GET", path, nil, 200)
	if preview["save_blocked_reason"] != nil || preview["source_release_id"] != originalID || len(preview["preserved"].([]any)) != 3 {
		t.Fatal("original candidate missing", preview)
	}
	saved := b.request("POST", path, map[string]any{}, 201)
	savedID := saved["id"].(string)
	release, err := svc.IP.ReadRelease(ctx, a, pid, savedID)
	if err != nil {
		t.Fatal(err)
	}
	if len(release.Manifest.Artifacts) != 3 || len(release.Manifest.PatchGroup.Members) != 2 {
		t.Fatal("incomplete mixed release", release.Manifest)
	}
	for i, ref := range release.Manifest.Artifacts {
		if ref.Origin.ParentReleaseID != originalID {
			t.Fatal("original lineage lost", ref)
		}
		switch ref.ResourceID {
		case sourceID:
			if release.Contents[i].Text != "故事改为新城" {
				t.Fatal("original source overwritten")
			}
		case targetID:
			if release.Contents[i].Text != "🌙新城。人物低声说话。雨下得更大了。" || ref.Origin.Generation == nil || ref.Origin.Generation.GenerationID != generation || ref.Dependencies.Inputs[0].Source.Revision != savedID {
				t.Fatal("unsafe merged content or lineage", ref, release.Contents[i])
			}
		case newID:
			if release.Contents[i].Text != "作者新增的后续章节笔记，必须保留。" || ref.ExpectedHead != "" {
				t.Fatal("new resource lost")
			}
		}
	}
	if replay := b.request("POST", path, map[string]any{}, 201); replay["id"] != savedID {
		t.Fatal("composition duplicated")
	}
	b.request("POST", root+"/releases/"+savedID+"/adopt", map[string]any{}, 503)
	partial := []map[string]any{}
	for _, item := range release.Contents {
		if item.ResourceID == newID {
			continue
		}
		partial = append(partial, map[string]any{"resource_id": item.ResourceID, "expected_head": item.ExpectedHead, "text": item.Text, "origin": map[string]any{"parent_release_id": savedID}})
	}
	b.request("POST", root+"/releases", map[string]any{"branch_id": bid, "operation_id": "composition-drop-new-resource", "updates": partial}, 409)
	current, err := svc.IP.ReadResource(ctx, a, pid, targetID)
	if err != nil || current.Text != baseText {
		t.Fatal("preview changed published text", current, err)
	}
	if transport.calls.Load() != 2 {
		t.Fatal("unexpected extra model calls")
	}
}
