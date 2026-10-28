package integration

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http/httptest"
	"novelstudio/internal/domain"
	"strings"
	"testing"
)

func originalSnapshot() domain.Snapshot {
	return domain.Snapshot{Entities: []domain.Entity{{ID: "lin-1", Kind: "character", Name: "林舟", Description: "谨慎的青年医师"}}, Assertions: []domain.Assertion{{ID: "identity", Subject: "lin-1", Predicate: "身份", Value: "医师", Kind: "fact", Source: "作者输入", Visibility: "public"}}}
}
func branch(b *browser, pid, name string) string {
	// Operations are actor-scoped. Equal branch names in two projects must not
	// reuse one operation identity and accidentally test a conflicting retry.
	operation := sha256.Sum256([]byte(pid + "\x00" + name))
	result := b.request("POST", "/api/projects/"+pid+"/branches", map[string]any{"name": name, "operation_id": "branch-" + hex.EncodeToString(operation[:])}, 201)
	return result["id"].(string)
}
func propose(b *browser, pid, bid, base, op string, snapshot domain.Snapshot) string {
	path := "/api/projects/" + pid + "/branches/" + bid
	preview := b.request("POST", path+"/delta-preview", map[string]any{"base_head": base, "snapshot": snapshot}, 200)
	reasons := deltaReasons(preview)
	result := b.request("POST", path+"/candidates", map[string]any{"base_head": base, "operation_id": op, "snapshot": snapshot, "change_reasons": reasons}, 201)
	return result["id"].(string)
}
func deltaReasons(preview map[string]any) map[string]string {
	reasons := map[string]string{}
	for _, raw := range preview["operations"].([]any) {
		op := raw.(map[string]any)
		reasons[op["object_kind"].(string)+":"+op["object_id"].(string)+":"+op["field"].(string)] = "测试明确确认这项内容变更"
	}
	return reasons
}
func publish(b *browser, pid, bid, cid string) {
	path := "/api/projects/" + pid + "/branches/" + bid + "/candidates/" + cid
	b.request("POST", path+"/adopt", map[string]any{}, 200)
	b.request("POST", path+"/publish", map[string]any{}, 200)
}
func TestCandidateInvisibleUntilPublished(t *testing.T) {
	b, _ := setup(t)
	t.Setenv("STUDIO_BLOB_ROOT", t.TempDir())
	pid := createProject(b, "设定测试", "one")
	bid := branch(b, pid, "正史")
	root := "/api/projects/" + pid + "/branches/" + bid
	preview := b.request("POST", root+"/delta-preview", map[string]any{"base_head": "", "snapshot": originalSnapshot()}, 200)
	request := map[string]any{"base_head": "", "operation_id": "proposal-1", "snapshot": originalSnapshot(), "change_reasons": deltaReasons(preview)}
	cid := b.request("POST", root+"/candidates", request, 201)["id"].(string)
	state := b.request("GET", "/api/projects/"+pid+"/branches/"+bid, nil, 200)
	if state["head"] != "" {
		t.Fatal("candidate became canonical before publication")
	}
	path := "/api/projects/" + pid + "/branches/" + bid + "/candidates/" + cid
	b.request("POST", path+"/publish", map[string]any{}, 409)
	publish(b, pid, bid, cid)
	state = b.request("GET", "/api/projects/"+pid+"/branches/"+bid, nil, 200)
	if state["head"] != cid {
		t.Fatal("published head not advanced")
	}
	entities := state["snapshot"].(map[string]any)["entities"].([]any)
	if len(entities) != 1 || entities[0].(map[string]any)["name"] != "林舟" {
		t.Fatal("published snapshot lost entity content")
	}
	b.request("POST", path+"/publish", map[string]any{}, 200)
	// Retry the exact original request, rather than asking for a new preview
	// against a stale base after publication.
	if id := b.request("POST", root+"/candidates", request, 201)["id"].(string); id != cid {
		t.Fatal("candidate retry duplicated content")
	}
}
func TestConcurrentPublishOneWinner(t *testing.T) {
	b, _ := setup(t)
	t.Setenv("STUDIO_BLOB_ROOT", t.TempDir())
	pid := createProject(b, "竞争版本", "one")
	bid := branch(b, pid, "正史")
	c1 := propose(b, pid, bid, "", "p1", originalSnapshot())
	next := originalSnapshot()
	next.Entities[0].Name = "林初"
	c2 := propose(b, pid, bid, "", "p2", next)
	b.request("POST", "/api/projects/"+pid+"/branches/"+bid+"/candidates/"+c2+"/adopt", map[string]any{}, 200)
	b.request("POST", "/api/projects/"+pid+"/branches/"+bid+"/candidates/"+c1+"/adopt", map[string]any{}, 200)
	start := make(chan struct{})
	codes := make(chan int, 2)
	for _, candidate := range []string{c1, c2} {
		go func(id string) {
			<-start
			req := httptest.NewRequest("POST", "http://127.0.0.1:18765/api/projects/"+pid+"/branches/"+bid+"/candidates/"+id+"/publish", strings.NewReader("{}"))
			req.Header.Set("Origin", "http://127.0.0.1:18765")
			req.Header.Set("Content-Type", "application/json")
			req.AddCookie(b.cookie)
			recorder := httptest.NewRecorder()
			b.handler.ServeHTTP(recorder, req)
			codes <- recorder.Code
		}(candidate)
	}
	close(start)
	first, second := <-codes, <-codes
	if !((first == 200 && second == 409) || (first == 409 && second == 200)) {
		t.Fatalf("concurrent publication statuses %d %d", first, second)
	}
	state := b.request("GET", "/api/projects/"+pid+"/branches/"+bid, nil, 200)
	if state["head"] != c1 && state["head"] != c2 {
		t.Fatal("stale candidate overwrote head")
	}
}
func TestBranchSnapshotIsolation(t *testing.T) {
	b, _ := setup(t)
	t.Setenv("STUDIO_BLOB_ROOT", t.TempDir())
	pid := createProject(b, "分支测试", "one")
	bid := branch(b, pid, "正史")
	cid := propose(b, pid, bid, "", "p1", originalSnapshot())
	publish(b, pid, bid, cid)
	child := b.request("POST", "/api/projects/"+pid+"/branches", map[string]any{"name": "改编", "operation_id": "child", "parent_id": bid, "base_revision": cid}, 201)["id"].(string)
	changed := originalSnapshot()
	changed.Entities[0].Name = "改编林舟"
	candidate := propose(b, pid, child, cid, "child-p1", changed)
	publish(b, pid, child, candidate)
	parent := b.request("GET", "/api/projects/"+pid+"/branches/"+bid, nil, 200)
	if parent["head"] != cid {
		t.Fatal("child modified original canon")
	}
	foreign := createProject(b, "另一个IP", "two")
	b.request("GET", "/api/projects/"+foreign+"/branches/"+child, nil, 403)
}
func TestLockedChangeRejectedBeforePublication(t *testing.T) {
	b, _ := setup(t)
	t.Setenv("STUDIO_BLOB_ROOT", t.TempDir())
	pid := createProject(b, "锁定事实", "one")
	bid := branch(b, pid, "正史")
	s := originalSnapshot()
	s.Assertions[0].Locked = true
	cid := propose(b, pid, bid, "", "p1", s)
	publish(b, pid, bid, cid)
	s.Assertions[0].Value = "刺客"
	b.request("POST", "/api/projects/"+pid+"/branches/"+bid+"/candidates", map[string]any{"base_head": cid, "operation_id": "p2", "snapshot": s, "change_reasons": map[string]string{"assertion:identity:/value": "提出锁定内容变化"}}, 409)
}
