package qualification_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/go-sql-driver/mysql"
	"novelstudio/internal/authority"
	"novelstudio/internal/domain"
	"novelstudio/internal/httpapi"
	"novelstudio/internal/service"
	"novelstudio/internal/storage"
)

type fixture struct {
	db              *sql.DB
	admin           *sql.DB
	config          *mysql.Config
	ledger          authority.Ledger
	svc             *service.Qualifications
	actor           domain.Actor
	project, branch string
	handler         http.Handler
	cookie          *http.Cookie
}

func isolated(t *testing.T) *fixture {
	t.Helper()
	dsn := os.Getenv("STUDIO_QUALIFICATION_TEST_ADMIN_DSN")
	if dsn == "" {
		t.Skip("explicit disposable MySQL admin configuration required")
	}
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		t.Fatal("invalid isolated configuration")
	}
	cfg.DBName = ""
	admin, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.Close() })
	business, ledger := "studio_source_test_"+service.NewID(), "studio_authority_test_"+service.NewID()
	if _, err = admin.Exec("CREATE DATABASE `" + business + "` CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, name := range []string{business, ledger} {
			if !strings.HasPrefix(name, "studio_source_test_") && !strings.HasPrefix(name, "studio_authority_test_") {
				t.Fatal("unsafe test cleanup")
			}
			if _, err := admin.Exec("DROP DATABASE IF EXISTS `" + name + "`"); err != nil {
				t.Error(err)
			}
		}
	})
	cfg.DBName = business
	cfg.ParseTime = true
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err = storage.RunMigrations(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	t.Setenv("STUDIO_AUTHORITY_SCHEMA", ledger)
	t.Setenv("STUDIO_BLOB_ROOT", t.TempDir())
	protected, err := authority.Provision(context.Background(), admin, ledger, business)
	if err != nil {
		t.Fatal(err)
	}
	w := &service.Workspace{DB: db}
	ctx := context.Background()
	if err = w.Setup(ctx, "qualification-owner", "qualification-password-123", "evaluation tests"); err != nil {
		t.Fatal(err)
	}
	token, err := w.Login(ctx, "qualification-owner", "qualification-password-123")
	if err != nil {
		t.Fatal(err)
	}
	a, err := w.Actor(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	p, err := w.CreateProject(ctx, a.ID, "evaluation project", service.NewID())
	if err != nil {
		t.Fatal(err)
	}
	ip := &service.IP{Workspace: w, Blobs: storage.LocalBlobs{Root: os.Getenv("STUDIO_BLOB_ROOT")}}
	b, err := ip.CreateBranch(ctx, a, p.ID, "main", "", "", service.NewID())
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{db: db, admin: admin, config: cfg, ledger: protected, svc: &service.Qualifications{IP: ip}, actor: a, project: p.ID, branch: b.ID, handler: httpapi.New(db, "http://127.0.0.1:18765"), cookie: &http.Cookie{Name: "studio_session", Value: token}}
	return f
}

func (f *fixture) request(t *testing.T, method, path string, input any, want int) map[string]any {
	t.Helper()
	raw, _ := json.Marshal(input)
	r := httptest.NewRequest(method, "http://127.0.0.1:18765/api/projects/"+f.project+path, bytes.NewReader(raw))
	r.Header.Set("Origin", "http://127.0.0.1:18765")
	r.Header.Set("Content-Type", "application/json")
	r.AddCookie(f.cookie)
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, r)
	if rec.Code != want {
		t.Fatalf("%s %s got %d want %d: %s", method, path, rec.Code, want, rec.Body)
	}
	var out map[string]any
	if json.Unmarshal(rec.Body.Bytes(), &out) != nil {
		t.Fatal(rec.Body.String())
	}
	return out
}

func TestAuthorCannotAlterHeldoutPolicyViaAPI(t *testing.T) {
	f := isolated(t)
	catalog := f.request(t, "GET", "/qualification/suites", nil, 200)
	raw, _ := json.Marshal(catalog)
	if bytes.Contains(raw, []byte(`"gold"`)) || bytes.Contains(raw, []byte(`"expected"`)) {
		t.Fatal("answers exposed")
	}
	f.request(t, "POST", "/qualification/quote", map[string]any{"suite_id": "shared-contract-v1", "gold": map[string]bool{"passed": true}}, 400)
}

func TestTrialSuccessCannotActivate(t *testing.T) {
	f := isolated(t)
	f.request(t, "POST", "/qualification/bindings", map[string]any{"operation_id": "invented-pass", "qualification_id": service.NewID(), "expected_binding": "", "task_kind": "shared.contract", "passed": true}, 400)
	f.request(t, "POST", "/qualification/bindings", map[string]any{"operation_id": "trial-not-qualification", "qualification_id": service.NewID(), "expected_binding": "", "task_kind": "shared.contract"}, 403)
}
