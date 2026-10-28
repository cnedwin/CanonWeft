package integration

import "testing"

func TestReleaseCompositionRebindsSelectedInputsAndRequiresNewReview(t *testing.T) {
	b, _ := setup(t)
	pid := createProject(b, "多批次组合", "composition-project")
	bid := branch(b, pid, "正史")
	a, c := resource(b, pid, bid, "前章"), resource(b, pid, bid, "后章")
	initial := prepareRelease(b, pid, bid, "", "", "initial", []map[string]any{{"resource_id": a, "text": "原前章"}, {"resource_id": c, "text": "原后章"}})
	releaseAdopt(b, pid, initial)
	releasePublish(b, pid, initial)
	catalog := b.request("GET", "/api/projects/"+pid+"/dependencies/catalog", nil, 200)
	var source any
	for _, v := range catalog["items"].([]any) {
		e := v.(map[string]any)
		r := e["ref"].(map[string]any)
		if r["id"] == a {
			source = r
		}
	}
	first := prepareRelease(b, pid, bid, "", "", "first", []map[string]any{{"resource_id": a, "expected_head": initial, "text": "修订前章"}})
	second := prepareRelease(b, pid, bid, "", "", "second", []map[string]any{{"resource_id": c, "expected_head": initial, "text": "修订后章", "dependencies": map[string]any{"frozen": false, "inputs": []any{map[string]any{"source": source, "kind": "semantic", "reason": "后章依据前章"}}}}})
	pins := []map[string]any{}
	for _, id := range []string{first, second} {
		r := b.request("GET", "/api/projects/"+pid+"/releases/"+id, nil, 200)
		pins = append(pins, map[string]any{"release_id": id, "manifest_hash": r["hash"]})
	}
	input := map[string]any{"operation_id": "compose", "releases": pins, "rebind_following": true}
	path := "/api/projects/" + pid + "/releases/composition"
	input["rebind_following"] = false
	unbound := b.request("POST", path+"/preview", input, 200)
	if len(unbound["conflicts"].([]any)) != 1 {
		t.Fatal("following old source silently accepted", unbound)
	}
	input["rebind_following"] = true
	other := prepareRelease(b, pid, bid, "", "", "conflicting-first", []map[string]any{{"resource_id": a, "expected_head": initial, "text": "另一份不同的前章修改"}})
	otherRelease := b.request("GET", "/api/projects/"+pid+"/releases/"+other, nil, 200)
	conflict := b.request("POST", path+"/preview", map[string]any{"operation_id": "conflicting-preview", "rebind_following": true, "releases": []any{pins[0], map[string]any{"release_id": other, "manifest_hash": otherRelease["hash"]}}}, 200)
	if len(conflict["conflicts"].([]any)) != 1 {
		t.Fatal("overlapping content silently selected", conflict)
	}
	pinned := prepareRelease(b, pid, bid, "", "", "historical-second", []map[string]any{{"resource_id": c, "expected_head": initial, "text": "保留历史前章的回忆", "dependencies": map[string]any{"frozen": false, "inputs": []any{map[string]any{"source": source, "kind": "semantic", "reason": "固定回忆中的原文", "pinned": true}}}}})
	pinnedRelease := b.request("GET", "/api/projects/"+pid+"/releases/"+pinned, nil, 200)
	history := b.request("POST", path+"/preview", map[string]any{"operation_id": "historical-preview", "rebind_following": true, "releases": []any{pins[0], map[string]any{"release_id": pinned, "manifest_hash": pinnedRelease["hash"]}}}, 200)
	if len(history["rebindings"].([]any)) != 0 || len(history["conflicts"].([]any)) != 0 {
		t.Fatal("historical pin silently rewritten", history)
	}
	preview := b.request("POST", path+"/preview", input, 200)
	if len(preview["updates"].([]any)) != 2 || len(preview["rebindings"].([]any)) != 1 {
		t.Fatal("composition omitted input or binding", preview)
	}
	result := b.request("POST", path, input, 201)
	id := result["id"].(string)
	again := b.request("POST", path, input, 201)
	if again["id"] != id {
		t.Fatal("duplicate composition")
	}
	manifest := result["manifest"].(map[string]any)
	if len(manifest["composition_parents"].([]any)) != 2 {
		t.Fatal("lost composition lineage")
	}
	for _, item := range manifest["artifacts"].([]any) {
		r := item.(map[string]any)
		if r["resource_id"] == c {
			ref := r["dependencies"].(map[string]any)["inputs"].([]any)[0].(map[string]any)["source"].(map[string]any)
			if ref["revision"] != id {
				t.Fatal("followed old revision", ref)
			}
		}
	}
	b.request("POST", "/api/projects/"+pid+"/releases/"+id+"/adopt", map[string]any{}, 503)
	b.request("POST", "/api/projects/"+pid+"/releases", map[string]any{"branch_id": bid, "operation_id": "split-composition", "updates": []any{map[string]any{"resource_id": a, "expected_head": initial, "text": "只取出前章", "origin": map[string]any{"parent_release_id": id}}}}, 409)
	input["rebind_following"] = false
	b.request("POST", path, input, 409)
	for _, resourceID := range []string{a, c} {
		if b.request("GET", "/api/projects/"+pid+"/resources/"+resourceID, nil, 200)["head"] != initial {
			t.Fatal("composition changed formal content")
		}
	}
}
