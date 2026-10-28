package integration

import "testing"

func TestLockedAmendmentRequiresExplicitOwnerApproval(t *testing.T) {
	b, _ := setup(t)
	pid := createProject(b, "锁定修订", "one")
	bid := branch(b, pid, "正史")
	s := originalSnapshot()
	s.Assertions[0].Locked = true
	initial := propose(b, pid, bid, "", "original", s)
	publish(b, pid, bid, initial)
	s.Assertions[0].Value = "隐退医师"
	path := "/api/projects/" + pid + "/branches/" + bid
	reasons := map[string]string{"assertion:identity:/value": "经作者核对前史调整职业状态"}
	b.request("POST", path+"/candidates", map[string]any{"base_head": initial, "operation_id": "ordinary", "snapshot": s, "change_reasons": reasons}, 409)
	change := b.request("POST", path+"/amendments", map[string]any{"base_head": initial, "operation_id": "amend", "reason": "身份修订，经作者核对前史", "snapshot": s, "change_reasons": reasons}, 201)
	cid := change["id"].(string)
	b.request("POST", path+"/candidates/"+cid+"/adopt", map[string]any{}, 409)
	b.request("POST", path+"/candidates/"+cid+"/publish", map[string]any{}, 409)
	b.request("POST", path+"/candidates/"+cid+"/approve-amendment", map[string]any{}, 200)
	b.request("POST", path+"/candidates/"+cid+"/publish", map[string]any{}, 200)
	state := b.request("GET", path, nil, 200)
	if state["head"] != cid {
		t.Fatal("approved amendment not published")
	}
	history := b.request("GET", path+"/candidates", nil, 200)["items"].([]any)
	if len(history) != 2 {
		t.Fatal("amendment erased original")
	}
}
