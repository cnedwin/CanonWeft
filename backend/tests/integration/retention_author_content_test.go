package integration

import (
	"strings"
	"testing"
)

func TestRetentionAuthorInventoryKeepsSavedDraftAndPresetFiles(t *testing.T) {
	b, db := setup(t)
	project := createProject(b, "作者内容保护", "author-content-project")
	branchID := branch(b, project, "正史")
	b.request("POST", "/api/projects/"+project+"/capabilities/presets/research", map[string]any{"operation_id": "author-content-presets"}, 201)
	privateDraft := map[string]any{"base_ip_head": "", "operation_id": "author-content-draft", "group": map[string]any{"id": strings.Repeat("a", 32), "members": []string{}, "patches": []any{}}, "selected_resource_id": "", "snippet": "不可被期限清理的私人草稿", "replacement": "还没写完", "reason": "待完善", "compensation_source_id": "", "compensation_reason": ""}
	path := "/api/projects/" + project + "/branches/" + branchID + "/patch-draft"
	b.request("PUT", path, map[string]any{"revision": 0, "content": privateDraft}, 200)
	report := b.request("GET", "/api/operations/retention-inventory", nil, 200)
	count, ok := report["author_content_references"].(float64)
	if !ok || count < 22 {
		t.Fatal("saved definitions, packages or private draft missing", report)
	}
	objects, ok := report["author_content_objects"].(float64)
	if !ok || objects != 4 {
		t.Fatal("preset packages missing", report)
	}
	if report["deletion_ready"] != false {
		t.Fatal("inventory granted deletion authority")
	}
	if body := b.request("GET", path, nil, 200)["content"].(map[string]any); body["snippet"] != privateDraft["snippet"] {
		t.Fatal("inventory changed private draft")
	}
	// Root validation must reject a saved definition whose package no longer
	// matches; it must not silently lower the count and permit future cleanup.
	if _, err := db.Exec("UPDATE skill_packages SET bundle_hash=? WHERE project_id=? LIMIT 1", strings.Repeat("f", 64), project); err != nil {
		t.Fatal(err)
	}
	b.request("GET", "/api/operations/retention-inventory", nil, 503)
}
