package integration

import (
	"context"
	"encoding/json"
	"novelstudio/internal/models"
	"novelstudio/internal/service"
	"strings"
	"testing"
)

func TestAdditionalChangeScopeReachesGenerationAndSurvivesRefresh(t *testing.T) {
	transport := &generationTransport{response: generationSuccess}
	b, svc, actor, pid, initial := generationFixture(t, transport)
	ctx := context.Background()
	root := "/api/projects/" + pid
	branchID := initial.BranchID
	b.request("PUT", "/api/model-bindings/continuity_editor", map[string]any{"connection_id": initial.Frozen.Config.ID, "connection_revision": 1, "revision": 0}, 200)
	base := propose(b, pid, branchID, "", "scope-base", originalSnapshot())
	publish(b, pid, branchID, base)
	targetBranch := branch(b, pid, "缺少依赖的改编分支")
	target := resource(b, pid, targetBranch, "未标姓名的场景")
	first := prepareRelease(b, pid, targetBranch, "", "", "scope-first", []map[string]any{{"resource_id": target, "text": "青衣人收起银针，没有留下姓名。"}})
	releaseAdopt(b, pid, first)
	releasePublish(b, pid, first)
	changed := originalSnapshot()
	changed.Entities[0].Description = "失去医术后寻找新的身份"
	version := propose(b, pid, branchID, base, "scope-change", changed)
	publish(b, pid, branchID, version)
	propagation, err := svc.IP.PropagatePublication(ctx, publicationEvent(t, svc.IP.Workspace.DB, version))
	if err != nil {
		t.Fatal(err)
	}
	old, err := svc.IP.ReadChangePlan(ctx, actor, pid, propagation.PlanID)
	if err != nil || len(old.Items) != 0 {
		t.Fatal(old, err)
	}
	ref := dependencyFromCatalog(t, b, pid, "resource", target)
	endpoint := root + "/changes/" + old.ID + "/scope"
	foreignProject := b.request("POST", "/api/projects", map[string]any{"name": "其他项目", "operation_id": "scope-foreign-project"}, 201)["id"].(string)
	foreignBranch := branch(b, foreignProject, "正史")
	foreignResource := resource(b, foreignProject, foreignBranch, "不属于当前作品的资料")
	foreignRelease := prepareRelease(b, foreignProject, foreignBranch, "", "", "scope-foreign-release", []map[string]any{{"resource_id": foreignResource, "text": "不能进入另一本作品的检查上下文"}})
	releaseAdopt(b, foreignProject, foreignRelease)
	releasePublish(b, foreignProject, foreignRelease)
	foreignRef := dependencyFromCatalog(t, b, foreignProject, "resource", foreignResource)
	b.request("POST", endpoint, map[string]any{"operation_id": "scope-foreign", "targets": []any{map[string]any{"node": foreignRef, "reason": "不能跨项目引用"}}}, 400)
	choice := map[string]any{"node": ref, "reason": "青衣人未写姓名，银针场景可能与医术变化冲突，纳入检查"}
	body := map[string]any{"operation_id": "scope-add", "targets": []any{choice}}
	stale := map[string]any{}
	for key, value := range ref {
		stale[key] = value
	}
	stale["revision"] = strings.Repeat("0", 32)
	b.request("POST", endpoint, map[string]any{"operation_id": "scope-stale-ref", "targets": []any{map[string]any{"node": stale, "reason": "使用旧引用"}}}, 400)
	b.request("POST", endpoint, map[string]any{"operation_id": "scope-duplicate", "targets": []any{choice, choice}}, 400)
	saved := b.request("POST", endpoint, body, 201)
	if saved["semantic_review"] != "NOT_RUN" || len(saved["items"].([]any)) != 1 {
		t.Fatal(saved)
	}
	if again := b.request("POST", endpoint, body, 201); again["id"] != saved["id"] {
		t.Fatal("duplicate expanded plan")
	}
	if oldAgain, e := svc.IP.ReadChangePlan(ctx, actor, pid, old.ID); e != nil || len(oldAgain.Items) != 0 {
		t.Fatal("original plan changed", e)
	}
	planID := saved["id"].(string)
	output, _ := json.Marshal(map[string]any{"schema": 1, "status": "CANDIDATE", "plan_id": planID, "patches": []any{}, "summary": "仅检查新增场景，未完成全书一致性判断"})
	response, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]any{"content": string(output)}}}, "usage": map[string]any{"prompt_tokens": 20, "completion_tokens": 10}})
	transport.response = string(response)
	input := service.GenerationInput{BranchID: targetBranch, ExpectedIPHead: "", Role: "continuity_editor", PriceID: initial.Frozen.Price.ID, OperationID: "scope-generate", ChangePlanID: planID, Request: models.Request{Prompt: "检查没有登记依赖的银针场景", MaxOutputTokens: 1000}}
	generation, err := svc.Prepare(ctx, actor, pid, input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Start(ctx, actor, pid, generation.ID); err != nil {
		t.Fatal(err)
	}
	if state, e := svc.Execute(ctx, generation.ID); e != nil || state != "SUCCEEDED" {
		t.Fatal(state, e)
	}
	if !strings.Contains(transport.payload, "青衣人收起银针") || !strings.Contains(transport.payload, "additional_review") {
		t.Fatal("supplemental target missing from actual frozen prompt")
	}
	later := prepareRelease(b, pid, targetBranch, "", "", "scope-later", []map[string]any{{"resource_id": target, "expected_head": first, "text": "青衣人封存银针，学习新的技艺。"}})
	releaseAdopt(b, pid, later)
	releasePublish(b, pid, later)
	input.OperationID = "scope-stale-generation"
	if _, err := svc.Prepare(ctx, actor, pid, input); err == nil {
		t.Fatal("stale scope accepted")
	}
	b.request("POST", endpoint, map[string]any{"operation_id": "scope-stale-plan", "targets": []any{choice}}, 409)
	fresh := b.request("POST", root+"/changes/"+planID+"/refresh", map[string]any{"operation_id": "scope-refresh"}, 201)
	scopes := fresh["additional_review"].([]any)
	if len(scopes) != 1 || scopes[0].(map[string]any)["node"].(map[string]any)["revision"] != later || scopes[0].(map[string]any)["reason"] != choice["reason"] {
		t.Fatal("refresh dropped scope or retained stale revision", fresh)
	}
	installSourceRegistrationFixture(t, svc.IP.Workspace.DB)
	if report, err := svc.IP.Workspace.RegisterRetentionSources(ctx, actor.ID, pid); err != nil || report.UnboundSources != 0 {
		t.Fatal("expanded scope source capture failed", report, err)
	}
	if transport.calls.Load() != 1 {
		t.Fatal("scope operation called additional models")
	}
}
