package integration

import (
	"context"
	"errors"
	"novelstudio/internal/domain"
	"novelstudio/internal/models"
	"novelstudio/internal/service"
	"strings"
	"testing"
)

func TestRequiredContextFollowsExactDependenciesAndDoesNotTruncate(t *testing.T) {
	b, _ := setup(t)
	pid := createProject(b, "必要依赖", "required")
	bid := branch(b, pid, "写作")
	other := branch(b, pid, "共享资料")
	source := resource(b, pid, other, "关键依据")
	body := "🌙药剂只在月圆生效。" + strings.Repeat("必须完整保留的药效条件。", 120)
	first := prepareRelease(b, pid, other, "", "", "source", []map[string]any{{"resource_id": source, "text": body}})
	releaseAdopt(b, pid, first)
	releasePublish(b, pid, first)
	root := resource(b, pid, bid, "场景规划")
	dep := dependencyFromCatalog(t, b, pid, "resource", source)
	inputs := map[string]any{"inputs": []any{map[string]any{"source": dep, "kind": "evidence", "reason": "场景必须遵循药效条件"}}}
	r := prepareRelease(b, pid, bid, "", "", "root", []map[string]any{{"resource_id": root, "text": "本场景安排在月圆。", "dependencies": inputs}})
	releaseAdopt(b, pid, r)
	releasePublish(b, pid, r)
	path := "/api/projects/" + pid + "/branches/" + bid + "/knowledge"
	query := map[string]any{"query": "无关的航海线索", "purpose": "author", "limit": 1, "required_resource_ids": []string{root}}
	result := b.request("POST", path+"/search", query, 200)
	required := result["required_inputs"].([]any)
	if len(required) != 2 {
		t.Fatal("required closure missing", result)
	}
	found := false
	for _, raw := range required {
		item := raw.(map[string]any)
		if item["name"] == "关键依据" {
			found = item["text"] == body
		}
	}
	if !found {
		t.Fatal("required source was similarity ranked or truncated")
	}
	id := result["id"].(string)
	b.request("GET", path+"/contexts/"+id, nil, 200)
	query["token_budget"] = 4096
	b.request("POST", path+"/search", query, 409)
	query["token_budget"] = 65536
	changed := prepareRelease(b, pid, other, "", "", "source2", []map[string]any{{"resource_id": source, "expected_head": first, "text": "药剂只在新月生效。"}})
	releaseAdopt(b, pid, changed)
	releasePublish(b, pid, changed)
	// Other-branch changes do not change this branch's index, but must invalidate its context.
	b.request("GET", path+"/contexts/"+id, nil, 409)
	b.request("POST", path+"/search", query, 409)
}

func TestRequiredContextPinnedHistoryStillHonorsCurrentPolicy(t *testing.T) {
	b, db := setup(t)
	pid := createProject(b, "历史引用", "pinned")
	bid := branch(b, pid, "写作")
	other := branch(b, pid, "共享资料")
	source := resource(b, pid, other, "依据")
	first := prepareRelease(b, pid, other, "", "", "source", []map[string]any{{"resource_id": source, "text": "旧版可引用原文"}})
	releaseAdopt(b, pid, first)
	releasePublish(b, pid, first)
	root := resource(b, pid, bid, "场景")
	ref := dependencyFromCatalog(t, b, pid, "resource", source)
	r := prepareRelease(b, pid, bid, "", "", "root", []map[string]any{{"resource_id": root, "text": "保持历史依据", "dependencies": map[string]any{"inputs": []any{map[string]any{"source": ref, "pinned": true, "kind": "evidence", "reason": "历史出处"}}}}})
	releaseAdopt(b, pid, r)
	releasePublish(b, pid, r)
	path := "/api/projects/" + pid + "/branches/" + bid + "/knowledge"
	query := map[string]any{"query": "场景", "purpose": "author", "limit": 1, "required_resource_ids": []string{root}}
	saved := b.request("POST", path+"/search", query, 200)
	next := prepareRelease(b, pid, other, "", "", "changed", []map[string]any{{"resource_id": source, "expected_head": first, "text": "新版原文"}})
	releaseAdopt(b, pid, next)
	releasePublish(b, pid, next)
	result := b.request("GET", path+"/contexts/"+saved["id"].(string), nil, 200)
	if result["required_inputs"].([]any)[1].(map[string]any)["text"] != "旧版可引用原文" {
		t.Fatal("pinned history replaced")
	}
	restricted := prepareRelease(b, pid, other, "", "", "restrict", []map[string]any{{"resource_id": source, "expected_head": next, "text": "新版原文", "policy": domain.KnowledgePolicy{Space: "research", Visibility: "author"}}})
	releaseAdopt(b, pid, restricted)
	releasePublish(b, pid, restricted)
	b.request("GET", path+"/contexts/"+saved["id"].(string), nil, 403)
	actor := b.request("GET", "/api/me", nil, 200)["id"].(string)
	installSourceRegistrationFixture(t, db)
	if report, err := (&service.Workspace{DB: db}).RegisterRetentionSources(context.Background(), actor, pid); err != nil || report.UnboundSources != 0 {
		t.Fatal("historical retention provenance treated as current access", report, err)
	}
}

func TestRequiredContextSkillRevocationBlocksQueuedAndCompletedGeneration(t *testing.T) {
	transport := &generationTransport{response: generationSuccess}
	b, gen, actor, pid, original := generationFixture(t, transport)
	ctx := context.Background()
	bid := original.BranchID
	b.request("PUT", "/api/model-bindings/critic", map[string]any{"connection_id": original.Frozen.Config.ID, "connection_revision": 1, "revision": 0}, 200)
	methods := &service.Skills{IP: gen.IP}
	version, err := methods.Create(ctx, actor, pid, "method", "---\nname: required-evidence\ndescription: Use when reviewing evidence.\nlicense: LicenseRef-Private\n---\nKeep exact source anchors.")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = methods.Reference(ctx, actor, pid, version.ID); err != nil {
		t.Fatal(err)
	}
	source := dependencyFromCatalog(t, b, pid, "skill", version.ID)
	root := resource(b, pid, bid, "审查依据")
	r := prepareRelease(b, pid, bid, "", "", "root", []map[string]any{{"resource_id": root, "text": "请按方法核对原文", "dependencies": map[string]any{"inputs": []any{map[string]any{"source": source, "kind": "capability", "reason": "核对证据"}}}}})
	releaseAdopt(b, pid, r)
	releasePublish(b, pid, r)
	path := "/api/projects/" + pid + "/branches/" + bid + "/knowledge"
	query := map[string]any{"query": "证据", "purpose": "critic", "limit": 1, "required_resource_ids": []string{root}}
	b.request("POST", path+"/search", query, 403)
	query["include_methods"] = true
	saved := b.request("POST", path+"/search", query, 200)
	input := service.GenerationInput{BranchID: bid, Role: "critic", ContextID: saved["id"].(string), PriceID: original.Frozen.Price.ID, OperationID: "review-done", Request: models.Request{Prompt: "审查，不执行工具", MaxOutputTokens: 100}}
	done, err := gen.Prepare(ctx, actor, pid, input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = gen.Start(ctx, actor, pid, done.ID); err != nil {
		t.Fatal(err)
	}
	if state, err := gen.Execute(ctx, done.ID); err != nil || state != "SUCCEEDED" {
		t.Fatal(state, err)
	}
	if !strings.Contains(transport.payload, "Keep exact source anchors.") {
		t.Fatal("required method text missing")
	}
	input.OperationID = "review-queued"
	queued, err := gen.Prepare(ctx, actor, pid, input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = gen.Start(ctx, actor, pid, queued.ID); err != nil {
		t.Fatal(err)
	}
	input.OperationID = "review-unstarted"
	unstarted, err := gen.Prepare(ctx, actor, pid, input)
	if err != nil {
		t.Fatal(err)
	}
	if err = methods.Revoke(ctx, actor, pid, version.ID, "方法不再允许引用"); err != nil {
		t.Fatal(err)
	}
	if _, err = gen.Start(ctx, actor, pid, unstarted.ID); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("revoked context reserved funds", err)
	}
	if _, err = gen.Execute(ctx, queued.ID); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("revoked context dispatched", err)
	}
	if _, err = gen.Read(ctx, actor, pid, done.ID); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("revoked result reused", err)
	}
	if transport.calls.Load() != 1 {
		t.Fatal("unexpected requests", transport.calls.Load())
	}
	if _, err = gen.Recover(ctx, queued.ID); err != nil {
		t.Fatal(err)
	}
	b.request("GET", path+"/contexts/"+saved["id"].(string), nil, 409)
	installSourceRegistrationFixture(t, gen.IP.Workspace.DB)
	if report, err := gen.IP.Workspace.RegisterRetentionSources(ctx, actor.ID, pid); err != nil || report.UnboundSources != 0 {
		t.Fatal("revoked skill reference lost immutable copy provenance", report, err)
	}
}

func TestRequiredContextCannotBypassSpacePerspectiveOrCurrentPolicy(t *testing.T) {
	b, _ := setup(t)
	pid := createProject(b, "必要输入权限", "required-policy")
	bid := branch(b, pid, "正史")
	cid := propose(b, pid, bid, "", "ip", originalSnapshot())
	publish(b, pid, bid, cid)
	secret := resource(b, pid, bid, "秘密计划")
	r := prepareRelease(b, pid, bid, cid, "", "source", []map[string]any{{"resource_id": secret, "text": "计划下卷找到密室。", "policy": domain.KnowledgePolicy{Space: "working", Visibility: "author"}}})
	releaseAdopt(b, pid, r)
	releasePublish(b, pid, r)
	path := "/api/projects/" + pid + "/branches/" + bid + "/knowledge/search"
	query := map[string]any{"query": "密室", "purpose": "author", "limit": 1, "required_resource_ids": []string{secret}}
	b.request("POST", path, query, 403)
	query["include_working"] = true
	result := b.request("POST", path, query, 200)
	if result["required_inputs"].([]any)[0].(map[string]any)["space"] != "working" {
		t.Fatal("plan became fact", result)
	}
	query["include_working"] = false
	query["purpose"] = "npc"
	query["perspective"] = "lin-1"
	b.request("POST", path, query, 403)
	query["purpose"] = "writer"
	b.request("POST", path, query, 403)
	query["purpose"] = "author"
	query["required_resource_ids"] = []string{secret, secret}
	b.request("POST", path, query, 400)
	other := createProject(b, "其他项目", "other")
	ob := branch(b, other, "另一正史")
	oid := resource(b, other, ob, "别的资料")
	query["required_resource_ids"] = []string{oid}
	b.request("POST", path, query, 403)
}
