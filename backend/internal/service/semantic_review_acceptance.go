package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"novelstudio/internal/domain"
	"novelstudio/internal/models"
	"slices"
)

type SemanticReviewAcceptance struct {
	ID         string `json:"id"`
	TargetKind string `json:"target_kind"`
	TargetID   string `json:"target_id"`
	TargetHash string `json:"target_hash"`
	Revision   int64  `json:"revision"`
	ReviewID   string `json:"review_id"`
	ReportHash string `json:"report_hash"`
	ActorID    string `json:"actor_id"`
}

// validateReportAuthorityTx checks current execution authority, not only the
// historical PASS. Restoration permanently disables that authority.
func (s *SemanticReviews) validateReportAuthorityTx(ctx context.Context, tx *sql.Tx, r SemanticReview) error {
	if r.RestoredDisabled || r.CancelRequested || r.Paused || r.State != "COMPLETED" || r.Verdict != "PASS" || r.ReportHash == "" || r.GenerationID == "" {
		return domain.ErrConflict
	}
	if err := s.validateTargetTx(ctx, tx, r); err != nil {
		return err
	}
	if err := s.noNewerSemanticNegativeTx(ctx, tx, r); err != nil {
		return err
	}
	author, err := s.Generations.IP.releaseSourceActorTx(ctx, tx, r.ActorID)
	if err != nil {
		return err
	}
	permission, err := projectTx(ctx, tx, author, r.ProjectID)
	if err != nil || !domain.CanEdit(author.Role) || !domain.CanEdit(permission.Role) {
		return domain.ErrForbidden
	}
	g, err := generationTx(ctx, tx, r.ProjectID, r.GenerationID)
	if err != nil {
		return err
	}
	if g.ActorID != r.ActorID || g.BranchID != r.BranchID || g.Frozen.SemanticReviewID != r.ID || g.State != "SUCCEEDED" || g.Cancelled || g.Paused || g.Frozen.Role != "critic" || g.Frozen.Capability == nil || g.Frozen.Capability.Mode != "QUALIFIED" || g.Frozen.Capability.TaskKind != "shared.semantic-review" || g.Frozen.Capability.BindingID != r.Frozen.ReviewerBindingID || slices.Contains(r.Frozen.SourceGenerationIDs, g.ID) {
		return domain.ErrConflict
	}
	if err = s.Generations.IP.lockReleaseQualificationActorTx(ctx, tx, g); err != nil {
		return err
	}
	if err = s.Generations.validateFrozen(ctx, tx, g); err != nil {
		return err
	}
	call, err := callTx(ctx, tx, g.WorkspaceID, r.ProjectID, g.CallID)
	if err != nil {
		return err
	}
	if call.State != "SUCCEEDED" || call.CancelRequested || call.OutputHash == "" {
		return domain.ErrConflict
	}
	expired, err := runtimeObjectRetentionTx(ctx, tx, r.ProjectID, "model_calls", call.ID, "output_hash", call.OutputHash)
	if err != nil {
		return err
	}
	if expired != nil {
		return domain.ErrMaterialExpired
	}
	var actor, operation, fingerprint string
	if err = tx.QueryRowContext(ctx, "SELECT actor_id,operation_id,input_hash FROM model_calls WHERE id=? AND project_id=?", g.CallID, r.ProjectID).Scan(&actor, &operation, &fingerprint); err != nil {
		return err
	}
	if actor != r.ActorID || operation != "generation-"+g.ID || fingerprint != modelCallFingerprint(r.ProjectID, g.Frozen.Estimate, g.Frozen.Price.Currency, g.Frozen.InputHash) {
		return domain.ErrConflict
	}
	raw, err := s.Generations.IP.Blobs.ReadObject(ctx, r.ProjectID, call.OutputHash)
	if err != nil || digest(string(raw)) != call.OutputHash {
		return domain.ErrNotReady
	}
	var output models.Result
	if json.Unmarshal(raw, &output) != nil || digest(output.Text) != r.ReportHash {
		return domain.ErrConflict
	}
	doc, err := s.document(ctx, r)
	if err != nil {
		return err
	}
	report, err := domain.ParseSemanticReviewReport([]byte(output.Text), doc)
	if err != nil || report.Verdict != "PASS" {
		return domain.ErrConflict
	}
	return nil
}

func (s *SemanticReviews) Select(ctx context.Context, a domain.Actor, project, id string, expectedRevision int64) (SemanticReviewAcceptance, error) {
	return retrySourceAuthority(ctx, func() (SemanticReviewAcceptance, error) { return s.selectReport(ctx, a, project, id, expectedRevision) })
}

func (s *SemanticReviews) selectReport(ctx context.Context, a domain.Actor, project, id string, expectedRevision int64) (SemanticReviewAcceptance, error) {
	var receipt SemanticReviewAcceptance
	if expectedRevision < 0 {
		return receipt, domain.ErrValidation
	}
	tx, a, permission, err := s.Generations.IP.begin(ctx, a, project, false)
	if err != nil {
		return receipt, err
	}
	defer tx.Rollback()
	if a.Role == "reader" || permission.Role == "reader" {
		return receipt, domain.ErrForbidden
	}
	r, err := semanticReviewTx(ctx, tx, project, id)
	if err != nil {
		return receipt, err
	}
	if err = s.validateReportAuthorityTx(ctx, tx, r); err != nil {
		return receipt, err
	}
	var prior SemanticReviewAcceptance
	err = tx.QueryRowContext(ctx, "SELECT id,revision,review_id,report_hash FROM semantic_review_acceptances WHERE project_id=? AND target_kind=? AND target_id=? ORDER BY revision DESC LIMIT 1", project, r.TargetKind, r.TargetID).Scan(&prior.ID, &prior.Revision, &prior.ReviewID, &prior.ReportHash)
	if err != nil && err != sql.ErrNoRows {
		return receipt, err
	}
	if prior.Revision != expectedRevision {
		if prior.ReviewID == r.ID && prior.ReportHash == r.ReportHash && prior.Revision == expectedRevision+1 {
			prior.TargetKind = r.TargetKind
			prior.TargetID = r.TargetID
			prior.TargetHash = r.TargetHash
			return prior, nil
		}
		return receipt, domain.ErrConflict
	}
	receipt = SemanticReviewAcceptance{ID: NewID(), TargetKind: r.TargetKind, TargetID: r.TargetID, TargetHash: r.TargetHash, Revision: expectedRevision + 1, ReviewID: r.ID, ReportHash: r.ReportHash, ActorID: a.ID}
	_, err = tx.ExecContext(ctx, "INSERT INTO semantic_review_acceptances(id,project_id,target_kind,target_id,target_hash,revision,review_id,report_hash,actor_id) VALUES(?,?,?,?,?,?,?,?,?)", receipt.ID, project, receipt.TargetKind, receipt.TargetID, receipt.TargetHash, receipt.Revision, id, receipt.ReportHash, a.ID)
	if err != nil {
		return receipt, err
	}
	if err = audit(ctx, tx, a, "semantic.review.select", id); err != nil {
		return receipt, err
	}
	if err = s.Generations.IP.captureSemanticSourceTx(ctx, tx, project, id, receipt.ID); err != nil {
		return receipt, err
	}
	return receipt, tx.Commit()
}
