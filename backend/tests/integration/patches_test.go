package integration

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

func patchFixtureHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func TestPatchGroupPreviewAndCandidatePreserveHeadsAndDrafts(t *testing.T) {
	b, db := setup(t)
	pid := createProject(b, "局部修订", "patch-project")
	bid := branch(b, pid, "正史")
	a, c := resource(b, pid, bid, "场景正文"), resource(b, pid, bid, "关联记录")
	text := "序章：林舟🌙走进旧城。\n他没有回头。"
	base := prepareRelease(b, pid, bid, "", "", "patch-base", []map[string]any{{"resource_id": a, "expected_head": "", "text": text}, {"resource_id": c, "expected_head": "", "text": text}})
	releaseAdopt(b, pid, base)
	releasePublish(b, pid, base)
	b.request("PUT", "/api/projects/"+pid+"/resources/"+a+"/draft", map[string]any{"revision": 0, "base_head": base, "text": "作者正在输入的新情节"}, 200)
	groupID := strings.Repeat("b", 32)
	patch := func(id, resource string) map[string]any {
		return map[string]any{"id": id, "group_id": groupID, "resource_id": resource, "base_revision": base, "base_hash": patchFixtureHash(text), "start_rune": 8, "end_rune": 10, "expected_slice_hash": patchFixtureHash("旧城"), "replacement": "新城", "reason": "同步地点设定"}
	}
	group := map[string]any{"id": groupID, "members": []string{a, c}, "patches": []map[string]any{patch(strings.Repeat("c", 32), a), patch(strings.Repeat("d", 32), c)}}
	request := map[string]any{"branch_id": bid, "expected_ip_head": "", "group": group}
	path := "/api/projects/" + pid + "/patches"
	preview := b.request("POST", path+"/preview", request, 200)
	items, ok := preview["items"].([]any)
	if !ok || len(items) != 2 {
		t.Fatal("incomplete patch preview", preview)
	}
	for _, item := range items {
		result := item.(map[string]any)["result"].(map[string]any)
		if result["text"] != "序章：林舟🌙走进新城。\n他没有回头。" {
			t.Fatal("incorrect preview", result)
		}
	}
	request["operation_id"] = "patch-candidate"
	saved := b.request("POST", path+"/releases", request, 201)
	id := saved["id"].(string)
	if repeated := b.request("POST", path+"/releases", request, 201); repeated["id"] != id {
		t.Fatal("patch candidate duplicated")
	}
	// Manual patch preparation also requires an independent review; it cannot
	// label itself reviewed or publish half of a declared group.
	b.request("POST", "/api/projects/"+pid+"/releases/"+id+"/adopt", map[string]any{}, 503)
	b.request("POST", "/api/projects/"+pid+"/releases/"+id+"/publish", map[string]any{}, 409)
	for _, resourceID := range []string{a, c} {
		current := b.request("GET", "/api/projects/"+pid+"/resources/"+resourceID, nil, 200)
		if current["head"] != base || current["text"] != text {
			t.Fatal("preview or unreviewed patch changed published state")
		}
	}
	draft := b.request("GET", "/api/projects/"+pid+"/resources/"+a+"/draft", nil, 200)
	if draft["text"] != "作者正在输入的新情节" || draft["revision"] != float64(1) {
		t.Fatal("patch replaced unsaved draft")
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM artifact_releases WHERE project_id=?", pid).Scan(&count); err != nil || count != 2 {
		t.Fatal("unexpected release count", count, err)
	}
	// Editing a patch candidate cannot shed its review requirement or split its
	// still-unpublished dependency group through the generic release endpoint.
	partial := []map[string]any{{"resource_id": a, "expected_head": base, "text": "手工调整后的新城场景", "origin": map[string]any{"parent_release_id": id}}}
	b.request("POST", "/api/projects/"+pid+"/releases", map[string]any{"branch_id": bid, "operation_id": "partial-patch-edit", "updates": partial}, 409)
	partial = append(partial, map[string]any{"resource_id": c, "expected_head": base, "text": "同步修改后的新城记录", "origin": map[string]any{"parent_release_id": id}})
	edited := prepareRelease(b, pid, bid, "", "", "full-patch-edit", partial)
	b.request("POST", "/api/projects/"+pid+"/releases/"+edited+"/adopt", map[string]any{}, 503)
}

func TestPatchPreviewRejectsStaleCrossProjectAndIncompleteGroup(t *testing.T) {
	b, _ := setup(t)
	pid := createProject(b, "修订范围", "patch-scope")
	bid := branch(b, pid, "正史")
	a := resource(b, pid, bid, "原文")
	base := prepareRelease(b, pid, bid, "", "", "patch-scope-base", []map[string]any{{"resource_id": a, "expected_head": "", "text": "旧城"}})
	releaseAdopt(b, pid, base)
	releasePublish(b, pid, base)
	groupID := strings.Repeat("b", 32)
	p := map[string]any{"id": strings.Repeat("c", 32), "group_id": groupID, "resource_id": a, "base_revision": base, "base_hash": patchFixtureHash("旧城"), "start_rune": 0, "end_rune": 2, "expected_slice_hash": patchFixtureHash("旧城"), "replacement": "新城", "reason": "调整地点"}
	group := map[string]any{"id": groupID, "members": []string{a}, "patches": []map[string]any{p}}
	request := map[string]any{"branch_id": bid, "expected_ip_head": "", "group": group}
	path := "/api/projects/" + pid + "/patches/preview"
	b.request("POST", path, request, 200)
	p["base_revision"] = strings.Repeat("e", 32)
	b.request("POST", path, request, 409)
	p["base_revision"] = base
	group["members"] = []string{a, strings.Repeat("e", 32)}
	b.request("POST", path, request, 403)
	group["members"] = []string{a}
	group["patches"] = []map[string]any{}
	b.request("POST", path, request, 400)
	group["patches"] = []map[string]any{p}
	other := createProject(b, "另一部作品", "patch-other")
	otherBranch := branch(b, other, "正史")
	request["branch_id"] = otherBranch
	b.request("POST", "/api/projects/"+other+"/patches/preview", request, 403)
}
