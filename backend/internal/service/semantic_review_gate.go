package service

import (
	"context"
	"database/sql"
	"novelstudio/internal/domain"
)

func (s *IP) requireSemanticReviewTx(ctx context.Context, tx *sql.Tx, project, kind, id, hash string) error {
	var acceptance, review, report, selectedHash, selector string
	err := tx.QueryRowContext(ctx, "SELECT id,review_id,report_hash,target_hash,actor_id FROM semantic_review_acceptances WHERE project_id=? AND target_kind=? AND target_id=? ORDER BY revision DESC LIMIT 1", project, kind, id).Scan(&acceptance, &review, &report, &selectedHash, &selector)
	if err == sql.ErrNoRows {
		return domain.ErrNotReady
	}
	if err != nil {
		return err
	}
	if hash != selectedHash {
		return domain.ErrConflict
	}
	if err := requireRuntimeObjectTx(ctx, tx, project, "semantic_review_acceptances", acceptance, "report_hash", report); err != nil {
		return err
	}
	actor, err := s.releaseSourceActorTx(ctx, tx, selector)
	if err != nil {
		return err
	}
	permission, err := projectTx(ctx, tx, actor, project)
	if err != nil || actor.Role == "reader" || permission.Role == "reader" {
		return domain.ErrForbidden
	}
	run, err := semanticReviewTx(ctx, tx, project, review)
	if err != nil {
		return err
	}
	if run.ReportHash != report || run.TargetHash != hash || run.TargetKind != kind || run.TargetID != id {
		return domain.ErrConflict
	}
	return (&SemanticReviews{Generations: &Generations{IP: s}}).validateReportAuthorityTx(ctx, tx, run)
}

func (s *IP) requireCandidateSemanticReviewTx(ctx context.Context, tx *sql.Tx, project string, c Candidate) error {
	var merges int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM ip_merge_candidates WHERE candidate_id=?", c.ID).Scan(&merges); err != nil {
		return err
	}
	scope, err := candidateSemanticScope(c)
	if err != nil {
		return err
	}
	negative, err := s.knownSemanticNegativeTx(ctx, tx, c.BranchID, "ip_candidate", scope, "")
	if err != nil {
		return err
	}
	if c.Promotion == nil && merges == 0 && !negative {
		return nil
	}
	return s.requireSemanticReviewTx(ctx, tx, project, "ip_candidate", c.ID, c.Hash)
}

func (s *IP) requireReleaseSemanticReviewTx(ctx context.Context, tx *sql.Tx, project string, r domain.Release) error {
	required := r.Manifest.IPCandidateID != "" || r.Manifest.PatchGroup != nil || len(r.Manifest.PatchParents) > 0 || len(r.Manifest.CompositionParents) > 0 || len(r.Manifest.PeerReleases) > 0
	doc, _, err := (&SemanticReviews{Generations: &Generations{IP: s}}).targetDocumentTx(ctx, tx, project, SemanticReviewInput{TargetKind: "artifact_release", TargetID: r.ID, TargetHash: r.Hash, BranchID: r.Manifest.BranchID})
	if err != nil {
		return err
	}
	scope, err := domain.SemanticContentScopeHash(doc)
	if err != nil {
		return err
	}
	negative, err := s.knownSemanticNegativeTx(ctx, tx, r.Manifest.BranchID, "artifact_release", scope, "")
	if err != nil {
		return err
	}
	required = required || negative
	for _, ref := range r.Manifest.Artifacts {
		required = required || (ref.Origin != nil && ref.Origin.ReviewRequired)
	}
	if !required {
		return nil
	}
	return s.requireSemanticReviewTx(ctx, tx, project, "artifact_release", r.ID, r.Hash)
}
