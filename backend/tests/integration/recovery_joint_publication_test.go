package integration

import (
	"novelstudio/internal/service"
	"testing"
)

func TestRecoveryPreservesJointHistoryWithoutReplayingPublication(t *testing.T) {
	b, db := setup(t)
	name := "联合历史原项目"
	source := createProject(b, name, "joint-recovery-source")
	pins := []map[string]any{}
	originalIDs := map[string]bool{}
	for _, label := range []string{"小说", "改编"} {
		bid := branch(b, source, label)
		rid := resource(b, source, bid, label+"场景")
		release := prepareRelease(b, source, bid, "", "", "joint-recovery-"+label, []map[string]any{{"resource_id": rid, "text": label + "场景正文"}})
		releaseAdopt(b, source, release)
		record := b.request("GET", "/api/projects/"+source+"/releases/"+release, nil, 200)
		pins = append(pins, map[string]any{"release_id": release, "manifest_hash": record["hash"]})
		originalIDs[release] = true
	}
	oldKey := "joint-original-publish"
	original := b.request("POST", "/api/projects/"+source+"/releases/joint-publication", map[string]any{"operation_id": oldKey, "releases": pins}, 200)
	b.request("DELETE", "/api/projects/"+source, map[string]any{"confirm_name": name, "revision": 1, "operation_id": "joint-source-delete"}, 200)
	input := service.ProjectRecoveryInput{Name: "联合历史恢复副本", Confirmation: name, Revision: 2, OperationID: "joint-recover-once"}
	path := "/api/projects/" + source + "/recover"
	// A late failure must also roll back the newly reconstructed group metadata.
	if _, err := db.Exec("CREATE TRIGGER joint_recovery_fault BEFORE INSERT ON index_jobs FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='late copy failure'"); err != nil {
		t.Fatal(err)
	}
	b.request("POST", path, input, 500)
	if _, err := db.Exec("DROP TRIGGER joint_recovery_fault"); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM operations WHERE kind='release.joint-restored'").Scan(&n); err != nil || n != 0 {
		t.Fatal("partial recovered membership", n, err)
	}
	restored := b.request("POST", path, input, 201)
	target := restored["id"].(string)
	if b.request("POST", path, input, 201)["id"] != target {
		t.Fatal("retry duplicated copy")
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM operations o JOIN artifact_releases r ON r.id=o.result_id WHERE r.project_id=? AND o.kind='release.joint-member'", target).Scan(&n); err != nil || n != 2 {
		t.Fatal("recovery lost joint membership", n, err)
	}
	root := "/api/projects/" + target
	readerID := b.request("POST", "/api/members", map[string]any{"username": "history-reader", "password": "history-reader-password", "role": "reader"}, 201)["id"].(string)
	reader := &browser{t: t, handler: b.handler}
	reader.request("POST", "/api/login", map[string]any{"username": "history-reader", "password": "history-reader-password"}, 200)
	reader.request("GET", root+"/joint-publications", nil, 403)
	b.request("PUT", root+"/members/"+readerID, map[string]any{"role": "reader"}, 200)
	reader.request("GET", root+"/joint-publications", nil, 200)
	page := b.request("GET", root+"/joint-publications", nil, 200)
	groups := page["items"].([]any)
	if len(groups) != 1 || page["next"] != "" {
		t.Fatal("wrong group list", page)
	}
	group := groups[0].(map[string]any)
	if group["recovered"] != true || group["id"] == original["id"] {
		t.Fatal("restored group not remapped", group)
	}
	detail := b.request("GET", root+"/joint-publications/"+group["id"].(string), nil, 200)
	reader.request("GET", root+"/joint-publications/"+group["id"].(string), nil, 200)
	recoveredPins := []map[string]any{}
	for _, raw := range detail["releases"].([]any) {
		r := raw.(map[string]any)
		id := r["id"].(string)
		if originalIDs[id] || r["status"] != "PUBLISHED" {
			t.Fatal("bad recovered release", r)
		}
		recoveredPins = append(recoveredPins, map[string]any{"release_id": id, "manifest_hash": r["hash"]})
	}
	if len(recoveredPins) != 2 {
		t.Fatal("missing release detail")
	}
	// Neither the old request nor the new historical marker/member is an action receipt.
	var marker, member string
	if err := db.QueryRow("SELECT o.operation_id FROM operations o JOIN artifact_releases r ON r.id=o.result_id WHERE r.project_id=? AND o.kind='release.joint-restored'", target).Scan(&marker); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT o.operation_id FROM operations o JOIN artifact_releases r ON r.id=o.result_id WHERE r.project_id=? AND o.kind='release.joint-member' LIMIT 1", target).Scan(&member); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{oldKey, marker, member, "new-republish-attempt"} {
		b.request("POST", root+"/releases/joint-publication", map[string]any{"operation_id": key, "releases": recoveredPins}, 409)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM outbox WHERE project_id=? AND kind='release.published'", target).Scan(&n); err != nil || n != 0 {
		t.Fatal("recovery replayed publication", n, err)
	}
	other := createProject(b, "无关项目", "joint-other-project")
	b.request("GET", "/api/projects/"+other+"/joint-publications/"+group["id"].(string), nil, 403)
	b.request("GET", root+"/joint-publications?after=not-a-hash", nil, 400)
	empty := b.request("GET", root+"/joint-publications?after="+group["id"].(string), nil, 200)
	if len(empty["items"].([]any)) != 0 {
		t.Fatal("history cursor repeated group")
	}
	// A recovered copy can itself be recovered without inventing a new publication.
	b.request("DELETE", root, map[string]any{"confirm_name": input.Name, "revision": 1, "operation_id": "joint-target-delete"}, 200)
	next := b.request("POST", root+"/recover", service.ProjectRecoveryInput{Name: "再次恢复", Confirmation: input.Name, Revision: 2, OperationID: "joint-recover-twice"}, 201)
	thirdRoot := "/api/projects/" + next["id"].(string)
	third := b.request("GET", thirdRoot+"/joint-publications", nil, 200)["items"].([]any)
	if len(third) != 1 || third[0].(map[string]any)["recovered"] != true {
		t.Fatal("second recovery lost history")
	}
	// Corrupted membership fails the whole copy instead of silently losing history.
	if _, err := db.Exec("DELETE FROM operations WHERE kind='release.joint-member' AND result_id=?", pins[0]["release_id"]); err != nil {
		t.Fatal(err)
	}
	before := 0
	if err := db.QueryRow("SELECT COUNT(*) FROM projects").Scan(&before); err != nil {
		t.Fatal(err)
	}
	input.OperationID = "joint-recover-corrupt"
	input.Name = "不应出现的副本"
	b.request("POST", path, input, 409)
	if err := db.QueryRow("SELECT COUNT(*) FROM projects").Scan(&n); err != nil || n != before {
		t.Fatal("incomplete group copied", n, before, err)
	}
}
