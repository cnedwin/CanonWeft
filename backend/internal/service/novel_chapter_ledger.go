package service

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"

	"novelstudio/internal/domain"
	"novelstudio/internal/novel"
)

type NovelLedgerDraft struct {
	Resource domain.Resource     `json:"resource"`
	Revision int64               `json:"revision"`
	BaseHead string              `json:"base_head"`
	Document novel.ChapterLedger `json:"document"`
	Ready    bool                `json:"ready"`
	Reason   string              `json:"reason"`
}

func chapterSource(bookID string, d NovelChapterDraft) domain.NovelChapterSource {
	return domain.NovelChapterSource{BookID: bookID, ResourceID: d.Resource.ID, Revision: d.Revision, Hash: d.Hash, BaseHead: d.BaseHead}
}

func (s *NovelChapters) OpenLedger(ctx context.Context, actor, bookID, chapter string) (NovelLedgerDraft, error) {
	tx, a, book, err := (&NovelPlanning{IP: s.IP}).begin(ctx, actor, bookID, true)
	if err != nil {
		return NovelLedgerDraft{}, err
	}
	defer tx.Rollback()
	d, err := s.draftTx(ctx, tx, a, book, chapter)
	if err != nil {
		return NovelLedgerDraft{}, err
	}
	var id string
	err = tx.QueryRowContext(ctx, "SELECT id FROM resources WHERE project_id=? AND branch_id=? AND kind='novel_chapter_ledger' AND name=?", book.ProjectID, book.BranchID, chapterName("剧情账本", d.Document.ChapterID)).Scan(&id)
	if err == sql.ErrNoRows {
		id = NewID()
		_, err = tx.ExecContext(ctx, "INSERT INTO resources(id,project_id,branch_id,name,kind) VALUES(?,?,?,?,'novel_chapter_ledger')", id, book.ProjectID, book.BranchID, chapterName("剧情账本", d.Document.ChapterID))
		if err == nil {
			err = audit(ctx, tx, a, "resource.create", id)
		}
		if err == nil {
			err = captureMetadataSourceTx(ctx, tx, book.ProjectID, "resources", id)
		}
	}
	if err != nil {
		return NovelLedgerDraft{}, err
	}
	if err = tx.Commit(); err != nil {
		return NovelLedgerDraft{}, err
	}
	return s.ReadLedger(ctx, actor, bookID, chapter)
}

func (s *NovelChapters) ledgerTx(ctx context.Context, tx *sql.Tx, a domain.Actor, book NovelBook, chapter string) (NovelLedgerDraft, error) {
	var out NovelLedgerDraft
	d, err := s.draftTx(ctx, tx, a, book, chapter)
	if err != nil {
		return out, err
	}
	var id string
	if err = tx.QueryRowContext(ctx, "SELECT id FROM resources WHERE project_id=? AND branch_id=? AND kind='novel_chapter_ledger' AND name=?", book.ProjectID, book.BranchID, chapterName("剧情账本", d.Document.ChapterID)).Scan(&id); err != nil {
		return out, err
	}
	out.Resource, err = resourceTx(ctx, tx, book.ProjectID, id)
	if err != nil {
		return out, err
	}
	stored, err := resourceDraftTx(ctx, tx, a.ID, book.ProjectID, id)
	if err != nil {
		return out, err
	}
	out.Revision, out.BaseHead = stored.Revision, stored.BaseHead
	if stored.Revision > 0 {
		decoder := json.NewDecoder(bytes.NewBufferString(stored.Text))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&out.Document) != nil || decoder.Decode(&struct{}{}) != io.EOF {
			return out, domain.ErrNotReady
		}
	} else if out.Resource.Head != "" {
		raw, e := s.IP.Blobs.ReadObject(ctx, book.ProjectID, out.Resource.Hash)
		if e != nil {
			return out, e
		}
		var published chapterReleaseLedger
		if json.Unmarshal(raw, &published) != nil || published.Schema != 1 {
			return out, domain.ErrNotReady
		}
		out.Document, out.BaseHead = published.Ledger, out.Resource.Head
	} else {
		out.BaseHead = out.Resource.Head
		out.Document = novel.ChapterLedger{Schema: 1, Source: chapterSource(book.ID, d), ChapterID: d.Document.ChapterID, BodyID: d.Document.BodyID, TextHash: digest(d.Document.Text), LedgerOutput: novel.LedgerOutput{Summary: "尚未提取剧情变化", Entries: []novel.LedgerEntry{}, Notes: []string{}}}
	}
	if out.Document.Source != chapterSource(book.ID, d) || !d.Ready {
		out.Reason = "正文或写作依据已变化，请保留旧账本并重新提取核对"
	} else {
		out.Ready = true
	}
	return out, nil
}

func (s *NovelChapters) ReadLedger(ctx context.Context, actor, bookID, chapter string) (NovelLedgerDraft, error) {
	tx, a, book, err := (&NovelPlanning{IP: s.IP}).begin(ctx, actor, bookID, false)
	if err != nil {
		return NovelLedgerDraft{}, err
	}
	defer tx.Rollback()
	return s.ledgerTx(ctx, tx, a, book, chapter)
}

func (s *NovelChapters) validateLedgerTx(ctx context.Context, tx *sql.Tx, book NovelBook, draft NovelChapterDraft, doc *novel.ChapterLedger) error {
	if !draft.Ready || doc.Source != chapterSource(book.ID, draft) || doc.ChapterID != draft.Document.ChapterID || doc.BodyID != draft.Document.BodyID || doc.TextHash != digest(draft.Document.Text) {
		return fmt.Errorf("%w：账本不再对应当前章稿", domain.ErrConflict)
	}
	if err := novel.ValidateEditedLedger(doc, draft.Document.Text); err != nil {
		return err
	}
	snapshot, err := snapshotTx(ctx, tx, book.ProjectID, draft.Document.IPHead)
	if err != nil {
		return err
	}
	entities := map[string]bool{}
	for _, entity := range snapshot.Entities {
		if entity.Status != "disabled" {
			entities[entity.ID] = true
		}
	}
	for _, entry := range doc.Entries {
		for _, id := range append([]string{entry.SubjectID, entry.HolderID, entry.ObjectID}, entry.KnownBy...) {
			if id != "" && !entities[id] {
				return fmt.Errorf("%w：账本引用了尚未确认或不存在的身份，请保留名称并重新关联", domain.ErrValidation)
			}
		}
		if entry.Visibility == "restricted" {
			for _, id := range entry.KnownBy {
				if id == "" {
					return domain.ErrValidation
				}
			}
		}
	}
	return nil
}

func (s *NovelChapters) SaveLedger(ctx context.Context, actor, bookID, chapter string, in NovelLedgerDraft) (NovelLedgerDraft, error) {
	tx, a, book, err := (&NovelPlanning{IP: s.IP}).begin(ctx, actor, bookID, true)
	if err != nil {
		return in, err
	}
	defer tx.Rollback()
	old, err := s.ledgerTx(ctx, tx, a, book, chapter)
	if err != nil {
		return in, err
	}
	if old.Revision != in.Revision || old.BaseHead != in.BaseHead {
		return in, domain.ErrConflict
	}
	d, err := s.draftTx(ctx, tx, a, book, chapter)
	if err != nil {
		return in, err
	}
	if err = s.validateLedgerTx(ctx, tx, book, d, &in.Document); err != nil {
		return in, err
	}
	if old.Document.GenerationID != "" && in.Document.GenerationID == "" {
		return in, domain.ErrConflict
	}
	if in.Document.GenerationID != "" {
		g, e := generationTx(ctx, tx, book.ProjectID, in.Document.GenerationID)
		if e != nil || g.ActorID != actor || g.Frozen.Role != "bookkeeper" || g.Frozen.NovelChapter == nil || *g.Frozen.NovelChapter != in.Document.Source || g.State != "SUCCEEDED" || g.Cancelled {
			return in, domain.ErrConflict
		}
		if err = s.IP.validateNovelChapterSourceTx(ctx, tx, g); err != nil {
			return in, err
		}
	}
	tx.Rollback()
	raw, _ := json.Marshal(in.Document)
	saved, err := s.IP.SaveResourceDraft(ctx, a, book.ProjectID, old.Resource.ID, ResourceDraft{Revision: in.Revision, BaseHead: in.BaseHead, Text: string(raw), Policy: &domain.KnowledgePolicy{Space: "working", Visibility: "author"}})
	if err != nil {
		return in, err
	}
	in.Resource, in.Revision, in.Ready, in.Reason = old.Resource, saved.Revision, true, ""
	return in, nil
}

func (s *NovelChapterWriter) LedgerSuggestion(ctx context.Context, actor, bookID, chapter, task string) (NovelLedgerDraft, error) {
	chapters := &NovelChapters{IP: s.Generations.IP}
	tx, a, book, err := (&NovelPlanning{IP: chapters.IP}).begin(ctx, actor, bookID, true)
	if err != nil {
		return NovelLedgerDraft{}, err
	}
	// Check the chapter pin before reading the old knowledge context: publishing
	// new memories can invalidate that context too, but this candidate is already
	// unusable because the author has changed/rebased the chapter itself.
	prior, err := generationTx(ctx, tx, book.ProjectID, task)
	if err == nil && (prior.ActorID != actor || prior.Frozen.Role != "bookkeeper" || prior.Frozen.NovelChapter == nil || prior.Frozen.NovelChapter.BookID != bookID || prior.Frozen.NovelChapter.ResourceID != chapter) {
		err = domain.ErrConflict
	}
	if err == nil {
		err = chapters.IP.validateNovelChapterSourceTx(ctx, tx, prior)
	}
	tx.Rollback()
	if err != nil {
		return NovelLedgerDraft{}, err
	}
	g, err := s.Generations.Read(ctx, a, book.ProjectID, task)
	if err != nil {
		return NovelLedgerDraft{}, err
	}
	if g.ActorID != actor || g.BranchID != book.BranchID || g.Frozen.Role != "bookkeeper" || g.Frozen.NovelChapter == nil || g.Frozen.NovelChapter.BookID != bookID || g.Frozen.NovelChapter.ResourceID != chapter || g.State != "SUCCEEDED" || g.Cancelled || g.Output == nil {
		return NovelLedgerDraft{}, domain.ErrConflict
	}
	tx, a, book, err = (&NovelPlanning{IP: chapters.IP}).begin(ctx, actor, bookID, true)
	if err != nil {
		return NovelLedgerDraft{}, err
	}
	defer tx.Rollback()
	if err = chapters.IP.validateNovelChapterSourceTx(ctx, tx, g); err != nil {
		return NovelLedgerDraft{}, err
	}
	d, err := chapters.draftTx(ctx, tx, a, book, chapter)
	if err != nil {
		return NovelLedgerDraft{}, err
	}
	ledger, err := chapters.ledgerTx(ctx, tx, a, book, chapter)
	if err != nil {
		return ledger, err
	}
	output, err := novel.ParseLedgerOutput([]byte(g.Output.Text), d.Document.Text, g.ID)
	if err != nil {
		return ledger, err
	}
	ledger.Document = novel.ChapterLedger{Schema: 1, Source: *g.Frozen.NovelChapter, ChapterID: d.Document.ChapterID, BodyID: d.Document.BodyID, TextHash: digest(d.Document.Text), GenerationID: g.ID, LedgerOutput: output}
	if err = chapters.validateLedgerTx(ctx, tx, book, d, &ledger.Document); err != nil {
		return ledger, err
	}
	ledger.Ready, ledger.Reason = true, ""
	return ledger, nil
}
