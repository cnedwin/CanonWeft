package service

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"golang.org/x/text/language"
	"novelstudio/internal/domain"
	"novelstudio/internal/novel"
)

type NovelExport struct{ IP *IP }
type NovelExportInput struct {
	EditionID       string `json:"edition_id,omitempty"`
	Source          string `json:"source"`
	VolumeID        string `json:"volume_id"`
	Author          string `json:"author"`
	Language        string `json:"language"`
	Description     string `json:"description"`
	CoverImageID    string `json:"cover_image_id,omitempty"`
	Format          string `json:"format"`
	PreviewHash     string `json:"preview_hash"`
	AllowIncomplete bool   `json:"allow_incomplete"`
}
type NovelExportChapter struct {
	ID         string `json:"id"`
	ResourceID string `json:"resource_id"`
	VolumeID   string `json:"volume_id"`
	Volume     string `json:"volume"`
	Title      string `json:"title"`
	Hash       string `json:"hash"`
	Head       string `json:"head"`
	Revision   int64  `json:"revision"`
	Characters int    `json:"characters"`
	Included   bool   `json:"included"`
	Warning    string `json:"warning"`
}
type NovelExportPreview struct {
	EditionMetadata *NovelEditionMetadata `json:"edition_metadata,omitempty"`
	EditionID       string                `json:"edition_id,omitempty"`
	Cover           *NovelExportCover     `json:"cover,omitempty"`
	Hash            string                `json:"hash"`
	Title           string                `json:"title"`
	Source          string                `json:"source"`
	Chapters        []NovelExportChapter  `json:"chapters"`
	Included        int                   `json:"included"`
	Missing         int                   `json:"missing"`
	Warnings        int                   `json:"warnings"`
	Characters      int                   `json:"characters"`
}

func (s *NovelExport) Preview(ctx context.Context, actor, bookID string, in NovelExportInput) (NovelExportPreview, error) {
	p, _, e := s.snapshot(ctx, actor, bookID, in)
	return p, e
}

func (s *NovelExport) snapshot(ctx context.Context, actor, bookID string, in NovelExportInput) (NovelExportPreview, novel.ExportBook, error) {
	out := NovelExportPreview{Source: in.Source, Chapters: []NovelExportChapter{}}
	var manuscript novel.ExportBook
	if in.Source == "edition" {
		if in.VolumeID != "" || in.Author != "" || in.Language != "" || in.Description != "" || in.CoverImageID != "" {
			return out, manuscript, domain.ErrValidation
		}
		return s.editionSnapshot(ctx, actor, bookID, in.EditionID)
	}
	if in.EditionID != "" {
		return out, manuscript, domain.ErrValidation
	}
	if (in.Source != "draft" && in.Source != "published") || (in.VolumeID != "" && !lowerHex32(in.VolumeID)) || len([]rune(in.Author)) > 120 || len([]rune(in.Description)) > 4000 || len(in.Language) > 80 {
		return out, manuscript, domain.ErrValidation
	}
	lang, e := language.Parse(in.Language)
	if e != nil || lang == language.Und {
		return out, manuscript, fmt.Errorf("%w：请选择有效的书籍语言", domain.ErrValidation)
	}
	// Author materials require editing rights, including when they are published
	// internally. This endpoint is not a public reader or a publication action.
	tx, a, book, err := (&NovelPlanning{IP: s.IP}).begin(ctx, actor, bookID, true)
	if err != nil {
		return out, manuscript, err
	}
	defer tx.Rollback()
	return s.snapshotTx(ctx, tx, a, book, in)
}

// snapshotTx also lets review dispatch compare every source under the existing
// project transaction, without opening a second transaction or copying prose.
func (s *NovelExport) snapshotTx(ctx context.Context, tx *sql.Tx, a domain.Actor, book NovelBook, in NovelExportInput) (NovelExportPreview, novel.ExportBook, error) {
	out := NovelExportPreview{Source: in.Source, Chapters: []NovelExportChapter{}}
	var manuscript novel.ExportBook
	lang, err := language.Parse(in.Language)
	if err != nil || lang == language.Und {
		return out, manuscript, domain.ErrValidation
	}
	check, err := newPlanningCheck(ctx, tx, s.IP, book.ProjectID, book.BranchID)
	if err != nil {
		return out, manuscript, err
	}
	outline, plan, err := check.current("book", "")
	if err != nil {
		return out, manuscript, err
	}
	pins := []domain.DependencyRef{planPin(outline)}
	out.Title = book.Setup.Title
	manuscript = novel.ExportBook{Title: out.Title, Author: in.Author, Language: lang.String(), Description: in.Description, Chapters: []novel.ExportChapter{}}
	found := in.VolumeID == ""
	totalBytes := 0
	for _, volume := range plan.Volumes {
		if in.VolumeID != "" && in.VolumeID != volume.ID {
			continue
		}
		found = true
		vr, v, e := check.current("volume", volume.ID)
		if e != nil {
			return out, manuscript, e
		}
		pins = append(pins, planPin(vr))
		for _, chapter := range v.Chapters {
			item := NovelExportChapter{ID: chapter.ID, VolumeID: volume.ID, Volume: volume.Title, Title: chapter.Title}
			text, e := s.chapterTx(ctx, tx, a, book, in.Source, &item)
			if e != nil {
				return out, manuscript, e
			}
			if item.Included {
				totalBytes += len(text)
				if totalBytes > 32*1024*1024 {
					return out, manuscript, fmt.Errorf("%w：本次正文超过32MiB，请按分卷导出", domain.ErrValidation)
				}
				item.Characters = len([]rune(text))
				out.Characters += item.Characters
				out.Included++
				manuscript.Chapters = append(manuscript.Chapters, novel.ExportChapter{Volume: item.Volume, Title: item.Title, Text: text})
			} else {
				out.Missing++
			}
			if item.Warning != "" {
				out.Warnings++
			}
			out.Chapters = append(out.Chapters, item)
		}
	}
	if !found {
		return out, manuscript, domain.ErrValidation
	}
	if in.CoverImageID != "" {
		out.Cover, manuscript.Cover, err = s.coverTx(ctx, tx, a, book, in.CoverImageID)
		if err != nil {
			return out, manuscript, err
		}
		if out.Cover.Warning != "" {
			out.Warnings++
		}
	}
	// No timestamp: unchanged previews have a stable token. Metadata, ordered
	// plan pins and private/public revision hashes all take part in confirmation.
	out.Hash = capabilityDigest(struct {
		Book                                  string
		Settings                              int64
		Volume, Author, Language, Description string
		Pins                                  []domain.DependencyRef
		Preview                               NovelExportPreview
	}{book.ID, book.SettingsRevision, in.VolumeID, in.Author, lang.String(), in.Description, pins, out})
	manuscript.ID = "urn:sha256:" + out.Hash
	manuscript.Notice = "本文件为作者导出稿，不代表通过平台审核或完成整卷语义审查。"
	if in.Source == "draft" {
		manuscript.Notice += " 来源：当前作者私人稿（没有私人稿时使用已正式采用稿）。"
	} else {
		manuscript.Notice += " 来源：已正式采用稿。"
	}
	if out.Missing > 0 || out.Warnings > 0 {
		manuscript.Notice += fmt.Sprintf(" 本次有%d章未导出、%d项待核对：", out.Missing, out.Warnings)
		if out.Cover != nil && out.Cover.Warning != "" {
			manuscript.Notice += "\n封面：" + out.Cover.Warning
		}
		for _, c := range out.Chapters {
			if c.Warning != "" {
				manuscript.Notice += "\n" + c.Volume + " / " + c.Title + "：" + c.Warning
			}
		}
	}
	return out, manuscript, nil
}

func (s *NovelExport) chapterTx(ctx context.Context, tx *sql.Tx, a domain.Actor, book NovelBook, source string, item *NovelExportChapter) (string, error) {
	var id string
	err := tx.QueryRowContext(ctx, "SELECT id FROM resources WHERE project_id=? AND branch_id=? AND kind='novel_chapter_control' AND name=?", book.ProjectID, book.BranchID, chapterName("章节工作稿", item.ID)).Scan(&id)
	if err == sql.ErrNoRows {
		item.Warning = "尚未创建正文"
		return "", nil
	}
	if err != nil {
		return "", err
	}
	item.ResourceID = id
	r, err := resourceTx(ctx, tx, book.ProjectID, id)
	if err != nil {
		return "", err
	}
	item.Head = r.Head
	var doc novel.ChapterDocument
	if source == "draft" {
		stored, e := resourceDraftTx(ctx, tx, a.ID, book.ProjectID, id)
		if e != nil {
			return "", e
		}
		if stored.Revision == 0 && r.Head == "" {
			item.Warning = "尚未保存正文"
			return "", nil
		}
		d, e := (&NovelChapters{IP: s.IP}).draftTx(ctx, tx, a, book, id)
		if e != nil {
			return "", e
		}
		doc = d.Document
		item.Revision = d.Revision
		item.Hash = d.Hash
		if !d.Ready {
			item.Warning = "写作依据待更新：" + d.Reason
		}
	} else {
		if r.Head == "" {
			item.Warning = "尚未正式采用"
			return "", nil
		}
		raw, e := s.IP.Blobs.ReadObject(ctx, book.ProjectID, r.Hash)
		if e != nil {
			return "", e
		}
		if digest(string(raw)) != r.Hash {
			return "", domain.ErrNotReady
		}
		doc, e = novel.DecodeChapter(raw)
		if e != nil {
			return "", e
		}
		item.Hash = r.Hash
		item.Revision = r.Revision
		body, e := resourceTx(ctx, tx, book.ProjectID, doc.BodyID)
		if e != nil {
			return "", e
		}
		if body.BranchID != book.BranchID || body.Kind != "novel_chapter_body" || body.Name != chapterName("章节正文", item.ID) || body.Head != r.Head || body.Hash != digest(doc.Text) {
			return "", domain.ErrNotReady
		}
		text, e := s.IP.Blobs.ReadObject(ctx, book.ProjectID, body.Hash)
		if e != nil {
			return "", e
		}
		if string(text) != doc.Text {
			return "", domain.ErrNotReady
		}
	}
	if doc.ChapterID != item.ID {
		return "", domain.ErrNotReady
	}
	if doc.Title != "" {
		item.Title = doc.Title
	}
	if strings.TrimSpace(doc.Text) == "" {
		item.Warning = "正文为空"
		return "", nil
	}
	item.Included = true
	return doc.Text, nil
}

func (s *NovelExport) Download(ctx context.Context, actor, bookID string, in NovelExportInput) ([]byte, string, error) {
	if in.Format != "epub" && in.Format != "txt" && in.Format != "pdf" && in.Format != "md" {
		return nil, "", domain.ErrValidation
	}
	p, b, e := s.snapshot(ctx, actor, bookID, in)
	if e != nil {
		return nil, "", e
	}
	if e = checkNovelExport(p, in); e != nil {
		return nil, "", e
	}
	if in.Format == "md" {
		return []byte(b.Markdown()), "text/markdown; charset=utf-8", nil
	}
	if in.Format == "txt" {
		return []byte(b.TXT()), "text/plain; charset=utf-8", nil
	}
	if in.Format == "pdf" {
		raw, err := b.PDF(ctx)
		return raw, "application/pdf", err
	}
	raw, e := b.EPUB(time.Now())
	return raw, "application/epub+zip", e
}

func checkNovelExport(p NovelExportPreview, in NovelExportInput) error {
	if len(in.PreviewHash) != 64 || strings.Trim(in.PreviewHash, "0123456789abcdef") != "" || (in.Source != "edition" && strings.TrimSpace(in.Author) == "") {
		return domain.ErrValidation
	}
	if p.Hash != in.PreviewHash {
		return fmt.Errorf("%w：目录、正文、封面或导出信息已变化，请重新预览", domain.ErrConflict)
	}
	if p.Included == 0 {
		return fmt.Errorf("%w：没有可以导出的正文", domain.ErrNotReady)
	}
	if (p.Missing > 0 || p.Warnings > 0) && !in.AllowIncomplete {
		return fmt.Errorf("%w：请核对缺章及提示，并明确选择导出当前可用稿", domain.ErrValidation)
	}
	return nil
}
