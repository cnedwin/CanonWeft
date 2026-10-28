package integration

import (
	"novelstudio/internal/domain"
	"testing"
)

func TestDraftDoesNotAdoptAndRejectsStaleSave(t *testing.T) {
	b, _ := setup(t)
	pid := createProject(b, "自动暂存", "one")
	bid := branch(b, pid, "正史")
	path := "/api/projects/" + pid + "/branches/" + bid
	first := b.request("PUT", path+"/draft", map[string]any{"revision": 0, "base_head": "", "snapshot": originalSnapshot()}, 200)
	if first["revision"] != float64(1) {
		t.Fatal("draft revision missing")
	}
	b.request("PUT", path+"/draft", map[string]any{"revision": 0, "base_head": "", "snapshot": originalSnapshot()}, 409)
	loaded := b.request("GET", path+"/draft", nil, 200)
	if loaded["snapshot"].(map[string]any)["entities"].([]any)[0].(map[string]any)["name"] != "林舟" {
		t.Fatal("draft not retained")
	}
	state := b.request("GET", path, nil, 200)
	if state["head"] != "" {
		t.Fatal("draft altered canon")
	}
	history := b.request("GET", path+"/candidates", nil, 200)
	if len(history["items"].([]any)) != 0 {
		t.Fatal("draft silently adopted")
	}
	b.request("PUT", path+"/draft", map[string]any{"revision": 1, "base_head": "", "snapshot": originalSnapshot(), "amendment": true, "amendment_reason": "待核对的身份修订理由"}, 200)
	intent := b.request("GET", path+"/draft", nil, 200)
	if intent["amendment"] != true || intent["amendment_reason"] != "待核对的身份修订理由" {
		t.Fatal("amendment intent lost on reload")
	}
}
func TestMergeRejectsUnresolvedAndStaleUpstream(t *testing.T) {
	b, _ := setup(t)
	pid := createProject(b, "三方合并", "one")
	bid := branch(b, pid, "正史")
	original := originalSnapshot()
	base := propose(b, pid, bid, "", "p1", original)
	publish(b, pid, bid, base)
	child := b.request("POST", "/api/projects/"+pid+"/branches", map[string]any{"name": "改编", "operation_id": "child", "parent_id": bid, "base_revision": base}, 201)["id"].(string)
	local := originalSnapshot()
	local.Entities[0].Description = "改编版身世"
	localID := propose(b, pid, child, base, "local", local)
	publish(b, pid, child, localID)
	upstream := originalSnapshot()
	upstream.Entities[0].Description = "正史新版身世"
	upID := propose(b, pid, bid, base, "upstream", upstream)
	publish(b, pid, bid, upID)
	path := "/api/projects/" + pid + "/branches/" + child
	preview := b.request("GET", path+"/merge-preview", nil, 200)
	if len(preview["conflicts"].([]any)) != 1 {
		t.Fatal("conflict missing")
	}
	request := map[string]any{"base_head": localID, "upstream_head": upID, "operation_id": "merge", "resolutions": map[string]string{}, "change_reasons": map[string]string{}}
	b.request("POST", path+"/merge", request, 409)
	request["resolutions"] = map[string]string{"entity:lin-1": "local"}
	merged := b.request("POST", path+"/merge", request, 201)["id"].(string)
	// Actual successful semantic review and baseline advancement are exercised
	// by TestSemanticReviewMergePromotionAndJointPublicationHaveActualExit.
	b.request("POST", path+"/candidates/"+merged+"/adopt", map[string]any{}, 503)
	state := b.request("GET", path, nil, 200)
	if state["base_revision"] != base || state["head"] != localID {
		t.Fatal("unreviewed merge changed authority")
	}
	request["operation_id"] = "stale"
	request["base_head"] = localID
	request["upstream_head"] = base
	b.request("POST", path+"/merge", request, 409)
	again := b.request("GET", path+"/merge-preview", nil, 200)
	if len(again["conflicts"].([]any)) != 1 {
		t.Fatal("unpublished merge erased pending conflict")
	}
}
func TestServerRecordedTimeAndLockedNoop(t *testing.T) {
	b, _ := setup(t)
	pid := createProject(b, "时间戳", "one")
	bid := branch(b, pid, "正史")
	s := originalSnapshot()
	s.Assertions[0].Locked = true
	s.Assertions[0].RecordedAt = "2099-01-01T00:00:00Z"
	cid := propose(b, pid, bid, "", "p1", s)
	publish(b, pid, bid, cid)
	path := "/api/projects/" + pid + "/branches/" + bid
	snapshot := b.request("GET", path, nil, 200)["snapshot"].(map[string]any)
	stamp := snapshot["assertions"].([]any)[0].(map[string]any)["recorded_at"]
	if stamp == s.Assertions[0].RecordedAt || stamp == "" {
		t.Fatal("record time trusted client")
	}
	s.Entities = append(s.Entities, domain.Entity{ID: "other", Name: "配角", Kind: "character"})
	propose(b, pid, bid, cid, "p2", s)
}
func TestWorldRuleConflictCannotPublish(t *testing.T) {
	b, _ := setup(t)
	pid := createProject(b, "规则门禁", "one")
	bid := branch(b, pid, "正史")
	s := originalSnapshot()
	s.Rules = []domain.WorldRule{{ID: "identity-rule", Name: "身份约束", Subject: "lin-1", Predicate: "身份", Operator: "equals", Operand: "刺客", Source: "作者"}}
	cid := propose(b, pid, bid, "", "p1", s)
	path := "/api/projects/" + pid + "/branches/" + bid + "/candidates/" + cid
	b.request("POST", path+"/adopt", map[string]any{}, 409)
	b.request("POST", path+"/publish", map[string]any{}, 409)
}
