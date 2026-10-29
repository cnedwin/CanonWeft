package integration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"novelstudio/internal/domain"
	"novelstudio/internal/novel"
	"novelstudio/internal/retrieval"
	"novelstudio/internal/service"
)

type editionChapterFixture struct {
	b          *browser
	db         *sql.DB
	entry      *service.NovelEntry
	ip         *service.IP
	actor      domain.Actor
	book       service.NovelBook
	preview    service.NovelExportPreview
	manuscript novel.ExportBook
}

func editionChapterHash(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// The existing edition save/publication chain has its own integration coverage.
// These fixtures supply archived publication rows directly so extraction tests
// neither execute a provider nor require model/index services just to read.
func newEditionChapterFixture(t *testing.T) editionChapterFixture {
	t.Helper()
	ctx := context.Background()
	b, db, entry, actor := novelEntryFixture(t)
	saved, err := entry.SaveDraft(ctx, actor.ID, service.NovelSetupDraft{ID: service.NewID(), Step: 5, Setup: validNovelSetup()})
	if err != nil {
		t.Fatal(err)
	}
	book, err := entry.Confirm(ctx, actor.ID, saved.ID, service.NovelConfirmation{Revision: saved.Revision, OperationID: "edition-chapter-book", AcceptSettings: true})
	if err != nil {
		t.Fatal(err)
	}
	volume := service.NewID()
	first, missing, last := service.NewID(), service.NewID(), service.NewID()
	one, two := " 甲🙂\r\n\t第一版正文。\n尾部空白  ", "另一章正文，不可因同名返回前章。\n"
	p := service.NovelExportPreview{Hash: editionChapterHash("isolated-original-preview"), Title: book.Setup.Title, Source: "published", Included: 2, Missing: 1, Warnings: 1, Characters: utf8.RuneCountInString(one) + utf8.RuneCountInString(two), Chapters: []service.NovelExportChapter{
		{ID: first, ResourceID: service.NewID(), VolumeID: volume, Volume: "第一卷", Title: "同名章节", Hash: editionChapterHash("original-control-first"), Head: service.NewID(), Revision: 3, Characters: utf8.RuneCountInString(one), Included: true},
		{ID: missing, VolumeID: volume, Volume: "第一卷", Title: "尚无正文", Warning: "尚未正式采用"},
		{ID: last, ResourceID: service.NewID(), VolumeID: volume, Volume: "第一卷", Title: "同名章节", Hash: editionChapterHash("original-control-last"), Head: service.NewID(), Revision: 6, Characters: utf8.RuneCountInString(two), Included: true},
	}}
	m := novel.ExportBook{ID: "urn:sha256:" + p.Hash, Title: book.Setup.Title, Author: "原版作者", Language: "zh-Hans", Description: "固定稿本", Chapters: []novel.ExportChapter{{Volume: "第一卷", Title: "同名章节", Text: one}, {Volume: "第一卷", Title: "同名章节", Text: two}}}
	return editionChapterFixture{b, db, entry, propagationService(t, db), actor, book, p, m}
}

func (f editionChapterFixture) archive(t *testing.T, p service.NovelExportPreview, m novel.ExportBook, published bool) (string, string) {
	t.Helper()
	ctx := context.Background()
	id := service.NewID()
	info := retrieval.NovelEdition{BookID: f.book.ID, Name: "旧成书稿本", Title: m.Title, Author: m.Author, PreviewHash: p.Hash, RequestHash: editionChapterHash(id), Included: p.Included, Missing: p.Missing}
	metadata, err := json.Marshal(struct {
		Schema  int                        `json:"schema"`
		Info    retrieval.NovelEdition     `json:"info"`
		Preview service.NovelExportPreview `json:"preview"`
	}{1, info, p})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := m.Archive(metadata)
	if err != nil {
		t.Fatal(err)
	}
	blob, err := f.ip.Blobs.PutObject(ctx, f.book.ProjectID, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	summary, _ := json.Marshal(info)
	report, _ := json.Marshal(retrieval.ImportReport{Parser: "novel-edition-v1", Complete: true, NovelEdition: &info, Text: string(summary), Units: []retrieval.ImportUnit{}})
	if _, err = f.db.Exec("INSERT INTO source_imports(id,project_id,branch_id,actor_id,filename,source_hash,source_size,report,request_hash,operation_id) VALUES(?,?,?,?,?,?,?,?,?,?)", id, f.book.ProjectID, f.book.BranchID, f.actor.ID, "成书版本.nsedition", blob.Hash, blob.Size, report, editionChapterHash(id), "archive-"+id); err != nil {
		t.Fatal(err)
	}
	if published {
		resource, release := service.NewID(), service.NewID()
		if _, err = f.db.Exec("INSERT INTO resources(id,project_id,branch_id,name,kind) VALUES(?,?,?,'成书版本','note')", resource, f.book.ProjectID, f.book.BranchID); err != nil {
			t.Fatal(err)
		}
		manifest, _ := json.Marshal(domain.Manifest{Schema: 1, ReleaseID: release, BranchID: f.book.BranchID, Artifacts: []domain.ArtifactRef{{ResourceID: resource, Hash: editionChapterHash(string(summary)), Size: int64(len(summary)), SourceImportID: id, OriginalHash: blob.Hash}}})
		if _, err = f.db.Exec("INSERT INTO artifact_releases(id,project_id,branch_id,manifest,content_hash,status,created_by) VALUES(?,?,?,?,?,'PUBLISHED',?)", release, f.book.ProjectID, f.book.BranchID, manifest, editionChapterHash(string(manifest)), f.actor.ID); err != nil {
			t.Fatal(err)
		}
		if _, err = f.db.Exec("INSERT INTO source_import_candidates(import_id,resource_id,release_id) VALUES(?,?,?)", id, resource, release); err != nil {
			t.Fatal(err)
		}
	}
	return id, blob.Hash
}

func TestNovelEditionChapterIsReadOnlyScopedAndExact(t *testing.T) {
	f := newEditionChapterFixture(t)
	id, hash := f.archive(t, f.preview, f.manuscript, true)
	path := func(chapter string) string {
		return "/api/novels/" + f.book.ID + "/export/editions/" + id + "/chapters/" + chapter
	}
	counts := func() []int {
		result := make([]int, 8)
		for i, table := range []string{"source_imports", "resources", "resource_drafts", "artifact_releases", "audit_events", "outbox", "generation_tasks", "model_calls"} {
			if err := f.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&result[i]); err != nil {
				t.Fatal(err)
			}
		}
		return result
	}
	before := counts()
	req := httptest.NewRequest("GET", "http://127.0.0.1:18765"+path(f.preview.Chapters[0].ID), nil)
	req.AddCookie(f.b.cookie)
	rec := httptest.NewRecorder()
	f.b.handler.ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Header().Get("Cache-Control"), "no-store") {
		t.Fatal("chapter read HTTP/cache contract", rec.Code, rec.Body)
	}
	var first service.NovelEditionChapter
	if json.Unmarshal(rec.Body.Bytes(), &first) != nil {
		t.Fatal("invalid chapter response")
	}
	if first.BookID != f.book.ID || first.EditionID != id || first.EditionHash != hash || first.Chapter.ID != f.preview.Chapters[0].ID || first.Text != f.manuscript.Chapters[0].Text || first.TextHash != editionChapterHash(first.Text) || first.TextHash == first.Chapter.Hash || strings.Contains(rec.Body.String(), f.manuscript.Chapters[1].Text) {
		t.Fatal("chapter extraction lost whitespace/Unicode, hashes, or chapter identity", first)
	}
	last := f.b.request("GET", path(f.preview.Chapters[2].ID), nil, 200)
	if last["text"] != f.manuscript.Chapters[1].Text {
		t.Fatal("same title or missing directory row shifted the original chapter mapping")
	}
	if !equalIntSlice(before, counts()) {
		t.Fatal("chapter GET wrote author, audit, publication, or execution state")
	}
	f.b.request("GET", path(f.preview.Chapters[1].ID), nil, 409)
	f.b.request("GET", path(service.NewID()), nil, 409)
	f.b.request("GET", path("not-a-chapter"), nil, 400)
	pending, _ := f.archive(t, f.preview, f.manuscript, false)
	f.b.request("GET", strings.Replace(path(f.preview.Chapters[0].ID), id, pending, 1), nil, 409)
	(&browser{t: t, handler: f.b.handler}).request("GET", path(f.preview.Chapters[0].ID), nil, 401)
	member := f.b.request("POST", "/api/members", map[string]any{"username": "edition-prose-editor", "password": "edition-prose-editor-password", "role": "editor"}, 201)
	editor := &browser{t: t, handler: f.b.handler}
	editor.request("POST", "/api/login", map[string]any{"username": "edition-prose-editor", "password": "edition-prose-editor-password"}, 200)
	editor.request("GET", path(f.preview.Chapters[0].ID), nil, 403)
	f.b.request("PUT", "/api/projects/"+f.book.ProjectID+"/members/"+member["id"].(string), map[string]any{"role": "editor"}, 200)
	editor.request("GET", path(f.preview.Chapters[0].ID), nil, 200)
	f.b.request("PUT", "/api/projects/"+f.book.ProjectID+"/members/"+member["id"].(string), map[string]any{"role": "reader"}, 200)
	editor.request("GET", path(f.preview.Chapters[0].ID), nil, 403)
	other, err := f.entry.SaveDraft(context.Background(), f.actor.ID, service.NovelSetupDraft{ID: service.NewID(), Step: 5, Setup: validNovelSetup()})
	if err != nil {
		t.Fatal(err)
	}
	book, err := f.entry.Confirm(context.Background(), f.actor.ID, other.ID, service.NovelConfirmation{Revision: other.Revision, OperationID: "other-edition-book", AcceptSettings: true})
	if err != nil {
		t.Fatal(err)
	}
	f.b.request("GET", strings.Replace(path(f.preview.Chapters[0].ID), f.book.ID, book.ID, 1), nil, 403)
	if _, err = f.db.Exec("UPDATE source_imports SET source_hash=? WHERE id=?", strings.Repeat("0", 64), id); err != nil {
		t.Fatal(err)
	}
	f.b.request("GET", path(f.preview.Chapters[0].ID), nil, 503)
}

func TestNovelEditionChapterRejectsBrokenArchiveMapping(t *testing.T) {
	f := newEditionChapterFixture(t)
	for _, scenario := range []string{"duplicate_chapter", "duplicate_resource", "wrong_title", "wrong_characters", "wrong_missing_count"} {
		t.Run(scenario, func(t *testing.T) {
			p, m := f.preview, f.manuscript
			p.Chapters = append([]service.NovelExportChapter(nil), p.Chapters...)
			m.Chapters = append([]novel.ExportChapter(nil), m.Chapters...)
			switch scenario {
			case "duplicate_chapter":
				p.Chapters[2].ID = p.Chapters[0].ID
			case "duplicate_resource":
				p.Chapters[2].ResourceID = p.Chapters[0].ResourceID
			case "wrong_title":
				m.Chapters[1].Title = "和固定目录不一致"
			case "wrong_characters":
				p.Chapters[2].Characters++
			case "wrong_missing_count":
				p.Missing++
			}
			id, _ := f.archive(t, p, m, true)
			f.b.request("GET", "/api/novels/"+f.book.ID+"/export/editions/"+id+"/chapters/"+p.Chapters[0].ID, nil, 503)
		})
	}
}

func equalIntSlice(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
