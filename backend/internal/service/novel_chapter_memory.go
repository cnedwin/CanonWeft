package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"reflect"
	"unicode/utf8"

	"novelstudio/internal/domain"
	"novelstudio/internal/memory"
	"novelstudio/internal/novel"
	"novelstudio/internal/retention"
)

// Only reviewed ledger entries are projected. The chapter and full ledger stay
// author-only; making one entry known never makes the whole prose public.
func chapterMemoryProjection(project string, release domain.Release, chapter novel.ChapterDocument, envelope chapterReleaseLedger, ledgerRef domain.ArtifactRef) ([]memory.Item, error) {
	raw, err := json.Marshal(envelope)
	if err != nil || digest(string(raw)) != ledgerRef.Hash || int64(len(raw)) != ledgerRef.Size || !lowerHex32(release.ID) {
		return nil, domain.ErrValidation
	}
	// Evidence validation mutates offsets, so work on a detached document.
	var detached chapterReleaseLedger
	if json.Unmarshal(raw, &detached) != nil {
		return nil, domain.ErrValidation
	}
	ledger := &detached.Ledger
	if ledger.BodyID != chapter.BodyID || ledger.ChapterID != chapter.ChapterID || ledger.TextHash != digest(chapter.Text) {
		return nil, domain.ErrConflict
	}
	if err = novel.ValidateEditedLedger(ledger, chapter.Text); err != nil {
		return nil, err
	}
	var body domain.ArtifactRef
	for _, ref := range release.Manifest.Artifacts {
		if ref.ResourceID == chapter.BodyID {
			body = ref
		}
	}
	if body.Hash != ledger.TextHash {
		return nil, domain.ErrConflict
	}
	items := make([]memory.Item, 0, len(ledger.Entries))
	for _, entry := range ledger.Entries {
		text, err := json.Marshal(entry)
		if err != nil {
			return nil, err
		}
		key := "novel-chapter-memory:" + release.ID + ":" + entry.ID
		item := memory.Item{ID: digest(key)[:32], Revision: digest(key + ":revision")[:32], Project: project, Branch: release.Manifest.BranchID, Kind: entry.Kind, State: "published", Visibility: entry.Visibility, KnownBy: append([]string(nil), entry.KnownBy...), From: entry.StoryTime, RevealAt: max(entry.KnownFrom, chapter.StoryTime), Text: string(text)}
		for _, quote := range entry.Evidence {
			item.Sources = append(item.Sources, memory.Source{Resource: chapter.BodyID, Revision: release.ID, Hash: body.Hash, Start: quote.Start, End: quote.End})
		}
		// Both prose and the reviewed interpretation must still be current.
		item.Sources = append(item.Sources, memory.Source{Resource: ledgerRef.ResourceID, Revision: release.ID, Hash: ledgerRef.Hash, Start: 0, End: utf8.RuneCount(raw)})
		if err = memory.ValidateItem(item); err != nil {
			return nil, domain.ErrValidation
		}
		items = append(items, item)
	}
	return items, nil
}

func (s *IP) chapterMemoryForReleaseTx(ctx context.Context, tx *sql.Tx, project string, release domain.Release) ([]memory.Item, error) {
	var chapter novel.ChapterDocument
	var envelope chapterReleaseLedger
	var ledgerRef domain.ArtifactRef
	found := false
	for _, ref := range release.Manifest.Artifacts {
		r, err := resourceTx(ctx, tx, project, ref.ResourceID)
		if err != nil {
			return nil, err
		}
		if !isNovelChapter(r.Kind) {
			continue
		}
		found = true
		if r.Kind == "novel_chapter_body" {
			continue
		}
		raw, err := s.Blobs.ReadObject(ctx, project, ref.Hash)
		if err != nil || digest(string(raw)) != ref.Hash {
			return nil, domain.ErrNotReady
		}
		if r.Kind == "novel_chapter_control" {
			chapter, err = novel.DecodeChapter(raw)
		} else {
			ledgerRef = ref
			err = json.Unmarshal(raw, &envelope)
		}
		if err != nil {
			return nil, err
		}
	}
	if !found {
		return nil, nil
	}
	return chapterMemoryProjection(project, release, chapter, envelope, ledgerRef)
}

// A narrow declassification proof: accept exactly the deterministic memory
// projection of an already published, reviewed chapter release. Neither callers
// nor hand-edited memory can widen a source's policy by claiming a chapter ID.
func (s *Memory) chapterProjectionPoliciesTx(ctx context.Context, tx *sql.Tx, project, branch string, item memory.Item, memo map[string]memorySourceInfo) ([]domain.KnowledgePolicy, bool, error) {
	if len(item.Sources) == 0 {
		return nil, false, nil
	}
	first := item.Sources[0]
	info, loaded := memo[first.Resource]
	if !loaded {
		if len(memo) >= 8192 {
			return nil, true, domain.ErrContextBudget
		}
		r, err := resourceTx(ctx, tx, project, first.Resource)
		if err != nil {
			return nil, true, err
		}
		if r.Kind != "novel_chapter_body" {
			return nil, false, nil
		}
		raw, err := s.IP.Blobs.ReadObject(ctx, project, r.Hash)
		if err != nil {
			return nil, true, domain.ErrNotReady
		}
		info = memorySourceInfo{Resource: r, Runes: utf8.RuneCount(raw)}
	}
	if info.Resource.Kind != "novel_chapter_body" {
		return nil, false, nil
	}
	if info.Resource.Head != first.Revision || info.Resource.Hash != first.Hash || info.Resource.BranchID != branch {
		return nil, true, domain.ErrConflict
	}
	if info.ChapterProjection == nil {
		r, err := releaseTx(ctx, tx, project, first.Revision)
		if err != nil {
			return nil, true, err
		}
		if r.Status != "PUBLISHED" || r.Manifest.BranchID != branch {
			return nil, true, domain.ErrNotReady
		}
		items, err := s.IP.chapterMemoryForReleaseTx(ctx, tx, project, r)
		if err != nil {
			return nil, true, err
		}
		info.ChapterProjection = map[string]memory.Item{}
		for _, entry := range items {
			info.ChapterProjection[entry.ID] = entry
		}
		memo[first.Resource] = info
	}
	expected, exists := info.ChapterProjection[item.ID]
	if !exists {
		return nil, false, nil
	}
	if !reflect.DeepEqual(item, expected) {
		return nil, true, domain.ErrValidation
	}
	for _, source := range item.Sources {
		pin, exists := memo[source.Resource]
		if !exists {
			if len(memo) >= 8192 {
				return nil, true, domain.ErrContextBudget
			}
			r, err := resourceTx(ctx, tx, project, source.Resource)
			if err != nil {
				return nil, true, err
			}
			raw, err := s.IP.Blobs.ReadObject(ctx, project, r.Hash)
			if err != nil {
				return nil, true, domain.ErrNotReady
			}
			pin = memorySourceInfo{Resource: r, Runes: utf8.RuneCount(raw)}
			memo[source.Resource] = pin
		}
		if pin.Resource.BranchID != branch || pin.Resource.Head != source.Revision || pin.Resource.Hash != source.Hash || source.Start < 0 || source.End > pin.Runes || source.Start >= source.End {
			return nil, true, domain.ErrConflict
		}
	}
	return []domain.KnowledgePolicy{{Space: "evidence", Visibility: item.Visibility, KnownBy: item.KnownBy, RevealAt: item.RevealAt}}, true, nil
}

// Rebase only the exact author's drafts already validated for this release.
// Capturing both sides preserves historical generation inputs for restoration.
func (s *IP) rebasePublishedChapterDraftsTx(ctx context.Context, tx *sql.Tx, project string, release domain.Release) error {
	for _, ref := range release.Manifest.Artifacts {
		r, err := resourceTx(ctx, tx, project, ref.ResourceID)
		if err != nil {
			return err
		}
		if r.Kind != "novel_chapter_control" && r.Kind != "novel_chapter_ledger" {
			continue
		}
		if ref.Origin == nil {
			return domain.ErrValidation
		}
		actor := ref.Origin.ActorID
		draft, err := resourceDraftTx(ctx, tx, actor, project, r.ID)
		if err != nil || draft.Revision < 1 {
			return domain.ErrConflict
		}
		sources := []retention.SourceKey{
			{Table: "resource_drafts", Primary: map[string]string{"resource_id": r.ID, "actor_id": actor}},
			{Table: "resource_policy_drafts", Primary: map[string]string{"resource_id": r.ID, "actor_id": actor}},
		}
		if err = s.captureSourceValuesTx(ctx, tx, project, sources...); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "UPDATE resource_drafts SET base_head=?,revision=revision+1,updated_at=CURRENT_TIMESTAMP(6) WHERE resource_id=? AND actor_id=?", release.ID, r.ID, actor); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "UPDATE resource_policy_drafts SET revision=revision+1 WHERE resource_id=? AND actor_id=?", r.ID, actor); err != nil {
			return err
		}
		if err = s.captureSourceValuesTx(ctx, tx, project, sources...); err != nil {
			return err
		}
	}
	return nil
}
