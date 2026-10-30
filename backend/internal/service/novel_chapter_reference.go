package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"novelstudio/internal/domain"
	"novelstudio/internal/novel"
)

var ErrChapterReferenceMissing = errors.New("尚无对应的已有章稿；未创建默认内容")

type NovelChapterReferenceItem struct {
	ChapterID string `json:"chapter_id"`
	ControlID string `json:"control_id"`
	Title     string `json:"title"`
	VolumeID  string `json:"volume_id"`
	Volume    string `json:"volume"`
	Reason    string `json:"reason"`
}
type NovelChapterReferenceDirectory struct {
	BookID   string                      `json:"book_id"`
	Items    []NovelChapterReferenceItem `json:"items"`
	Complete bool                        `json:"complete"`
	Notes    []string                    `json:"notes"`
}
type NovelChapterReference struct {
	BookID     string `json:"book_id"`
	ChapterID  string `json:"chapter_id"`
	ControlID  string `json:"control_id"`
	Source     string `json:"source"`
	Origin     string `json:"origin"`
	Title      string `json:"title"`
	Revision   int64  `json:"revision"`
	SourceHash string `json:"source_hash"`
	TextHash   string `json:"text_hash"`
	Characters int    `json:"characters"`
	Text       string `json:"text"`
	Reason     string `json:"reason"`
}

// This directory reads adopted plan bytes for order only. It deliberately
// grants no generation/adoption qualification and does not load chapter prose.
func (s *NovelChapters) ReferenceDirectory(ctx context.Context, actor, bookID string) (NovelChapterReferenceDirectory, error) {
	out := NovelChapterReferenceDirectory{BookID: bookID, Items: []NovelChapterReferenceItem{}, Complete: true, Notes: []string{"目录来自已采用规划的读取快照；不代表当前写作依据有效，也不包含未保存的手改。"}}
	if !lowerHex32(bookID) {
		return out, domain.ErrValidation
	}
	tx, _, book, err := (&NovelPlanning{IP: s.IP}).begin(ctx, actor, bookID, true)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	plan, err := s.referencePlanTx(ctx, tx, book, "book", "")
	if err != nil {
		return out, err
	}
	rows, err := tx.QueryContext(ctx, "SELECT id,name FROM resources WHERE project_id=? AND branch_id=? AND kind='novel_chapter_control' ORDER BY id LIMIT 10001", book.ProjectID, book.BranchID)
	if err != nil {
		return out, err
	}
	controls := map[string][]string{}
	count := 0
	for rows.Next() {
		var id, name string
		if err = rows.Scan(&id, &name); err != nil {
			rows.Close()
			return out, err
		}
		count++
		const prefix = "章节工作稿 · "
		chapter := strings.TrimPrefix(name, prefix)
		if name != chapter && lowerHex32(chapter) {
			controls[chapter] = append(controls[chapter], id)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	if count > 10000 {
		return out, fmt.Errorf("%w：参考目录超过一万份已有章稿，请使用全书正文查找分段回查", domain.ErrValidation)
	}
	seen := map[string]bool{}
	// Reserve space for bounded volume warnings and the response envelope.
	responseBytes := 512 << 10
	for _, volume := range plan.Volumes {
		document, e := s.referencePlanTx(ctx, tx, book, "volume", volume.ID)
		if e != nil {
			if !proseSearchUnavailable(e) {
				return out, e
			}
			out.Complete = false
			out.Notes = append(out.Notes, "分卷“"+volume.Title+"”的已采用目录不可核验，未猜测其章节顺序。")
			continue
		}
		for _, chapter := range document.Chapters {
			if seen[chapter.ID] {
				return out, fmt.Errorf("%w：参考目录存在重复章节身份，请先核对规划", domain.ErrConflict)
			}
			seen[chapter.ID] = true
			if len(out.Items) >= 10000 {
				return out, fmt.Errorf("%w：本次参考目录最多读取一万章，请使用全书正文查找分段回查", domain.ErrValidation)
			}
			item := NovelChapterReferenceItem{ChapterID: chapter.ID, Title: chapter.Title, VolumeID: volume.ID, Volume: volume.Title}
			switch len(controls[chapter.ID]) {
			case 0:
				item.Reason = "尚无已有章稿，不会自动创建"
			case 1:
				item.ControlID = controls[chapter.ID][0]
			default:
				item.Reason = "已有章稿身份存在歧义，未猜选"
				out.Complete = false
			}
			encoded, e := json.Marshal(item)
			if e != nil {
				return out, e
			}
			responseBytes += len(encoded) + 1
			if responseBytes > 2<<20 {
				out.Complete = false
				out.Notes = append(out.Notes, "参考目录达到2MiB读取上限，仅展示已读部分；可用准确章节编号读取其他已有原文。")
				return out, nil
			}
			out.Items = append(out.Items, item)
		}
	}
	if ctx.Err() != nil {
		return out, ctx.Err()
	}
	return out, nil
}

func (s *NovelChapters) referencePlanTx(ctx context.Context, tx *sql.Tx, book NovelBook, kind, scope string) (novel.PlanningDocument, error) {
	rows, err := tx.QueryContext(ctx, "SELECT id FROM resources WHERE project_id=? AND branch_id=? AND kind=? AND name=? ORDER BY id LIMIT 2", book.ProjectID, book.BranchID, novel.PlanKind(kind), planResourceName(kind, scope))
	if err != nil {
		return novel.PlanningDocument{}, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return novel.PlanningDocument{}, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return novel.PlanningDocument{}, err
	}
	if len(ids) != 1 {
		return novel.PlanningDocument{}, fmt.Errorf("%w：没有唯一可读取的已采用目录，请先核对规划", domain.ErrConflict)
	}
	resource, err := resourceTx(ctx, tx, book.ProjectID, ids[0])
	if err != nil {
		return novel.PlanningDocument{}, err
	}
	check := planningCheck{ip: s.IP, ctx: ctx, tx: tx, project: book.ProjectID, branch: book.BranchID}
	doc, err := check.document(resource)
	if ctx.Err() != nil {
		return doc, ctx.Err()
	}
	return doc, err
}

func (s *NovelChapters) ReadReference(ctx context.Context, actor, bookID, chapterID, source string) (NovelChapterReference, error) {
	out := NovelChapterReference{BookID: bookID, ChapterID: chapterID, Source: source}
	if !lowerHex32(bookID) || !lowerHex32(chapterID) || !slices.Contains([]string{"working", "published"}, source) {
		return out, domain.ErrValidation
	}
	tx, a, book, err := (&NovelPlanning{IP: s.IP}).begin(ctx, actor, bookID, true)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, "SELECT id FROM resources WHERE project_id=? AND branch_id=? AND kind='novel_chapter_control' AND name=? ORDER BY id LIMIT 2", book.ProjectID, book.BranchID, chapterName("章节工作稿", chapterID))
	if err != nil {
		return out, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return out, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	if len(ids) == 0 {
		return out, ErrChapterReferenceMissing
	}
	if len(ids) != 1 {
		return out, fmt.Errorf("%w：同一章节对应多个已有章稿，未猜选原文", domain.ErrConflict)
	}
	item := NovelProseSearchItem{ChapterID: chapterID, ControlID: ids[0], Origin: "unavailable"}
	text, err := s.savedProseTx(ctx, tx, a, book, source, &item)
	if err != nil {
		if !proseSearchUnavailable(err) {
			return out, err
		}
		return out, fmt.Errorf("%w：已保存原文或其完整来源不可核验，请重新读取或核对原稿", domain.ErrConflict)
	}
	if item.Origin == "unavailable" {
		return out, fmt.Errorf("%w：%s", domain.ErrConflict, item.Reason)
	}
	if ctx.Err() != nil {
		return out, ctx.Err()
	}
	out.ControlID = item.ControlID
	out.Origin = item.Origin
	out.Title = item.ChapterTitle
	out.Revision = item.Revision
	out.SourceHash = item.SourceHash
	out.TextHash = digest(text)
	out.Characters = item.Characters
	out.Text = text
	out.Reason = item.Reason
	return out, nil
}
