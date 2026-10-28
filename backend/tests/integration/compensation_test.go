package integration

import "testing"

func TestTextCompensationPreviewKeepsLaterWorkAndRequiresReview(t *testing.T) {
	b, _ := setup(t)
	pid := createProject(b, "正文补偿", "compensation-project")
	bid := branch(b, pid, "正史")
	rid := resource(b, pid, bid, "场景")
	root := "/api/projects/" + pid
	publishText := func(previous, text, op string) string {
		id := prepareRelease(b, pid, bid, "", "", op, []map[string]any{{"resource_id": rid, "expected_head": previous, "text": text}})
		releaseAdopt(b, pid, id)
		releasePublish(b, pid, id)
		return id
	}
	base := publishText("", "序章🌙：旧城。\n保留结尾。", "compensation-base")
	changed := publishText(base, "序章🌙：新城。\n保留结尾。", "compensation-change")
	later := publishText(changed, "前言\n序章🌙：新城。\n保留结尾。", "compensation-prefix")
	latest := publishText(later, "前言\n序章🌙：新城。\n另写的结尾。", "compensation-suffix")
	request := map[string]any{"branch_id": bid, "release_id": changed, "expected_ip_head": "", "reason": "撤回地点改名，保留后续创作"}
	preview := b.request("POST", root+"/compensations/preview", request, 200)
	if len(preview["conflicts"].([]any)) != 0 {
		t.Fatal(preview)
	}
	items := preview["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["result"].(map[string]any)["text"] != "前言\n序章🌙：旧城。\n另写的结尾。" {
		t.Fatal(preview)
	}
	group := preview["group"].(map[string]any)
	if group["compensates_release_id"] != changed {
		t.Fatal("missing compensation ancestry", group)
	}
	input := map[string]any{"branch_id": bid, "expected_ip_head": "", "operation_id": "compensation-candidate", "group": group}
	candidate := b.request("POST", root+"/patches/releases", input, 201)
	if retry := b.request("POST", root+"/patches/releases", input, 201); retry["id"] != candidate["id"] {
		t.Fatal("duplicate compensation")
	}
	b.request("POST", root+"/releases/"+candidate["id"].(string)+"/adopt", map[string]any{}, 503)
	current := b.request("GET", root+"/resources/"+rid, nil, 200)
	if current["head"] != latest || current["text"] != "前言\n序章🌙：新城。\n另写的结尾。" {
		t.Fatal("preview overwrote latest", current)
	}
	// A client cannot label unrelated edits as an exact compensation.
	group["patches"].([]any)[0].(map[string]any)["replacement"] = "任意替换"
	input["operation_id"] = "forged-compensation"
	b.request("POST", root+"/patches/releases", input, 409)
}

func TestTextCompensationReportsConflictWithoutPartialGroup(t *testing.T) {
	b, _ := setup(t)
	pid := createProject(b, "重叠冲突", "compensation-conflict")
	bid := branch(b, pid, "正史")
	a, c := resource(b, pid, bid, "已再改"), resource(b, pid, bid, "可以撤回")
	base := prepareRelease(b, pid, bid, "", "", "compensation-conflict-base", []map[string]any{{"resource_id": a, "text": "旧城"}, {"resource_id": c, "text": "旧城"}})
	releaseAdopt(b, pid, base)
	releasePublish(b, pid, base)
	changed := prepareRelease(b, pid, bid, "", "", "compensation-conflict-change", []map[string]any{{"resource_id": a, "expected_head": base, "text": "新城"}, {"resource_id": c, "expected_head": base, "text": "新城"}})
	releaseAdopt(b, pid, changed)
	releasePublish(b, pid, changed)
	later := prepareRelease(b, pid, bid, "", "", "compensation-conflict-later", []map[string]any{{"resource_id": a, "expected_head": changed, "text": "古城"}})
	releaseAdopt(b, pid, later)
	releasePublish(b, pid, later)
	preview := b.request("POST", "/api/projects/"+pid+"/compensations/preview", map[string]any{"branch_id": bid, "release_id": changed, "expected_ip_head": "", "reason": "撤回地名变更"}, 200)
	if preview["group"] != nil || len(preview["items"].([]any)) != 0 || len(preview["conflicts"].([]any)) != 1 {
		t.Fatal("returned partial group", preview)
	}
	if preview["conflicts"].([]any)[0].(map[string]any)["resource_id"] != a {
		t.Fatal(preview)
	}
}
