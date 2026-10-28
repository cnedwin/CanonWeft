package service

import (
	"context"
	"encoding/json"
	"strings"

	"novelstudio/internal/domain"
	"novelstudio/internal/novel"
)

// Refresh is an explicit author action on a saved notebook, never a background
// rewrite. Retain old quotations, edits and model attribution. Changed sources
// revoke the affected cards' selection until the author resolves their evidence.
func (s *NovelResearch) RefreshSources(ctx context.Context, actor, bookID, id string, in ResearchReleaseInput) (ResearchDraft, error) {
	tx, a, book, err := s.begin(ctx, actor, bookID)
	if err != nil {
		return ResearchDraft{}, err
	}
	defer tx.Rollback()
	d, w, rd, err := s.readTx(ctx, tx, a, book, id)
	if err != nil {
		return d, err
	}
	if d.Revision != in.Revision || d.Hash != in.Hash {
		return d, domain.ErrConflict
	}
	prior := map[string]novel.ResearchSource{}
	for _, pin := range d.Document.Sources {
		prior[pin.ID] = pin
	}
	fresh := d.Document
	fresh.Sources = nil
	fresh.SourceIDs = []string{}
	removed := map[string]bool{}
	for _, sourceID := range d.Document.SourceIDs {
		r, err := resourceTx(ctx, tx, book.ProjectID, sourceID)
		if err != nil {
			return d, err
		}
		if r.BranchID != book.BranchID || r.Head == "" {
			return d, domain.ErrForbidden
		}
		if r.Policy == nil || !r.Policy.Withdrawn {
			fresh.SourceIDs = append(fresh.SourceIDs, sourceID)
			continue
		}
		old, ok := prior[sourceID]
		if !ok {
			return d, domain.ErrNotReady
		}
		removed[sourceID] = true
		d.Document.WithdrawnSources = append(d.Document.WithdrawnSources, novel.WithdrawnResearchSource{Source: old, Reason: r.Policy.WithdrawalReason})
	}
	pins, texts, err := s.sourcesTx(ctx, tx, book, id, fresh)
	if err != nil {
		return d, err
	}
	changed := map[string]novel.ResearchSource{}
	for i, pin := range pins {
		old, exists := prior[pin.ID]
		if exists && old.Revision == pin.Revision && old.Hash == pin.Hash {
			pins[i].CapturedAt = old.CapturedAt
		} else {
			changed[pin.ID] = pin
		}
	}
	if len(changed) == 0 && len(removed) == 0 {
		return d, nil
	}
	d.Document.Sources = pins
	d.Document.SourceIDs = fresh.SourceIDs
	for i := range d.Document.Cards {
		card := &d.Document.Cards[i]
		current := []novel.ResearchCitation{}
		for _, cite := range card.Sources {
			if removed[cite.SourceID] {
				card.Approved = false
				card.Status = "unverified"
				card.StaleSources = append(card.StaleSources, cite)
				continue
			}
			pin, updated := changed[cite.SourceID]
			if !updated {
				current = append(current, cite)
				continue
			}
			card.Approved = false
			card.Status = "unverified"
			card.StaleSources = append(card.StaleSources, cite)
			if strings.Contains(texts[pin.ID], cite.Evidence) {
				cite.URL, cite.Locator, cite.CapturedAt = pin.URL, pin.Locator, pin.CapturedAt
				current = append(current, cite)
			}
		}
		card.Sources = current
	}
	if err = novel.ValidateResearch(d.Document, texts); err != nil {
		return d, err
	}
	w.Document = d.Document
	raw, err := json.Marshal(w)
	if err != nil {
		return d, err
	}
	rd.Text, rd.BaseHead = string(raw), d.Head
	rd, err = s.Generations.IP.saveResourceDraftTx(ctx, tx, a, book.ProjectID, id, rd)
	if err != nil {
		return d, err
	}
	d.Revision, d.Hash = rd.Revision, capabilityDigest(d.Document)
	return d, tx.Commit()
}
