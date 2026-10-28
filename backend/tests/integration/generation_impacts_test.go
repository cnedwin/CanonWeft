package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"novelstudio/internal/domain"
	"novelstudio/internal/models"
	"novelstudio/internal/service"
	"strings"
	"testing"
)

func TestGenerationImpactGuardRejectsStalePlanBeforeSendAndOutputReuse(t *testing.T) {
	transport := &generationTransport{response: generationSuccess}
	b, svc, actor, pid, original := generationFixture(t, transport)
	ctx := context.Background()
	bid := original.BranchID
	root := "/api/projects/" + pid
	b.request("PUT", "/api/model-bindings/continuity_editor", map[string]any{"connection_id": original.Frozen.Config.ID, "connection_revision": 1, "revision": 0}, 200)
	base := propose(b, pid, bid, "", "base-ip", originalSnapshot())
	publish(b, pid, bid, base)
	target := resource(b, pid, bid, "修订对象")
	source := dependencyFromCatalog(t, b, pid, "entity", "lin-1")
	first := prepareRelease(b, pid, bid, base, "", "original-target", []map[string]any{{"resource_id": target, "text": "下游原稿：林舟的旧形象必须具体检查。", "dependencies": map[string]any{"inputs": []any{map[string]any{"source": source, "kind": "representation", "reason": "角色的外貌表现"}}}}})
	releaseAdopt(b, pid, first)
	releasePublish(b, pid, first)
	snapshot := originalSnapshot()
	snapshot.Entities[0].Description = "更改外貌设定"
	candidate := propose(b, pid, bid, base, "planned-ip", snapshot)
	plan := b.request("POST", root+"/changes", map[string]any{"operation_id": "planned-impact", "branch_id": bid, "candidate_id": candidate}, 201)
	structured, _ := json.Marshal(map[string]any{"schema": 1, "status": "CANDIDATE", "plan_id": plan["id"], "patches": []any{}, "summary": "仅核对版本与证据；尚未提出文本修改。"})
	provider, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]any{"content": string(structured)}}}, "usage": map[string]any{"prompt_tokens": 20, "completion_tokens": 10}})
	transport.response = string(provider)
	input := map[string]any{"branch_id": bid, "expected_ip_head": base, "role": "continuity_editor", "price_id": original.Frozen.Price.ID, "operation_id": "impact-generation", "change_plan_id": plan["id"], "request": map[string]any{"prompt": "核对变更范围，不发布任何正文", "max_output_tokens": 100}}
	result := b.request("POST", root+"/generations", input, 201)
	if result["frozen"].(map[string]any)["change_plan_id"] != plan["id"] {
		t.Fatal("plan not frozen", result)
	}
	id := result["id"].(string)
	if _, err := svc.Start(ctx, actor, pid, id); err != nil {
		t.Fatal(err)
	}
	if state, err := svc.Execute(ctx, id); err != nil || state != "SUCCEEDED" {
		t.Fatal(state, err)
	}
	if !strings.Contains(transport.payload, "下游原稿：林舟的旧形象必须具体检查。") || !strings.Contains(transport.payload, "proposed-change-data") {
		t.Fatal("actual affected text missing from frozen request")
	}
	input["operation_id"] = "queued-impact"
	queued := b.request("POST", root+"/generations", input, 201)["id"].(string)
	if _, err := svc.Start(ctx, actor, pid, queued); err != nil {
		t.Fatal(err)
	}
	input["operation_id"] = "unstarted-impact"
	unstarted := b.request("POST", root+"/generations", input, 201)["id"].(string)
	// Change a resource while the IP head stays unchanged: the whole recorded graph matters.
	resourceID := resource(b, pid, bid, "新增材料")
	release := prepareRelease(b, pid, bid, base, "", "material", []map[string]any{{"resource_id": resourceID, "text": "新增需要复核的材料"}})
	releaseAdopt(b, pid, release)
	releasePublish(b, pid, release)
	if _, err := svc.Start(ctx, actor, pid, unstarted); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("stale plan reserved funds", err)
	}
	if _, err := svc.Execute(ctx, queued); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("stale queued task dispatched", err)
	}
	if transport.calls.Load() != 1 {
		t.Fatal("unexpected provider requests", transport.calls.Load())
	}
	if _, err := svc.Read(ctx, actor, pid, id); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("stale result reusable", err)
	}
	if _, err := svc.Recover(ctx, queued); err != nil {
		t.Fatal(err)
	}
	input["operation_id"] = "already-stale"
	b.request("POST", root+"/generations", input, 409)
	input["role"] = "writer"
	input["operation_id"] = "writer-must-not-read-full-plan"
	b.request("POST", root+"/generations", input, 400)

	// Current graph changes must block new usage while preserving the original
	// material copied into an already prepared request, without sending it again.
	db := svc.IP.Workspace.DB
	installSourceRegistrationFixture(t, db)
	if report, err := svc.IP.Workspace.RegisterRetentionSources(ctx, actor.ID, pid); err != nil || report.UnboundSources != 0 {
		t.Fatal("historical change context lost its original sources", report, err)
	}
	var originalFrozen string
	if err := db.QueryRow("SELECT frozen FROM generation_tasks WHERE id=?", unstarted).Scan(&originalFrozen); err != nil {
		t.Fatal(err)
	}
	var frozen service.GenerationFrozen
	if json.Unmarshal([]byte(originalFrozen), &frozen) != nil {
		t.Fatal("unreadable frozen change task")
	}
	raw, err := svc.IP.Blobs.ReadObject(ctx, pid, frozen.InputHash)
	if err != nil {
		t.Fatal(err)
	}
	for _, replacement := range []struct{ before, after string }{
		{"下游原稿：林舟的旧形象必须具体检查。", "未经审阅的新正文"},
		{"更改外貌设定", "伪造变更计划"},
		{"角色的外貌表现", "替换依赖理由"},
	} {
		var request models.Request
		if json.Unmarshal(raw, &request) != nil {
			t.Fatal("unreadable original request")
		}
		request.Prompt = strings.ReplaceAll(request.Prompt, replacement.before, replacement.after)
		changed, _ := json.Marshal(request)
		if bytes.Equal(raw, changed) {
			t.Fatal("counterexample did not change original input")
		}
		object, err := svc.IP.Blobs.PutObject(ctx, pid, bytes.NewReader(changed))
		if err != nil {
			t.Fatal(err)
		}
		updated := frozen
		updated.InputHash = object.Hash
		updatedRaw, _ := json.Marshal(updated)
		if _, err := db.Exec("UPDATE generation_tasks SET frozen=?,input_hash=? WHERE id=?", updatedRaw, object.Hash, unstarted); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.IP.Workspace.RegisterRetentionSources(ctx, actor.ID, pid); !errors.Is(err, domain.ErrNotReady) {
			t.Fatal("substituted change copy accepted", replacement.before, err)
		}
	}
	if _, err := db.Exec("UPDATE generation_tasks SET frozen=?,input_hash=? WHERE id=?", originalFrozen, frozen.InputHash, unstarted); err != nil {
		t.Fatal(err)
	}
	if report, err := svc.IP.Workspace.RegisterRetentionSources(ctx, actor.ID, pid); err != nil || report.UpdatedReferences != 0 || report.UnboundSources != 0 || transport.calls.Load() != 1 {
		t.Fatal("change request replay changed sources or dispatched again", report, err)
	}
}
