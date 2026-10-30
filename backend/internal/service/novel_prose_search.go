package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"unicode/utf8"

	"novelstudio/internal/domain"
	"novelstudio/internal/novel"
)

const proseSearchPageSize = 5
const proseSearchPageBytes = 1 << 20
const proseSearchDocumentBytes = 512 << 10

type NovelProseSearchInput struct {
	Source string `json:"source"`
	Query  string `json:"query"`
	After  string `json:"after"`
}
type NovelProseMatch struct {
	Start  int    `json:"start"`
	End    int    `json:"end"`
	Quote  string `json:"quote"`
	Before string `json:"before"`
	After  string `json:"after"`
}
type NovelProseSearchItem struct {
	ChapterID    string            `json:"chapter_id"`
	ControlID    string            `json:"control_id"`
	ChapterTitle string            `json:"chapter_title"`
	Origin       string            `json:"origin"`
	SourceHash   string            `json:"source_hash"`
	Revision     int64             `json:"revision"`
	Characters   int               `json:"characters"`
	TotalMatches int               `json:"total_matches"`
	Truncated    bool              `json:"truncated"`
	Reason       string            `json:"reason"`
	Matches      []NovelProseMatch `json:"matches"`
}
type NovelProseSearchPage struct {
	BookID     string                 `json:"book_id"`
	Source     string                 `json:"source"`
	Query      string                 `json:"query"`
	Items      []NovelProseSearchItem `json:"items"`
	NextCursor string                 `json:"next_cursor"`
	Scanned    int                    `json:"scanned"`
	Notes      []string               `json:"notes"`
}

func validateProseSearchInput(in NovelProseSearchInput) error {
	if !slices.Contains([]string{"working", "published"}, in.Source) || !utf8.ValidString(in.Query) || strings.TrimSpace(in.Query) == "" || utf8.RuneCountInString(in.Query) > 120 || (in.After != "" && !lowerHex32(in.After)) {
		return domain.ErrValidation
	}
	return nil
}

// ProseSearch is a bounded read-only POST. Queries and prose never enter a URL,
// audit event, receipt, index, task, or model input. Saved control documents are
// scanned independently of the current planning hierarchy.
func (s *NovelChapters) ProseSearch(ctx context.Context, actor, bookID string, in NovelProseSearchInput) (NovelProseSearchPage, error) {
	out := NovelProseSearchPage{BookID: bookID, Source: in.Source, Query: in.Query, Items: []NovelProseSearchItem{}, Notes: []string{}}
	if !lowerHex32(bookID) {
		return out, domain.ErrValidation
	}
	if err := validateProseSearchInput(in); err != nil {
		return out, err
	}
	tx, a, book, err := (&NovelPlanning{IP: s.IP}).begin(ctx, actor, bookID, true)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	if in.After != "" {
		var id string
		err = tx.QueryRowContext(ctx, "SELECT id FROM resources WHERE id=? AND project_id=? AND branch_id=? AND kind='novel_chapter_control'", in.After, book.ProjectID, book.BranchID).Scan(&id)
		if err == sql.ErrNoRows {
			return out, domain.ErrForbidden
		}
		if err != nil {
			return out, err
		}
	}
	rows, err := tx.QueryContext(ctx, "SELECT id,name FROM resources WHERE project_id=? AND branch_id=? AND kind='novel_chapter_control' AND id>? ORDER BY id LIMIT 6", book.ProjectID, book.BranchID, in.After)
	if err != nil {
		return out, err
	}
	type controlRow struct{ id, name string }
	controls := []controlRow{}
	for rows.Next() {
		var row controlRow
		if err = rows.Scan(&row.id, &row.name); err != nil {
			rows.Close()
			return out, err
		}
		controls = append(controls, row)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	last := ""
	pageBytes := 2048
	for i, row := range controls {
		if i == proseSearchPageSize {
			out.NextCursor = last
			break
		}
		item := NovelProseSearchItem{ControlID: row.id, Origin: "unavailable", Matches: []NovelProseMatch{}}
		const prefix = "章节工作稿 · "
		if strings.HasPrefix(row.name, prefix) && lowerHex32(strings.TrimPrefix(row.name, prefix)) {
			item.ChapterID = strings.TrimPrefix(row.name, prefix)
			err = s.proseSearchItemTx(ctx, tx, a, book, in, &item)
		} else {
			err = domain.ErrNotReady
		}
		if err != nil {
			if !proseSearchUnavailable(err) {
				return out, err
			}
			item.Origin = "unavailable"
			item.Matches = []NovelProseMatch{}
			item.TotalMatches = 0
			item.Truncated = false
			item.Reason = "已保存章稿或其完整来源不可读，未将其计作没有命中。"
		}
		raw, e := json.Marshal(item)
		if e != nil {
			return out, e
		}
		if pageBytes+len(raw) > proseSearchPageBytes {
			out.NextCursor = last
			break
		}
		out.Items = append(out.Items, item)
		out.Scanned++
		last = row.id
		pageBytes += len(raw) + 1
	}
	if out.NextCursor != "" {
		out.Notes = append(out.Notes, "仍有现存章稿尚未扫描；当前结果不代表整书查找已经完成。")
	}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	return out, nil
}

func proseSearchUnavailable(err error) bool {
	// Authorization, cancellation and database errors remain request failures.
	return errors.Is(err, domain.ErrNotReady) || errors.Is(err, domain.ErrConflict) || errors.Is(err, domain.ErrValidation) || errors.Is(err, domain.ErrMaterialExpired) || errors.Is(err, sql.ErrNoRows)
}

func (s *NovelChapters) proseSearchItemTx(ctx context.Context, tx *sql.Tx, a domain.Actor, book NovelBook, in NovelProseSearchInput, out *NovelProseSearchItem) error {
	text, err := s.savedProseTx(ctx, tx, a, book, in.Source, out)
	if err != nil {
		return err
	}
	if out.Origin != "unavailable" {
		out.Matches, out.TotalMatches = proseSearchMatches(text, in.Query)
		out.Truncated = out.TotalMatches > len(out.Matches)
	}
	return nil
}

// Read one saved narrative under the caller's authority snapshot. Working
// sources prefer this actor's draft; public fallbacks require the full release.
func (s *NovelChapters) savedProseTx(ctx context.Context, tx *sql.Tx, a domain.Actor, book NovelBook, choice string, out *NovelProseSearchItem) (string, error) {
	var head, hash string
	var revision int64
	if err := tx.QueryRowContext(ctx, "SELECT head_revision,content_hash,revision FROM resources WHERE id=? AND project_id=? AND branch_id=? AND kind='novel_chapter_control'", out.ControlID, book.ProjectID, book.BranchID).Scan(&head, &hash, &revision); err != nil {
		return "", err
	}
	var count int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM resources WHERE project_id=? AND branch_id=? AND kind='novel_chapter_control' AND name=?", book.ProjectID, book.BranchID, chapterName("章节工作稿", out.ChapterID)).Scan(&count); err != nil {
		return "", err
	}
	if count != 1 {
		return "", domain.ErrNotReady
	}
	private := false
	if choice == "working" {
		var size int
		var storedRevision int64
		err := tx.QueryRowContext(ctx, "SELECT revision,OCTET_LENGTH(body) FROM resource_drafts WHERE resource_id=? AND actor_id=?", out.ControlID, a.ID).Scan(&storedRevision, &size)
		if err != nil && err != sql.ErrNoRows {
			return "", err
		}
		if err == nil && storedRevision > 0 {
			if size > proseSearchDocumentBytes {
				out.Reason = "私人章稿超过本次单章读取容量，未截断查找。"
				return "", nil
			}
			private = true
		}
	}
	source := "published"
	if private {
		source = "draft"
	} else {
		if head == "" {
			out.Reason = "尚无当前作者已保存的章稿或正式采用正文，未创建默认章稿。"
			return "", nil
		}
		if err := s.validateProsePublishedTx(ctx, tx, book, out.ControlID, out.ChapterID, head, hash); err != nil {
			return "", err
		}
	}
	chapter := NovelExportChapter{ID: out.ChapterID}
	text, err := (&NovelExport{IP: s.IP}).chapterTx(ctx, tx, a, book, source, &chapter)
	if err != nil {
		return "", err
	}
	if chapter.ResourceID != out.ControlID {
		return "", domain.ErrNotReady
	}
	out.ChapterTitle = chapter.Title
	out.SourceHash = chapter.Hash
	out.Revision = chapter.Revision
	out.Reason = chapter.Warning
	if !chapter.Included {
		return "", nil
	}
	if !utf8.ValidString(text) {
		return "", domain.ErrNotReady
	}
	out.Origin = "published"
	if private {
		out.Origin = "private"
	}
	out.Characters = utf8.RuneCountInString(text)
	return text, nil
}

// Check the actual immutable publication before using the shared exporter. It
// must declare both control and body; matching row heads alone are insufficient.
func (s *NovelChapters) validateProsePublishedTx(ctx context.Context, tx *sql.Tx, book NovelBook, control, chapterID, head, hash string) error {
	if !lowerHex32(head) || !storyLedgerHash(hash) {
		return domain.ErrNotReady
	}
	var count int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM artifact_releases WHERE id=? AND project_id=?", head, book.ProjectID).Scan(&count); err != nil {
		return err
	}
	if count != 1 {
		return domain.ErrNotReady
	}
	release, err := releaseTx(ctx, tx, book.ProjectID, head)
	if err != nil {
		// This error is known to come from decoding stored release content.
		// Other errors, including authority, SQL and cancellation, stay intact.
		var syntax *json.SyntaxError
		var fieldType *json.UnmarshalTypeError
		if errors.As(err, &syntax) || errors.As(err, &fieldType) {
			return domain.ErrNotReady
		}
		return err
	}
	if release.Status != "PUBLISHED" || release.Manifest.BranchID != book.BranchID {
		return domain.ErrNotReady
	}
	refs := map[string]domain.ArtifactRef{}
	for _, ref := range release.Manifest.Artifacts {
		if _, dup := refs[ref.ResourceID]; dup {
			return domain.ErrNotReady
		}
		refs[ref.ResourceID] = ref
	}
	ref, exists := refs[control]
	if !exists || ref.Hash != hash || ref.Size < 1 || ref.Size > proseSearchDocumentBytes {
		return domain.ErrNotReady
	}
	raw, err := s.proseSearchBlob(ctx, book.ProjectID, ref)
	if err != nil {
		return err
	}
	doc, err := novel.DecodeChapter(raw)
	if err != nil {
		return err
	}
	if doc.ChapterID != chapterID {
		return domain.ErrNotReady
	}
	bodyRef, exists := refs[doc.BodyID]
	if !exists || bodyRef.Hash != digest(doc.Text) || bodyRef.Size < 0 || bodyRef.Size > proseSearchDocumentBytes {
		return domain.ErrNotReady
	}
	var kind, branch, name, bodyHead, bodyHash string
	err = tx.QueryRowContext(ctx, "SELECT kind,branch_id,name,head_revision,content_hash FROM resources WHERE id=? AND project_id=?", doc.BodyID, book.ProjectID).Scan(&kind, &branch, &name, &bodyHead, &bodyHash)
	if err == sql.ErrNoRows {
		return domain.ErrNotReady
	}
	if err != nil {
		return err
	}
	if kind != "novel_chapter_body" || branch != book.BranchID || name != chapterName("章节正文", chapterID) || bodyHead != head || bodyHash != bodyRef.Hash {
		return domain.ErrNotReady
	}
	body, err := s.proseSearchBlob(ctx, book.ProjectID, bodyRef)
	if err != nil {
		return err
	}
	if string(body) != doc.Text {
		return domain.ErrNotReady
	}
	return nil
}
func (s *NovelChapters) proseSearchBlob(ctx context.Context, project string, ref domain.ArtifactRef) ([]byte, error) {
	raw, err := s.IP.Blobs.ReadObject(ctx, project, ref.Hash)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil || int64(len(raw)) != ref.Size || digest(string(raw)) != ref.Hash {
		return nil, domain.ErrNotReady
	}
	return raw, nil
}

// Literal, case-sensitive and non-overlapping. Saved evidence uses Unicode
// codepoints; conversion to textarea UTF-16 belongs to the editor, not storage.
func proseSearchMatches(text, query string) ([]NovelProseMatch, int) {
	matches := []NovelProseMatch{}
	if query == "" {
		return matches, 0
	}
	total := strings.Count(text, query)
	if total == 0 {
		return matches, 0
	}
	runes := []rune(text)
	queryLength := utf8.RuneCountInString(query)
	byteAt, runeAt := 0, 0
	for len(matches) < min(total, 50) {
		relative := strings.Index(text[byteAt:], query)
		if relative < 0 {
			break
		}
		byteStart := byteAt + relative
		start := runeAt + utf8.RuneCountInString(text[byteAt:byteStart])
		end := start + queryLength
		matches = append(matches, NovelProseMatch{Start: start, End: end, Quote: query, Before: string(runes[max(0, start-32):start]), After: string(runes[end:min(len(runes), end+32)])})
		byteAt = byteStart + len(query)
		runeAt = end
	}
	return matches, total
}
