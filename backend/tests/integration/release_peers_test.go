package integration

import (
	"strings"
	"testing"
)

func TestPeerReleaseHTTPBindsExactUnpublishedIPAndPreservesReviewAncestry(t *testing.T) {
	b, _ := setup(t)
	project := createProject(b, "共同候选", "peer-http-project")
	up := branch(b, project, "小说")
	down := branch(b, project, "改编")
	ipCandidate := propose(b, project, up, "", "peer-ip", originalSnapshot())
	b.request("POST", "/api/projects/"+project+"/branches/"+up+"/candidates/"+ipCandidate+"/adopt", map[string]any{}, 200)
	sourceResource := resource(b, project, up, "新小说资料")
	upstream := prepareRelease(b, project, up, "", ipCandidate, "upstream", []map[string]any{{"resource_id": sourceResource, "text": "与新人设共同发布的情节。"}})
	root := "/api/projects/" + project
	sources := b.request("GET", root+"/releases/"+upstream+"/peer-sources", nil, 200)
	var entity map[string]any
	for _, item := range sources["entries"].([]any) {
		ref := item.(map[string]any)["ref"].(map[string]any)
		if ref["kind"] == "entity" && ref["id"] == "lin-1" {
			entity = ref
		}
	}
	if entity == nil || entity["revision"] != ipCandidate || len(sources["snapshot"].(map[string]any)["entities"].([]any)) != 1 {
		t.Fatal("unpublished IP material not returned")
	}
	foreign := createProject(b, "无关小说", "peer-foreign-project")
	b.request("GET", "/api/projects/"+foreign+"/releases/"+upstream+"/peer-sources", nil, 403)
	target := resource(b, project, down, "改编新场景")
	update := map[string]any{"resource_id": target, "text": "按新人设编排角色高光。", "dependencies": map[string]any{"frozen": false, "inputs": []any{map[string]any{"source": entity, "kind": "representation", "reason": "保留小说人物身份"}}}}
	input := map[string]any{"branch_id": down, "operation_id": "peer-target", "updates": []any{update}, "peer_releases": []any{map[string]any{"release_id": upstream, "manifest_hash": sources["release"].(map[string]any)["hash"]}}}
	correctHash := entity["hash"]
	entity["hash"] = strings.Repeat("0", 64)
	b.request("POST", root+"/releases", input, 409)
	entity["hash"] = correctHash
	release := b.request("POST", root+"/releases", input, 201)
	id := release["id"].(string)
	b.request("POST", root+"/releases/"+id+"/adopt", map[string]any{}, 503)
	update["text"] = "手改后继续保留小说人物身份与高光。"
	update["origin"] = map[string]any{"parent_release_id": id}
	input["operation_id"] = "peer-edited"
	delete(input, "peer_releases")
	child := b.request("POST", root+"/releases", input, 201)
	manifest := child["manifest"].(map[string]any)
	if manifest["peer_releases"].([]any)[0] != upstream || manifest["patch_parents"].([]any)[0] != id {
		t.Fatal("manual editing lost peer or review history", manifest)
	}
	b.request("POST", root+"/releases/"+child["id"].(string)+"/adopt", map[string]any{}, 503)
	// A later edited candidate must not retroactively become an ancestor of the
	// original candidate. Both still lack their own independent review.
	b.request("POST", root+"/releases/"+id+"/adopt", map[string]any{}, 503)
	if b.request("GET", root+"/resources/"+target, nil, 200)["head"] != "" {
		t.Fatal("preparation published target")
	}
}
