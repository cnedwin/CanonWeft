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

func workingChoice(t *testing.T, b *browser, path, kind, id string) map[string]any {
	t.Helper()
	list := b.request("GET", path+"/working-inputs", nil, 200)["items"].([]any)
	for _, raw := range list {
		v := raw.(map[string]any)
		ref := v["ref"].(map[string]any)
		if ref["kind"] == kind && ref["id"] == id {
			return ref
		}
	}
	t.Fatal("working input not listed", kind, id)
	return nil
}

func TestWorkingContextDraftIsPrivateUnpublishedAndRevisionBound(t *testing.T) {
	b, _ := setup(t)
	pid := createProject(b, "草稿协作", "draft-context")
	bid := branch(b, pid, "正史")
	rid := resource(b, pid, bid, "尚未确认的故事")
	resourcePath := "/api/projects/" + pid + "/resources/" + rid
	b.request("PUT", resourcePath+"/draft", map[string]any{"revision": 0, "base_head": "", "text": "计划：主角将在下一卷寻找姐姐。"}, 200)
	path := "/api/projects/" + pid + "/branches/" + bid + "/knowledge"
	b.request("POST", path+"/index", map[string]any{}, 200)
	ref := workingChoice(t, b, path, "resource_draft", rid)
	query := map[string]any{"query": "寻找姐姐", "purpose": "author", "limit": 1, "include_working": true, "working_refs": []any{ref}}
	result := b.request("POST", path+"/search", query, 200)
	items := result["unconfirmed_inputs"].([]any)
	if len(items) != 1 || len(result["evidence"].([]any)) != 0 || len(result["facts"].([]any)) != 0 {
		t.Fatal("draft became fact", result)
	}
	parts := items[0].(map[string]any)["parts"].([]any)
	if parts[0].(map[string]any)["text"] != "计划：主角将在下一卷寻找姐姐。" {
		t.Fatal("draft text missing")
	}
	if b.request("GET", resourcePath, nil, 200)["head"] != "" {
		t.Fatal("context published a draft")
	}
	member := b.request("POST", "/api/members", map[string]any{"username": "draft-reader", "password": "draft-reader-password", "role": "editor"}, 201)["id"].(string)
	b.request("PUT", "/api/projects/"+pid+"/members/"+member, map[string]any{"role": "editor"}, 200)
	editor := &browser{t: t, handler: b.handler}
	editor.request("POST", "/api/login", map[string]any{"username": "draft-reader", "password": "draft-reader-password"}, 200)
	if len(editor.request("GET", path+"/working-inputs", nil, 200)["items"].([]any)) != 0 {
		t.Fatal("private draft exposed in catalog")
	}
	editor.request("POST", path+"/search", query, 403)
	query["include_working"] = false
	b.request("POST", path+"/search", query, 400)
	query["include_working"] = true
	b.request("GET", path+"/contexts/"+result["id"].(string), nil, 200)
	b.request("PUT", resourcePath+"/draft", map[string]any{"revision": 1, "base_head": "", "text": "修改计划：主角寻找哥哥。"}, 200)
	b.request("GET", path+"/contexts/"+result["id"].(string), nil, 409)
	b.request("POST", path+"/search", query, 409)
	query["working_refs"] = []any{workingChoice(t, b, path, "resource_draft", rid)}
	b.request("POST", path+"/search", query, 200)
}

func TestWorkingContextIncludesIPDraftAndCandidatesWithoutAdoption(t *testing.T) {
	b, _ := setup(t)
	pid := createProject(b, "设定候选协作", "ip-working")
	bid := branch(b, pid, "正史")
	path := "/api/projects/" + pid + "/branches/" + bid
	b.request("PUT", path+"/draft", map[string]any{"revision": 0, "base_head": "", "snapshot": originalSnapshot()}, 200)
	candidate := propose(b, pid, bid, "", "ip-candidate", originalSnapshot())
	rid := resource(b, pid, bid, "世界观说明")
	release := prepareRelease(b, pid, bid, "", "", "working-release", []map[string]any{{"resource_id": rid, "text": "尚未确认的世界观"}})
	knowledge := path + "/knowledge"
	b.request("POST", knowledge+"/index", map[string]any{}, 200)
	refs := []any{workingChoice(t, b, knowledge, "ip_draft", bid), workingChoice(t, b, knowledge, "ip_candidate", candidate), workingChoice(t, b, knowledge, "release_candidate", release)}
	query := map[string]any{"query": "完善世界观", "purpose": "critic", "include_working": true, "limit": 1, "working_refs": refs}
	result := b.request("POST", knowledge+"/search", query, 200)
	if len(result["unconfirmed_inputs"].([]any)) != 3 || len(result["facts"].([]any)) != 0 {
		t.Fatal("candidate not isolated", result)
	}
	b.request("POST", path+"/candidates/"+candidate+"/adopt", map[string]any{}, 200)
	b.request("GET", knowledge+"/contexts/"+result["id"].(string), nil, 409)
	query["working_refs"] = []any{workingChoice(t, b, knowledge, "release_candidate", release)}
	result = b.request("POST", knowledge+"/search", query, 200)
	releaseAdopt(b, pid, release)
	releasePublish(b, pid, release)
	b.request("GET", knowledge+"/contexts/"+result["id"].(string), nil, 503)
	b.request("POST", knowledge+"/search", query, 409)
}

func TestWorkingDraftEditInvalidatesFrozenGenerationWithoutChangingIndex(t *testing.T) {
	transport := &generationTransport{response: generationSuccess}
	b, svc, actor, pid, original := generationFixture(t, transport)
	ctx := context.Background()
	bid := original.BranchID
	b.request("PUT", "/api/model-bindings/critic", map[string]any{"connection_id": original.Frozen.Config.ID, "connection_revision": 1, "revision": 0}, 200)
	rid := resource(b, pid, bid, "草稿协作")
	draftPath := "/api/projects/" + pid + "/resources/" + rid + "/draft"
	b.request("PUT", draftPath, map[string]any{"revision": 0, "base_head": "", "text": "未确认计划：姐姐是航海家。"}, 200)
	path := "/api/projects/" + pid + "/branches/" + bid + "/knowledge"
	b.request("POST", path+"/index", map[string]any{}, 200)
	saved := b.request("POST", path+"/search", map[string]any{"query": "完善人物动机", "purpose": "critic", "include_working": true, "limit": 1, "working_refs": []any{workingChoice(t, b, path, "resource_draft", rid)}}, 200)
	input := service.GenerationInput{BranchID: bid, Role: "critic", ContextID: saved["id"].(string), PriceID: original.Frozen.Price.ID, OperationID: "draft-completed", Request: models.Request{Prompt: "审查草稿，不采用", MaxOutputTokens: 100}}
	done, err := svc.Prepare(ctx, actor, pid, input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Start(ctx, actor, pid, done.ID); err != nil {
		t.Fatal(err)
	}
	if state, err := svc.Execute(ctx, done.ID); err != nil || state != "SUCCEEDED" {
		t.Fatal(state, err)
	}
	if !strings.Contains(transport.payload, "姐姐是航海家。") || !strings.Contains(transport.payload, "unconfirmed_inputs") {
		t.Fatal("unconfirmed input missing or misclassified")
	}
	input.OperationID = "draft-queued"
	queued, err := svc.Prepare(ctx, actor, pid, input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Start(ctx, actor, pid, queued.ID); err != nil {
		t.Fatal(err)
	}
	input.OperationID = "draft-unstarted"
	unstarted, err := svc.Prepare(ctx, actor, pid, input)
	if err != nil {
		t.Fatal(err)
	}
	b.request("PUT", draftPath, map[string]any{"revision": 1, "base_head": "", "text": "未确认计划：姐姐是药师。"}, 200)
	if _, err = svc.Start(ctx, actor, pid, unstarted.ID); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("edited draft reserved funds", err)
	}
	if _, err = svc.Execute(ctx, queued.ID); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("edited draft dispatched", err)
	}
	if _, err = svc.Read(ctx, actor, pid, done.ID); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("edited draft result reused", err)
	}
	if transport.calls.Load() != 1 {
		t.Fatal("unexpected external calls")
	}
	if _, err = svc.Recover(ctx, queued.ID); err != nil {
		t.Fatal(err)
	}
}
