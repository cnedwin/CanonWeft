package integration

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"novelstudio/internal/domain"
	"novelstudio/internal/retrieval"
	"novelstudio/internal/service"
)

func assertNovelExport(t *testing.T, b *browser, ip *service.IP, actor domain.Actor, book service.NovelBook, chapterID string) {
	t.Helper()
	ctx := context.Background()
	svc := &service.NovelExport{IP: ip}
	in := service.NovelExportInput{Source: "draft", Author: "测试作者", Language: "zh-Hans", Description: "测试简介", Format: "epub"}
	b.request("POST", "/api/novels/"+book.ID+"/export/editions", map[string]any{"name": "不能把草稿存成已发布稿本", "operation_id": "draft-edition-rejected", "accept": true, "export": in}, 400)
	p, e := svc.Preview(ctx, actor.ID, book.ID, in)
	if e != nil || p.Included != 1 || p.Missing != 1 {
		t.Fatal("draft export preview", p, e)
	}
	in.PreviewHash = p.Hash
	in.Format = "pdf"
	if _, _, e = svc.Download(ctx, actor.ID, book.ID, in); !errors.Is(e, domain.ErrValidation) {
		t.Fatal("PDF skipped missing chapter confirmation", e)
	}
	in.AllowIncomplete = true
	pdf, pdfMime, e := svc.Download(ctx, actor.ID, book.ID, in)
	if e != nil || pdfMime != "application/pdf" || !bytes.HasPrefix(pdf, []byte("%PDF-")) {
		t.Fatal("real PDF export", e)
	}
	in.Format = "epub"
	in.AllowIncomplete = false
	if _, _, e = svc.Download(ctx, actor.ID, book.ID, in); !errors.Is(e, domain.ErrValidation) {
		t.Fatal("missing chapters silently omitted", e)
	}
	in.AllowIncomplete = true
	raw, mime, e := svc.Download(ctx, actor.ID, book.ID, in)
	if e != nil || mime != "application/epub+zip" {
		t.Fatal("EPUB download", e)
	}
	z, e := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if e != nil {
		t.Fatal(e)
	}
	text := ""
	for _, f := range z.File {
		if strings.HasSuffix(f.Name, "chapter-1.xhtml") {
			r, e := f.Open()
			if e != nil {
				t.Fatal(e)
			}
			v, e := io.ReadAll(r)
			r.Close()
			if e != nil {
				t.Fatal(e)
			}
			text = string(v)
		}
	}
	if !strings.Contains(text, "林舟") || strings.Contains(text, "chapter_ledger") {
		t.Fatal("EPUB missing prose or exposed ledger")
	}
	public := in
	public.Source = "published"
	empty, e := svc.Preview(ctx, actor.ID, book.ID, public)
	if e != nil || empty.Included != 0 || empty.Missing != 2 {
		t.Fatal("unpublished draft leaked", empty, e)
	}
	public.PreviewHash = empty.Hash
	public.AllowIncomplete = true
	if _, _, e = svc.Download(ctx, actor.ID, book.ID, public); !errors.Is(e, domain.ErrNotReady) {
		t.Fatal("empty export accepted", e)
	}
	other := b.request("POST", "/api/members", map[string]any{"username": "export-editor", "password": "export-editor-password", "role": "editor"}, 201)
	otherID := other["id"].(string)
	b.request("PUT", "/api/projects/"+book.ProjectID+"/members/"+otherID, map[string]any{"role": "editor"}, 200)
	isolated, e := svc.Preview(ctx, otherID, book.ID, in)
	if e != nil || isolated.Included != 0 {
		t.Fatal("another author's draft leaked", isolated, e)
	}
	b.request("PUT", "/api/projects/"+book.ProjectID+"/members/"+otherID, map[string]any{"role": "reader"}, 200)
	if _, e = svc.Preview(ctx, otherID, book.ID, in); !errors.Is(e, domain.ErrForbidden) {
		t.Fatal("viewer exported author-only manuscript", e)
	}
	chapters := &service.NovelChapters{IP: ip}
	r, e := chapters.Open(ctx, actor.ID, book.ID, chapterID)
	if e != nil {
		t.Fatal(e)
	}
	d, e := chapters.Read(ctx, actor.ID, book.ID, r.ID)
	if e != nil {
		t.Fatal(e)
	}
	original := d.Document.Text
	d.Document.Text += "\n导出预览后修改。"
	d, e = chapters.Save(ctx, actor.ID, book.ID, r.ID, d)
	if e != nil {
		t.Fatal(e)
	}
	if _, _, e = svc.Download(ctx, actor.ID, book.ID, in); !errors.Is(e, domain.ErrConflict) {
		t.Fatal("stale preview exported new text", e)
	}
	in.Format = "pdf"
	if _, _, e = svc.Download(ctx, actor.ID, book.ID, in); !errors.Is(e, domain.ErrConflict) {
		t.Fatal("PDF accepted stale preview", e)
	}
	in.Format = "epub"
	d.Document.Text = original
	if _, e = chapters.Save(ctx, actor.ID, book.ID, r.ID, d); e != nil {
		t.Fatal(e)
	}
	p, e = svc.Preview(ctx, actor.ID, book.ID, in)
	if e != nil {
		t.Fatal(e)
	}
	in.VolumeID = p.Chapters[0].VolumeID
	v, e := svc.Preview(ctx, actor.ID, book.ID, in)
	if e != nil || v.Included != 1 {
		t.Fatal("volume export", e)
	}
	in.PreviewHash = v.Hash
	in.Format = "txt"
	raw, _, e = svc.Download(ctx, actor.ID, book.ID, in)
	if e != nil || !strings.Contains(string(raw), original) {
		t.Fatal("TXT changed original prose", e)
	}
	b.request("POST", "/api/novels/"+book.ID+"/export/preview", in, 200)
	assertNovelCoverExport(t, b, ip, actor, book)
	t.Log("EPUB/TXT exported real private prose; unpublished, other-author, viewer, changed-preview and volume selection verified; no model call")
}

func assertNovelCoverExport(t *testing.T, b *browser, ip *service.IP, actor domain.Actor, book service.NovelBook) {
	t.Helper()
	ctx := context.Background()
	images := &service.NovelImages{Generations: &service.Generations{IP: ip}}
	pixels, _ := base64.StdEncoding.DecodeString("UklGRh4AAABXRUJQVlA4TBEAAAAvAkAAAAdQrYIUr/+BiOh/AAA=")
	upload := func(kind, name string) service.SourceImport {
		item, e := images.Import(ctx, actor.ID, book.ID, name+".webp", "export-"+name, retrieval.NovelImage{BookID: book.ID, Kind: kind, Name: name, Source: "原创测试图", Rights: "测试原创"}, pixels)
		if e != nil {
			t.Fatal("WebP import", e)
		}
		return item
	}
	adopt := func(id string) string {
		w, e := images.Workspace(ctx, actor.ID, book.ID)
		if e != nil {
			t.Fatal(e)
		}
		r, e := images.Candidate(ctx, actor.ID, book.ID, id, w.Branch.Head, "export-adopt-"+id)
		if e != nil {
			t.Fatal(e)
		}
		releaseAdopt(b, book.ProjectID, r.ID)
		releasePublish(b, book.ProjectID, r.ID)
		return r.ID
	}
	cover := upload("cover", "已采用封面")
	input := map[string]any{"source": "draft", "author": "测试作者", "language": "zh-Hans", "cover_image_id": cover.ID}
	// The endpoint must reject an author's own upload until it is explicitly adopted.
	b.request("POST", "/api/novels/"+book.ID+"/export/preview", input, 400)
	release := adopt(cover.ID)
	result := b.request("POST", "/api/novels/"+book.ID+"/export/preview", input, 200)
	frozen, ok := result["cover"].(map[string]any)
	if !ok || frozen["image_id"] != cover.ID || frozen["hash"] != cover.Hash || frozen["release_id"] != release || frozen["mime"] != "image/webp" {
		t.Fatal("selected adopted cover not pinned", result)
	}
	input["preview_hash"], input["allow_incomplete"], input["format"] = result["hash"], true, "epub"
	var in service.NovelExportInput
	raw, _ := json.Marshal(input)
	_ = json.Unmarshal(raw, &in)
	epub, _, e := (&service.NovelExport{IP: ip}).Download(ctx, actor.ID, book.ID, in)
	if e != nil {
		t.Fatal(e)
	}
	z, e := zip.NewReader(bytes.NewReader(epub), int64(len(epub)))
	if e != nil {
		t.Fatal(e)
	}
	found := false
	for _, f := range z.File {
		if f.Name == "EPUB/cover.png" {
			found = true
		}
	}
	if !found {
		t.Fatal("WebP export was not converted to compatible cover")
	}
	_, original, e := images.Read(ctx, actor.ID, book.ID, cover.ID)
	if e != nil || !bytes.Equal(original, pixels) {
		t.Fatal("export mutated original WebP", e)
	}
	alternative := upload("cover", "另一个已采用封面")
	adopt(alternative.ID)
	input["cover_image_id"] = alternative.ID
	b.request("POST", "/api/novels/"+book.ID+"/export/download", input, 409)
	// Scene artwork is never silently treated as a book cover, even when adopted.
	scene := upload("scene", "已采用场景")
	adopt(scene.ID)
	input["cover_image_id"] = scene.ID
	b.request("POST", "/api/novels/"+book.ID+"/export/preview", input, 400)
	input["cover_image_id"] = "ffffffffffffffffffffffffffffffff"
	b.request("POST", "/api/novels/"+book.ID+"/export/preview", input, 403)
	entry := &service.NovelEntry{Workspace: ip.Workspace}
	otherSetup := validNovelSetup()
	otherSetup.Title = "另一本封面隔离测试书"
	draft, e := entry.SaveDraft(ctx, actor.ID, service.NovelSetupDraft{ID: service.NewID(), Step: 5, Setup: otherSetup})
	if e != nil {
		t.Fatal(e)
	}
	otherBook, e := entry.Confirm(ctx, actor.ID, draft.ID, service.NovelConfirmation{Revision: draft.Revision, OperationID: "export-other-book", AcceptSettings: true})
	if e != nil {
		t.Fatal(e)
	}
	foreign, e := images.Import(ctx, actor.ID, otherBook.ID, "other.webp", "export-other-image", retrieval.NovelImage{BookID: otherBook.ID, Kind: "cover", Name: "另书封面", Source: "原创", Rights: "测试原创"}, pixels)
	if e != nil {
		t.Fatal(e)
	}
	input["cover_image_id"] = foreign.ID
	b.request("POST", "/api/novels/"+book.ID+"/export/preview", input, 403)
	if os.Getenv("STUDIO_NOVEL_COVER_BROWSER_NODE") != "" {
		assertNovelCoverExportBrowser(t, ip, book.Setup.Title, book.ID, cover.ID, alternative.ID, scene.ID)
	}
}
