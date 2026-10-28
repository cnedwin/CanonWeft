package integration

import "testing"

func TestDiscoverySourceRegistrationVersionAndRunControl(t *testing.T) {
	b, _ := setup(t)
	p := createProject(b, "发现来源", "discovery-project")
	root := "/api/projects/" + p + "/skill-discovery"
	input := map[string]any{"operation_id": "source-one", "repository": "anthropics/skills", "ref": "main", "path": "skills", "keyword": "", "enabled": true, "limit": 2, "expected_revision": 0}
	first := b.request("POST", root+"/sources", input, 201)
	same := b.request("POST", root+"/sources", input, 201)
	if first["id"] != same["id"] {
		t.Fatal("source duplicated")
	}
	id := first["id"].(string)
	run := b.request("POST", root+"/sources/"+id+"/runs", map[string]any{"operation_id": "run-one"}, 201)
	retry := b.request("POST", root+"/sources/"+id+"/runs", map[string]any{"operation_id": "run-one"}, 201)
	if run["id"] != retry["id"] {
		t.Fatal("run duplicated")
	}
	b.request("POST", root+"/sources/"+id+"/runs", map[string]any{"operation_id": "run-two"}, 409)
	b.request("POST", root+"/runs/"+run["id"].(string)+"/control", map[string]any{"action": "pause"}, 200)
	b.request("POST", root+"/runs/"+run["id"].(string)+"/control", map[string]any{"action": "resume"}, 200)
	b.request("POST", root+"/runs/"+run["id"].(string)+"/control", map[string]any{"action": "cancel"}, 200)
	input["operation_id"] = "source-edit"
	input["enabled"] = false
	b.request("PUT", root+"/sources/"+id, input, 409)
	input["expected_revision"] = 1
	b.request("PUT", root+"/sources/"+id, input, 200)
	b.request("POST", root+"/sources/"+id+"/runs", map[string]any{"operation_id": "disabled"}, 409)
	other := createProject(b, "隔离", "other-discovery")
	b.request("POST", "/api/projects/"+other+"/skill-discovery/sources/"+id+"/runs", map[string]any{"operation_id": "foreign"}, 403)
}
