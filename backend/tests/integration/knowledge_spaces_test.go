package integration

import (
	"novelstudio/internal/domain"
	"testing"
)

func TestKnowledgeSpacesPublishedPolicyAndCharacterReveal(t *testing.T) {
	b, _ := setup(t)
	pid := createProject(b, "资料空间", "one")
	bid := branch(b, pid, "正史")
	cid := propose(b, pid, bid, "", "ip", originalSnapshot())
	publish(b, pid, bid, cid)
	ids := map[string]string{}
	updates := []map[string]any{}
	for _, space := range []string{"evidence", "working", "methods", "research"} {
		id := resource(b, pid, bid, space)
		ids[space] = id
		policy := domain.KnowledgePolicy{Space: space, Visibility: "author"}
		if space == "evidence" {
			policy.Visibility = "restricted"
			policy.KnownBy = []string{"lin-1"}
			policy.RevealAt = 5
		}
		updates = append(updates, map[string]any{"resource_id": id, "text": "宝剑记录。侠客在山洞寻找失落的宝剑。" + space, "policy": policy})
	}
	rid := prepareRelease(b, pid, bid, cid, "", "release", updates)
	releaseAdopt(b, pid, rid)
	releasePublish(b, pid, rid)
	path := "/api/projects/" + pid + "/branches/" + bid + "/knowledge"
	b.request("POST", path+"/index", map[string]any{}, 200)
	query := map[string]any{"query": "宝剑", "purpose": "author", "limit": 10}
	result := b.request("POST", path+"/search", query, 200)
	if len(result["evidence"].([]any)) != 1 || result["methods"] != nil || result["working_materials"] != nil || result["research_candidates"] != nil {
		t.Fatal("spaces mixed in default context", result)
	}
	query["include_working"] = true
	query["include_methods"] = true
	result = b.request("POST", path+"/search", query, 200)
	if len(result["methods"].([]any)) != 1 || len(result["working_materials"].([]any)) != 1 || result["research_candidates"] != nil {
		t.Fatal("explicit space selection failed", result)
	}
	query["purpose"] = "research"
	result = b.request("POST", path+"/search", query, 200)
	if len(result["research_candidates"].([]any)) != 1 {
		t.Fatal("research candidate missing")
	}
	query["purpose"] = "npc"
	query["perspective"] = "lin-1"
	query["story_time"] = 4
	query["include_working"] = false
	query["include_methods"] = false
	result = b.request("POST", path+"/search", query, 200)
	if len(result["evidence"].([]any)) != 0 {
		t.Fatal("future reveal leaked")
	}
	query["story_time"] = 5
	result = b.request("POST", path+"/search", query, 200)
	if len(result["evidence"].([]any)) != 1 || result["research_candidates"] != nil {
		t.Fatal("known evidence not supplied or candidate leaked")
	}
	// Classification is an adopted immutable revision, not a mutable flag.
	next := prepareRelease(b, pid, bid, cid, "", "move-space", []map[string]any{{"resource_id": ids["evidence"], "expected_head": rid, "text": "宝剑记录。侠客在山洞寻找失落的宝剑。evidence", "policy": domain.KnowledgePolicy{Space: "research", Visibility: "author"}}})
	b.request("GET", path+"/contexts/"+result["id"].(string), nil, 200)
	releaseAdopt(b, pid, next)
	releasePublish(b, pid, next)
	b.request("GET", path+"/contexts/"+result["id"].(string), nil, 503)
	b.request("POST", path+"/index", map[string]any{}, 200)
	result = b.request("POST", path+"/search", query, 200)
	if len(result["evidence"].([]any)) != 0 {
		t.Fatal("moved evidence still exposed")
	}
}
