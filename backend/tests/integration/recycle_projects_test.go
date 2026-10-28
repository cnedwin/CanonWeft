package integration

import (
	"fmt"
	"strings"
	"testing"
)

func TestRecycleProjectsAuthorityPaginationAndIsolation(t *testing.T) {
	b, db := setup(t)
	createProject(b, "仍在创作", "recycle-live")
	for i := 0; i < 26; i++ {
		name := fmt.Sprintf("已删除作品%d", i)
		id := createProject(b, name, fmt.Sprintf("recycle-create-%d", i))
		b.request("DELETE", "/api/projects/"+id, map[string]any{"confirm_name": name, "revision": 1, "operation_id": fmt.Sprintf("recycle-delete-%d", i)}, 200)
	}
	// A foreign deleted project lacks local evidence; it must never be inspected.
	foreign := strings.Repeat("f", 32)
	if _, err := db.Exec("INSERT INTO workspaces(id,name) VALUES(?, 'foreign')", foreign); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO projects(id,workspace_id,name,deleted_at) VALUES(?,?,'foreign secret',UTC_TIMESTAMP(6))", foreign, foreign); err != nil {
		t.Fatal(err)
	}
	first := b.request("GET", "/api/operations/recycle-bin", nil, 200)
	items := first["items"].([]any)
	if len(items) != 25 || first["next_cursor"] == "" {
		t.Fatal("unbounded or incomplete page")
	}
	item := items[0].(map[string]any)
	if item["window"] != "WITHIN_WINDOW" || item["recover_until"] == nil {
		t.Fatal("verified deletion window missing", item)
	}
	id := item["id"].(string)
	// Business timestamps cannot extend, shorten or replace the protected clock.
	if _, err := db.Exec("UPDATE projects SET deleted_at='2000-01-01' WHERE id=?", id); err != nil {
		t.Fatal(err)
	}
	again := b.request("GET", "/api/operations/recycle-bin", nil, 200)["items"].([]any)[0].(map[string]any)
	if again["recover_until"] != item["recover_until"] || again["deleted_at"] != item["deleted_at"] {
		t.Fatal("business timestamp replaced protected deletion evidence")
	}
	last := b.request("GET", "/api/operations/recycle-bin?after="+first["next_cursor"].(string), nil, 200)
	if len(last["items"].([]any)) != 1 || last["next_cursor"] != "" {
		t.Fatal("pagination includes foreign or active projects")
	}
	lastID := last["items"].([]any)[0].(map[string]any)["id"]
	for _, raw := range items {
		if raw.(map[string]any)["id"] == lastID {
			t.Fatal("pagination repeated a project")
		}
	}
	b.request("GET", "/api/operations/recycle-bin?after=invalid", nil, 400)
	b.request("POST", "/api/members", map[string]any{"username": "reader", "password": "reader-password-123", "role": "reader"}, 201)
	reader := &browser{t: t, handler: b.handler}
	reader.request("POST", "/api/login", map[string]any{"username": "reader", "password": "reader-password-123"}, 200)
	reader.request("GET", "/api/operations/recycle-bin", nil, 403)
	// A business-only tombstone must not invent recovery eligibility.
	workspace := b.request("GET", "/api/me", nil, 200)["workspace_id"]
	if _, err := db.Exec("INSERT INTO projects(id,workspace_id,name,deleted_at) VALUES(?,?,'缺少权威删除记录',UTC_TIMESTAMP(6))", strings.Repeat("0", 31)+"1", workspace); err != nil {
		t.Fatal(err)
	}
	b.request("GET", "/api/operations/recycle-bin", nil, 503)
}
