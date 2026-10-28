package service

import (
	"context"
	"database/sql"
	"encoding/json"

	"novelstudio/internal/domain"
)

// Saving and editing a research proposal advances its input draft. Preserve
// attribution to that completed call without authorizing a stale execution.
// Only the same actor's notebook actually included in the frozen input may
// use this path; releaseGenerationEvidenceTx still verifies the call/output,
// and derived content must receive an independent review.
func (s *IP) researchHistoricalOriginTx(ctx context.Context, tx *sql.Tx, project string, resource domain.Resource, actor, generation string) (bool, error) {
	if resource.Kind != "note" {
		return false, nil
	}
	d, err := resourceDraftTx(ctx, tx, actor, project, resource.ID)
	if err != nil {
		return false, err
	}
	var current researchWorkspace
	if json.Unmarshal([]byte(d.Text), &current) != nil || current.Schema != "novel-research-v1" || current.ActorID != actor || current.Document.GenerationID != generation {
		return false, nil
	}
	g, err := generationTx(ctx, tx, project, generation)
	if err != nil {
		return false, err
	}
	if g.ActorID != actor || g.BranchID != resource.BranchID || g.Frozen.Role != "researcher" || g.Frozen.ContextID == "" || g.Frozen.Capability == nil {
		return false, domain.ErrForbidden
	}
	var raw []byte
	var hash string
	if err = tx.QueryRowContext(ctx, "SELECT payload,content_hash FROM context_packages WHERE id=? AND project_id=? AND branch_id=? AND actor_id=?", g.Frozen.ContextID, project, resource.BranchID, actor).Scan(&raw, &hash); err != nil {
		return false, err
	}
	var pack domain.ContextPackage
	if json.Unmarshal(raw, &pack) != nil {
		return false, domain.ErrNotReady
	}
	canonical, err := json.Marshal(pack)
	if err != nil || digest(string(canonical)) != hash || pack.ID != g.Frozen.ContextID {
		return false, domain.ErrNotReady
	}
	for _, input := range pack.Unconfirmed {
		if input.Ref.Kind != "resource_draft" || input.Ref.ID != resource.ID {
			continue
		}
		for _, part := range input.Parts {
			var prior researchWorkspace
			if json.Unmarshal([]byte(part.Text), &prior) == nil && prior.Schema == current.Schema && prior.ActorID == actor && prior.BookID == current.BookID {
				return true, nil
			}
		}
	}
	return false, domain.ErrForbidden
}
