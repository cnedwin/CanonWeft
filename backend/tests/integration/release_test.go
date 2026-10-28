package integration

import "testing"

func resource(b *browser, pid, bid, name string) string {
	return b.request("POST", "/api/projects/"+pid+"/resources", map[string]any{"branch_id": bid, "name": name, "kind": "note", "operation_id": "resource-" + name}, 201)["id"].(string)
}
func prepareRelease(b *browser, pid, bid, head, ipCandidate, op string, updates []map[string]any) string {
	return b.request("POST", "/api/projects/"+pid+"/releases", map[string]any{"branch_id": bid, "expected_ip_head": head, "ip_candidate_id": ipCandidate, "operation_id": op, "updates": updates}, 201)["id"].(string)
}
func releaseAdopt(b *browser, pid, id string) {
	b.request("POST", "/api/projects/"+pid+"/releases/"+id+"/adopt", map[string]any{}, 200)
}
func releasePublish(b *browser, pid, id string) {
	b.request("POST", "/api/projects/"+pid+"/releases/"+id+"/publish", map[string]any{}, 200)
}
func TestManualArtifactGroupIsAtomicAndRetrySafe(t *testing.T) {
	b, db := setup(t)
	pid := createProject(b, "联合内容", "one")
	bid := branch(b, pid, "正史")
	ip := propose(b, pid, bid, "", "ip", originalSnapshot())
	b.request("POST", "/api/projects/"+pid+"/branches/"+bid+"/candidates/"+ip+"/adopt", map[string]any{}, 200)
	// A separately confirmed manual IP publication stays available without a
	// model. The qualified IP+artifact atomic path is covered in qualification.
	b.request("POST", "/api/projects/"+pid+"/branches/"+bid+"/candidates/"+ip+"/publish", map[string]any{}, 200)
	a, c := resource(b, pid, bid, "故事说明"), resource(b, pid, bid, "事件记录")
	updates := []map[string]any{{"resource_id": a, "expected_head": "", "text": "林舟走进雾城。"}, {"resource_id": c, "expected_head": "", "text": "入城：林舟抵达雾城。"}}
	rid := prepareRelease(b, pid, bid, ip, "", "release", updates)
	if retried := prepareRelease(b, pid, bid, ip, "", "release", updates); retried != rid {
		t.Fatal("retry duplicated release")
	}
	state := b.request("GET", "/api/projects/"+pid+"/resources/"+a, nil, 200)
	if state["head"] != "" || state["text"] != "" {
		t.Fatal("prepared content leaked into published view")
	}
	b.request("POST", "/api/projects/"+pid+"/releases/"+rid+"/publish", map[string]any{}, 409)
	releaseAdopt(b, pid, rid)
	releasePublish(b, pid, rid)
	releasePublish(b, pid, rid)
	for _, id := range []string{a, c} {
		if b.request("GET", "/api/projects/"+pid+"/resources/"+id, nil, 200)["head"] != rid {
			t.Fatal("artifact head not switched")
		}
	}
	if b.request("GET", "/api/projects/"+pid+"/branches/"+bid, nil, 200)["head"] != ip {
		t.Fatal("IP did not publish with content")
	}
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM outbox WHERE kind='release.published' AND resource_id=?", rid).Scan(&n); err != nil || n != 1 {
		t.Fatal("duplicate publication event", n, err)
	}
}
func TestStaleComponentBlocksEntireRelease(t *testing.T) {
	b, _ := setup(t)
	pid := createProject(b, "原子失败", "one")
	bid := branch(b, pid, "正史")
	a, c := resource(b, pid, bid, "资料甲"), resource(b, pid, bid, "资料乙")
	updates := []map[string]any{{"resource_id": a, "expected_head": "", "text": "共同修改甲"}, {"resource_id": c, "expected_head": "", "text": "共同修改乙"}}
	stale := prepareRelease(b, pid, bid, "", "", "stale", updates)
	releaseAdopt(b, pid, stale)
	winner := prepareRelease(b, pid, bid, "", "", "winner", []map[string]any{{"resource_id": a, "expected_head": "", "text": "其他新修改"}})
	releaseAdopt(b, pid, winner)
	releasePublish(b, pid, winner)
	b.request("POST", "/api/projects/"+pid+"/releases/"+stale+"/publish", map[string]any{}, 409)
	if b.request("GET", "/api/projects/"+pid+"/resources/"+c, nil, 200)["head"] != "" {
		t.Fatal("unrelated half of stale release committed")
	}
}
func TestBlobStoredButInvalidResourceDoesNotCommit(t *testing.T) {
	b, db := setup(t)
	pid := createProject(b, "孤立对象", "one")
	bid := branch(b, pid, "正史")
	b.request("POST", "/api/projects/"+pid+"/releases", map[string]any{"branch_id": bid, "operation_id": "invalid", "updates": []map[string]any{{"resource_id": "missing", "expected_head": "", "text": "不可发布"}}}, 403)
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM artifact_releases").Scan(&count); err != nil || count != 0 {
		t.Fatal("invalid release committed", count, err)
	}
}
func TestPublishRevokedScope(t *testing.T) {
	b, _ := setup(t)
	pid := createProject(b, "撤销发布", "one")
	bid := branch(b, pid, "正史")
	resourceID := resource(b, pid, bid, "待发布资料")
	rid := prepareRelease(b, pid, bid, "", "", "pending", []map[string]any{{"resource_id": resourceID, "expected_head": "", "text": "候选内容"}})
	member := b.request("POST", "/api/members", map[string]any{"username": "publisher", "password": "publisher-password-123", "role": "publisher"}, 201)["id"].(string)
	b.request("PUT", "/api/projects/"+pid+"/members/"+member, map[string]any{"role": "publisher"}, 200)
	publisher := &browser{t: t, handler: b.handler}
	publisher.request("POST", "/api/login", map[string]any{"username": "publisher", "password": "publisher-password-123"}, 200)
	releaseAdopt(publisher, pid, rid)
	b.request("DELETE", "/api/projects/"+pid+"/members/"+member, nil, 200)
	publisher.request("POST", "/api/projects/"+pid+"/releases/"+rid+"/publish", map[string]any{}, 403)
	if b.request("GET", "/api/projects/"+pid+"/resources/"+resourceID, nil, 200)["head"] != "" {
		t.Fatal("revoked publisher advanced content")
	}
}
func TestResourceDraftIsPrivateAndRecoverable(t *testing.T) {
	b, _ := setup(t)
	pid := createProject(b, "资料草稿", "one")
	bid := branch(b, pid, "正史")
	id := resource(b, pid, bid, "创作笔记")
	path := "/api/projects/" + pid + "/resources/" + id
	b.request("PUT", path+"/draft", map[string]any{"revision": 0, "base_head": "", "text": "未公开的灵感", "policy": map[string]any{"space": "working", "visibility": "author", "reveal_at": 0}}, 200)
	saved := b.request("GET", path+"/draft", nil, 200)
	if saved["text"] != "未公开的灵感" {
		t.Fatal("draft not recovered")
	}
	if saved["policy"].(map[string]any)["space"] != "working" {
		t.Fatal("draft classification lost")
	}
	b.request("PUT", path+"/draft", map[string]any{"revision": 0, "base_head": "", "text": "旧标签页"}, 409)
	if b.request("GET", path, nil, 200)["text"] != "" {
		t.Fatal("draft became public")
	}
}
