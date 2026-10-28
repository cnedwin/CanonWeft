package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"

	"novelstudio/internal/domain"
	"novelstudio/internal/novel"
)

type NovelCharacters struct {
	Generations *Generations
	Knowledge   *Knowledge
}

type CharacterDesignCreate struct {
	OperationID string   `json:"operation_id"`
	Notes       string   `json:"notes"`
	Genre       string   `json:"genre"`
	Count       int      `json:"count"`
	EntityIDs   []string `json:"entity_ids"`
	SourceIDs   []string `json:"source_ids,omitempty"`
}

type CharacterDesignDraft struct {
	ResourceID string                `json:"resource_id"`
	Revision   int64                 `json:"revision"`
	BaseHead   string                `json:"base_head"`
	IPRevision int64                 `json:"ip_revision"`
	Hash       string                `json:"hash"`
	Document   novel.CharacterDesign `json:"document"`
}

type characterWorkspace struct {
	Schema           string                `json:"schema"`
	ActorID          string                `json:"actor_id"`
	CreateHash       string                `json:"create_hash"`
	Design           novel.CharacterDesign `json:"design"`
	Applied          map[string]string     `json:"applied"`
	SettingsRevision int64                 `json:"settings_revision"`
	Setup            novel.Setup           `json:"novel_setup"`
}

func (s *NovelCharacters) begin(ctx context.Context, actor, bookID string) (*sql.Tx, domain.Actor, NovelBook, error) {
	return (&NovelPlanning{IP: s.Generations.IP}).begin(ctx, actor, bookID, true)
}

func (s *NovelCharacters) readTx(ctx context.Context, tx *sql.Tx, a domain.Actor, book NovelBook, id string) (CharacterDesignDraft, characterWorkspace, ResourceDraft, error) {
	var out CharacterDesignDraft
	var stored characterWorkspace
	r, err := resourceTx(ctx, tx, book.ProjectID, id)
	if err != nil {
		return out, stored, ResourceDraft{}, err
	}
	if r.BranchID != book.BranchID || r.Kind != "note" {
		return out, stored, ResourceDraft{}, domain.ErrForbidden
	}
	rd, err := resourceDraftTx(ctx, tx, a.ID, book.ProjectID, id)
	if err != nil {
		return out, stored, rd, err
	}
	if rd.Revision == 0 || json.Unmarshal([]byte(rd.Text), &stored) != nil || stored.Schema != "novel-character-workspace-v1" || stored.ActorID != a.ID || stored.Design.BookID != book.ID {
		return out, stored, rd, domain.ErrForbidden
	}
	if err = novel.ValidateCharacterDesign(stored.Design); err != nil {
		return out, stored, rd, err
	}
	ip, err := ipDraftTx(ctx, tx, a.ID, book.ProjectID, book.BranchID)
	if err != nil {
		return out, stored, rd, err
	}
	out = CharacterDesignDraft{ResourceID: id, Revision: rd.Revision, BaseHead: rd.BaseHead, IPRevision: ip.Revision, Hash: capabilityDigest(stored.Design), Document: stored.Design}
	return out, stored, rd, nil
}

func (s *NovelCharacters) Read(ctx context.Context, actor, bookID, id string) (CharacterDesignDraft, error) {
	tx, a, book, err := s.begin(ctx, actor, bookID)
	if err != nil {
		return CharacterDesignDraft{}, err
	}
	defer tx.Rollback()
	d, _, _, err := s.readTx(ctx, tx, a, book, id)
	return d, err
}

func (s *NovelCharacters) Open(ctx context.Context, actor, bookID string, in CharacterDesignCreate) (CharacterDesignDraft, error) {
	var out CharacterDesignDraft
	if !validName(in.OperationID, 120) || !validName(strings.TrimSpace(in.Notes), 10000) || len([]rune(in.Genre)) > 300 || in.Count < 0 || in.Count > 100 || len(in.EntityIDs) > 100 || (in.Count > 0) == (len(in.EntityIDs) > 0) {
		return out, domain.ErrValidation
	}
	tx, a, book, err := s.begin(ctx, actor, bookID)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	id := novel.CharacterDesignID(bookID, actor, in.OperationID)
	hash := capabilityDigest(in)
	_, err = resourceTx(ctx, tx, book.ProjectID, id)
	if err == nil {
		var stored characterWorkspace
		out, stored, _, err = s.readTx(ctx, tx, a, book, id)
		if err == nil && stored.CreateHash != hash {
			err = domain.ErrConflict
		}
		return out, err
	}
	if err != domain.ErrForbidden {
		return out, err
	}
	b, err := branchTx(ctx, tx, book.ProjectID, book.BranchID)
	if err != nil {
		return out, err
	}
	current, err := ipDraftTx(ctx, tx, a.ID, book.ProjectID, book.BranchID)
	if err != nil {
		return out, err
	}
	if current.Revision == 0 {
		current.BaseHead = b.Head
		current.Snapshot, err = snapshotTx(ctx, tx, book.ProjectID, b.Head)
		if err != nil {
			return out, err
		}
	}
	if current.BaseHead != b.Head {
		return out, fmt.Errorf("%w：请先处理角色库与当前分支的版本差异", domain.ErrConflict)
	}
	doc := novel.CharacterDesign{Schema: "character-design-v1", BookID: bookID, Notes: in.Notes, Genre: in.Genre, IDs: slices.Clone(in.EntityIDs), SourceIDs: slices.Clone(in.SourceIDs), Base: current.Snapshot, Candidate: current.Snapshot}
	doc.Candidate.Entities = slices.Clone(doc.Base.Entities)
	if in.Count > 0 {
		for i := 0; i < in.Count; i++ {
			candidateID := novel.CharacterDesignID(id, "entity", fmt.Sprint(i))
			doc.IDs = append(doc.IDs, candidateID)
			doc.Candidate.Entities = append(doc.Candidate.Entities, domain.Entity{ID: candidateID, Kind: "character", Name: fmt.Sprintf("待完善角色 %d", i+1), Aliases: []string{}})
		}
	}
	if err = novel.ValidateCharacterDesign(doc); err != nil {
		return out, err
	}
	if err = validateCharacterSourcesTx(ctx, tx, book, doc.SourceIDs); err != nil {
		return out, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO resources(id,project_id,branch_id,name,kind) VALUES(?,?,?,?,?)", id, book.ProjectID, book.BranchID, "角色创作 · "+id, "note"); err != nil {
		return out, err
	}
	if err = audit(ctx, tx, a, "resource.create", id); err != nil {
		return out, err
	}
	if err = captureMetadataSourceTx(ctx, tx, book.ProjectID, "resources", id); err != nil {
		return out, err
	}
	stored := characterWorkspace{Schema: "novel-character-workspace-v1", ActorID: actor, CreateHash: hash, Design: doc, Applied: map[string]string{}, SettingsRevision: book.SettingsRevision, Setup: book.Setup}
	raw, err := json.Marshal(stored)
	if err != nil {
		return out, err
	}
	rd, err := s.Generations.IP.saveResourceDraftTx(ctx, tx, a, book.ProjectID, id, ResourceDraft{BaseHead: b.Head, Text: string(raw), Policy: &domain.KnowledgePolicy{Space: "working", Visibility: "author"}})
	if err != nil {
		return out, err
	}
	out = CharacterDesignDraft{ResourceID: id, Revision: rd.Revision, BaseHead: b.Head, IPRevision: current.Revision, Hash: capabilityDigest(doc), Document: doc}
	return out, tx.Commit()
}

func (s *NovelCharacters) List(ctx context.Context, actor, bookID string) ([]CharacterDesignDraft, error) {
	tx, a, book, err := s.begin(ctx, actor, bookID)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT r.id FROM resources r JOIN resource_drafts d ON d.resource_id=r.id AND d.actor_id=? WHERE r.project_id=? AND r.branch_id=? AND JSON_UNQUOTE(JSON_EXTRACT(IF(JSON_VALID(d.body),d.body,'{}'),'$.schema'))='novel-character-workspace-v1' AND JSON_UNQUOTE(JSON_EXTRACT(IF(JSON_VALID(d.body),d.body,'{}'),'$.design.book_id'))=? ORDER BY d.updated_at DESC LIMIT 100`, actor, book.ProjectID, book.BranchID, bookID)
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
	out := []CharacterDesignDraft{}
	for _, id := range ids {
		d, _, _, e := s.readTx(ctx, tx, a, book, id)
		if e != nil {
			return nil, e
		}
		out = append(out, d)
	}
	return out, nil
}

func (s *NovelCharacters) Save(ctx context.Context, actor, bookID, id string, in CharacterDesignDraft) (CharacterDesignDraft, error) {
	// A new provenance pin must belong to this author's current design. Manual
	// edits after preview are permitted, but cannot claim another task's identity.
	prior, err := s.Read(ctx, actor, bookID, id)
	if err != nil {
		return CharacterDesignDraft{}, err
	}
	if in.Document.Naming != nil && in.Document.Naming.GenerationID != "" && (prior.Document.Naming == nil || prior.Document.Naming.GenerationID != in.Document.Naming.GenerationID) {
		proposal, e := s.characterSuggestion(ctx, actor, bookID, id, in.Document.Naming.GenerationID, true)
		if e != nil {
			return CharacterDesignDraft{}, e
		}
		if proposal.Draft.Revision != in.Revision || proposal.Draft.Hash != in.Hash {
			return CharacterDesignDraft{}, domain.ErrConflict
		}
	}
	if in.Document.GenerationID != "" && in.Document.RecipeRunID != "" {
		return CharacterDesignDraft{}, domain.ErrValidation
	}
	if in.Document.GenerationID != prior.Document.GenerationID || in.Document.RecipeRunID != prior.Document.RecipeRunID {
		var proposal CharacterDesignSuggestion
		if in.Document.RecipeRunID != "" {
			report, e := s.BatchReport(ctx, actor, bookID, id, in.Document.RecipeRunID)
			if e != nil {
				return CharacterDesignDraft{}, e
			}
			if report.Suggestion == nil {
				return CharacterDesignDraft{}, domain.ErrConflict
			}
			proposal = *report.Suggestion
		} else if in.Document.GenerationID != "" {
			var e error
			proposal, e = s.Suggestion(ctx, actor, bookID, id, in.Document.GenerationID)
			if e != nil {
				return CharacterDesignDraft{}, e
			}
		} else {
			return CharacterDesignDraft{}, domain.ErrValidation
		}
		if proposal.Draft.Revision != in.Revision || proposal.Draft.Hash != in.Hash {
			return CharacterDesignDraft{}, domain.ErrConflict
		}
	}
	tx, a, book, err := s.begin(ctx, actor, bookID)
	if err != nil {
		return CharacterDesignDraft{}, err
	}
	defer tx.Rollback()
	d, stored, rd, err := s.readTx(ctx, tx, a, book, id)
	if err != nil {
		return d, err
	}
	if d.Revision != in.Revision || d.Hash != in.Hash {
		return d, domain.ErrConflict
	}
	if in.Document.Schema != d.Document.Schema || in.Document.BookID != bookID || !reflect.DeepEqual(in.Document.IDs, d.Document.IDs) || !reflect.DeepEqual(in.Document.Base, d.Document.Base) {
		return d, domain.ErrValidation
	}
	if err = novel.ValidateCharacterDesign(in.Document); err != nil {
		return d, err
	}
	if err = validateCharacterSourcesTx(ctx, tx, book, in.Document.SourceIDs); err != nil {
		return d, err
	}
	// Authors can deliberately unlock a field in the private editor. Published
	// locks from Base remain protected by ValidateCharacterDesign.
	stored.Design = in.Document
	raw, err := json.Marshal(stored)
	if err != nil {
		return d, err
	}
	rd.Text = string(raw)
	rd, err = s.Generations.IP.saveResourceDraftTx(ctx, tx, a, book.ProjectID, id, rd)
	if err != nil {
		return d, err
	}
	d.Document = stored.Design
	d.Revision = rd.Revision
	d.Hash = capabilityDigest(stored.Design)
	return d, tx.Commit()
}

type CharacterDesignApply struct {
	Revision    int64    `json:"revision"`
	Hash        string   `json:"hash"`
	IPRevision  int64    `json:"ip_revision"`
	Selected    []string `json:"selected"`
	OperationID string   `json:"operation_id"`
}
type CharacterDesignApplied struct {
	Draft    CharacterDesignDraft `json:"draft"`
	Warnings []string             `json:"warnings"`
	Replay   bool                 `json:"replay"`
}

func (s *NovelCharacters) Apply(ctx context.Context, actor, bookID, id string, in CharacterDesignApply) (CharacterDesignApplied, error) {
	var out CharacterDesignApplied
	if !validName(in.OperationID, 120) {
		return out, domain.ErrValidation
	}
	tx, a, book, err := s.begin(ctx, actor, bookID)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	d, stored, rd, err := s.readTx(ctx, tx, a, book, id)
	if err != nil {
		return out, err
	}
	fingerprint := capabilityDigest(in)
	if old, ok := stored.Applied[in.OperationID]; ok {
		if old != fingerprint {
			return out, domain.ErrConflict
		}
		return CharacterDesignApplied{Draft: d, Warnings: []string{}, Replay: true}, nil
	}
	if len(stored.Applied) >= 100 || d.Revision != in.Revision || d.Hash != in.Hash || d.IPRevision != in.IPRevision {
		return out, domain.ErrConflict
	}
	if err = validateCharacterSettings(ctx, tx, book.ProjectID, book.BranchID, stored); err != nil {
		return out, err
	}
	b, err := branchTx(ctx, tx, book.ProjectID, book.BranchID)
	if err != nil {
		return out, err
	}
	current, err := ipDraftTx(ctx, tx, a.ID, book.ProjectID, book.BranchID)
	if err != nil {
		return out, err
	}
	if current.Revision == 0 {
		current.BaseHead = b.Head
		current.Snapshot, err = snapshotTx(ctx, tx, book.ProjectID, b.Head)
		if err != nil {
			return out, err
		}
	}
	if b.Head != d.BaseHead || current.BaseHead != b.Head {
		return out, domain.ErrConflict
	}
	merged, warnings, err := novel.MergeCharacterDesign(d.Document, current.Snapshot, in.Selected)
	if err != nil {
		return out, err
	}
	current.Snapshot = merged
	current, err = s.Generations.IP.saveIPDraftTx(ctx, tx, a, book.ProjectID, book.BranchID, current)
	if err != nil {
		return out, err
	}
	if stored.Applied == nil {
		stored.Applied = map[string]string{}
	}
	stored.Applied[in.OperationID] = fingerprint
	raw, err := json.Marshal(stored)
	if err != nil {
		return out, err
	}
	rd.Text = string(raw)
	rd, err = s.Generations.IP.saveResourceDraftTx(ctx, tx, a, book.ProjectID, id, rd)
	if err != nil {
		return out, err
	}
	d.Revision = rd.Revision
	d.IPRevision = current.Revision
	if err = audit(ctx, tx, a, "character.design.apply", id); err != nil {
		return out, err
	}
	out = CharacterDesignApplied{Draft: d, Warnings: warnings}
	return out, tx.Commit()
}
