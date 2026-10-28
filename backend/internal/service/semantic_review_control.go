package service

import (
	"context"
	"novelstudio/internal/domain"
	"slices"
)

func (s *SemanticReviews) Control(ctx context.Context, a domain.Actor, project, id, action string) (SemanticReview, error) {
	if !slices.Contains([]string{"pause", "resume", "cancel"}, action) {
		return SemanticReview{}, domain.ErrValidation
	}
	tx, a, _, err := s.Generations.IP.begin(ctx, a, project, true)
	if err != nil {
		return SemanticReview{}, err
	}
	defer tx.Rollback()
	r, err := semanticReviewTx(ctx, tx, project, id)
	if err != nil {
		return r, err
	}
	if a.ID != r.ActorID && a.Role != "owner" {
		return r, domain.ErrForbidden
	}
	if r.RestoredDisabled {
		return r, domain.ErrConflict
	}
	if r.State == "CANCELLED" || r.State == "COMPLETED" || r.State == "FAILED" {
		return r, nil
	}
	if action == "cancel" {
		r.CancelRequested = true
		r.Paused = false
		r.State = "CANCELLED"
		r.Reason = "已取消；保留费用与已有报告，晚到输出不能采用。"
	} else {
		if action == "resume" {
			if err = s.validateTargetTx(ctx, tx, r); err != nil {
				return r, err
			}
		}
		r.Paused = action == "pause"
	}
	if err = captureDiagnosticSourceTx(ctx, tx, project, "semantic_review_runs", id); err != nil {
		return r, err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE semantic_review_runs SET state=?,reason=?,paused=?,cancel_requested=?,updated_at=UTC_TIMESTAMP(6) WHERE id=?", r.State, r.Reason, r.Paused, r.CancelRequested, id); err != nil {
		return r, err
	}
	if err = captureDiagnosticSourceTx(ctx, tx, project, "semantic_review_runs", id); err != nil {
		return r, err
	}
	if r.GenerationID != "" && action == "cancel" {
		if _, err = tx.ExecContext(ctx, "UPDATE generation_tasks SET cancel_requested=TRUE,paused=FALSE WHERE id=? AND project_id=?", r.GenerationID, project); err != nil {
			return r, err
		}
		if _, err = tx.ExecContext(ctx, "UPDATE model_calls c JOIN generation_tasks g ON g.call_id=c.id SET c.cancel_requested=TRUE WHERE g.id=? AND g.project_id=?", r.GenerationID, project); err != nil {
			return r, err
		}
	}
	if err = audit(ctx, tx, a, "semantic.review."+action, id); err != nil {
		return r, err
	}
	return r, tx.Commit()
}
