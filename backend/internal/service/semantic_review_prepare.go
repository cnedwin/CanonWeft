package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"novelstudio/internal/domain"
	"novelstudio/internal/objectwriter"
	"slices"
)

func (s *SemanticReviews) Prepare(ctx context.Context, a domain.Actor, project string, input SemanticReviewInput) (SemanticReview, error) {
	var r SemanticReview
	if !validName(input.OperationID, 120) || !slices.Contains([]string{"ip_candidate", "artifact_release"}, input.TargetKind) || input.TargetID == "" || len(input.TargetHash) != 64 || input.MaxOutputTokens < 1 || input.MaxOutputTokens > 131072 {
		return r, domain.ErrValidation
	}
	raw, _ := json.Marshal(input)
	requestHash := digest("semantic.review\x00" + project + "\x00" + string(raw))
	tx, a, _, err := s.Generations.IP.begin(ctx, a, project, true)
	if err != nil {
		return r, err
	}
	defer tx.Rollback()
	var prior, priorHash string
	prior, priorHash, err = requestReceiptTx(ctx, tx, a, "semantic_review_runs", input.OperationID, false)
	if err == nil {
		if priorHash != requestHash {
			return r, domain.ErrConflict
		}
		tx.Rollback()
		return s.Read(ctx, a, project, prior)
	}
	if err != sql.ErrNoRows {
		return r, err
	}
	doc, sources, err := s.targetDocumentTx(ctx, tx, project, input)
	if err != nil {
		return r, err
	}
	documentRaw, err := json.Marshal(doc)
	if err != nil || len(documentRaw) > 1024*1024 {
		return r, domain.ErrValidation
	}
	object, err := objectwriter.Write(ctx, tx, s.Generations.IP.Blobs, project, documentRaw)
	if err != nil {
		return r, err
	}
	r = SemanticReview{ID: NewID(), ProjectID: project, ActorID: a.ID, BranchID: input.BranchID, OperationID: input.OperationID, RequestHash: requestHash, TargetKind: input.TargetKind, TargetID: input.TargetID, TargetHash: input.TargetHash, State: "PREPARED", Verdict: "PENDING", Frozen: domain.SemanticReviewFrozen{Schema: 1, InputHash: object.Hash, ExpectedIPHead: doc.Target.ExpectedIPHead, SourceGenerationIDs: sources, ReviewerBindingID: input.ReviewerBindingID, PriceID: input.PriceID, MaxOutputTokens: input.MaxOutputTokens, Scope: doc.Scope}}
	r.Frozen.ContentScopeHash, err = domain.SemanticContentScopeHash(doc)
	if err != nil {
		return r, err
	}
	if doc.Delta != nil {
		deltaRaw, _ := json.Marshal(doc.Delta)
		r.Frozen.DeltaHash = digest(string(deltaRaw))
	}
	if input.ReviewerBindingID == "" || input.PriceID == "" {
		r.State = "BLOCKED"
		r.Reason = "已保存待审内容；请在能力页启用独立语义审查用途，并配置审查模型和价格后重新准备。"
	}
	frozen, _ := json.Marshal(r.Frozen)
	_, err = tx.ExecContext(ctx, "INSERT INTO semantic_review_runs(id,project_id,branch_id,actor_id,operation_id,request_hash,target_kind,target_id,target_hash,frozen,state,verdict,reason) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)", r.ID, project, input.BranchID, a.ID, input.OperationID, requestHash, r.TargetKind, r.TargetID, r.TargetHash, string(frozen), r.State, r.Verdict, r.Reason)
	if err != nil {
		return r, err
	}
	if err = audit(ctx, tx, a, "semantic.review.prepare", r.ID); err != nil {
		return r, err
	}
	if err = s.Generations.IP.captureSemanticSourceTx(ctx, tx, project, r.ID, ""); err != nil {
		return r, err
	}
	if err = tx.Commit(); err != nil {
		return r, err
	}
	if r.State == "PREPARED" {
		child, childErr := s.prepareChild(ctx, r)
		if childErr != nil {
			_, stateErr := s.setState(ctx, r, "BLOCKED", reviewBlockedReason(childErr))
			if stateErr != nil {
				return r, stateErr
			}
		} else {
			r.GenerationID = child.ID
		}
	}
	return s.Read(ctx, a, project, r.ID)
}

func (s *SemanticReviews) Start(ctx context.Context, a domain.Actor, project, id string) (SemanticReview, error) {
	tx, a, _, err := s.Generations.IP.begin(ctx, a, project, true)
	if err != nil {
		return SemanticReview{}, err
	}
	defer tx.Rollback()
	r, err := semanticReviewTx(ctx, tx, project, id)
	if err != nil {
		return r, err
	}
	if a.ID != r.ActorID {
		return r, domain.ErrForbidden
	}
	if r.RestoredDisabled || r.CancelRequested || r.State == "UNKNOWN" || r.State == "CANCELLED" || r.State == "FAILED" {
		return r, domain.ErrConflict
	}
	if r.State == "COMPLETED" || r.State == "RUNNING" || r.State == "QUEUED" {
		return r, nil
	}
	if r.Frozen.ReviewerBindingID == "" || r.Frozen.PriceID == "" {
		return r, domain.ErrNotReady
	}
	if err = s.validateTargetTx(ctx, tx, r); err != nil {
		return r, err
	}
	r.State = "QUEUED"
	r.Reason = ""
	r.Paused = false
	if err = captureDiagnosticSourceTx(ctx, tx, project, "semantic_review_runs", id); err != nil {
		return r, err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE semantic_review_runs SET state='QUEUED',reason='',paused=FALSE,updated_at=UTC_TIMESTAMP(6) WHERE id=?", id); err != nil {
		return r, err
	}
	if err = captureDiagnosticSourceTx(ctx, tx, project, "semantic_review_runs", id); err != nil {
		return r, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO outbox(id,project_id,kind,resource_id,payload) VALUES(?,?,'semantic.review.start',?,?) ON DUPLICATE KEY UPDATE processed_at=NULL", NewID(), project, id, `{}`); err != nil {
		return r, err
	}
	if err = audit(ctx, tx, a, "semantic.review.start", id); err != nil {
		return r, err
	}
	return r, tx.Commit()
}
