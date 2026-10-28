package integration

import (
	"context"
	"encoding/json"
	"novelstudio/internal/domain"
	"novelstudio/internal/models"
	"novelstudio/internal/service"
	"strings"
	"testing"
)

func TestCrossBranchDeletedSourceDecisionsAreVersionedCandidates(t *testing.T) {
	transport := &generationTransport{response: generationSuccess}
	b, svc, a, pid, initial := generationFixture(t, transport)
	ctx := context.Background()
	sourceBranch, targetBranch := initial.BranchID, branch(b, pid, "改编路线")
	root := "/api/projects/" + pid
	b.request("PUT", "/api/model-bindings/continuity_editor", map[string]any{"connection_id": initial.Frozen.Config.ID, "connection_revision": 1, "revision": 0}, 200)
	base := propose(b, pid, sourceBranch, "", "decision-base", originalSnapshot())
	publish(b, pid, sourceBranch, base)
	targetBase := propose(b, pid, targetBranch, "", "decision-target-base", domain.Snapshot{Entities: []domain.Entity{{ID: "adapt-1", Kind: "character", Name: "改编分支人物", Description: "独立的分支设定"}}})
	publish(b, pid, targetBranch, targetBase)
	target := resource(b, pid, targetBranch, "保留旧主线的章节")
	source := dependencyFromCatalog(t, b, pid, "entity", "lin-1")
	first := prepareRelease(b, pid, targetBranch, targetBase, "", "decision-old-chapter", []map[string]any{{"resource_id": target, "text": "林舟在旧时间线仍是一位医师。", "dependencies": map[string]any{"inputs": []any{map[string]any{"source": source, "kind": "representation", "reason": "引用原作角色"}}}}})
	releaseAdopt(b, pid, first)
	releasePublish(b, pid, first)
	removed := propose(b, pid, sourceBranch, base, "decision-remove", domain.Snapshot{Entities: []domain.Entity{}, Assertions: []domain.Assertion{}})
	pending := b.request("POST", root+"/changes", map[string]any{"operation_id": "decision-unpublished", "branch_id": sourceBranch, "candidate_id": removed}, 201)
	input := service.GenerationInput{BranchID: targetBranch, ExpectedIPHead: targetBase, Role: "continuity_editor", PriceID: initial.Frozen.Price.ID, OperationID: "decision-cross-unpublished", ChangePlanID: pending["id"].(string), Request: models.Request{Prompt: "审查旧路线是否保留历史", MaxOutputTokens: 1000}}
	if _, err := svc.Prepare(ctx, a, pid, input); err == nil {
		t.Fatal("unpublished cross-branch proposal became authority")
	}
	publish(b, pid, sourceBranch, removed)
	propagation, err := svc.IP.PropagatePublication(ctx, publicationEvent(t, svc.IP.Workspace.DB, removed))
	if err != nil {
		t.Fatal(err)
	}
	output, _ := json.Marshal(map[string]any{"schema": 1, "status": "CANDIDATE", "plan_id": propagation.PlanID, "patches": []any{}, "summary": "旧路线可考虑保留历史，需作者决定与独立复核。"})
	response, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]any{"content": string(output)}}}, "usage": map[string]any{"prompt_tokens": 20, "completion_tokens": 10}})
	transport.response = string(response)
	input.OperationID, input.ChangePlanID = "decision-cross-published", propagation.PlanID
	g, err := svc.Prepare(ctx, a, pid, input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Start(ctx, a, pid, g.ID); err != nil {
		t.Fatal(err)
	}
	if state, e := svc.Execute(ctx, g.ID); e != nil || state != "SUCCEEDED" {
		t.Fatal(state, e)
	}
	if !strings.Contains(transport.payload, "focus_branch_id") {
		t.Fatal("missing distinct target branch context")
	}
	path := root + "/generations/" + g.ID + "/patches"
	preview := b.request("GET", path, nil, 200)
	if preview["branch_id"] != targetBranch {
		t.Fatal("repair focused wrong branch", preview)
	}
	options := preview["dependency_options"].([]any)
	if len(options) != 1 || options[0].(map[string]any)["deleted"] != true {
		t.Fatal("missing deleted source decision", options)
	}
	option := options[0].(map[string]any)
	choice := map[string]any{"resource_id": target, "source": option["source"], "kind": "representation", "action": "KEEP_CURRENT", "reason": "不可使用已删除的当前对象"}
	b.request("POST", path+"/preview", map[string]any{"decisions": []any{choice}}, 400)
	choice["action"], choice["reason"] = "KEEP_HISTORY", "这条改编路线保留原作旧时间线的人物身份"
	checked := b.request("POST", path+"/preview", map[string]any{"decisions": []any{choice}}, 200)
	if checked["save_blocked_reason"] != nil || len(checked["decisions"].([]any)) != 1 {
		t.Fatal(checked)
	}
	saved := b.request("POST", path, map[string]any{"decisions": []any{choice}}, 201)
	savedID := saved["id"].(string)
	r, err := svc.IP.ReadRelease(ctx, a, pid, savedID)
	if err != nil {
		t.Fatal(err)
	}
	ref := r.Manifest.Artifacts[0]
	if r.Manifest.BranchID != targetBranch || r.Manifest.IPCandidateID != "" || !ref.Dependencies.Inputs[0].Pinned || ref.Dependencies.Inputs[0].Source.Revision != base || ref.Origin.Generation.GenerationID != g.ID || !ref.Origin.ReviewRequired {
		t.Fatal("decision escaped version or review boundary", r)
	}
	if again := b.request("POST", path, map[string]any{"decisions": []any{choice}}, 201); again["id"] != savedID {
		t.Fatal("decision duplicated")
	}
	b.request("POST", root+"/releases/"+savedID+"/adopt", map[string]any{}, 503)
	choice["action"], choice["reason"] = "DROP_REFERENCE", "旧章节改为独立人物，解除原作人物依赖，正文另行修订"
	dropped := b.request("POST", path, map[string]any{"decisions": []any{choice}}, 201)
	if dropped["id"] == savedID {
		t.Fatal("new decision overwrote old candidate")
	}
	dropRelease, err := svc.IP.ReadRelease(ctx, a, pid, dropped["id"].(string))
	if err != nil || len(dropRelease.Manifest.Artifacts[0].Dependencies.Inputs) != 0 || !strings.Contains(dropRelease.Manifest.Artifacts[0].FrozenChangeReason, "解除引用") {
		t.Fatal("drop missing reason", dropRelease, err)
	}
	current, err := svc.IP.ReadResource(ctx, a, pid, target)
	if err != nil || current.Head != first || current.Dependencies.Inputs[0].Pinned {
		t.Fatal("decision prematurely changed published chapter")
	}
	outcomes := b.request("GET", root+"/changes/"+propagation.PlanID+"/outcomes", nil, 200)
	if len(outcomes["items"].([]any)) != 2 {
		t.Fatal("missing saved decisions", outcomes)
	}
	firstPage := b.request("GET", root+"/changes/"+propagation.PlanID+"/outcomes?limit=1", nil, 200)
	if len(firstPage["items"].([]any)) != 1 || firstPage["truncated"] != true || firstPage["next"] == nil {
		t.Fatal("missing outcome cursor", firstPage)
	}
	secondPage := b.request("GET", root+"/changes/"+propagation.PlanID+"/outcomes?limit=1&after="+firstPage["next"].(string), nil, 200)
	if len(secondPage["items"].([]any)) != 1 || secondPage["truncated"] != false || secondPage["next"] != nil {
		t.Fatal("incorrect terminal outcome page", secondPage)
	}
	if firstPage["items"].([]any)[0].(map[string]any)["release_id"] == secondPage["items"].([]any)[0].(map[string]any)["release_id"] {
		t.Fatal("cursor repeated a release")
	}
	b.request("GET", root+"/changes/"+propagation.PlanID+"/outcomes?limit=101", nil, 400)
	b.request("GET", root+"/changes/"+propagation.PlanID+"/outcomes?after=invalid", nil, 400)
	for _, item := range outcomes["items"].([]any) {
		if item.(map[string]any)["current"] == true || item.(map[string]any)["status"] != "PREPARED" {
			t.Fatal("candidate marked completed", outcomes)
		}
	}
	other := resource(b, pid, targetBranch, "后来补充的材料")
	later := prepareRelease(b, pid, targetBranch, targetBase, "", "decision-later", []map[string]any{{"resource_id": other, "text": "新材料改变目标水位"}})
	releaseAdopt(b, pid, later)
	releasePublish(b, pid, later)
	choice["reason"] = "新一轮决定必须先重新检查"
	b.request("POST", path, map[string]any{"decisions": []any{choice}}, 409)
	refreshBody := map[string]any{"operation_id": "decision-refresh"}
	fresh := b.request("POST", root+"/changes/"+propagation.PlanID+"/refresh", refreshBody, 201)
	if fresh["id"] == propagation.PlanID || fresh["stale"] != false {
		t.Fatal("watermark not refreshed", fresh)
	}
	if again := b.request("POST", root+"/changes/"+propagation.PlanID+"/refresh", refreshBody, 201); again["id"] != fresh["id"] {
		t.Fatal("refresh duplicated")
	}
	outcomes = b.request("GET", root+"/changes/"+fresh["id"].(string)+"/outcomes", nil, 200)
	if len(outcomes["items"].([]any)) != 2 {
		t.Fatal("same published source lost decision history", outcomes)
	}
	installSourceRegistrationFixture(t, svc.IP.Workspace.DB)
	if report, err := svc.IP.Workspace.RegisterRetentionSources(ctx, a.ID, pid); err != nil || report.UnboundSources != 0 {
		t.Fatal("cross-branch source attribution failed", report, err)
	}
	if transport.calls.Load() != 1 {
		t.Fatal("decisions dispatched extra models")
	}
}
