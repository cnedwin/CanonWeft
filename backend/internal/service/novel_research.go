package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"novelstudio/internal/domain"
	"novelstudio/internal/novel"
)

type NovelResearch struct {
	Generations *Generations
	Knowledge   *Knowledge
}
type ResearchDraft struct {
	ResourceID string                 `json:"resource_id"`
	Revision   int64                  `json:"revision"`
	Hash       string                 `json:"hash"`
	Head       string                 `json:"head"`
	Document   novel.ResearchNotebook `json:"document"`
}
type researchWorkspace struct {
	Schema     string                 `json:"schema"`
	ActorID    string                 `json:"actor_id"`
	BookID     string                 `json:"book_id"`
	CreateHash string                 `json:"create_hash"`
	Document   novel.ResearchNotebook `json:"document"`
}
type ResearchCreate struct {
	OperationID string                 `json:"operation_id"`
	Document    novel.ResearchNotebook `json:"document"`
}

func (s *NovelResearch) begin(ctx context.Context, actor, book string) (*sql.Tx, domain.Actor, NovelBook, error) {
	return (&NovelPlanning{IP: s.Generations.IP}).begin(ctx, actor, book, true)
}

func (s *NovelResearch) readTx(ctx context.Context, tx *sql.Tx, a domain.Actor, book NovelBook, id string) (ResearchDraft, researchWorkspace, ResourceDraft, error) {
	var out ResearchDraft
	var w researchWorkspace
	r, err := resourceTx(ctx, tx, book.ProjectID, id)
	if err != nil {
		return out, w, ResourceDraft{}, err
	}
	if r.BranchID != book.BranchID || r.Kind != "note" {
		return out, w, ResourceDraft{}, domain.ErrForbidden
	}
	d, err := resourceDraftTx(ctx, tx, a.ID, book.ProjectID, id)
	if err != nil {
		return out, w, d, err
	}
	if d.Revision < 1 || json.Unmarshal([]byte(d.Text), &w) != nil || w.Schema != "novel-research-v1" || w.ActorID != a.ID || w.BookID != book.ID {
		return out, w, d, domain.ErrForbidden
	}
	out = ResearchDraft{ResourceID: id, Revision: d.Revision, Hash: capabilityDigest(w.Document), Head: r.Head, Document: w.Document}
	return out, w, d, nil
}

func (s *NovelResearch) sourcesTx(ctx context.Context, tx *sql.Tx, book NovelBook, id string, doc novel.ResearchNotebook) ([]novel.ResearchSource, map[string]string, error) {
	if len(doc.SourceIDs) > 12 || validateRequiredIDs(doc.SourceIDs) != nil {
		return nil, nil, domain.ErrValidation
	}
	pins := []novel.ResearchSource{}
	texts := map[string]string{}
	for _, sourceID := range doc.SourceIDs {
		if sourceID == id {
			return nil, nil, domain.ErrValidation
		}
		r, err := resourceTx(ctx, tx, book.ProjectID, sourceID)
		if err != nil {
			return nil, nil, err
		}
		if r.Policy != nil && r.Policy.Withdrawn {
			return nil, nil, fmt.Errorf("%w：研究来源已撤回使用，请刷新来源以保留旧引文并移出当前证据", domain.ErrConflict)
		}
		if r.Head == "" || r.BranchID != book.BranchID || !allowsRequiredPolicy(r.Policy, SearchInput{Purpose: "research", IncludeWorking: true}) {
			return nil, nil, domain.ErrForbidden
		}
		pin := novel.ResearchSource{ID: r.ID, Name: r.Name, Revision: r.Head, Hash: r.Hash, URL: "studio:resource/" + r.ID + "?revision=" + r.Head, Locator: "resource revision " + r.Head, CapturedAt: time.Now().UTC().Format(time.RFC3339)}
		for _, prior := range doc.Sources {
			if prior.ID == r.ID {
				if prior.Revision != r.Head || prior.Hash != r.Hash {
					return nil, nil, fmt.Errorf("%w：研究来源已更新，请刷新来源并核对受影响的事实卡；当前稿和旧引用仍保留", domain.ErrConflict)
				}
				pin.CapturedAt = prior.CapturedAt
			}
		}
		raw, err := s.Generations.IP.Blobs.ReadObject(ctx, book.ProjectID, r.Hash)
		if err != nil {
			return nil, nil, domain.ErrNotReady
		}
		pins = append(pins, pin)
		texts[r.ID] = string(raw)
	}
	return pins, texts, nil
}

func (s *NovelResearch) Create(ctx context.Context, actor, bookID string, in ResearchCreate) (ResearchDraft, error) {
	var empty ResearchDraft
	if !validName(in.OperationID, 120) || in.Document.GenerationID != "" {
		return empty, domain.ErrValidation
	}
	tx, a, book, err := s.begin(ctx, actor, bookID)
	if err != nil {
		return empty, err
	}
	defer tx.Rollback()
	id := novel.CharacterDesignID(bookID, actor, "research:"+in.OperationID)
	hash := capabilityDigest(in)
	if _, err = resourceTx(ctx, tx, book.ProjectID, id); err == nil {
		d, w, _, e := s.readTx(ctx, tx, a, book, id)
		if e == nil && w.CreateHash != hash {
			e = domain.ErrConflict
		}
		return d, e
	} else if err != domain.ErrForbidden {
		return empty, err
	}
	in.Document.Sources = nil
	in.Document.WithdrawnSources = nil
	pins, texts, err := s.sourcesTx(ctx, tx, book, id, in.Document)
	if err != nil {
		return empty, err
	}
	in.Document.Sources = pins
	if err = novel.ValidateResearch(in.Document, texts); err != nil {
		return empty, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO resources(id,project_id,branch_id,name,kind) VALUES(?,?,?,?,?)", id, book.ProjectID, book.BranchID, "题材研究 · "+in.Document.Title, "note"); err != nil {
		return empty, err
	}
	if err = audit(ctx, tx, a, "resource.create", id); err != nil {
		return empty, err
	}
	if err = captureMetadataSourceTx(ctx, tx, book.ProjectID, "resources", id); err != nil {
		return empty, err
	}
	w := researchWorkspace{Schema: "novel-research-v1", ActorID: a.ID, BookID: book.ID, CreateHash: hash, Document: in.Document}
	raw, _ := json.Marshal(w)
	rd, err := s.Generations.IP.saveResourceDraftTx(ctx, tx, a, book.ProjectID, id, ResourceDraft{Text: string(raw), Policy: &domain.KnowledgePolicy{Space: "research", Visibility: "author"}})
	if err != nil {
		return empty, err
	}
	return ResearchDraft{ResourceID: id, Revision: rd.Revision, Hash: capabilityDigest(in.Document), Document: in.Document}, tx.Commit()
}

func (s *NovelResearch) Read(ctx context.Context, actor, bookID, id string) (ResearchDraft, error) {
	tx, a, book, err := s.begin(ctx, actor, bookID)
	if err != nil {
		return ResearchDraft{}, err
	}
	defer tx.Rollback()
	d, _, _, err := s.readTx(ctx, tx, a, book, id)
	return d, err
}
func (s *NovelResearch) List(ctx context.Context, actor, bookID string) ([]ResearchDraft, error) {
	tx, a, book, err := s.begin(ctx, actor, bookID)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT r.id FROM resources r JOIN resource_drafts d ON d.resource_id=r.id AND d.actor_id=? WHERE r.project_id=? AND r.branch_id=? AND JSON_UNQUOTE(JSON_EXTRACT(IF(JSON_VALID(d.body),d.body,'{}'),'$.schema'))='novel-research-v1' AND JSON_UNQUOTE(JSON_EXTRACT(IF(JSON_VALID(d.body),d.body,'{}'),'$.book_id'))=? ORDER BY d.updated_at DESC LIMIT 100`, a.ID, book.ProjectID, book.BranchID, book.ID)
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	items := []ResearchDraft{}
	for _, id := range ids {
		d, _, _, e := s.readTx(ctx, tx, a, book, id)
		if e != nil {
			return nil, e
		}
		items = append(items, d)
	}
	return items, nil
}

func (s *NovelResearch) Save(ctx context.Context, actor, bookID, id string, in ResearchDraft) (ResearchDraft, error) {
	prior, err := s.Read(ctx, actor, bookID, id)
	if err != nil {
		return prior, err
	}
	if in.Document.GenerationID != prior.Document.GenerationID {
		if in.Document.GenerationID == "" {
			return prior, domain.ErrValidation
		}
		proposal, e := s.Suggestion(ctx, actor, bookID, id, in.Document.GenerationID)
		if e != nil {
			return prior, e
		}
		if proposal.Revision != in.Revision || proposal.Hash != in.Hash {
			return prior, domain.ErrConflict
		}
	}
	tx, a, book, err := s.begin(ctx, actor, bookID)
	if err != nil {
		return prior, err
	}
	defer tx.Rollback()
	d, w, rd, err := s.readTx(ctx, tx, a, book, id)
	if err != nil {
		return d, err
	}
	if in.Revision != d.Revision || in.Hash != d.Hash || in.Head != d.Head {
		return d, domain.ErrConflict
	}
	// Source identities are derived from stored revisions, never client URLs.
	in.Document.Sources = d.Document.Sources
	in.Document.WithdrawnSources = d.Document.WithdrawnSources
	pins, texts, err := s.sourcesTx(ctx, tx, book, id, in.Document)
	if err != nil {
		return d, err
	}
	in.Document.Sources = pins
	if err = novel.ValidateResearch(in.Document, texts); err != nil {
		return d, err
	}
	w.Document = in.Document
	raw, _ := json.Marshal(w)
	rd.Text = string(raw)
	rd.BaseHead = d.Head
	rd, err = s.Generations.IP.saveResourceDraftTx(ctx, tx, a, book.ProjectID, id, rd)
	if err != nil {
		return d, err
	}
	d.Revision = rd.Revision
	d.Document = in.Document
	d.Hash = capabilityDigest(in.Document)
	return d, tx.Commit()
}

type ResearchReleaseInput struct {
	Revision    int64  `json:"revision"`
	Hash        string `json:"hash"`
	OperationID string `json:"operation_id"`
}

func (s *NovelResearch) PrepareRelease(ctx context.Context, actor, bookID, id string, in ResearchReleaseInput) (domain.Release, error) {
	var empty domain.Release
	tx, a, book, err := s.begin(ctx, actor, bookID)
	if err != nil {
		return empty, err
	}
	defer tx.Rollback()
	d, _, _, err := s.readTx(ctx, tx, a, book, id)
	if err != nil {
		return empty, err
	}
	if d.Revision != in.Revision || d.Hash != in.Hash {
		return empty, domain.ErrConflict
	}
	pins, texts, err := s.sourcesTx(ctx, tx, book, id, d.Document)
	if err != nil {
		return empty, err
	}
	d.Document.Sources = pins
	if err = novel.ValidateResearch(d.Document, texts); err != nil {
		return empty, err
	}
	used := []string{}
	count := 0
	for _, f := range d.Document.Cards {
		if !f.Approved {
			continue
		}
		count++
		for _, cite := range f.Sources {
			if !slices.Contains(used, cite.SourceID) {
				used = append(used, cite.SourceID)
			}
		}
	}
	if count == 0 {
		return empty, fmt.Errorf("%w：请先逐项选择要采用的事实卡", domain.ErrValidation)
	}
	b, err := branchTx(ctx, tx, book.ProjectID, book.BranchID)
	if err != nil {
		return empty, err
	}
	deps := &domain.DependencyPolicy{Inputs: []domain.Dependency{}}
	for _, pin := range d.Document.Sources {
		if slices.Contains(used, pin.ID) {
			source, readErr := resourceTx(ctx, tx, book.ProjectID, pin.ID)
			if readErr != nil {
				return empty, readErr
			}
			deps.Inputs = append(deps.Inputs, domain.Dependency{Source: domain.DependencyRef{Kind: "resource", BranchID: book.BranchID, ID: pin.ID, Revision: pin.Revision, Hash: resourceNodeHash(source.Hash, source.Policy, source.Dependencies)}, Kind: "evidence", Reason: "事实卡引用的原始资料版本"})
		}
	}
	var origin *domain.ArtifactOriginInput
	if d.Document.GenerationID != "" {
		origin = &domain.ArtifactOriginInput{GenerationID: d.Document.GenerationID}
	}
	tx.Rollback()
	return s.Generations.IP.PrepareRelease(ctx, a, book.ProjectID, domain.ReleaseInput{BranchID: book.BranchID, ExpectedIPHead: b.Head, OperationID: in.OperationID, Updates: []domain.ResourceUpdate{{ResourceID: id, ExpectedHead: d.Head, Text: novel.ResearchText(d.Document), Policy: &domain.KnowledgePolicy{Space: "working", Visibility: "author"}, Dependencies: deps, Origin: origin}}})
}
