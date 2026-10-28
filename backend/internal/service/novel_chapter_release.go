package service

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strings"

	"novelstudio/internal/domain"
	"novelstudio/internal/novel"
)

type NovelChapterReleaseInput struct {
	Revision       int64  `json:"revision"`
	Hash           string `json:"hash"`
	LedgerRevision int64  `json:"ledger_revision"`
	OperationID    string `json:"operation_id"`
}

func (s *NovelChapters) ConfirmRelease(ctx context.Context, actor, bookID, chapter, releaseID, hash string, accept bool) (domain.Release, error) {
	if !accept {
		return domain.Release{}, domain.ErrValidation
	}
	tx, a, book, err := (&NovelPlanning{IP: s.IP}).begin(ctx, actor, bookID, true)
	if err != nil {
		return domain.Release{}, err
	}
	defer tx.Rollback()
	r, err := resourceTx(ctx, tx, book.ProjectID, chapter)
	if err != nil {
		return domain.Release{}, err
	}
	if r.Kind != "novel_chapter_control" || r.BranchID != book.BranchID {
		return domain.Release{}, domain.ErrForbidden
	}
	release, err := releaseTx(ctx, tx, book.ProjectID, releaseID)
	if err != nil {
		return release, err
	}
	if release.Hash != hash || release.Manifest.BranchID != book.BranchID || len(release.Manifest.Artifacts) != 3 {
		return release, domain.ErrConflict
	}
	if _, err = originArtifact(release, chapter); err != nil {
		return release, err
	}
	tx.Rollback()
	if release.Status == "PUBLISHED" {
		return release, nil
	}
	if release.Status == "PREPARED" {
		if release, err = s.IP.AdoptRelease(ctx, a, book.ProjectID, releaseID); err != nil {
			return release, err
		}
	}
	return s.IP.PublishRelease(ctx, a, book.ProjectID, releaseID)
}

// Publication-only envelope. Private ledger edits never accept these authority
// pins, and every review/adoption rechecks both private drafts under project lock.
type chapterReleaseLedger struct {
	Schema         int                 `json:"schema"`
	ActorID        string              `json:"actor_id"`
	LedgerRevision int64               `json:"ledger_revision"`
	LedgerHash     string              `json:"ledger_hash"`
	LedgerBaseHead string              `json:"ledger_base_head"`
	Ledger         novel.ChapterLedger `json:"ledger"`
}

func isNovelChapter(kind string) bool {
	return kind == "novel_chapter_control" || kind == "novel_chapter_body" || kind == "novel_chapter_ledger"
}

func (s *NovelChapters) LatestRelease(ctx context.Context, actor, bookID, chapter string) (*domain.Release, error) {
	tx, a, book, err := (&NovelPlanning{IP: s.IP}).begin(ctx, actor, bookID, false)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err = s.draftTx(ctx, tx, a, book, chapter); err != nil {
		return nil, err
	}
	var id string
	err = tx.QueryRowContext(ctx, `SELECT r.id FROM artifact_releases r
 JOIN JSON_TABLE(r.manifest, '$.artifacts[*]' COLUMNS(resource_id VARCHAR(32) PATH '$.resource_id')) AS member ON member.resource_id=?
 WHERE r.project_id=? AND r.branch_id=? AND r.created_by=?
 ORDER BY r.created_at DESC,r.id DESC LIMIT 1`, chapter, book.ProjectID, book.BranchID, actor).Scan(&id)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	tx.Rollback()
	release, err := s.IP.ReadRelease(ctx, a, book.ProjectID, id)
	return &release, err
}

// Critic receives confirmed story planning as evidence, never Writer reasoning.
// Exact anchor content participates in its frozen input hash and invalidation.
func (s *IP) appendChapterReviewPlansTx(ctx context.Context, tx *sql.Tx, project string, manifest domain.Manifest, doc *domain.SemanticReviewDocument) error {
	for _, ref := range manifest.Artifacts {
		r, err := resourceTx(ctx, tx, project, ref.ResourceID)
		if err != nil {
			return err
		}
		if r.Kind != "novel_chapter_control" {
			continue
		}
		raw, err := s.Blobs.ReadObject(ctx, project, ref.Hash)
		if err != nil {
			return err
		}
		chapter, err := novel.DecodeChapter(raw)
		if err != nil {
			return err
		}
		materials, err := s.chapterResearchInputsTx(ctx, tx, NovelBook{ProjectID: project, BranchID: manifest.BranchID}, chapter)
		if err != nil {
			return err
		}
		for i, material := range materials {
			key := "chapter-research:" + ref.ResourceID + ":" + material.Dependency.Source.ID
			content, err := json.Marshal(struct {
				Reference novel.ChapterReference `json:"reference"`
				Material  domain.RequiredInput   `json:"material"`
			}{chapter.References[i], material})
			if err != nil {
				return err
			}
			doc.RequiredCoverage = append(doc.RequiredCoverage, key)
			doc.Anchors = append(doc.Anchors, domain.SemanticReviewAnchor{Key: key, TargetKey: key, Text: string(content)})
		}
		check, err := newPlanningCheck(ctx, tx, s, project, manifest.BranchID)
		if err != nil {
			return err
		}
		add := func(resource domain.Resource) error {
			content, err := s.Blobs.ReadObject(ctx, project, resource.Hash)
			if err != nil || digest(string(content)) != resource.Hash {
				return domain.ErrNotReady
			}
			key := "chapter-plan:" + resource.ID
			doc.RequiredCoverage = append(doc.RequiredCoverage, key)
			doc.Anchors = append(doc.Anchors, domain.SemanticReviewAnchor{Key: key, TargetKey: key, Text: string(content)})
			return nil
		}
		detail, plan, err := check.current("detail", chapter.ChapterID)
		if err != nil {
			return err
		}
		if planPin(detail) != chapter.Detail {
			return domain.ErrConflict
		}
		if err = add(detail); err != nil {
			return err
		}
		for _, pin := range plan.Inputs {
			resource, err := resourceTx(ctx, tx, project, pin.ID)
			if err != nil {
				return err
			}
			if !isNovelPlan(resource.Kind) {
				continue
			}
			if planPin(resource) != pin {
				return domain.ErrConflict
			}
			if err = add(resource); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *NovelChapters) PrepareRelease(ctx context.Context, actor, bookID, chapter string, in NovelChapterReleaseInput) (domain.Release, error) {
	tx, a, book, err := (&NovelPlanning{IP: s.IP}).begin(ctx, actor, bookID, true)
	if err != nil {
		return domain.Release{}, err
	}
	defer tx.Rollback()
	draft, err := s.draftTx(ctx, tx, a, book, chapter)
	if err != nil {
		return domain.Release{}, err
	}
	ledger, err := s.ledgerTx(ctx, tx, a, book, chapter)
	if err != nil {
		return domain.Release{}, err
	}
	if !draft.Ready || !ledger.Ready || draft.Revision < 1 || ledger.Revision < 1 || draft.Revision != in.Revision || draft.Hash != in.Hash || ledger.Revision != in.LedgerRevision || strings.TrimSpace(draft.Document.Text) == "" || ledger.Document.GenerationID == "" {
		return domain.Release{}, fmt.Errorf("%w：请保存当前正文与独立记账结果，再提交审稿", domain.ErrConflict)
	}
	if err = s.validateLedgerTx(ctx, tx, book, draft, &ledger.Document); err != nil {
		return domain.Release{}, err
	}
	body, err := resourceTx(ctx, tx, book.ProjectID, draft.Document.BodyID)
	if err != nil {
		return domain.Release{}, err
	}
	stored, err := resourceDraftTx(ctx, tx, actor, book.ProjectID, ledger.Resource.ID)
	if err != nil {
		return domain.Release{}, err
	}
	envelope := chapterReleaseLedger{Schema: 1, ActorID: actor, LedgerRevision: stored.Revision, LedgerHash: digest(stored.Text), LedgerBaseHead: stored.BaseHead, Ledger: ledger.Document}
	ledgerRaw, err := json.Marshal(envelope)
	if err != nil {
		return domain.Release{}, err
	}
	control, err := resourceDraftTx(ctx, tx, actor, book.ProjectID, chapter)
	if err != nil {
		return domain.Release{}, err
	}
	chapterDependencies := novel.ChapterDependencies(draft.Document)
	dependencies := &chapterDependencies
	policy := &domain.KnowledgePolicy{Space: "working", Visibility: "author"}
	var writer *domain.ArtifactOriginInput
	if draft.Document.WriterGenerationID != "" {
		writer = &domain.ArtifactOriginInput{GenerationID: draft.Document.WriterGenerationID}
	}
	input := domain.ReleaseInput{BranchID: book.BranchID, ExpectedIPHead: draft.Document.IPHead, OperationID: in.OperationID, Updates: []domain.ResourceUpdate{
		{ResourceID: chapter, ExpectedHead: draft.BaseHead, Text: control.Text, Policy: policy, Dependencies: dependencies, Origin: writer},
		{ResourceID: body.ID, ExpectedHead: body.Head, Text: draft.Document.Text, Policy: policy, Dependencies: dependencies, Origin: writer},
		{ResourceID: ledger.Resource.ID, ExpectedHead: ledger.BaseHead, Text: string(ledgerRaw), Policy: policy, Dependencies: dependencies, Origin: &domain.ArtifactOriginInput{GenerationID: ledger.Document.GenerationID}},
	}}
	tx.Rollback()
	return s.IP.PrepareRelease(ctx, a, book.ProjectID, input)
}

// Full structural validation is shared by generic Release preparation, review
// and adoption. A caller cannot publish one member or swap another author's draft.
func (s *IP) validateNovelChapterManifestTx(ctx context.Context, tx *sql.Tx, project string, manifest domain.Manifest) error {
	refs := map[string]domain.ArtifactRef{}
	resources := map[string]domain.Resource{}
	for _, ref := range manifest.Artifacts {
		r, err := resourceTx(ctx, tx, project, ref.ResourceID)
		if err != nil {
			return err
		}
		if isNovelChapter(r.Kind) {
			if _, exists := refs[r.Kind]; exists {
				return domain.ErrNotReady
			}
			refs[r.Kind], resources[r.Kind] = ref, r
		}
	}
	if len(refs) == 0 {
		return nil
	}
	if len(refs) != 3 || len(manifest.Artifacts) != 3 || manifest.IPCandidateID != "" || manifest.PatchGroup != nil || len(manifest.PeerReleases) > 0 || len(manifest.CompositionParents) > 0 {
		return fmt.Errorf("%w：正文、章节依据与剧情账本必须一起提交", domain.ErrNotReady)
	}
	ledgerRef := refs["novel_chapter_ledger"]
	raw, err := s.Blobs.ReadObject(ctx, project, ledgerRef.Hash)
	if err != nil {
		return err
	}
	var envelope chapterReleaseLedger
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&envelope) != nil || decoder.Decode(&struct{}{}) != io.EOF || envelope.Schema != 1 || envelope.LedgerRevision < 1 {
		return domain.ErrValidation
	}
	a, err := s.releaseSourceActorTx(ctx, tx, envelope.ActorID)
	if err != nil {
		return err
	}
	permission, err := projectTx(ctx, tx, a, project)
	if err != nil || !domain.CanEdit(a.Role) || !domain.CanEdit(permission.Role) {
		return domain.ErrForbidden
	}
	book, err := readNovelTx(ctx, tx, a, envelope.Ledger.Source.BookID)
	if err != nil {
		return err
	}
	if book.ProjectID != project || book.BranchID != manifest.BranchID || envelope.Ledger.Source.ResourceID != refs["novel_chapter_control"].ResourceID {
		return domain.ErrForbidden
	}
	chapters := &NovelChapters{IP: s}
	draft, err := chapters.draftTx(ctx, tx, a, book, envelope.Ledger.Source.ResourceID)
	if err != nil {
		return err
	}
	ledger, err := chapters.ledgerTx(ctx, tx, a, book, draft.Resource.ID)
	if err != nil {
		return err
	}
	stored, err := resourceDraftTx(ctx, tx, a.ID, project, ledger.Resource.ID)
	if err != nil {
		return err
	}
	if !draft.Ready || !ledger.Ready || chapterSource(book.ID, draft) != envelope.Ledger.Source || draft.Hash != refs["novel_chapter_control"].Hash || draft.BaseHead != refs["novel_chapter_control"].ExpectedHead || stored.Revision != envelope.LedgerRevision || digest(stored.Text) != envelope.LedgerHash || stored.BaseHead != envelope.LedgerBaseHead || stored.BaseHead != ledgerRef.ExpectedHead || !reflect.DeepEqual(ledger.Document, envelope.Ledger) {
		return fmt.Errorf("%w：审稿期间正文或账本已修改，请重新提交", domain.ErrConflict)
	}
	if err = chapters.validateLedgerTx(ctx, tx, book, draft, &envelope.Ledger); err != nil {
		return err
	}
	if ledger.Resource.ID != ledgerRef.ResourceID || refs["novel_chapter_body"].ResourceID != draft.Document.BodyID || refs["novel_chapter_body"].Hash != digest(draft.Document.Text) || strings.TrimSpace(draft.Document.Text) == "" {
		return domain.ErrConflict
	}
	if resources["novel_chapter_body"].Name != chapterName("章节正文", draft.Document.ChapterID) {
		return domain.ErrConflict
	}
	for kind, ref := range refs {
		if ref.Origin == nil || ref.Origin.ActorID != a.ID || ref.Policy == nil || !reflect.DeepEqual(*ref.Policy, domain.KnowledgePolicy{Space: "working", Visibility: "author"}) {
			return domain.ErrValidation
		}
		dependencies := novel.ChapterDependencies(draft.Document)
		if ref.Dependencies == nil || !reflect.DeepEqual(*ref.Dependencies, dependencies) {
			return domain.ErrValidation
		}
		expected := draft.Document.WriterGenerationID
		role := "writer"
		if kind == "novel_chapter_ledger" {
			expected, role = ledger.Document.GenerationID, "bookkeeper"
			if expected == "" {
				return domain.ErrNotReady
			}
		}
		if expected == "" {
			if ref.Origin.Generation != nil {
				return domain.ErrConflict
			}
			continue
		}
		if ref.Origin.Generation == nil || ref.Origin.Generation.GenerationID != expected || ref.Origin.Generation.Role != role || !ref.Origin.ReviewRequired {
			return domain.ErrConflict
		}
		g, err := generationTx(ctx, tx, project, expected)
		if err != nil {
			return err
		}
		if g.ActorID != a.ID || g.Frozen.NovelChapter == nil || g.Frozen.NovelChapter.BookID != book.ID || g.Frozen.NovelChapter.ResourceID != draft.Resource.ID {
			return domain.ErrForbidden
		}
		if role == "bookkeeper" {
			if err = s.validateNovelChapterSourceTx(ctx, tx, g); err != nil {
				return err
			}
		}
	}
	return nil
}

// Historical attribution is not renewed execution authority. Writer adoption
// necessarily changes its input draft; only its same-actor chapter descendants
// may keep this history, and the joint manifest always requires fresh review.
func (s *IP) chapterWriterHistoricalOriginTx(ctx context.Context, tx *sql.Tx, project string, resource domain.Resource, actor, generation string) (bool, error) {
	if resource.Kind != "novel_chapter_control" && resource.Kind != "novel_chapter_body" {
		return false, nil
	}
	g, err := generationTx(ctx, tx, project, generation)
	if err != nil {
		return false, err
	}
	if g.ActorID != actor || g.BranchID != resource.BranchID || g.Frozen.Role != "writer" || g.Frozen.NovelChapter == nil {
		return false, domain.ErrForbidden
	}
	control, err := resourceTx(ctx, tx, project, g.Frozen.NovelChapter.ResourceID)
	if err != nil {
		return false, err
	}
	if control.Kind != "novel_chapter_control" || (resource.Kind == "novel_chapter_control" && control.ID != resource.ID) || (resource.Kind == "novel_chapter_body" && resource.Name != strings.Replace(control.Name, "章节工作稿", "章节正文", 1)) {
		return false, domain.ErrForbidden
	}
	return true, nil
}
