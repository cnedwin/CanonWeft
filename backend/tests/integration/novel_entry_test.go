package integration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"novelstudio/internal/authority"
	"novelstudio/internal/backup"

	"novelstudio/internal/domain"
	"novelstudio/internal/httpapi"
	"novelstudio/internal/novel"
	"novelstudio/internal/service"
	"novelstudio/internal/storage"
)

func TestNovelEntryHTTPAndProtectedInventory(t *testing.T) {
	b, db, entry, actor := novelEntryFixture(t)
	b.request("GET", "/api/novel-setups/defaults", nil, 200)
	choices := b.request("GET", "/api/novel-setups/model-choices", nil, 200)
	if len(choices["items"].([]any)) != 0 {
		t.Fatal("unconfigured workspace offered a usable model")
	}
	draft := service.NovelSetupDraft{ID: service.NewID(), Step: 5, Setup: validNovelSetup()}
	base := "/api/novel-setups/" + draft.ID
	saved := b.request("PUT", base, draft, 200)
	revision := int64(saved["revision"].(float64))
	confirmation := service.NovelConfirmation{Revision: revision, OperationID: "novel-http-confirm", AcceptSettings: false}
	b.request("POST", base+"/confirm", confirmation, 400)
	confirmation.AcceptSettings = true
	created := b.request("POST", base+"/confirm", confirmation, 201)
	id, project, branch := created["id"].(string), created["project_id"].(string), created["branch_id"].(string)
	retry := b.request("POST", base+"/confirm", confirmation, 201)
	if retry["id"] != id {
		t.Fatal("HTTP retry duplicated the novel")
	}
	if len(b.request("GET", "/api/novels", nil, 200)["items"].([]any)) != 1 || len(b.request("GET", "/api/novel-setups", nil, 200)["items"].([]any)) != 0 {
		t.Fatal("confirmed novel missing from bookshelf or still offered as unfinished")
	}
	b.request("GET", "/api/novels/"+id, nil, 200)
	b.request("GET", "/api/projects/"+project+"/branches/"+branch+"/draft", nil, 200)
	member := b.request("POST", "/api/members", map[string]any{"username": "novel-http-reader", "password": "test-novel-reader-123", "role": "reader"}, 201)
	reader := &browser{t: t, handler: b.handler}
	reader.request("POST", "/api/login", map[string]any{"username": "novel-http-reader", "password": "test-novel-reader-123"}, 200)
	reader.request("GET", base, nil, 403)
	reader.request("PUT", base, draft, 403)
	reader.request("GET", "/api/novels/"+id, nil, 403)
	b.request("PUT", "/api/projects/"+project+"/members/"+member["id"].(string), map[string]any{"role": "reader"}, 200)
	reader.request("GET", "/api/novels/"+id, nil, 200)
	reader.request("GET", base, nil, 403)
	scanner := service.RetentionScanner{Workspace: entry.Workspace, Blobs: storage.LocalBlobs{Root: t.TempDir()}}
	plan, err := scanner.PlanJob(context.Background(), actor.ID, project, "request-v2:"+service.NewID())
	if err != nil || plan.State != "PLANNED" || !plan.CheckpointCurrent || plan.Expiring != 0 {
		t.Fatalf("novel author settings must survive complete inventory: %+v %v", plan, err)
	}
	var catalog int
	if err = db.QueryRow("SELECT catalog_version FROM project_lifecycle WHERE project_id=?", project).Scan(&catalog); err != nil || catalog != 3 {
		t.Fatalf("novel inventory catalog not persisted: %d %v", catalog, err)
	}
	if _, err = scanner.Scan(context.Background(), actor.ID, project); err != nil {
		t.Fatal("second complete scan rejected the novel catalog", err)
	}
	archive := filepath.Join(t.TempDir(), "scanned-novel.studio")
	if _, err = backup.Create(context.Background(), db, storage.LocalBlobs{Root: os.Getenv("STUDIO_BLOB_ROOT")}, archive); err != nil {
		t.Fatal("novel inventory prevented archive creation", err)
	}
}

func TestNovelEntryBrowserCreationAndCharacters(t *testing.T) {
	node := os.Getenv("STUDIO_NOVEL_BROWSER_NODE")
	if node == "" {
		t.Skip("explicit installed browser runtime required")
	}
	_, db, _, _ := novelEntryFixture(t)
	startNovelBrowserIngest(t, db)
	web, err := filepath.Abs("../../../web")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(web, "dist", "index.html")); err != nil {
		t.Fatal("build the actual Vue app first", err)
	}
	server := httptest.NewUnstartedServer(nil)
	origin := "http://" + server.Listener.Addr().String()
	app := httpapi.New(db, origin)
	app.Static("/", filepath.Join(web, "dist"))
	server.Config.Handler = app
	server.Start()
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, filepath.Join(web, "tests", "novel-entry-browser.cjs"), origin)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("real novel browser flow: %v\n%s", err, output)
	}
	t.Log(strings.TrimSpace(string(output)))
	var count int
	if err = db.QueryRow("SELECT COUNT(*) FROM novels").Scan(&count); err != nil || count != 1 {
		t.Fatalf("novels=%d err=%v", count, err)
	}
	if err = db.QueryRow("SELECT COUNT(*) FROM model_calls").Scan(&count); err != nil || count != 0 {
		t.Fatalf("creation made model calls: %d %v", count, err)
	}
	if err = db.QueryRow("SELECT COUNT(*) FROM ip_revisions").Scan(&count); err != nil || count != 0 {
		t.Fatalf("creation published character draft: %d %v", count, err)
	}
}

func TestNovelEntryProjectRecoveryPreservesBookAndPrivateSettings(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(map[bool]string{false: "new-ip", true: "existing-ip"}[existing], func(t *testing.T) {
			b, db, entry, actor := novelEntryFixture(t)
			ctx := context.Background()
			input := service.NovelSetupDraft{ID: service.NewID(), Step: 5, Setup: validNovelSetup()}
			if existing {
				project := createProject(b, "原IP", "recover-novel-ip")
				parent := branch(b, project, "正史")
				head := propose(b, project, parent, "", "recover-novel-head", originalSnapshot())
				publish(b, project, parent, head)
				input.Setup.IPMode, input.Setup.ProjectID, input.Setup.BranchID, input.Setup.BaseHead = "existing", project, parent, head
			}
			draft, err := entry.SaveDraft(ctx, actor.ID, input)
			if err != nil {
				t.Fatal(err)
			}
			book, err := entry.Confirm(ctx, actor.ID, draft.ID, service.NovelConfirmation{Revision: draft.Revision, OperationID: "recover-novel-create", AcceptSettings: true})
			if err != nil {
				t.Fatal(err)
			}
			private, err := entry.SaveDraft(ctx, actor.ID, service.NovelSetupDraft{ID: service.NewID(), Step: 2, Setup: novel.Defaults()})
			if err != nil {
				t.Fatal(err)
			}
			project := b.request("GET", "/api/projects/"+book.ProjectID, nil, 200)
			b.request("DELETE", "/api/projects/"+book.ProjectID, map[string]any{"confirm_name": project["name"], "revision": project["revision"], "operation_id": "delete-novel"}, 200)
			recovery := service.ProjectRecoveryInput{Name: "小说恢复副本", Confirmation: project["name"].(string), Revision: int64(project["revision"].(float64)) + 1, OperationID: "recover-novel-copy"}
			path := "/api/projects/" + book.ProjectID + "/recover"
			if _, err = db.Exec("CREATE TRIGGER novel_recovery_fault BEFORE INSERT ON novel_settings_versions FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='novel recovery test fault'"); err != nil {
				t.Fatal(err)
			}
			_, failed := (&service.IP{Workspace: entry.Workspace, Blobs: storage.LocalBlobs{Root: os.Getenv("STUDIO_BLOB_ROOT")}}).RecoverProject(ctx, actor.ID, book.ProjectID, recovery)
			var sqlFault *mysql.MySQLError
			if !errors.As(failed, &sqlFault) || sqlFault.Number != 1644 {
				t.Fatal("did not reach intended recovery fault", failed)
			}
			if _, err = db.Exec("DROP TRIGGER novel_recovery_fault"); err != nil {
				t.Fatal(err)
			}
			var before int
			for _, table := range []string{"projects", "novels", "novel_settings_versions"} {
				if err = db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&before); err != nil || before != 1 {
					t.Fatal("failed recovery left partial content", table, before, err)
				}
			}
			copied := b.request("POST", path, recovery, 201)
			if retry := b.request("POST", path, recovery, 201); retry["id"] != copied["id"] {
				t.Fatal("recovery retry duplicated copy")
			}
			books, err := entry.Books(ctx, actor.ID)
			if err != nil || len(books) != 1 {
				t.Fatalf("recovered novel missing from shelf: %d %v", len(books), err)
			}
			got := books[0]
			if got.ID == book.ID || got.ProjectID != copied["id"] || got.BranchID == book.BranchID || got.Stage != book.Stage || got.Setup.Title != book.Setup.Title || len(got.Setup.Characters) != len(book.Setup.Characters) {
				t.Fatal("novel recovery lost content or reused revoked identity", got)
			}
			if existing && (got.Setup.ProjectID != got.ProjectID || got.Setup.BranchID == book.Setup.BranchID || got.Setup.BaseHead == book.Setup.BaseHead) {
				t.Fatal("existing IP settings still point at revoked source", got.Setup)
			}
			if _, err = entry.ReadDraft(ctx, actor.ID, private.ID); err != nil {
				t.Fatal("unrelated workspace draft changed", err)
			}
			var count int
			if err = db.QueryRow("SELECT COUNT(*) FROM novel_setup_drafts WHERE confirmed_novel_id='' ").Scan(&count); err != nil || count != 1 {
				t.Fatal("recovery copied unrelated private draft", count, err)
			}
			if _, err = (&service.RetentionScanner{Workspace: entry.Workspace, Blobs: storage.LocalBlobs{Root: os.Getenv("STUDIO_BLOB_ROOT")}}).Scan(ctx, actor.ID, got.ProjectID); err != nil {
				t.Fatal("recovered novel sources invalid", err)
			}
			if _, err = backup.Create(ctx, db, storage.LocalBlobs{Root: os.Getenv("STUDIO_BLOB_ROOT")}, filepath.Join(t.TempDir(), "recovered-novel.studio")); err != nil {
				t.Fatal("recovered novel cannot be archived", err)
			}
		})
	}
}

func novelEntryFixture(t *testing.T) (*browser, *sql.DB, *service.NovelEntry, domain.Actor) {
	t.Helper()
	b, db := setup(t)
	var actor domain.Actor
	if err := db.QueryRow("SELECT id,workspace_id,username,role FROM users WHERE username='owner'").Scan(&actor.ID, &actor.WorkspaceID, &actor.Username, &actor.Role); err != nil {
		t.Fatal(err)
	}
	return b, db, &service.NovelEntry{Workspace: &service.Workspace{DB: db}}, actor
}

func TestNovelEntrySnapshotRestoresPrivateDraftAndBook(t *testing.T) {
	_, db, entry, actor := novelEntryFixture(t)
	ctx := context.Background()
	private, err := entry.SaveDraft(ctx, actor.ID, service.NovelSetupDraft{ID: service.NewID(), Step: 2, Setup: novel.Defaults()})
	if err != nil {
		t.Fatal(err)
	}
	private.Setup.Idea = "只保存在未确认草稿里的创作方向"
	private, err = entry.SaveDraft(ctx, actor.ID, private)
	if err != nil {
		t.Fatal(err)
	}
	draft, err := entry.SaveDraft(ctx, actor.ID, service.NovelSetupDraft{ID: service.NewID(), Step: 5, Setup: validNovelSetup()})
	if err != nil {
		t.Fatal(err)
	}
	book, err := entry.Confirm(ctx, actor.ID, draft.ID, service.NovelConfirmation{Revision: draft.Revision, OperationID: "novel-archive-confirm", AcceptSettings: true})
	if err != nil {
		t.Fatal(err)
	}
	settingsDraft := service.NovelSettingsDraft{BaseRevision: 1, Setup: book.Setup}
	settingsDraft.Setup.Title = "尚未确认的书名修改"
	if _, err = entry.SaveSettingsDraft(ctx, actor.ID, book.ID, settingsDraft); err != nil {
		t.Fatal(err)
	}
	if _, err = (&service.RetentionScanner{Workspace: entry.Workspace, Blobs: storage.LocalBlobs{Root: os.Getenv("STUDIO_BLOB_ROOT")}}).Scan(ctx, actor.ID, book.ProjectID); err != nil {
		t.Fatal("scan before archive", err)
	}
	root := t.TempDir()
	archive := filepath.Join(root, "novels.zip")
	if _, err = backup.Create(ctx, db, storage.LocalBlobs{Root: root}, archive); err != nil {
		t.Fatal("novel archive", err)
	}
	f, err := os.Open(archive)
	if err != nil {
		t.Fatal(err)
	}
	stat, _ := f.Stat()
	validated, err := backup.ValidateArchive(f, stat.Size())
	f.Close()
	if err != nil || validated.Manifest.Schema != storage.SchemaVersion {
		t.Fatal("novel archive validation", err)
	}
	cfg, err := mysql.ParseDSN(os.Getenv("STUDIO_INTEGRATION_TEST_ADMIN_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.DBName = "studio_restore_" + service.NewID()
	t.Cleanup(func() {
		if _, err := db.Exec("DROP DATABASE IF EXISTS `" + cfg.DBName + "`"); err != nil {
			t.Error(err)
		}
	})
	_, err = backup.Restore(ctx, backup.RestoreOptions{ArchivePath: archive, TargetDSN: cfg.FormatDSN(), BlobRoot: filepath.Join(root, "restored"), Authority: backup.ProtectedAuthority{DB: db, Ledger: authority.Ledger{Schema: os.Getenv("STUDIO_AUTHORITY_SCHEMA")}}})
	if err != nil {
		t.Fatal("novel archive restore", err)
	}
	restored, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	var payload string
	var revision int64
	if err = restored.QueryRow("SELECT payload,revision FROM novel_setup_drafts WHERE id=? AND actor_id=?", private.ID, actor.ID).Scan(&payload, &revision); err != nil || revision != private.Revision {
		t.Fatal("private draft not restored", revision, err)
	}
	var saved novel.Setup
	if err = json.Unmarshal([]byte(payload), &saved); err != nil || saved.Idea != private.Setup.Idea {
		t.Fatal("private draft text lost", err)
	}
	var stage string
	if err = restored.QueryRow("SELECT stage FROM novels WHERE id=?", book.ID).Scan(&stage); err != nil || stage != "WORLD_PLANNING" {
		t.Fatal("novel stage lost", stage, err)
	}
	if err = restored.QueryRow("SELECT payload FROM novel_settings_drafts WHERE novel_id=? AND actor_id=?", book.ID, actor.ID).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal([]byte(payload), &saved); err != nil || saved.Title != settingsDraft.Setup.Title {
		t.Fatal("settings draft missing from isolated restore", err)
	}
	var active int
	if err = restored.QueryRow("SELECT COUNT(*) FROM users WHERE enabled=TRUE").Scan(&active); err != nil || active != 0 {
		t.Fatal("restoration re-enabled author identity", active, err)
	}
}

func validNovelSetup() novel.Setup {
	s := novel.Defaults()
	s.Title = "守灯人"
	s.Genre = "悬疑"
	s.Idea = "旧城守灯人追查失踪者"
	s.Conflict = "查明真相会暴露亲人"
	s.CharacterMode = "manual"
	s.Characters = []novel.Character{{Name: "林舟", Identity: "守灯人", Goal: "找到失踪的妹妹"}, {Name: "林舟", Identity: "医生", Goal: "保护病人"}}
	s.CreationMode = "assisted"
	s.ModelChoice = "later"
	return s
}

func TestNovelEntryPrivateDraftAndAtomicConfirmation(t *testing.T) {
	_, db, entry, actor := novelEntryFixture(t)
	ctx := context.Background()
	d := service.NovelSetupDraft{ID: service.NewID(), Step: 1, Setup: novel.Defaults()}
	saved, err := entry.SaveDraft(ctx, actor.ID, d)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := entry.SaveDraft(ctx, actor.ID, d)
	if err != nil || retry.Revision != saved.Revision {
		t.Fatal("lost save response duplicated revision", retry, err)
	}
	d.Setup.Title = "另一本"
	if _, err = entry.SaveDraft(ctx, actor.ID, d); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("stale edit overwrote draft", err)
	}
	other, err := entry.Workspace.CreateMember(ctx, actor.ID, "other_editor", "other-password-123", "editor")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = entry.ReadDraft(ctx, other.ID, saved.ID); !errors.Is(err, domain.ErrForbidden) {
		t.Fatal("private draft leaked", err)
	}
	if _, err = entry.SaveDraft(ctx, other.ID, saved); !errors.Is(err, domain.ErrForbidden) {
		t.Fatal("other author overwrote draft", err)
	}
	if list, e := entry.Drafts(ctx, other.ID); e != nil || len(list) != 0 {
		t.Fatal("another author's draft listed", e)
	}
	confirm := service.NovelConfirmation{Revision: saved.Revision, OperationID: "novel-confirm-1", AcceptSettings: true}
	if _, err = entry.Confirm(ctx, actor.ID, saved.ID, confirm); !errors.Is(err, domain.ErrValidation) {
		t.Fatal("incomplete settings created a book", err)
	}
	var count int
	db.QueryRow("SELECT COUNT(*) FROM projects").Scan(&count)
	if count != 0 {
		t.Fatal("draft or validation left orphan project")
	}
	saved.Setup = validNovelSetup()
	saved.Step = 5
	saved, err = entry.SaveDraft(ctx, actor.ID, saved)
	if err != nil {
		t.Fatal(err)
	}
	confirm.Revision = saved.Revision
	if _, err = db.Exec("CREATE TRIGGER novel_entry_failure BEFORE INSERT ON novels FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='injected final insertion failure'"); err != nil {
		t.Fatal(err)
	}
	_, err = entry.Confirm(ctx, actor.ID, saved.ID, confirm)
	var injected *mysql.MySQLError
	if !errors.As(err, &injected) || injected.Number != 1644 {
		t.Fatal("did not reach the injected final-insert failure", err)
	}
	for _, table := range []string{"projects", "branches", "ip_drafts", "novels", "novel_settings_versions"} {
		if err = db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Fatal("partial creation survived", table, count, err)
		}
	}
	if _, err = db.Exec("DROP TRIGGER novel_entry_failure"); err != nil {
		t.Fatal(err)
	}
	book, err := entry.Confirm(ctx, actor.ID, saved.ID, confirm)
	if err != nil {
		t.Fatal(err)
	}
	again, err := entry.Confirm(ctx, actor.ID, saved.ID, confirm)
	if err != nil || again.ID != book.ID {
		t.Fatal("confirmation duplicated novel", again, err)
	}
	if book.Stage != "WORLD_PLANNING" || book.SettingsRevision != 1 {
		t.Fatal("creation skipped world planning", book)
	}
	for _, table := range []string{"projects", "novels", "novel_settings_versions"} {
		if err = db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil || count != 1 {
			t.Fatal("unexpected creation count", table, count, err)
		}
	}
	if err = db.QueryRow("SELECT COUNT(*) FROM ip_revisions").Scan(&count); err != nil || count != 0 {
		t.Fatal("creation published canonical facts", count, err)
	}
	characterDraft, err := (&service.IP{Workspace: entry.Workspace}).ReadDraft(ctx, actor, book.ProjectID, book.BranchID)
	if err != nil {
		t.Fatal(err)
	}
	if len(characterDraft.Snapshot.Entities) != 2 || characterDraft.Snapshot.Entities[0].ID == characterDraft.Snapshot.Entities[1].ID {
		t.Fatal("characters are missing or namesakes merged")
	}
	if list, e := entry.Drafts(ctx, actor.ID); e != nil || len(list) != 0 {
		t.Fatal("confirmed setup stayed unfinished", e)
	}
	if _, err = entry.SaveDraft(ctx, actor.ID, saved); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("confirmed draft edited in place", err)
	}
	confirm.OperationID = "novel-confirm-another"
	if _, err = entry.Confirm(ctx, actor.ID, saved.ID, confirm); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("one setup created two books", err)
	}
	if _, err = entry.Book(ctx, other.ID, book.ID); !errors.Is(err, domain.ErrForbidden) {
		t.Fatal("book bypassed project permissions", err)
	}
}

func TestNovelEntryExistingIPPreservesOriginalAndChecksAccess(t *testing.T) {
	b, db, entry, actor := novelEntryFixture(t)
	ctx := context.Background()
	project := createProject(b, "原IP", "novel-existing-project")
	parent := branch(b, project, "正史")
	head := propose(b, project, parent, "", "novel-existing-candidate", originalSnapshot())
	publish(b, project, parent, head)
	d := service.NovelSetupDraft{ID: service.NewID(), Step: 5, Setup: validNovelSetup()}
	d.Setup.IPMode = "existing"
	d.Setup.ProjectID = project
	d.Setup.BranchID = parent
	d.Setup.BaseHead = head
	saved, err := entry.SaveDraft(ctx, actor.ID, d)
	if err != nil {
		t.Fatal(err)
	}
	input := service.NovelConfirmation{Revision: saved.Revision, OperationID: "novel-existing-confirm", AcceptSettings: true}
	book, err := entry.Confirm(ctx, actor.ID, saved.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	if book.ProjectID != project || book.BranchID == parent {
		t.Fatal("existing IP overwritten or duplicated", book)
	}
	var current string
	if err = db.QueryRow("SELECT head_revision FROM branches WHERE id=?", parent).Scan(&current); err != nil || current != head {
		t.Fatal("original branch changed", current, err)
	}
	state, err := (&service.IP{Workspace: entry.Workspace}).ReadDraft(ctx, actor, project, book.BranchID)
	if err != nil {
		t.Fatal(err)
	}
	if state.BaseHead != head || len(state.Snapshot.Entities) != len(originalSnapshot().Entities)+2 {
		t.Fatal("original characters lost", state)
	}
	d = service.NovelSetupDraft{ID: service.NewID(), Step: 5, Setup: validNovelSetup()}
	d.Setup.CharacterMode = "architect"
	d.Setup.Characters = nil
	saved, err = entry.SaveDraft(ctx, actor.ID, d)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("UPDATE users SET enabled=FALSE WHERE id=?", actor.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = entry.Confirm(ctx, actor.ID, saved.ID, service.NovelConfirmation{Revision: saved.Revision, OperationID: "revoked-novel", AcceptSettings: true}); !errors.Is(err, domain.ErrForbidden) {
		t.Fatal("revoked author created book", err)
	}
}
