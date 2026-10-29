package integration

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"

	"novelstudio/internal/domain"
	"novelstudio/internal/httpapi"
	"novelstudio/internal/novel"
	"novelstudio/internal/service"
)

const proseFixtureOrigin = "http://127.0.0.1:18867"

type proseSearchClient struct {
	t       *testing.T
	handler http.Handler
	cookie  *http.Cookie
}

func (b *proseSearchClient) request(method, path string, input any, want int) []byte {
	b.t.Helper()
	raw, _ := json.Marshal(input)
	if value, ok := input.(json.RawMessage); ok {
		raw = value
	}
	req := httptest.NewRequest(method, proseFixtureOrigin+path, bytes.NewReader(raw))
	req.Header.Set("Origin", proseFixtureOrigin)
	req.Header.Set("Content-Type", "application/json")
	if b.cookie != nil {
		req.AddCookie(b.cookie)
	}
	rec := httptest.NewRecorder()
	b.handler.ServeHTTP(rec, req)
	if rec.Code != want {
		b.t.Fatalf("prose search %s: got %d want %d (%s)", method, rec.Code, want, rec.Body)
	}
	for _, cookie := range rec.Result().Cookies() {
		b.cookie = cookie
	}
	if want == 200 && method == "POST" && strings.Contains(path, "prose-search") && !strings.Contains(rec.Header().Get("Cache-Control"), "no-store") {
		b.t.Fatal("query response lacks private no-store")
	}
	return rec.Body.Bytes()
}

type proseSearchFixture struct {
	b       *proseSearchClient
	db      *sql.DB
	entry   *service.NovelEntry
	ip      *service.IP
	actor   domain.Actor
	book    service.NovelBook
	control string
	doc     novel.ChapterDocument
}

func newProseSearchFixture(t *testing.T) proseSearchFixture {
	t.Helper()
	if os.Getenv("STUDIO_INTEGRATION_TEST_ADMIN_DSN") == "" {
		t.Skip("requires explicitly disposable integration administrator DSN")
	}
	t.Setenv("STUDIO_BLOB_ROOT", t.TempDir())
	db := database(t)
	b := &proseSearchClient{t: t, handler: httpapi.New(db, proseFixtureOrigin)}
	b.request("POST", "/api/setup", map[string]any{"username": "owner", "password": "test-owner-password-123", "workspace": "正文查找隔离工作室"}, 201)
	b.request("POST", "/api/login", map[string]any{"username": "owner", "password": "test-owner-password-123"}, 200)
	var actor domain.Actor
	if err := db.QueryRow("SELECT id,workspace_id,username,role FROM users WHERE username='owner'").Scan(&actor.ID, &actor.WorkspaceID, &actor.Username, &actor.Role); err != nil {
		t.Fatal(err)
	}
	entry := &service.NovelEntry{Workspace: &service.Workspace{DB: db}}
	ctx := context.Background()
	saved, err := entry.SaveDraft(ctx, actor.ID, service.NovelSetupDraft{ID: service.NewID(), Step: 5, Setup: validNovelSetup()})
	if err != nil {
		t.Fatal(err)
	}
	book, err := entry.Confirm(ctx, actor.ID, saved.ID, service.NovelConfirmation{Revision: saved.Revision, OperationID: "prose-search-book", AcceptSettings: true})
	if err != nil {
		t.Fatal(err)
	}
	var settingsHash, head string
	if err = db.QueryRow("SELECT content_hash FROM novel_settings_versions WHERE novel_id=? AND revision=1", book.ID).Scan(&settingsHash); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow("SELECT head_revision FROM branches WHERE id=?", book.BranchID).Scan(&head); err != nil {
		t.Fatal(err)
	}
	doc := novel.ChapterDocument{Schema: 1, ChapterID: service.NewID(), BodyID: service.NewID(), Detail: domain.DependencyRef{Kind: "resource", BranchID: book.BranchID, ID: service.NewID(), Revision: service.NewID(), Hash: strings.Repeat("a", 64)}, SettingsRevision: 1, SettingsHash: settingsHash, IPHead: head, Title: "同名章节", Text: "🙂林舟Aa 林舟aa 林舟\r\n旧城与线索。"}
	f := proseSearchFixture{b, db, entry, propagationService(t, db), actor, book, service.NewID(), doc}
	f.add(t, f.control, doc, true)
	return f
}
func (f proseSearchFixture) add(t *testing.T, control string, doc novel.ChapterDocument, private bool) {
	t.Helper()
	for _, r := range []struct{ id, kind, name string }{{control, "novel_chapter_control", "章节工作稿 · " + doc.ChapterID}, {doc.BodyID, "novel_chapter_body", "章节正文 · " + doc.ChapterID}} {
		if _, err := f.db.Exec("INSERT INTO resources(id,project_id,branch_id,name,kind) VALUES(?,?,?,?,?)", r.id, f.book.ProjectID, f.book.BranchID, r.name, r.kind); err != nil {
			t.Fatal(err)
		}
	}
	if private {
		raw, _ := json.Marshal(doc)
		if _, err := f.db.Exec("INSERT INTO resource_drafts(resource_id,actor_id,revision,base_head,body) VALUES(?,?,1,'',?)", control, f.actor.ID, raw); err != nil {
			t.Fatal(err)
		}
	}
}
func (f proseSearchFixture) publish(t *testing.T) string {
	t.Helper()
	id := service.NewID()
	control, _ := json.Marshal(f.doc)
	manifest := domain.Manifest{Schema: 2, ReleaseID: id, BranchID: f.book.BranchID, Artifacts: []domain.ArtifactRef{}}
	for _, r := range []struct {
		id  string
		raw []byte
	}{{f.control, control}, {f.doc.BodyID, []byte(f.doc.Text)}} {
		blob, err := f.ip.Blobs.PutObject(context.Background(), f.book.ProjectID, bytes.NewReader(r.raw))
		if err != nil {
			t.Fatal(err)
		}
		manifest.Artifacts = append(manifest.Artifacts, domain.ArtifactRef{ResourceID: r.id, Hash: blob.Hash, Size: blob.Size})
		if _, err = f.db.Exec("UPDATE resources SET head_revision=?,content_hash=?,revision=2 WHERE id=?", id, blob.Hash, r.id); err != nil {
			t.Fatal(err)
		}
	}
	raw, _ := json.Marshal(manifest)
	if _, err := f.db.Exec("INSERT INTO artifact_releases(id,project_id,branch_id,manifest,content_hash,status,created_by) VALUES(?,?,?,?,?,'PUBLISHED',?)", id, f.book.ProjectID, f.book.BranchID, raw, editionChapterHash(string(raw)), f.actor.ID); err != nil {
		t.Fatal(err)
	}
	return id
}
func prosePage(t *testing.T, b *proseSearchClient, book string, in service.NovelProseSearchInput) service.NovelProseSearchPage {
	t.Helper()
	raw := b.request("POST", "/api/novels/"+book+"/prose-search", in, 200)
	var page service.NovelProseSearchPage
	if err := json.Unmarshal(raw, &page); err != nil {
		t.Fatal(err)
	}
	return page
}

func TestNovelProseSearchIsReadOnlyScopedAndExact(t *testing.T) {
	f := newProseSearchFixture(t)
	path := "/api/novels/" + f.book.ID + "/prose-search"
	in := service.NovelProseSearchInput{Source: "working", Query: "林舟"}
	counts := func() []int {
		values := make([]int, 9)
		for i, table := range []string{"resources", "resource_drafts", "artifact_releases", "audit_events", "outbox", "generation_tasks", "model_calls", "memory_items", "operations"} {
			if err := f.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&values[i]); err != nil {
				t.Fatal(err)
			}
		}
		return values
	}
	before := counts()
	p := prosePage(t, f.b, f.book.ID, in)
	if p.Scanned != 1 || len(p.Items) != 1 || p.Items[0].Origin != "private" || p.Items[0].SourceHash != editionChapterHash(string(mustProseJSON(f.doc))) || p.Items[0].TotalMatches != 3 || p.Items[0].Matches[0].Start != 1 || p.Items[0].Matches[0].End != 3 || p.Items[0].Matches[0].Before != "🙂" || p.Items[0].ChapterID != f.doc.ChapterID {
		t.Fatal("lost exact Unicode/source contract", p)
	}
	in.Source = "published"
	p = prosePage(t, f.b, f.book.ID, in)
	if p.Items[0].Origin != "unavailable" || p.Items[0].TotalMatches != 0 {
		t.Fatal("private source passed as adopted")
	}
	if !reflect.DeepEqual(before, counts()) {
		t.Fatal("query persisted text, audit, or execution state")
	}
	f.publish(t)
	before = counts()
	p = prosePage(t, f.b, f.book.ID, in)
	if p.Items[0].Origin != "published" || p.Items[0].TotalMatches != 3 {
		t.Fatal("published source unavailable", p)
	}
	if !reflect.DeepEqual(before, counts()) {
		t.Fatal("published search wrote state")
	}
	changed := f.doc
	changed.Text = "当前私人林舟只出现一次。"
	if _, err := f.db.Exec("UPDATE resource_drafts SET body=?,revision=2 WHERE resource_id=? AND actor_id=?", mustProseJSON(changed), f.control, f.actor.ID); err != nil {
		t.Fatal(err)
	}
	in.Source = "working"
	if p = prosePage(t, f.b, f.book.ID, in); p.Items[0].TotalMatches != 1 || p.Items[0].Origin != "private" {
		t.Fatal("working search did not use actor draft", p)
	}
	in.Source = "published"
	if p = prosePage(t, f.b, f.book.ID, in); p.Items[0].TotalMatches != 3 {
		t.Fatal("private draft leaked into published")
	}
	memberRaw := f.b.request("POST", "/api/members", map[string]any{"username": "prose-editor", "password": "prose-editor-password", "role": "editor"}, 201)
	var member map[string]any
	json.Unmarshal(memberRaw, &member)
	editor := &proseSearchClient{t: t, handler: f.b.handler}
	editor.request("POST", "/api/login", map[string]any{"username": "prose-editor", "password": "prose-editor-password"}, 200)
	editor.request("POST", path, in, 403)
	f.b.request("PUT", "/api/projects/"+f.book.ProjectID+"/members/"+member["id"].(string), map[string]any{"role": "editor"}, 200)
	in.Source = "working"
	if p = prosePage(t, editor, f.book.ID, in); p.Items[0].Origin != "published" || p.Items[0].TotalMatches != 3 {
		t.Fatal("other actor private isolation failed", p)
	}
	f.b.request("PUT", "/api/projects/"+f.book.ProjectID+"/members/"+member["id"].(string), map[string]any{"role": "reader"}, 200)
	editor.request("POST", path, in, 403)
	(&proseSearchClient{t: t, handler: f.b.handler}).request("POST", path, in, 401)
	f.b.request("POST", path, json.RawMessage(`{"source":"working","query":"林舟","extra":true}`), 400)
	f.b.request("POST", path, json.RawMessage(`{"source":"working","query":"林舟"}{}`), 400)
	f.b.request("GET", path, nil, 404)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (&service.NovelChapters{IP: f.ip}).ProseSearch(ctx, f.actor.ID, f.book.ID, in); err == nil {
		t.Fatal("cancelled query returned a false empty result")
	}
}

func TestNovelProseSearchCoveragePaginationAndBrokenSources(t *testing.T) {
	f := newProseSearchFixture(t)
	in := service.NovelProseSearchInput{Source: "working", Query: "林舟"}
	// Equal chapter titles remain separate identities. No planning resource is
	// created: saved prose must remain searchable with missing/invalid planning.
	for i := 0; i < 5; i++ {
		doc := f.doc
		doc.ChapterID = service.NewID()
		doc.BodyID = service.NewID()
		if i == 0 {
			doc.Text = strings.Repeat("🙂林舟", 80)
		} else {
			doc.Text = "没有搜索词的另一章。"
		}
		f.add(t, service.NewID(), doc, true)
	}
	unsaved := f.doc
	unsaved.ChapterID = service.NewID()
	unsaved.BodyID = service.NewID()
	f.add(t, service.NewID(), unsaved, false)
	seen := map[string]bool{}
	matches := 0
	foundUnsaved := false
	foundCap := false
	for {
		p := prosePage(t, f.b, f.book.ID, in)
		if p.Scanned > 5 || lenMustJSON(t, p) > 1048576 {
			t.Fatal("unbounded page")
		}
		for _, item := range p.Items {
			if seen[item.ControlID] {
				t.Fatal("repeated cursor row")
			}
			seen[item.ControlID] = true
			matches += item.TotalMatches
			if item.ChapterID == unsaved.ChapterID {
				foundUnsaved = item.Origin == "unavailable"
			}
			if item.TotalMatches == 80 {
				foundCap = item.Truncated && len(item.Matches) == 50
			}
		}
		if p.NextCursor == "" {
			break
		}
		in.After = p.NextCursor
	}
	if len(seen) != 7 || matches != 83 || !foundUnsaved || !foundCap {
		t.Fatal("coverage falsely complete, default draft initialized, or matches truncated", len(seen), matches, foundUnsaved, foundCap)
	}
	f.publish(t)
	in.Source = "published"
	in.After = ""
	if _, err := f.db.Exec("UPDATE resources SET content_hash=? WHERE id=?", strings.Repeat("0", 64), f.doc.BodyID); err != nil {
		t.Fatal(err)
	}
	for {
		p := prosePage(t, f.b, f.book.ID, in)
		for _, item := range p.Items {
			if item.ControlID == f.control && (item.Origin != "unavailable" || len(item.Matches) != 0 || item.Reason == "") {
				t.Fatal("broken body accepted", item)
			}
		}
		if p.NextCursor == "" {
			break
		}
		in.After = p.NextCursor
	}
	// An overlarge saved control is a visible exclusion and does not block the
	// following rows or cause a default author chapter to be initialized.
	if _, err := f.db.Exec("UPDATE resource_drafts SET body=? WHERE resource_id=? AND actor_id=?", strings.Repeat("x", 600000), f.control, f.actor.ID); err != nil {
		t.Fatal(err)
	}
	in.Source = "working"
	in.After = ""
	foundLarge := false
	for {
		p := prosePage(t, f.b, f.book.ID, in)
		for _, item := range p.Items {
			if item.ControlID == f.control {
				foundLarge = item.Origin == "unavailable" && item.Reason != ""
			}
		}
		if p.NextCursor == "" {
			break
		}
		in.After = p.NextCursor
	}
	if !foundLarge {
		t.Fatal("oversize exclusion missing")
	}
	other, err := f.entry.SaveDraft(context.Background(), f.actor.ID, service.NovelSetupDraft{ID: service.NewID(), Step: 5, Setup: validNovelSetup()})
	if err != nil {
		t.Fatal(err)
	}
	book, err := f.entry.Confirm(context.Background(), f.actor.ID, other.ID, service.NovelConfirmation{Revision: other.Revision, OperationID: "other-prose-book", AcceptSettings: true})
	if err != nil {
		t.Fatal(err)
	}
	f.b.request("POST", "/api/novels/"+book.ID+"/prose-search", service.NovelProseSearchInput{Source: "working", Query: "林舟", After: f.control}, 403)
}
func mustProseJSON(value any) []byte {
	raw, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return raw
}

func TestNovelProseSearchMalformedPublicationIsVisible(t *testing.T) {
	f := newProseSearchFixture(t)
	release := f.publish(t)
	for i := 0; i < 6; i++ {
		doc := f.doc
		doc.ChapterID = service.NewID()
		doc.BodyID = service.NewID()
		f.add(t, service.NewID(), doc, false)
	}
	// MySQL's JSON column accepts valid JSON with the wrong manifest field type.
	// releaseTx returns *json.UnmarshalTypeError before checking the hash.
	if _, err := f.db.Exec("UPDATE artifact_releases SET manifest=JSON_OBJECT('artifacts','not-an-array') WHERE id=?", release); err != nil {
		t.Fatal(err)
	}
	in := service.NovelProseSearchInput{Source: "published", Query: "林舟"}
	scanned := 0
	found := false
	for {
		p := prosePage(t, f.b, f.book.ID, in)
		scanned += p.Scanned
		for _, item := range p.Items {
			if item.ControlID == f.control {
				found = item.Origin == "unavailable" && item.Reason != "" && len(item.Matches) == 0
			}
		}
		if p.NextCursor == "" {
			break
		}
		in.After = p.NextCursor
	}
	if scanned != 7 || !found {
		t.Fatal("malformed source blocked page continuation or disappeared", scanned, found)
	}
}
