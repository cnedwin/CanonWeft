package integration

import (
	"sort"
	"testing"
)

func TestJointPublicationIsAtomicAndReceiptBound(t *testing.T) {
	b, db := setup(t)
	pid := createProject(b, "跨分支联合发布", "joint-project")
	branches := []string{branch(b, pid, "正史"), branch(b, pid, "改编")}
	resources, releases := []string{}, []string{}
	refs := []map[string]any{}
	for i, bid := range branches {
		label := []string{"小说场景", "改编场景"}[i]
		rid := resource(b, pid, bid, label)
		release := prepareRelease(b, pid, bid, "", "", "joint-"+label, []map[string]any{{"resource_id": rid, "expected_head": "", "text": label + "修订"}})
		resources = append(resources, rid)
		releases = append(releases, release)
		value := b.request("GET", "/api/projects/"+pid+"/releases/"+release, nil, 200)
		refs = append(refs, map[string]any{"release_id": release, "manifest_hash": value["hash"]})
	}
	path := "/api/projects/" + pid + "/releases/joint-publication"
	input := map[string]any{"operation_id": "joint-publish", "releases": refs}
	b.request("POST", path, input, 409)
	for _, id := range releases {
		releaseAdopt(b, pid, id)
	}
	editorID := b.request("POST", "/api/members", map[string]any{"username": "joint-editor", "password": "joint-editor-password-123", "role": "editor"}, 201)["id"].(string)
	b.request("PUT", "/api/projects/"+pid+"/members/"+editorID, map[string]any{"role": "editor"}, 200)
	editor := &browser{t: t, handler: b.handler}
	editor.request("POST", "/api/login", map[string]any{"username": "joint-editor", "password": "joint-editor-password-123"}, 200)
	editor.request("POST", path, input, 403)
	wrong := []map[string]any{refs[0], {"release_id": releases[1], "manifest_hash": "changed"}}
	b.request("POST", path, map[string]any{"operation_id": "wrong-hash", "releases": wrong}, 409)
	// A late write failure must roll back the earlier branch, receipts and events.
	order := append([]string{}, releases...)
	sort.Strings(order)
	blockedResource := resources[0]
	if order[1] == releases[1] {
		blockedResource = resources[1]
	}
	if _, err := db.Exec("CREATE TRIGGER reject_joint_tail BEFORE UPDATE ON resources FOR EACH ROW BEGIN IF NEW.id='" + blockedResource + "' THEN SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='joint test failure'; END IF; END"); err != nil {
		t.Fatal(err)
	}
	b.request("POST", path, input, 500)
	if _, err := db.Exec("DROP TRIGGER reject_joint_tail"); err != nil {
		t.Fatal(err)
	}
	for i, id := range resources {
		if b.request("GET", "/api/projects/"+pid+"/resources/"+id, nil, 200)["head"] != "" {
			t.Fatal("partial head committed")
		}
		if b.request("GET", "/api/projects/"+pid+"/releases/"+releases[i], nil, 200)["status"] != "READY" {
			t.Fatal("partial status committed")
		}
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM operations WHERE kind LIKE 'release.joint%'").Scan(&count); err != nil || count != 0 {
		t.Fatal("partial receipt", count, err)
	}
	result := b.request("POST", path, input, 200)
	again := b.request("POST", path, input, 200)
	if result["id"] != again["id"] || len(result["releases"].([]any)) != 2 {
		t.Fatal("retry changed group")
	}
	for i, id := range resources {
		if b.request("GET", "/api/projects/"+pid+"/resources/"+id, nil, 200)["head"] != releases[i] {
			t.Fatal("group head missing")
		}
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM outbox WHERE kind='release.published' AND project_id=?", pid).Scan(&count); err != nil || count != 2 {
		t.Fatal("duplicate events", count, err)
	}
	stored := b.request("GET", "/api/projects/"+pid+"/joint-publications/"+result["id"].(string), nil, 200)
	if len(stored["releases"].([]any)) != 2 {
		t.Fatal("durable membership missing")
	}
	b.request("POST", path, map[string]any{"operation_id": "different-group", "releases": refs}, 409)
	changed := []map[string]any{refs[0], {"release_id": releases[1], "manifest_hash": "wrong"}}
	b.request("POST", path, map[string]any{"operation_id": "joint-publish", "releases": changed}, 409)
	winner := prepareRelease(b, pid, branches[0], "", "", "later-edit", []map[string]any{{"resource_id": resources[0], "expected_head": releases[0], "text": "之后确认的新版本"}})
	releaseAdopt(b, pid, winner)
	releasePublish(b, pid, winner)
	b.request("POST", path, input, 200)
	if b.request("GET", "/api/projects/"+pid+"/resources/"+resources[0], nil, 200)["head"] != winner {
		t.Fatal("old joint replay overwrote newer work")
	}
	if _, err := db.Exec("DELETE FROM operations WHERE kind='release.joint-member' AND result_id=?", releases[0]); err != nil {
		t.Fatal(err)
	}
	b.request("GET", "/api/projects/"+pid+"/joint-publications/"+result["id"].(string), nil, 403)
}
