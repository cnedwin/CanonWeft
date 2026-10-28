package integration

import (
	"strings"
	"testing"
)

func TestPatchDraftPrivateCASRetryAndStaleRecovery(t *testing.T) {
	b, _ := setup(t)
	pid := createProject(b, "修订工作区", "patch-draft-project")
	bid := branch(b, pid, "正史")
	path := "/api/projects/" + pid + "/branches/" + bid + "/patch-draft"
	if empty := b.request("GET", path, nil, 200); empty["revision"] != float64(0) || empty["content"] != nil {
		t.Fatal(empty)
	}
	content := map[string]any{"base_ip_head": "", "operation_id": "pending-patch-candidate", "group": map[string]any{"id": strings.Repeat("a", 32), "members": []string{}, "patches": []any{}}, "selected_resource_id": "", "snippet": "还没写完的原文🌙", "replacement": "新的描写", "reason": "待完善理由", "compensation_source_id": "", "compensation_reason": ""}
	request := map[string]any{"revision": 0, "content": content}
	saved := b.request("PUT", path, request, 200)
	if saved["revision"] != float64(1) {
		t.Fatal(saved)
	}
	if retry := b.request("PUT", path, request, 200); retry["revision"] != float64(1) {
		t.Fatal("lost save receipt incremented twice", retry)
	}
	content["snippet"] = "另一标签页试图覆盖"
	b.request("PUT", path, request, 409)
	loaded := b.request("GET", path, nil, 200)
	if loaded["content"].(map[string]any)["snippet"] != "还没写完的原文🌙" {
		t.Fatal(loaded)
	}
	member := b.request("POST", "/api/members", map[string]any{"username": "draft-editor", "password": "draft-editor-password", "role": "editor"}, 201)
	b.request("PUT", "/api/projects/"+pid+"/members/"+member["id"].(string), map[string]any{"role": "editor"}, 200)
	editor := &browser{t: t, handler: b.handler}
	editor.request("POST", "/api/login", map[string]any{"username": "draft-editor", "password": "draft-editor-password"}, 200)
	if own := editor.request("GET", path, nil, 200); own["content"] != nil {
		t.Fatal("another author read private draft", own)
	}
	head := propose(b, pid, bid, "", "patch-draft-ip-change", originalSnapshot())
	publish(b, pid, bid, head)
	loaded = b.request("GET", path, nil, 200)
	if loaded["stale"] != true || loaded["content"].(map[string]any)["base_ip_head"] != "" {
		t.Fatal("stale draft silently rebased", loaded)
	}
	b.request("DELETE", "/api/projects/"+pid+"/members/"+member["id"].(string), nil, 200)
	editor.request("GET", path, nil, 403)
}

func TestPatchDraftRejectsCrossProjectResourceAndDeletedProject(t *testing.T) {
	b, _ := setup(t)
	pid := createProject(b, "本书", "patch-draft-scope")
	bid := branch(b, pid, "正史")
	other := createProject(b, "别书", "patch-draft-other")
	ob := branch(b, other, "正史")
	rid := resource(b, other, ob, "别书正文")
	path := "/api/projects/" + pid + "/branches/" + bid + "/patch-draft"
	content := map[string]any{"base_ip_head": "", "operation_id": "draft-op", "group": map[string]any{"id": strings.Repeat("a", 32), "members": []string{}, "patches": []any{}}, "selected_resource_id": rid}
	b.request("PUT", path, map[string]any{"revision": 0, "content": content}, 403)
	content["selected_resource_id"] = ""
	b.request("PUT", path, map[string]any{"revision": 0, "content": content}, 200)
	b.request("DELETE", "/api/projects/"+pid, map[string]any{"confirm_name": "本书", "revision": 1, "operation_id": "delete-patch-draft"}, 200)
	b.request("GET", path, nil, 403)
	b.request("PUT", path, map[string]any{"revision": 1, "content": content}, 403)
}
