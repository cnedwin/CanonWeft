package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"

	"novelstudio/internal/domain"
	"novelstudio/internal/novel"
)

type NovelChapters struct{ IP *IP }
type NovelChapterDraft struct {
	HistoryReason string                `json:"history_reason,omitempty"`
	Resource      domain.Resource       `json:"resource"`
	Revision      int64                 `json:"revision"`
	BaseHead      string                `json:"base_head"`
	Hash          string                `json:"hash"`
	Document      novel.ChapterDocument `json:"document"`
	Ready         bool                  `json:"ready"`
	Reason        string                `json:"reason"`
	Notes         []string              `json:"notes,omitempty"`
}

func chapterName(kind, id string) string { return kind + " · " + id }

func (s *NovelChapters) Open(ctx context.Context, actor, bookID, chapterID string) (domain.Resource, error) {
	if !lowerHex32(chapterID) {
		return domain.Resource{}, domain.ErrValidation
	}
	tx, a, book, err := (&NovelPlanning{IP: s.IP}).begin(ctx, actor, bookID, true)
	if err != nil {
		return domain.Resource{}, err
	}
	defer tx.Rollback()
	var id string
	err = tx.QueryRowContext(ctx, "SELECT id FROM resources WHERE project_id=? AND branch_id=? AND kind='novel_chapter_control' AND name=?", book.ProjectID, book.BranchID, chapterName("章节工作稿", chapterID)).Scan(&id)
	if err == nil {
		return resourceTx(ctx, tx, book.ProjectID, id)
	}
	if err != sql.ErrNoRows {
		return domain.Resource{}, err
	}
	// Existing prose must remain reachable when its planning or references need
	// review. Only creation needs a current detail; generation/publishing retain
	// their own current-basis validation.
	check, err := newPlanningCheck(ctx, tx, s.IP, book.ProjectID, book.BranchID)
	if err != nil {
		return domain.Resource{}, err
	}
	if _, _, err = check.current("detail", chapterID); err != nil {
		return domain.Resource{}, err
	}
	var control domain.Resource
	for _, kind := range []string{"novel_chapter_control", "novel_chapter_body"} {
		name := "章节工作稿"
		if kind == "novel_chapter_body" {
			name = "章节正文"
		}
		r := domain.Resource{ID: NewID(), BranchID: book.BranchID, Kind: kind, Name: chapterName(name, chapterID), Revision: 1}
		if _, err = tx.ExecContext(ctx, "INSERT INTO resources(id,project_id,branch_id,name,kind) VALUES(?,?,?,?,?)", r.ID, book.ProjectID, book.BranchID, r.Name, kind); err != nil {
			return control, err
		}
		if err = audit(ctx, tx, a, "resource.create", r.ID); err != nil {
			return control, err
		}
		if err = captureMetadataSourceTx(ctx, tx, book.ProjectID, "resources", r.ID); err != nil {
			return control, err
		}
		if kind == "novel_chapter_control" {
			control = r
		}
	}
	return control, tx.Commit()
}

func (s *NovelChapters) draftTx(ctx context.Context, tx *sql.Tx, a domain.Actor, book NovelBook, id string) (NovelChapterDraft, error) {
	var out NovelChapterDraft
	r, err := resourceTx(ctx, tx, book.ProjectID, id)
	if err != nil {
		return out, err
	}
	if r.BranchID != book.BranchID || r.Kind != "novel_chapter_control" {
		return out, domain.ErrForbidden
	}
	const prefix = "章节工作稿 · "
	if len(r.Name) < len(prefix) || r.Name[:len(prefix)] != prefix {
		return out, domain.ErrNotReady
	}
	chapterID := r.Name[len(prefix):]
	if !lowerHex32(chapterID) {
		return out, domain.ErrNotReady
	}
	stored, err := resourceDraftTx(ctx, tx, a.ID, book.ProjectID, id)
	if err != nil {
		return out, err
	}
	out = NovelChapterDraft{Resource: r, Revision: stored.Revision, BaseHead: stored.BaseHead}
	if stored.Revision > 0 {
		out.Document, err = novel.DecodeChapter([]byte(stored.Text))
		if err != nil {
			return out, err
		}
		out.Hash = digest(stored.Text)
	} else if r.Head != "" {
		raw, e := s.IP.Blobs.ReadObject(ctx, book.ProjectID, r.Hash)
		if e != nil {
			return out, e
		}
		out.Document, err = novel.DecodeChapter(raw)
		if err != nil {
			return out, err
		}
		out.Hash, out.BaseHead = digest(string(raw)), r.Head
	} else {
		check, e := newPlanningCheck(ctx, tx, s.IP, book.ProjectID, book.BranchID)
		if e != nil {
			return out, e
		}
		detail, plan, e := check.current("detail", chapterID)
		if e != nil {
			return out, e
		}
		var body string
		if e = tx.QueryRowContext(ctx, "SELECT id FROM resources WHERE project_id=? AND branch_id=? AND kind='novel_chapter_body' AND name=?", book.ProjectID, book.BranchID, chapterName("章节正文", chapterID)).Scan(&body); e != nil {
			return out, e
		}
		out.Document = novel.ChapterDocument{Schema: 1, ChapterID: chapterID, BodyID: body, Detail: planPin(detail), SettingsRevision: check.settingsRevision, SettingsHash: check.settingsHash, IPHead: check.head, Title: plan.Title}
		// Story position is author-editable, not a fabricated calendar timestamp.
		_, outline, e := check.current("book", "")
		if e != nil {
			return out, e
		}
		position := int64(0)
		for _, volume := range outline.Volumes {
			_, v, e := check.current("volume", volume.ID)
			if e != nil {
				return out, e
			}
			for _, chapter := range v.Chapters {
				position++
				if chapter.ID == chapterID {
					out.Document.StoryTime = position
				}
			}
		}
		raw, _ := json.Marshal(out.Document)
		out.Hash = digest(string(raw))
		out.BaseHead = r.Head
	}
	if out.Document.ChapterID != chapterID {
		return out, domain.ErrNotReady
	}
	if out.BaseHead != r.Head {
		out.Reason = "本章已存在更新的正式版本，请核对后更新写作依据。"
	} else if err = s.validateDocumentTx(ctx, tx, book, out.Document); err != nil {
		out.Reason = err.Error()
	} else {
		out.Ready = true
	}
	return out, nil
}

func (s *NovelChapters) validateDocumentTx(ctx context.Context, tx *sql.Tx, book NovelBook, d novel.ChapterDocument) error {
	if err := d.Validate(); err != nil {
		return err
	}
	if d.Unplanned {
		return fmt.Errorf("%w：导入私稿尚未确认规划，可以手动编辑；续写和发布前请先确认细纲并更新写作依据", domain.ErrNotReady)
	}
	check, err := newPlanningCheck(ctx, tx, s.IP, book.ProjectID, book.BranchID)
	if err != nil {
		return err
	}
	if err = check.validateSettings(novel.PlanningDocument{SettingsRevision: d.SettingsRevision, SettingsHash: d.SettingsHash, IPHead: d.IPHead}); err != nil {
		return err
	}
	if d.SettingsRevision != check.settingsRevision {
		var raw string
		if err = tx.QueryRowContext(ctx, "SELECT payload FROM novel_settings_versions WHERE novel_id=? AND revision=?", book.ID, d.SettingsRevision).Scan(&raw); err != nil {
			return err
		}
		var old novel.Setup
		if json.Unmarshal([]byte(raw), &old) != nil {
			return domain.ErrNotReady
		}
		for _, change := range novel.SettingsChanges(old, check.setup) {
			if change.Scope == "generation" {
				return fmt.Errorf("%w：写作%s已变化，请更新本章依据", domain.ErrConflict, change.Label)
			}
		}
	}
	r, _, err := check.current("detail", d.ChapterID)
	if err != nil {
		return err
	}
	if planPin(r) != d.Detail {
		return fmt.Errorf("%w：细纲已改变，请保留正文并重新核对依据", domain.ErrConflict)
	}
	body, err := resourceTx(ctx, tx, book.ProjectID, d.BodyID)
	if err != nil {
		return err
	}
	if body.Kind != "novel_chapter_body" || body.BranchID != book.BranchID || body.Name != chapterName("章节正文", d.ChapterID) {
		return domain.ErrForbidden
	}
	if d.Perspective != "" {
		snapshot, e := snapshotTx(ctx, tx, book.ProjectID, check.head)
		if e != nil {
			return e
		}
		found := false
		for _, entity := range snapshot.Entities {
			if entity.ID == d.Perspective && entity.Kind == "character" && entity.Status != "disabled" {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("%w：视角人物尚未确认或已停用", domain.ErrConflict)
		}
	}
	_, err = s.IP.chapterResearchInputsTx(ctx, tx, book, d)
	return err
}

func (s *NovelChapters) Read(ctx context.Context, actor, bookID, id string) (NovelChapterDraft, error) {
	tx, a, book, err := (&NovelPlanning{IP: s.IP}).begin(ctx, actor, bookID, false)
	if err != nil {
		return NovelChapterDraft{}, err
	}
	defer tx.Rollback()
	return s.draftTx(ctx, tx, a, book, id)
}

func (s *NovelChapters) Save(ctx context.Context, actor, bookID, id string, in NovelChapterDraft) (NovelChapterDraft, error) {
	tx, a, book, err := (&NovelPlanning{IP: s.IP}).begin(ctx, actor, bookID, true)
	if err != nil {
		return in, err
	}
	defer tx.Rollback()
	out, err := s.saveTx(ctx, tx, a, book, id, in)
	if err != nil {
		return out, err
	}
	return out, tx.Commit()
}

func (s *NovelChapters) saveTx(ctx context.Context, tx *sql.Tx, a domain.Actor, book NovelBook, id string, in NovelChapterDraft) (NovelChapterDraft, error) {
	if in.HistoryReason != "" && in.HistoryReason != "before_restore" && in.HistoryReason != "before_replace" {
		return in, domain.ErrValidation
	}
	if err := in.Document.Validate(); err != nil {
		return in, err
	}
	old, err := s.draftTx(ctx, tx, a, book, id)
	if err != nil {
		tx.Rollback()
		return in, err
	}
	if old.Document.ChapterID != in.Document.ChapterID || old.Document.BodyID != in.Document.BodyID || old.Revision != in.Revision || old.BaseHead != in.BaseHead {
		tx.Rollback()
		return in, domain.ErrConflict
	}
	if old.Document.WriterGenerationID != "" && in.Document.WriterGenerationID == "" {
		tx.Rollback()
		return in, domain.ErrConflict
	}
	if in.Document.WriterGenerationID != "" {
		g, e := generationTx(ctx, tx, book.ProjectID, in.Document.WriterGenerationID)
		if e != nil || g.ActorID != a.ID || g.BranchID != book.BranchID || g.Frozen.Role != "writer" || g.Frozen.NovelChapter == nil || g.Frozen.NovelChapter.ResourceID != id || g.State != "SUCCEEDED" || g.Cancelled {
			tx.Rollback()
			return in, domain.ErrConflict
		}
		if old.Document.WriterGenerationID != g.ID {
			if g.Frozen.RecipeRunID != "" {
				if e = (&Generations{IP: s.IP}).validateRecipeChildTx(ctx, tx, g, true); e != nil {
					tx.Rollback()
					return in, e
				}
			}
			if e = s.IP.validateNovelChapterSourceTx(ctx, tx, g); e != nil {
				tx.Rollback()
				return in, e
			}
		}
	}
	// Stale prose remains editable; only an explicit basis refresh can replace
	// authority pins, and generation/publication recheck them against current state.
	if old.Document.Unplanned != in.Document.Unplanned || !reflect.DeepEqual(old.Document.Detail, in.Document.Detail) || old.Document.SettingsRevision != in.Document.SettingsRevision || old.Document.SettingsHash != in.Document.SettingsHash || old.Document.IPHead != in.Document.IPHead {
		if err = s.validateDocumentTx(ctx, tx, book, in.Document); err != nil {
			tx.Rollback()
			return in, err
		}
	}
	in.Ready, in.Reason, in.Notes = true, "", nil
	if e := s.validateDocumentTx(ctx, tx, book, in.Document); e != nil {
		in.Ready, in.Reason = false, e.Error()
	}
	raw, _ := json.Marshal(in.Document)
	draft, err := s.IP.saveResourceDraftTx(ctx, tx, a, book.ProjectID, id, ResourceDraft{Revision: in.Revision, BaseHead: in.BaseHead, Text: string(raw), Policy: &domain.KnowledgePolicy{Space: "working", Visibility: "author"}, historyReason: in.HistoryReason, historySource: &chapterHistorySource{old.Document, old.Revision, old.Hash, old.BaseHead}})
	if err != nil {
		return in, err
	}
	in.Revision = draft.Revision
	in.Hash = digest(string(raw))
	in.HistoryReason = ""
	in.Resource = old.Resource
	return in, nil
}

func (s *NovelChapters) Basis(ctx context.Context, actor, bookID, id string, revision int64, reviewedHash ...string) (NovelChapterDraft, error) {
	if len(reviewedHash) > 1 {
		return NovelChapterDraft{}, domain.ErrValidation
	}
	tx, a, book, err := (&NovelPlanning{IP: s.IP}).begin(ctx, actor, bookID, true)
	if err != nil {
		return NovelChapterDraft{}, err
	}
	defer tx.Rollback()
	d, err := s.draftTx(ctx, tx, a, book, id)
	if err != nil {
		return d, err
	}
	if d.Revision != revision {
		return d, domain.ErrConflict
	}
	if len(reviewedHash) == 1 && reviewedHash[0] != "" {
		preview, err := s.basisPreviewTx(ctx, tx, book, d)
		if err != nil {
			return d, err
		}
		if !preview.CanRefresh || preview.BasisHash != reviewedHash[0] {
			return d, fmt.Errorf("%w：章稿或规划依据已变化，请重新查看依据对照", domain.ErrConflict)
		}
	}
	check, err := newPlanningCheck(ctx, tx, s.IP, book.ProjectID, book.BranchID)
	if err != nil {
		return d, err
	}
	r, _, err := check.current("detail", d.Document.ChapterID)
	if err != nil {
		return d, err
	}
	d.Document.Detail = planPin(r)
	d.Document.Unplanned = false
	d.Document.SettingsRevision = check.settingsRevision
	d.Document.SettingsHash = check.settingsHash
	d.Document.IPHead = check.head
	d.Ready = false
	d.Reason = "写作依据已更新，请保留原稿并核对与新细纲的差异。"
	return d, nil
}
