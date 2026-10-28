package integration

import (
	"novelstudio/internal/domain"
	"testing"
)

func TestSemanticEvidenceAndStaleIndex(t *testing.T) {
	b, _ := setup(t)
	pid := createProject(b, "中文知识", "one")
	bid := branch(b, pid, "正史")
	rid := resource(b, pid, bid, "洞穴记录")
	text := "🌙剑客进入山洞寻找失落的宝剑。\n\n溪流在洞穴深处汇聚。"
	release := prepareRelease(b, pid, bid, "", "", "r1", []map[string]any{{"resource_id": rid, "expected_head": "", "text": text}})
	releaseAdopt(b, pid, release)
	releasePublish(b, pid, release)
	path := "/api/projects/" + pid + "/branches/" + bid + "/knowledge"
	input := map[string]any{"query": "侠客寻觅遗失的兵器", "purpose": "author", "limit": 4}
	b.request("POST", path+"/search", input, 200)
	built := b.request("POST", path+"/index", map[string]any{}, 200)
	if built["chunks"].(float64) < 1 {
		t.Fatal("empty index")
	}
	result := b.request("POST", path+"/search", input, 200)
	evidence := result["evidence"].([]any)
	if len(evidence) == 0 {
		t.Fatal("semantic-only query returned no evidence")
	}
	first := evidence[0].(map[string]any)
	start, end := int(first["start"].(float64)), int(first["end"].(float64))
	if string([]rune(text)[start:end]) != first["text"] {
		t.Fatal("Unicode anchor does not roundtrip")
	}
	contextID := result["id"].(string)
	b.request("GET", path+"/contexts/"+contextID, nil, 200)
	next := prepareRelease(b, pid, bid, "", "", "r2", []map[string]any{{"resource_id": rid, "expected_head": release, "text": "宝剑已被带走。"}})
	releaseAdopt(b, pid, next)
	releasePublish(b, pid, next)
	latest := b.request("POST", path+"/search", input, 200)
	for _, item := range latest["evidence"].([]any) {
		if item.(map[string]any)["text"] != "宝剑已被带走。" {
			t.Fatal("new search mixed old source")
		}
	}
	b.request("GET", path+"/contexts/"+contextID, nil, 503)
}

func TestContextSecretsAndCurrentPermissions(t *testing.T) {
	b, _ := setup(t)
	pid := createProject(b, "视角隔离", "one")
	bid := branch(b, pid, "正史")
	snap := originalSnapshot()
	snap.Assertions = append(snap.Assertions, domain.Assertion{ID: "secret", Subject: "lin-1", Predicate: "秘密", Value: "密室入口在树下", Kind: "fact", Visibility: "author", Source: "作者"})
	cid := propose(b, pid, bid, "", "ip", snap)
	publish(b, pid, bid, cid)
	path := "/api/projects/" + pid + "/branches/" + bid + "/knowledge"
	b.request("POST", path+"/index", map[string]any{}, 200)
	member := b.request("POST", "/api/members", map[string]any{"username": "researcher", "password": "researcher-password-123", "role": "editor"}, 201)["id"].(string)
	b.request("PUT", "/api/projects/"+pid+"/members/"+member, map[string]any{"role": "editor"}, 200)
	editor := &browser{t: t, handler: b.handler}
	editor.request("POST", "/api/login", map[string]any{"username": "researcher", "password": "researcher-password-123"}, 200)
	result := editor.request("POST", path+"/search", map[string]any{"query": "密室在哪里", "purpose": "npc", "perspective": "lin-1", "limit": 1}, 200)
	if len(result["facts"].([]any)) != 1 || len(result["hidden_constraints"].([]any)) != 0 || len(result["evidence"].([]any)) != 0 {
		t.Fatal("NPC received secret/author evidence")
	}
	author := editor.request("POST", path+"/search", map[string]any{"query": "无关检索词", "purpose": "author", "limit": 1}, 200)
	if len(author["facts"].([]any)) != 2 {
		t.Fatal("required constraints were similarity-truncated")
	}
	b.request("DELETE", "/api/projects/"+pid+"/members/"+member, nil, 200)
	editor.request("GET", path+"/contexts/"+author["id"].(string), nil, 403)
	other := createProject(b, "另一个IP", "other")
	otherBranch := branch(b, other, "另一正史")
	b.request("GET", "/api/projects/"+other+"/branches/"+otherBranch+"/knowledge/contexts/"+author["id"].(string), nil, 403)
}
