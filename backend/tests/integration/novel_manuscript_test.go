package integration

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/go-sql-driver/mysql"
	"novelstudio/internal/authority"
	"novelstudio/internal/backup"
	"novelstudio/internal/domain"
	"novelstudio/internal/novel"
	"novelstudio/internal/service"
	"novelstudio/internal/storage"
)

func uploadManuscript(t *testing.T, f proseSearchFixture, text string) service.ManuscriptSource {
	t.Helper()
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	file, err := form.CreateFormFile("file", "长稿.txt")
	if err != nil {
		t.Fatal(err)
	}
	file.Write([]byte(text))
	form.Close()
	req := httptest.NewRequest("POST", proseFixtureOrigin+"/api/novels/"+f.book.ID+"/manuscripts/sources", &body)
	req.Header.Set("Origin", proseFixtureOrigin)
	req.Header.Set("Content-Type", form.FormDataContentType())
	req.Header.Set("Idempotency-Key", "long-source-"+service.NewID())
	req.AddCookie(f.b.cookie)
	rec := httptest.NewRecorder()
	f.b.handler.ServeHTTP(rec, req)
	var out service.ManuscriptSource
	if rec.Code != 201 || json.Unmarshal(rec.Body.Bytes(), &out) != nil {
		t.Fatal("manuscript upload", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "\"text\"") {
		t.Fatal("upload response eagerly loads long prose")
	}
	return out
}
func manuscriptCreate(t *testing.T, f proseSearchFixture, source string) service.ManuscriptBatch {
	t.Helper()
	raw := f.b.request("POST", "/api/novels/"+f.book.ID+"/manuscripts", service.ManuscriptCreate{SourceID: source, OperationID: "create-" + service.NewID()}, 201)
	var b service.ManuscriptBatch
	if json.Unmarshal(raw, &b) != nil {
		t.Fatal("batch decode")
	}
	return b
}
func manuscriptApplyInput(b service.ManuscriptBatch, p novel.ManuscriptPart) service.ManuscriptApply {
	rev := int64(0)
	for _, progress := range b.Progress {
		if progress.PartID == p.ID {
			rev = progress.Revision
		}
	}
	return service.ManuscriptApply{Revision: b.Revision, Hash: b.Hash, ProgressRevision: rev, OperationID: "chapter-" + service.NewID()}
}
func TestNovelManuscriptLongSourceLayoutResumeAndReplace(t *testing.T) {
	f := newHistoryFixture(t)
	ctx := context.Background()
	svc := &service.NovelManuscripts{IP: f.ip}
	chapters := &service.NovelChapters{IP: f.ip}
	original := "\ufeff第一章 雨夜\r\n原稿🌙保留。\r\n第二章 回信\r\n下一章。\n第三章 长文\n" + strings.Repeat("原文细节。", 18000)
	source := uploadManuscript(t, f, original)
	if source.Size <= 256<<10 || !source.Complete || source.Characters < 90000 {
		t.Fatal("long manuscript did not use native profile", source)
	}
	name, raw, err := f.ip.ReadImportFile(ctx, f.actor, f.book.ProjectID, f.book.BranchID, source.ID)
	if err != nil || name != "长稿.txt" || string(raw) != original {
		t.Fatal("original bytes/BOM lost", err)
	}
	badSource := uploadManuscript(t, f, string([]byte{255, 254, 1}))
	if badSource.Complete || badSource.Characters != 0 || badSource.Pending != 1 {
		t.Fatal("failed parser claimed coverage")
	}
	_, badRaw, e := f.ip.ReadImportFile(ctx, f.actor, f.book.ProjectID, f.book.BranchID, badSource.ID)
	if e != nil || !bytes.Equal(badRaw, []byte{255, 254, 1}) {
		t.Fatal("parse failure lost original", e)
	}
	b := manuscriptCreate(t, f, source.ID)
	if len(b.Document.Parts) != 3 {
		t.Fatal("headings", b.Document.Parts)
	}
	again := manuscriptCreate(t, f, source.ID)
	if b.ID != again.ID || again.Revision != 1 {
		t.Fatal("duplicate source created second session")
	}
	preview, err := svc.Preview(ctx, f.actor.ID, f.book.ID, b.ID, b.Document.Parts[0].ID, 0)
	if err != nil || preview.Text != "第一章 雨夜\r\n原稿🌙保留。\r\n" {
		t.Fatal("source offsets changed", preview, err)
	}
	broken := append([]novel.ManuscriptPart(nil), b.Document.Parts...)
	broken[0].End--
	if _, err = svc.Save(ctx, f.actor.ID, f.book.ID, b.ID, service.ManuscriptLayout{Revision: b.Revision, Hash: b.Hash, Parts: broken, OperationID: "bad-layout"}); err == nil {
		t.Fatal("source gap accepted")
	}
	p0 := b.Document.Parts[0]
	input := manuscriptApplyInput(b, p0)
	saved, err := svc.Apply(ctx, f.actor.ID, f.book.ID, b.ID, p0.ID, input)
	if err != nil || saved.Status != "done" {
		t.Fatal("first chapter", saved, err)
	}
	replay, err := svc.Apply(ctx, f.actor.ID, f.book.ID, b.ID, p0.ID, input)
	if err != nil || replay.ControlID != saved.ControlID || replay.Revision != saved.Revision {
		t.Fatal("lost response repeated chapter", err)
	}
	current, err := chapters.Read(ctx, f.actor.ID, f.book.ID, saved.ControlID)
	if err != nil || !current.Document.Unplanned || current.Ready || current.Document.Text != preview.Text {
		t.Fatal("unplanned editable chapter", err, current.Reason)
	}
	ref, err := chapters.ReadReference(ctx, f.actor.ID, f.book.ID, saved.ChapterID, "working")
	if err != nil || ref.ControlID != saved.ControlID || ref.Text != preview.Text {
		t.Fatal("imported chapter cannot use existing read workflow", err)
	}
	current.Document.Text += "手动精修。"
	current, err = chapters.Save(ctx, f.actor.ID, f.book.ID, saved.ControlID, current)
	if err != nil || current.Ready {
		t.Fatal("manual editing or planning gate", err)
	}
	if _, err = chapters.BasisPreview(ctx, f.actor.ID, f.book.ID, saved.ControlID); err != nil {
		t.Fatal("unplanned basis inspection", err)
	}
	f.b.request("POST", "/api/novels/"+f.book.ID+"/chapters/"+saved.ControlID+"/release", service.NovelChapterReleaseInput{Revision: current.Revision, Hash: current.Hash, OperationID: "unplanned-blocked"}, 409)
	before := referenceCounts(t, f)
	if _, err = f.db.Exec(`CREATE TRIGGER manuscript_fault BEFORE INSERT ON resource_drafts FOR EACH ROW BEGIN IF JSON_VALID(NEW.body) AND JSON_UNQUOTE(JSON_EXTRACT(NEW.body,'$.title'))='第二章 回信' THEN SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='isolated manuscript fault'; END IF; END`); err != nil {
		t.Fatal(err)
	}
	p1 := b.Document.Parts[1]
	if _, err = svc.Apply(ctx, f.actor.ID, f.book.ID, b.ID, p1.ID, manuscriptApplyInput(b, p1)); err == nil {
		t.Fatal("write fault not reached")
	}
	if _, err = f.db.Exec("DROP TRIGGER manuscript_fault"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, referenceCounts(t, f)) {
		t.Fatal("failed chapter left partial draft/progress")
	}
	svc = &service.NovelManuscripts{IP: f.ip}
	b, err = svc.Read(ctx, f.actor.ID, f.book.ID, b.ID)
	if err != nil || len(b.Progress) != 1 {
		t.Fatal("restart progress", err)
	}
	second, err := svc.Apply(ctx, f.actor.ID, f.book.ID, b.ID, p1.ID, manuscriptApplyInput(b, p1))
	if err != nil || second.Status != "done" {
		t.Fatal("resume failed chapter", second, err)
	}
	p2 := b.Document.Parts[2]
	failed, err := svc.Apply(ctx, f.actor.ID, f.book.ID, b.ID, p2.ID, manuscriptApplyInput(b, p2))
	if err != nil || failed.Status != "failed" {
		t.Fatal("oversize part not retained retryable", err, failed)
	}
	b, err = svc.Read(ctx, f.actor.ID, f.book.ID, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	layout := append([]novel.ManuscriptPart(nil), b.Document.Parts...)
	end := layout[2].End
	layout[2].End = layout[2].Start + 45000
	layout = append(layout, novel.ManuscriptPart{ID: service.NewID(), Start: layout[2].End, End: end, Title: "第三章（续）", Mode: "new"})
	b, err = svc.Save(ctx, f.actor.ID, f.book.ID, b.ID, service.ManuscriptLayout{Revision: b.Revision, Hash: b.Hash, Parts: layout, OperationID: "split-large"})
	if err != nil {
		t.Fatal("manual boundary adjustment", err)
	}
	retry, err := svc.Apply(ctx, f.actor.ID, f.book.ID, b.ID, p2.ID, manuscriptApplyInput(b, layout[2]))
	if err != nil || retry.Status != "done" {
		t.Fatal("split failure resume", retry, err)
	}
	layout[0].Start++
	if _, err = svc.Save(ctx, f.actor.ID, f.book.ID, b.ID, service.ManuscriptLayout{Revision: b.Revision, Hash: b.Hash, Parts: layout, OperationID: "change-completed"}); err == nil {
		t.Fatal("completed source range was changed")
	}
	// Exact text is distinguished from a title collision; replacement uses the
	// editor's current CAS and its existing pre-change revision checkpoint.
	duplicate := uploadManuscript(t, f, f.doc.Text)
	d := manuscriptCreate(t, f, duplicate.ID)
	part := d.Document.Parts[0]
	same, err := svc.Preview(ctx, f.actor.ID, f.book.ID, d.ID, part.ID, 0)
	if err != nil || len(same.Matches) != 1 || !same.Matches[0].Exact {
		t.Fatal("exact duplicate detection", same, err)
	}
	blocked, err := svc.Apply(ctx, f.actor.ID, f.book.ID, d.ID, part.ID, manuscriptApplyInput(d, part))
	if err != nil || blocked.Status != "failed" {
		t.Fatal("default duplicated prose imported", err)
	}
	old, err := chapters.Read(ctx, f.actor.ID, f.book.ID, f.control)
	if err != nil {
		t.Fatal(err)
	}
	part.Mode = "replace"
	part.Target = &novel.ManuscriptTarget{ControlID: f.control, Revision: old.Revision, Hash: old.Hash, BaseHead: old.BaseHead}
	d, err = svc.Save(ctx, f.actor.ID, f.book.ID, d.ID, service.ManuscriptLayout{Revision: d.Revision, Hash: d.Hash, Parts: []novel.ManuscriptPart{part}, OperationID: "select-target"})
	if err != nil {
		t.Fatal(err)
	}
	old.Document.Text += "已有手改。"
	old, err = chapters.Save(ctx, f.actor.ID, f.book.ID, f.control, old)
	if err != nil {
		t.Fatal(err)
	}
	replace := manuscriptApplyInput(d, part)
	replace.ConfirmReplace = true
	stale, err := svc.Apply(ctx, f.actor.ID, f.book.ID, d.ID, part.ID, replace)
	if err != nil || stale.Status != "failed" {
		t.Fatal("stale replacement overwrote prose", err)
	}
	d, err = svc.Read(ctx, f.actor.ID, f.book.ID, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	part.Target.Revision = old.Revision
	part.Target.Hash = old.Hash
	d, err = svc.Save(ctx, f.actor.ID, f.book.ID, d.ID, service.ManuscriptLayout{Revision: d.Revision, Hash: d.Hash, Parts: []novel.ManuscriptPart{part}, OperationID: "refresh-target"})
	if err != nil {
		t.Fatal(err)
	}
	replace = manuscriptApplyInput(d, part)
	replace.ConfirmReplace = true
	replaced, err := svc.Apply(ctx, f.actor.ID, f.book.ID, d.ID, part.ID, replace)
	if err != nil || replaced.Status != "done" {
		t.Fatal("explicit replacement", replaced, err)
	}
	histories, err := chapters.History(ctx, f.actor.ID, f.book.ID, f.control, "", "")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, h := range histories.Items {
		if h.Kind == "before_replace" {
			v, e := chapters.HistoryText(ctx, f.actor.ID, f.book.ID, f.control, h.ID)
			if e != nil {
				t.Fatal(e)
			}
			found = v.Text == old.Document.Text
		}
	}
	if !found {
		t.Fatal("replacement did not keep current old manuscript")
	}
	// Private import orchestration is not a generic editable resource or model input.
	if _, err = f.ip.SaveResourceDraft(ctx, f.actor, f.book.ProjectID, b.ID, service.ResourceDraft{Revision: b.Revision, Text: "mutate"}); !errors.Is(err, domain.ErrForbidden) {
		t.Fatal("generic writer mutated import state", err)
	}
	member := referenceObject(t, f.b.request("POST", "/api/members", map[string]any{"username": "import-editor", "password": "import-editor-password", "role": "editor"}, 201))
	f.b.request("PUT", "/api/projects/"+f.book.ProjectID+"/members/"+member["id"].(string), map[string]any{"role": "editor"}, 200)
	other := &proseSearchClient{t: t, handler: f.b.handler}
	other.request("POST", "/api/login", map[string]any{"username": "import-editor", "password": "import-editor-password"}, 200)
	other.request("GET", "/api/novels/"+f.book.ID+"/manuscripts/"+b.ID, nil, 403)
	// The directory only projects source metadata; a normal expired-report
	// envelope must not hide other saved originals or the import ledger.
	if _, err = f.db.Exec(`UPDATE source_imports SET report='{"schema":1,"state":"EXPIRED","summary":{}}' WHERE id=?`, badSource.ID); err != nil {
		t.Fatal(err)
	}
	items, err := svc.Sources(ctx, f.actor.ID, f.book.ID, "")
	if err != nil {
		t.Fatal("expired source blocked directory", err)
	}
	for _, v := range items {
		if v.ID == badSource.ID {
			t.Fatal("expired source offered for new import")
		}
	}
	if batches, e := svc.List(ctx, f.actor.ID, f.book.ID, ""); e != nil || len(batches) != 2 {
		t.Fatal("existing import batches hidden by expired source", e)
	}
}

func TestNovelManuscriptOriginalRetentionBackupAndProjectRecovery(t *testing.T) {
	f := newHistoryFixture(t)
	ctx := context.Background()
	svc := &service.NovelManuscripts{IP: f.ip}
	original := "\ufeff第一章 原稿🌙\r\n原始内容。\r\n第二章 尚未导入\n保留进度。" + strings.Repeat("长稿细节🌙。", 140000)
	source := uploadManuscript(t, f, original)
	b := manuscriptCreate(t, f, source.ID)
	p := b.Document.Parts[0]
	saved, err := svc.Apply(ctx, f.actor.ID, f.book.ID, b.ID, p.ID, manuscriptApplyInput(b, p))
	if err != nil || saved.Status != "done" {
		t.Fatal(err, saved)
	}
	scanner := &service.RetentionScanner{Workspace: f.ip.Workspace, Blobs: storage.LocalBlobs{Root: os.Getenv("STUDIO_BLOB_ROOT")}}
	if _, err = scanner.Scan(ctx, f.actor.ID, f.book.ProjectID); err != nil {
		t.Fatal("manuscript author retention", err)
	}
	archive := filepath.Join(t.TempDir(), "manuscript.studio")
	if _, err = backup.Create(ctx, f.db, f.ip.Blobs, archive); err != nil {
		t.Fatal("manuscript backup", err)
	}
	cfg, err := mysql.ParseDSN(os.Getenv("STUDIO_INTEGRATION_TEST_ADMIN_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.DBName = "studio_restore_" + service.NewID()
	t.Cleanup(func() { f.db.Exec("DROP DATABASE IF EXISTS `" + cfg.DBName + "`") })
	blobRoot := filepath.Join(t.TempDir(), "restored")
	if _, err = backup.Restore(ctx, backup.RestoreOptions{ArchivePath: archive, TargetDSN: cfg.FormatDSN(), BlobRoot: blobRoot, Authority: backup.ProtectedAuthority{DB: f.db, Ledger: authority.Ledger{Schema: os.Getenv("STUDIO_AUTHORITY_SCHEMA")}}}); err != nil {
		t.Fatal("manuscript restore", err)
	}
	restored, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	for _, kind := range []string{"novel_manuscript_import", "novel_manuscript_progress", "novel_chapter_control"} {
		rows, e := f.db.Query("SELECT r.id,d.body FROM resources r JOIN resource_drafts d ON d.resource_id=r.id AND d.actor_id=? WHERE r.project_id=? AND r.kind=?", f.actor.ID, f.book.ProjectID, kind)
		if e != nil {
			t.Fatal(e)
		}
		for rows.Next() {
			var id, body, copy string
			rows.Scan(&id, &body)
			if e = restored.QueryRow("SELECT body FROM resource_drafts WHERE resource_id=? AND actor_id=?", id, f.actor.ID).Scan(&copy); e != nil || copy != body {
				t.Fatal("restore changed exact source/layout/progress", kind, e)
			}
		}
		rows.Close()
	}
	restoredIP := &service.IP{Workspace: &service.Workspace{DB: restored}, Blobs: storage.LocalBlobs{Root: blobRoot}}
	if _, err = (&service.NovelManuscripts{IP: restoredIP}).Read(ctx, f.actor.ID, f.book.ID, b.ID); !errors.Is(err, domain.ErrForbidden) {
		t.Fatal("restore bypassed user reauthorization", err)
	}
	bytes, err := restoredIP.Blobs.ReadObject(ctx, f.book.ProjectID, source.Hash)
	if err != nil || string(bytes) != original {
		t.Fatal("restore lost raw original", err)
	}
	var name string
	f.db.QueryRow("SELECT name FROM projects WHERE id=?", f.book.ProjectID).Scan(&name)
	f.b.request("DELETE", "/api/projects/"+f.book.ProjectID, map[string]any{"confirm_name": name, "revision": 1, "operation_id": "delete-import-project"}, 200)
	copied, err := f.ip.RecoverProject(ctx, f.actor.ID, f.book.ProjectID, service.ProjectRecoveryInput{Name: "长稿恢复副本", Confirmation: name, Revision: 2, OperationID: "copy-import-project"})
	if err != nil {
		t.Fatal("project copy remapping", err)
	}
	var bookID, batchID string
	f.db.QueryRow("SELECT id FROM novels WHERE project_id=?", copied.ID).Scan(&bookID)
	f.db.QueryRow("SELECT id FROM resources WHERE project_id=? AND kind='novel_manuscript_import'", copied.ID).Scan(&batchID)
	recovered, err := svc.Read(ctx, f.actor.ID, bookID, batchID)
	if err != nil || !recovered.Document.Recovered || recovered.Document.SourceID == source.ID || len(recovered.Progress) != 1 {
		t.Fatal("recovered import ledger", err)
	}
	_, raw, err := f.ip.ReadImportFile(ctx, f.actor, copied.ID, recoveredProgressBranch(t, f.db, copied.ID), recovered.Document.SourceID)
	if err != nil || string(raw) != original {
		t.Fatal("project recovery original", err)
	}
	if _, err = svc.Apply(ctx, f.actor.ID, bookID, batchID, recovered.Document.Parts[1].ID, manuscriptApplyInput(recovered, recovered.Document.Parts[1])); !errors.Is(err, domain.ErrNotReady) {
		t.Fatal("project copy silently resumed stale import", err)
	}
}
func recoveredProgressBranch(t *testing.T, db *sql.DB, project string) string {
	t.Helper()
	var branch string
	if err := db.QueryRow("SELECT branch_id FROM novels WHERE project_id=?", project).Scan(&branch); err != nil {
		t.Fatal(err)
	}
	return branch
}
