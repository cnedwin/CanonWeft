package integration

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/go-sql-driver/mysql"
	"novelstudio/internal/httpapi"
	"novelstudio/internal/storage"
)

type browser struct {
	t       *testing.T
	handler http.Handler
	cookie  *http.Cookie
}

func (b *browser) request(method, path string, input any, want int) map[string]any {
	b.t.Helper()
	raw, err := json.Marshal(input)
	if err != nil {
		b.t.Fatal(err)
	}
	req := httptest.NewRequest(method, "http://127.0.0.1:18765"+path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "http://127.0.0.1:18765")
	if b.cookie != nil {
		req.AddCookie(b.cookie)
	}
	rec := httptest.NewRecorder()
	b.handler.ServeHTTP(rec, req)
	if rec.Code != want {
		b.t.Fatalf("%s %s: got %d want %d: %s", method, path, rec.Code, want, rec.Body)
	}
	for _, cookie := range rec.Result().Cookies() {
		b.cookie = cookie
	}
	result := map[string]any{}
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		b.t.Fatalf("JSON response: %v (%s)", err, rec.Body)
	}
	return result
}

func database(t *testing.T) *sql.DB {
	t.Helper()
	if admin := os.Getenv("STUDIO_INTEGRATION_TEST_ADMIN_DSN"); admin != "" {
		return disposableHTTPDatabase(t, admin)
	}
	dsn := os.Getenv("STUDIO_TEST_DSN")
	config, err := mysql.ParseDSN(dsn)
	if err != nil || config.DBName != "studio_test" || config.User != "studio_test" {
		t.Fatal("explicit isolated STUDIO_TEST_DSN required")
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Ping(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func fresh(t *testing.T) (*browser, *sql.DB) {
	t.Helper()
	t.Setenv("STUDIO_BLOB_ROOT", t.TempDir())
	db := database(t)
	if err := storage.RunMigrations(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	// Each test owns the explicitly named disposable test database. Never touch studio.
	var exists int
	if err := db.QueryRow("SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name='users'").Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if exists > 0 {
		for _, table := range []string{"memory_revisions", "memory_items", "memory_sessions", "recipe_steps", "recipe_runs", "import_drafts", "ingest_jobs", "patch_drafts", "semantic_review_acceptances", "semantic_review_runs", "ip_branch_deltas"} {
			if _, err := db.Exec("DELETE FROM " + table); err != nil {
				t.Fatal(err)
			}
		}
		for _, table := range []string{"qualification_calls", "qualification_week_grants"} {
			if _, err := db.Exec("DELETE FROM " + table); err != nil {
				t.Fatal(err)
			}
		}
		for _, table := range []string{"capability_binding_heads", "capability_bindings", "capability_qualifications", "qualification_approvals", "qualification_steps", "qualification_schedules", "qualification_runs"} {
			if _, err := db.Exec("DELETE FROM " + table); err != nil {
				t.Fatal(err)
			}
		}
		for _, table := range []string{"working_chunks", "working_indexes", "tool_trials", "skill_discovery_candidates", "skill_discovery_runs", "skill_discovery_sources", "capability_preset_steps", "capability_revocations", "capability_heads", "capability_versions", "index_jobs", "change_plan_releases", "change_plans", "model_reconciliations", "model_call_receipts", "model_call_accounting"} {
			if _, err := db.Exec("DELETE FROM " + table); err != nil {
				t.Fatal(err)
			}
		}
		for _, table := range []string{"skill_revocations", "skill_packages"} {
			if _, err := db.Exec("DELETE FROM " + table); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := db.Exec("DELETE FROM resource_policy_drafts"); err != nil {
			t.Fatal(err)
		}
		for _, table := range []string{"generation_tasks", "model_prices", "source_import_candidates", "source_imports", "context_packages", "knowledge_chunks", "knowledge_indexes", "platform_tasks", "ip_promotions", "model_calls", "model_budgets", "model_bindings", "model_connection_versions", "model_connections", "outbox", "resource_drafts", "artifact_releases", "resources", "ip_amendments", "ip_merge_candidates", "ip_drafts", "adoptions", "ip_revisions", "branches", "audit_events", "operations", "project_members", "projects", "sessions", "users", "workspaces"} {
			if _, err := db.Exec("DELETE FROM " + table); err != nil {
				t.Fatal(err)
			}
		}
	}
	return &browser{t: t, handler: httpapi.New(db, "http://127.0.0.1:18765")}, db
}

func setup(t *testing.T) (*browser, *sql.DB) {
	t.Helper()
	b, db := fresh(t)
	b.request("POST", "/api/setup", map[string]any{"username": "owner", "password": "test-owner-password-123", "workspace": "我的工作室"}, 201)
	b.request("POST", "/api/login", map[string]any{"username": "owner", "password": "test-owner-password-123"}, 200)
	return b, db
}

func createProject(b *browser, name, op string) string {
	result := b.request("POST", "/api/projects", map[string]any{"name": name, "operation_id": op}, 201)
	return result["id"].(string)
}

func TestCreateProjectIdempotent(t *testing.T) {
	b, db := setup(t)
	first := createProject(b, "第一部小说", "create-book-1")
	second := createProject(b, "第一部小说", "create-book-1")
	if first != second {
		t.Fatal("lost response retry created another project")
	}
	b.request("POST", "/api/projects", map[string]any{"name": "不同内容", "operation_id": "create-book-1"}, 409)
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM projects").Scan(&count); err != nil || count != 1 {
		t.Fatalf("projects=%d err=%v", count, err)
	}
}

func TestProjectIsolation(t *testing.T) {
	owner, _ := setup(t)
	a := createProject(owner, "作品甲", "a")
	b := createProject(owner, "作品乙", "b")
	c := createProject(owner, "作品丙", "c")
	member := owner.request("POST", "/api/members", map[string]any{"username": "editor", "password": "editor-password-123", "role": "editor"}, 201)
	owner.request("PUT", "/api/projects/"+a+"/members/"+member["id"].(string), map[string]any{"role": "editor"}, 200)
	editor := &browser{t: t, handler: owner.handler}
	editor.request("POST", "/api/login", map[string]any{"username": "editor", "password": "editor-password-123"}, 200)
	editor.request("GET", "/api/projects/"+a, nil, 200)
	editor.request("GET", "/api/projects/"+b, nil, 403)
	editor.request("GET", "/api/projects/"+c, nil, 403)
	result := editor.request("GET", "/api/projects", nil, 200)
	if len(result["items"].([]any)) != 1 {
		t.Fatal("list leaked another project")
	}
	// Revocation must affect already authenticated sessions immediately.
	owner.request("DELETE", "/api/projects/"+a+"/members/"+member["id"].(string), nil, 200)
	editor.request("GET", "/api/projects/"+a, nil, 403)
}

func TestReadOnlyCannotWrite(t *testing.T) {
	owner, _ := setup(t)
	id := createProject(owner, "只读作品", "book")
	member := owner.request("POST", "/api/members", map[string]any{"username": "reader", "password": "reader-password-123", "role": "reader"}, 201)
	owner.request("PUT", "/api/projects/"+id+"/members/"+member["id"].(string), map[string]any{"role": "reader"}, 200)
	reader := &browser{t: t, handler: owner.handler}
	reader.request("POST", "/api/login", map[string]any{"username": "reader", "password": "reader-password-123"}, 200)
	reader.request("GET", "/api/projects/"+id, nil, 200)
	reader.request("PATCH", "/api/projects/"+id, map[string]any{"name": "越权", "revision": 1}, 403)
	reader.request("POST", "/api/projects", map[string]any{"name": "越权", "operation_id": "reader-create"}, 403)
	reader.request("POST", "/api/members", map[string]any{"username": "intruder", "password": "some-password-123", "role": "owner"}, 403)
}

func TestOptimisticProjectUpdate(t *testing.T) {
	b, _ := setup(t)
	id := createProject(b, "初始", "one")
	result := b.request("PATCH", "/api/projects/"+id, map[string]any{"name": "我的小说", "revision": 1}, 200)
	if result["revision"] != float64(2) {
		t.Fatal("revision did not advance")
	}
	b.request("PATCH", "/api/projects/"+id, map[string]any{"name": "过期写入", "revision": 1}, 409)
	result = b.request("GET", "/api/projects/"+id, nil, 200)
	if result["name"] != "我的小说" {
		t.Fatal("stale writer overwrote new head")
	}
}

func TestSessionPersistsAndLogoutRevokes(t *testing.T) {
	b, db := setup(t)
	if b.cookie == nil || !b.cookie.HttpOnly || b.cookie.SameSite != http.SameSiteStrictMode {
		t.Fatal("unsafe session cookie")
	}
	id := createProject(b, "重启保留", "one")
	restarted := &browser{t: t, handler: httpapi.New(db, "http://127.0.0.1:18765"), cookie: b.cookie}
	restarted.request("GET", "/api/projects/"+id, nil, 200)
	prior := b.cookie
	b.request("POST", "/api/logout", nil, 200)
	restarted.cookie = prior
	restarted.request("GET", "/api/projects/"+id, nil, 401)
	b.request("POST", "/api/login", map[string]any{"username": "owner", "password": "wrong"}, 401)
	b.request("POST", "/api/setup", map[string]any{"username": "other", "password": "another-password-123", "workspace": "重建"}, 409)
}

func TestHostAndOriginRejected(t *testing.T) {
	b, _ := setup(t)
	for _, tc := range []struct{ host, origin string }{{"evil.example", "http://127.0.0.1:18765"}, {"127.0.0.1:18765", "http://evil.example"}, {"127.0.0.1:18765", "null"}} {
		t.Run(fmt.Sprint(tc), func(t *testing.T) {
			req := httptest.NewRequest("POST", "http://"+tc.host+"/api/projects", bytes.NewBufferString(`{"name":"attack","operation_id":"attack"}`))
			req.Header.Set("Origin", tc.origin)
			req.Header.Set("Content-Type", "application/json")
			req.AddCookie(b.cookie)
			rec := httptest.NewRecorder()
			b.handler.ServeHTTP(rec, req)
			if rec.Code != 403 {
				t.Fatalf("untrusted request accepted: %d", rec.Code)
			}
		})
	}
}

func TestNewerSchemaRefusesWrite(t *testing.T) {
	b, db := setup(t)
	if _, err := db.Exec("UPDATE schema_version SET version=999 WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.Exec("UPDATE schema_version SET version=? WHERE id=1", storage.SchemaVersion) })
	newer := &browser{t: t, handler: httpapi.New(db, "http://127.0.0.1:18765"), cookie: b.cookie}
	newer.request("POST", "/api/projects", map[string]any{"name": "bad", "operation_id": "newer"}, 503)
}
