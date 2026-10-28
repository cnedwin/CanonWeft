package integration

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"image"
	"image/png"
	"os"
	"strings"
	"testing"

	"novelstudio/internal/domain"
	"novelstudio/internal/novel"
	"novelstudio/internal/retrieval"
	"novelstudio/internal/service"
	"novelstudio/internal/storage"
)

func assertNovelEditionArchive(t *testing.T, ip *service.IP, actor domain.Actor, book service.NovelBook) {
	t.Helper()
	ctx := context.Background()
	svc := &service.NovelExport{IP: ip}
	images := &service.NovelImages{Generations: &service.Generations{IP: ip}}
	var pixels bytes.Buffer
	_ = png.Encode(&pixels, image.NewRGBA(image.Rect(0, 0, 3, 2)))
	picture, err := images.Import(ctx, actor.ID, book.ID, "成书封面.png", "edition-cover", retrieval.NovelImage{BookID: book.ID, Kind: "cover", Name: "成书封面", Source: "本地原创测试", Rights: "原创"}, pixels.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	w, err := images.Workspace(ctx, actor.ID, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	cover, err := images.Candidate(ctx, actor.ID, book.ID, picture.ID, w.Branch.Head, "edition-cover-release")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ip.AdoptRelease(ctx, actor, book.ProjectID, cover.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = ip.PublishRelease(ctx, actor, book.ProjectID, cover.ID); err != nil {
		t.Fatal(err)
	}
	in := service.NovelExportInput{Source: "published", Author: "原版作者", Language: "zh-Hans", Description: "保存时的简介", CoverImageID: picture.ID, AllowIncomplete: true, Format: "txt"}
	p, err := svc.Preview(ctx, actor.ID, book.ID, in)
	if err != nil || p.Included < 1 {
		t.Fatal("published edition preview", p, err)
	}
	in.PreviewHash = p.Hash
	original, _, err := svc.Download(ctx, actor.ID, book.ID, in)
	if err != nil {
		t.Fatal(err)
	}
	save := service.NovelEditionInput{Name: "首卷投稿版", OperationID: "edition-original", Accept: true, Export: in}
	rejected := save
	rejected.Accept = false
	if _, err = svc.SaveEdition(ctx, actor.ID, book.ID, rejected); !errors.Is(err, domain.ErrValidation) {
		t.Fatal("implicit edition save", err)
	}
	saved, err := svc.SaveEdition(ctx, actor.ID, book.ID, save)
	if err != nil || !saved.Published {
		t.Fatal("save edition", saved, err)
	}
	again, err := svc.SaveEdition(ctx, actor.ID, book.ID, save)
	if err != nil || again.ID != saved.ID || again.ReleaseID != saved.ReleaseID {
		t.Fatal("edition replay duplicated", again, err)
	}
	altered := save
	altered.Name = "同操作不同版本"
	if _, err = svc.SaveEdition(ctx, actor.ID, book.ID, altered); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("edition operation rewritten", err)
	}
	historical := service.NovelExportInput{Source: "edition", EditionID: saved.ID, AllowIncomplete: true}
	frozen, err := svc.Preview(ctx, actor.ID, book.ID, historical)
	if err != nil || frozen.EditionID != saved.ID {
		t.Fatal(err)
	}
	historical.PreviewHash = frozen.Hash
	for _, format := range []string{"txt", "md", "epub", "pdf"} {
		historical.Format = format
		raw, mime, e := svc.Download(ctx, actor.ID, book.ID, historical)
		if e != nil || len(raw) == 0 || mime == "" {
			t.Fatal("historical download", format, e)
		}
		if format == "txt" && !bytes.Equal(raw, original) {
			t.Fatal("saved version changed manuscript or metadata")
		}
		if format == "md" && (!bytes.Contains(raw, []byte("# "+book.Setup.Title)) || !bytes.Contains(raw, []byte("原版作者"))) {
			t.Fatal("Markdown metadata missing")
		}
	}
	raw, mime, err := svc.EditionCover(ctx, actor.ID, book.ID, saved.ID)
	if err != nil || mime != "image/png" || !bytes.Equal(raw, pixels.Bytes()) {
		t.Fatal("archived cover", err)
	}
	// Simulate a save failing after the managed archive is durable, before adoption.
	if _, err = ip.Workspace.DB.Exec("CREATE TRIGGER reject_novel_edition BEFORE INSERT ON artifact_releases FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='test-only edition save interruption'"); err != nil {
		t.Fatal(err)
	}
	defer ip.Workspace.DB.Exec("DROP TRIGGER IF EXISTS reject_novel_edition")
	interrupted := save
	interrupted.Name = "中断待续版"
	interrupted.OperationID = "edition-interrupted"
	if _, err = svc.SaveEdition(ctx, actor.ID, book.ID, interrupted); err == nil || !strings.Contains(err.Error(), "test-only edition save interruption") {
		t.Fatal("expected archive save interruption", err)
	}
	if _, err = ip.Workspace.DB.Exec("DROP TRIGGER reject_novel_edition"); err != nil {
		t.Fatal(err)
	}
	list, err := svc.Editions(ctx, actor.ID, book.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	var pending service.NovelEditionItem
	for _, v := range list.Items {
		if v.Name == interrupted.Name {
			pending = v
		}
	}
	if pending.ID == "" || pending.Published {
		t.Fatal("interrupted archive was lost or auto-published")
	}
	if _, err = svc.Preview(ctx, actor.ID, book.ID, service.NovelExportInput{Source: "edition", EditionID: pending.ID}); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("unadopted archive exported", err)
	}
	if _, err = svc.ConfirmEdition(ctx, actor.ID, book.ID, pending.ID, strings.Repeat("0", 64), true); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("wrong archive hash accepted", err)
	}
	if resumed, err := svc.SaveEdition(ctx, actor.ID, book.ID, interrupted); err != nil || resumed.ID != pending.ID || !resumed.Published {
		t.Fatal("interrupted save replay", err)
	}
}

func assertNovelEditionAfterPublication(t *testing.T, b *browser, ip *service.IP, a domain.Actor, book service.NovelBook) {
	t.Helper()
	ctx := context.Background()
	svc := &service.NovelExport{IP: ip}
	list, err := svc.Editions(ctx, a.ID, book.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	var id string
	for _, item := range list.Items {
		if item.Name == "首卷投稿版" {
			id = item.ID
		}
	}
	if id == "" {
		t.Fatal("original edition disappeared")
	}
	old := service.NovelExportInput{Source: "edition", EditionID: id, AllowIncomplete: true, Format: "txt"}
	p, err := svc.Preview(ctx, a.ID, book.ID, old)
	if err != nil {
		t.Fatal(err)
	}
	old.PreviewHash = p.Hash
	raw, _, err := svc.Download(ctx, a.ID, book.ID, old)
	if err != nil || bytes.Contains(raw, []byte("药铺的灯熄灭了")) {
		t.Fatal("old edition followed republication", err)
	}
	current := service.NovelExportInput{Source: "published", Author: "新版作者", Language: "zh-Hans", AllowIncomplete: true, Format: "txt"}
	p, err = svc.Preview(ctx, a.ID, book.ID, current)
	if err != nil {
		t.Fatal(err)
	}
	current.PreviewHash = p.Hash
	changed, _, err := svc.Download(ctx, a.ID, book.ID, current)
	if err != nil || !bytes.Contains(changed, []byte("药铺的灯熄灭了")) {
		t.Fatal("test did not publish changed prose", err)
	}
	outsider := b.request("POST", "/api/members", map[string]any{"username": "edition-reader", "password": "edition-reader-password", "role": "reader"}, 201)["id"].(string)
	b.request("PUT", "/api/projects/"+book.ProjectID+"/members/"+outsider, map[string]any{"role": "reader"}, 200)
	if _, err = svc.Editions(ctx, outsider, book.ID, ""); !errors.Is(err, domain.ErrForbidden) {
		t.Fatal("reader accessed author archive", err)
	}
	entry := &service.NovelEntry{Workspace: ip.Workspace}
	setup := validNovelSetup()
	setup.Title = "其他作品"
	draft, err := entry.SaveDraft(ctx, a.ID, service.NovelSetupDraft{ID: service.NewID(), Step: 5, Setup: setup})
	if err != nil {
		t.Fatal(err)
	}
	other, err := entry.Confirm(ctx, a.ID, draft.ID, service.NovelConfirmation{Revision: draft.Revision, OperationID: "edition-other-book", AcceptSettings: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Preview(ctx, a.ID, other.ID, old); !errors.Is(err, domain.ErrForbidden) {
		t.Fatal("cross-book archive exported", err)
	}
	if os.Getenv("STUDIO_NOVEL_EDITION_BROWSER_NODE") != "" {
		assertNovelEditionBrowser(t, ip, book.Setup.Title, book.ID, id)
	}
}

func assertNovelEditionRestore(t *testing.T, source, restored *sql.DB, blobs storage.LocalBlobs, project string) {
	t.Helper()
	rows, err := source.Query(`SELECT id,source_hash FROM source_imports WHERE project_id=? AND JSON_UNQUOTE(JSON_EXTRACT(report,'$.parser'))='novel-edition-v1'`, project)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, hash, actual string
		if err = rows.Scan(&id, &hash); err != nil {
			t.Fatal(err)
		}
		if err = restored.QueryRow("SELECT source_hash FROM source_imports WHERE id=?", id).Scan(&actual); err != nil || actual != hash {
			t.Fatal("edition archive reference restore", err)
		}
		raw, e := blobs.ReadObject(context.Background(), project, hash)
		if e != nil {
			t.Fatal(e)
		}
		book, _, e := novel.ReadExportArchive(raw)
		if e != nil || book.Cover == nil || book.Author == "" {
			t.Fatal("edition prose/cover restore", e)
		}
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
}
