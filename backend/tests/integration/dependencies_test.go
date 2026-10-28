package integration

import "testing"

func TestJointReleasePinsProspectiveInputsAndRejectsCircularVersions(t *testing.T) {
	b, _ := setup(t)
	pid := createProject(b, "同期版本引用", "joint-pins")
	bid := branch(b, pid, "正史")
	candidate := propose(b, pid, bid, "", "future-ip", originalSnapshot())
	root := "/api/projects/" + pid
	b.request("POST", root+"/branches/"+bid+"/candidates/"+candidate+"/adopt", map[string]any{}, 200)
	first, second := resource(b, pid, bid, "上游"), resource(b, pid, bid, "下游")
	dep := func(kind, id, revision string) map[string]any {
		return map[string]any{"inputs": []any{map[string]any{"kind": "evidence", "reason": "明确采用同批内容", "source": map[string]any{"kind": kind, "branch_id": bid, "id": id, "revision": revision, "hash": ""}}}}
	}
	updates := []map[string]any{{"resource_id": second, "expected_head": "", "text": "下游采用上游新证据", "dependencies": dep("resource", first, "$release")}, {"resource_id": first, "expected_head": "", "text": "上游引用新设定", "dependencies": dep("entity", "lin-1", "$candidate")}}
	id := prepareRelease(b, pid, bid, "", candidate, "future-bundle", updates)
	if retry := prepareRelease(b, pid, bid, "", candidate, "future-bundle", updates); retry != id {
		t.Fatal("future refs broke idempotency")
	}
	prepared := b.request("GET", root+"/releases/"+id, nil, 200)
	refs := prepared["manifest"].(map[string]any)["artifacts"].([]any)
	for _, raw := range refs {
		r := raw.(map[string]any)
		source := r["dependencies"].(map[string]any)["inputs"].([]any)[0].(map[string]any)["source"].(map[string]any)
		want := candidate
		if r["resource_id"] == second {
			want = id
		}
		if source["revision"] != want || len(source["hash"].(string)) != 64 {
			t.Fatal("unresolved prospective source", source)
		}
	}
	// Preparing exact prospective references is not independent review. The
	// qualified positive publication/retry case lives in the semantic suite.
	b.request("POST", root+"/releases/"+id+"/adopt", map[string]any{}, 503)
	b.request("POST", root+"/releases/"+id+"/publish", map[string]any{}, 409)
	for _, resourceID := range []string{first, second} {
		if b.request("GET", root+"/resources/"+resourceID, nil, 200)["head"] != "" {
			t.Fatal("unreviewed joint content became current")
		}
	}
	// Current-release self-reference and mutual recursion cannot yield immutable hashes.
	cycle := []map[string]any{{"resource_id": first, "expected_head": "", "text": "甲", "dependencies": dep("resource", second, "$release")}, {"resource_id": second, "expected_head": "", "text": "乙", "dependencies": dep("resource", first, "$release")}}
	b.request("POST", root+"/releases", map[string]any{"branch_id": bid, "expected_ip_head": "", "ip_candidate_id": candidate, "operation_id": "cycle", "updates": cycle}, 400)
}

func dependencyFromCatalog(t *testing.T, b *browser, pid, kind, id string) map[string]any {
	t.Helper()
	items := b.request("GET", "/api/projects/"+pid+"/dependencies/catalog", nil, 200)["items"].([]any)
	for _, raw := range items {
		ref := raw.(map[string]any)["ref"].(map[string]any)
		if ref["kind"] == kind && ref["id"] == id {
			return ref
		}
	}
	t.Fatal("reference missing", kind, id)
	return nil
}
func TestResourceReleaseImpactIncludesDependentContentAndSavedHistory(t *testing.T) {
	b, _ := setup(t)
	pid := createProject(b, "资料变更传播", "resource-impact")
	bid := branch(b, pid, "正史")
	a, c := resource(b, pid, bid, "研究依据"), resource(b, pid, bid, "故事规划")
	root := "/api/projects/" + pid
	original := prepareRelease(b, pid, bid, "", "", "original", []map[string]any{{"resource_id": a, "expected_head": "", "text": "药剂仅在月圆有效"}})
	releaseAdopt(b, pid, original)
	releasePublish(b, pid, original)
	source := dependencyFromCatalog(t, b, pid, "resource", a)
	downstream := prepareRelease(b, pid, bid, "", "", "downstream", []map[string]any{{"resource_id": c, "expected_head": "", "text": "故事安排在月圆之夜", "dependencies": map[string]any{"inputs": []any{map[string]any{"source": source, "kind": "evidence", "reason": "药效时间决定剧情安排"}}}}})
	releaseAdopt(b, pid, downstream)
	releasePublish(b, pid, downstream)
	changed := prepareRelease(b, pid, bid, "", "", "change", []map[string]any{{"resource_id": a, "expected_head": original, "text": "药剂仅在新月有效"}})
	plan := b.request("POST", root+"/changes", map[string]any{"operation_id": "release-impact", "branch_id": bid, "release_id": changed}, 201)
	items := plan["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["node"].(map[string]any)["id"] != c || items[0].(map[string]any)["action"] != "REBUILD" {
		t.Fatal(plan)
	}
	history := b.request("GET", root+"/changes?branch="+bid, nil, 200)["items"].([]any)
	if len(history) != 1 || history[0].(map[string]any)["id"] != plan["id"] {
		t.Fatal(history)
	}
	if got := b.request("GET", root+"/changes/"+plan["id"].(string), nil, 200); got["release_id"] != changed || got["stale"] != false {
		t.Fatal(got)
	}
	releaseAdopt(b, pid, changed)
	releasePublish(b, pid, changed)
	if got := b.request("GET", root+"/changes/"+plan["id"].(string), nil, 200); got["stale"] != true {
		t.Fatal("resource-head change not recognized", got)
	}
	if b.request("GET", root+"/resources/"+c, nil, 200)["text"] != "故事安排在月圆之夜" {
		t.Fatal("impact rewrote downstream")
	}
}
func TestVersionedDependenciesPropagateAcrossBranchesWithoutRewritingFrozenContent(t *testing.T) {
	b, _ := setup(t)
	pid := createProject(b, "跨作品变更", "dependencies")
	canon := branch(b, pid, "正史")
	base := propose(b, pid, canon, "", "base", originalSnapshot())
	publish(b, pid, canon, base)
	adapted := branch(b, pid, "改编")
	first, second, unrelated := resource(b, pid, adapted, "角色形象说明"), resource(b, pid, adapted, "后续表现说明"), resource(b, pid, adapted, "无关风景")
	root := "/api/projects/" + pid
	entity := dependencyFromCatalog(t, b, pid, "entity", "lin-1")
	policy := map[string]any{"frozen": true, "inputs": []any{map[string]any{"source": entity, "kind": "representation", "reason": "沿用林舟的角色形象"}}}
	a := prepareRelease(b, pid, adapted, "", "", "first", []map[string]any{{"resource_id": first, "expected_head": "", "text": "形象说明原稿", "dependencies": policy}})
	releaseAdopt(b, pid, a)
	releasePublish(b, pid, a)
	ref := dependencyFromCatalog(t, b, pid, "resource", first)
	secondPolicy := map[string]any{"frozen": false, "inputs": []any{map[string]any{"source": ref, "kind": "production", "reason": "使用上游形象说明"}}}
	r := prepareRelease(b, pid, adapted, "", "", "second", []map[string]any{{"resource_id": second, "expected_head": "", "text": "后续场景原稿", "dependencies": secondPolicy}, {"resource_id": unrelated, "expected_head": "", "text": "山川记录"}})
	releaseAdopt(b, pid, r)
	releasePublish(b, pid, r)
	snapshot := originalSnapshot()
	snapshot.Entities[0].Description = "改为短发，左眼有伤痕"
	changed := propose(b, pid, canon, base, "changed", snapshot)
	plan := b.request("POST", root+"/changes", map[string]any{"operation_id": "impact", "branch_id": canon, "candidate_id": changed}, 201)
	items := plan["items"].([]any)
	if len(items) != 2 {
		t.Fatal("lost downstream or changed unrelated", plan)
	}
	for _, raw := range items {
		item := raw.(map[string]any)
		id := item["node"].(map[string]any)["id"]
		if id == first && item["action"] != "DECIDE" {
			t.Fatal(item)
		}
		if id == second && item["action"] != "REVIEW" {
			t.Fatal(item)
		}
	}
	for id, want := range map[string]string{first: "形象说明原稿", second: "后续场景原稿", unrelated: "山川记录"} {
		if b.request("GET", root+"/resources/"+id, nil, 200)["text"] != want {
			t.Fatal("analysis rewrote source")
		}
	}
	// Frozen edits need an explicit reason and owner adoption; no silent bypass.
	b.request("POST", root+"/releases", map[string]any{"operation_id": "frozen-bypass", "branch_id": adapted, "updates": []any{map[string]any{"resource_id": first, "expected_head": a, "text": "直接覆盖"}}}, 400)
	// Project boundary remains authoritative even when another project knows an ID.
	other := createProject(b, "其他作品", "other-project")
	b.request("GET", "/api/projects/"+other+"/changes/"+plan["id"].(string), nil, 403)
	if retry := b.request("POST", root+"/changes", map[string]any{"operation_id": "impact", "branch_id": canon, "candidate_id": changed}, 201); retry["id"] != plan["id"] {
		t.Fatal("retry changed immutable impact plan")
	}
	if history := b.request("GET", root+"/changes/"+plan["id"].(string), nil, 200); history["stale"] != false || len(history["untracked"].([]any)) != 1 {
		t.Fatal("history or coverage missing", history)
	}
	member := b.request("POST", "/api/members", map[string]any{"username": "editor", "password": "editor-password-123", "role": "editor"}, 201)
	b.request("PUT", root+"/members/"+member["id"].(string), map[string]any{"role": "editor"}, 200)
	editor := &browser{t: t, handler: b.handler}
	editor.request("POST", "/api/login", map[string]any{"username": "editor", "password": "editor-password-123"}, 200)
	frozenEdit := prepareRelease(editor, pid, adapted, "", "", "frozen-edit", []map[string]any{{"resource_id": first, "expected_head": a, "text": "经过裁决的形象说明", "frozen_change_reason": "服从设定修订，逐项核对影响后修改"}})
	editor.request("POST", root+"/releases/"+frozenEdit+"/adopt", map[string]any{}, 403)
	releaseAdopt(b, pid, frozenEdit)
	releasePublish(b, pid, frozenEdit)
	if history := b.request("GET", root+"/changes/"+plan["id"].(string), nil, 200); history["stale"] != true {
		t.Fatal("changed graph accepted as current", history)
	}
	// A follow-latest edge is checked again when creating/adopting/publishing candidates.
	staleInput := map[string]any{"operation_id": "stale-source", "branch_id": adapted, "updates": []any{map[string]any{"resource_id": second, "expected_head": r, "text": "新场景", "dependencies": secondPolicy}}}
	b.request("POST", root+"/releases", staleInput, 409)
	secondPolicy["inputs"].([]any)[0].(map[string]any)["pinned"] = true
	pinnedRelease := prepareRelease(b, pid, adapted, "", "", "pinned-source", []map[string]any{{"resource_id": second, "expected_head": r, "text": "明确沿用旧形象的场景", "dependencies": secondPolicy}})
	releaseAdopt(b, pid, pinnedRelease)
	releasePublish(b, pid, pinnedRelease)
}
