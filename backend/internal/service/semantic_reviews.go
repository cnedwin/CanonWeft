package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"novelstudio/internal/domain"
)

type SemanticReviews struct {
	Generations *Generations
	Knowledge   *Knowledge
}

type SemanticReviewInput struct {
	OperationID       string `json:"operation_id"`
	TargetKind        string `json:"target_kind"`
	TargetID          string `json:"target_id"`
	TargetHash        string `json:"target_hash"`
	BranchID          string `json:"branch_id"`
	ReviewerBindingID string `json:"reviewer_binding_id"`
	PriceID           string `json:"price_id"`
	MaxOutputTokens   int    `json:"max_output_tokens"`
}

type SemanticReview struct {
	Retention         *RuntimeRetention `json:"retention,omitempty"`
	expiredMaterial   []byte
	ID                string                        `json:"id"`
	ProjectID         string                        `json:"-"`
	BranchID          string                        `json:"branch_id"`
	ActorID           string                        `json:"-"`
	OperationID       string                        `json:"operation_id"`
	RequestHash       string                        `json:"-"`
	TargetKind        string                        `json:"target_kind"`
	TargetID          string                        `json:"target_id"`
	TargetHash        string                        `json:"target_hash"`
	Frozen            domain.SemanticReviewFrozen   `json:"frozen"`
	GenerationID      string                        `json:"generation_id"`
	State             string                        `json:"state"`
	Verdict           string                        `json:"verdict"`
	Paused            bool                          `json:"paused"`
	CancelRequested   bool                          `json:"cancel_requested"`
	RestoredDisabled  bool                          `json:"restored_disabled"`
	ReportHash        string                        `json:"report_hash"`
	Reason            string                        `json:"reason"`
	EstimateMicros    int64                         `json:"estimate_micros"`
	EstimateCurrency  string                        `json:"estimate_currency"`
	SelectionRevision int64                         `json:"selection_revision"`
	Selected          bool                          `json:"selected"`
	SelectionReady    bool                          `json:"selection_ready"`
	Report            *domain.SemanticReviewReport  `json:"report,omitempty"`
	Anchors           []domain.SemanticReviewAnchor `json:"anchors,omitempty"`
}

const semanticReviewColumns = "id,project_id,branch_id,actor_id,operation_id,request_hash,target_kind,target_id,target_hash,frozen,COALESCE(generation_id,''),state,verdict,paused,cancel_requested,restored_disabled,report_hash,reason"

func scanSemanticReview(row interface{ Scan(...any) error }) (SemanticReview, error) {
	var r SemanticReview
	var frozen []byte
	err := row.Scan(&r.ID, &r.ProjectID, &r.BranchID, &r.ActorID, &r.OperationID, &r.RequestHash, &r.TargetKind, &r.TargetID, &r.TargetHash, &frozen, &r.GenerationID, &r.State, &r.Verdict, &r.Paused, &r.CancelRequested, &r.RestoredDisabled, &r.ReportHash, &r.Reason)
	if err == sql.ErrNoRows {
		return r, domain.ErrForbidden
	}
	if err != nil {
		return r, err
	}
	if err := rejectExpiredRuntime(frozen, "semantic_review_runs.frozen"); err != nil {
		r.expiredMaterial = frozen
		return r, err
	}
	if err = json.Unmarshal(frozen, &r.Frozen); err != nil || r.Frozen.Schema != 1 {
		return r, domain.ErrNotReady
	}
	return r, nil
}

func semanticReviewTx(ctx context.Context, tx *sql.Tx, project, id string) (SemanticReview, error) {
	run, err := scanSemanticReview(tx.QueryRowContext(ctx, "SELECT "+semanticReviewColumns+" FROM semantic_review_runs WHERE project_id=? AND id=? FOR UPDATE", project, id))
	if err == nil {
		err = requireRuntimeObjectTx(ctx, tx, project, "semantic_review_runs", id, "report_hash", run.ReportHash)
	}
	return run, err
}

func (s *SemanticReviews) document(ctx context.Context, r SemanticReview) (domain.SemanticReviewDocument, error) {
	var input domain.SemanticReviewDocument
	raw, err := s.Generations.IP.Blobs.ReadObject(ctx, r.ProjectID, r.Frozen.InputHash)
	if err != nil || digest(string(raw)) != r.Frozen.InputHash {
		return input, domain.ErrNotReady
	}
	if err = json.Unmarshal(raw, &input); err != nil || input.Schema != 1 || input.Target.ID != r.TargetID || input.Target.Kind != r.TargetKind || input.Target.Hash != r.TargetHash || input.Target.BranchID != r.BranchID || input.Target.ExpectedIPHead != r.Frozen.ExpectedIPHead {
		return input, domain.ErrNotReady
	}
	if r.Frozen.ContentScopeHash != "" {
		scope, e := domain.SemanticContentScopeHash(input)
		if e != nil || scope != r.Frozen.ContentScopeHash {
			return input, domain.ErrNotReady
		}
	}
	return input, nil
}

func (s *SemanticReviews) Read(ctx context.Context, a domain.Actor, project, id string) (SemanticReview, error) {
	tx, _, _, err := s.Generations.IP.begin(ctx, a, project, false)
	if err != nil {
		return SemanticReview{}, err
	}
	defer tx.Rollback()
	r, err := semanticReviewTx(ctx, tx, project, id)
	if err != nil {
		return r, err
	}
	r.EstimateMicros, r.EstimateCurrency, err = s.estimate(ctx, r)
	if err != nil {
		return r, err
	}
	if r.ReportHash != "" {
		input, readErr := s.document(ctx, r)
		if readErr != nil {
			return r, readErr
		}
		raw, readErr := s.Generations.IP.Blobs.ReadObject(ctx, project, r.ReportHash)
		if readErr != nil || digest(string(raw)) != r.ReportHash {
			return r, domain.ErrNotReady
		}
		report, readErr := domain.ParseSemanticReviewReport(raw, input)
		if readErr != nil {
			return r, domain.ErrNotReady
		}
		r.Report = &report
		cited := map[string]bool{}
		for _, issue := range report.Issues {
			for _, key := range issue.AnchorKeys {
				cited[key] = true
			}
		}
		for _, anchor := range input.Anchors {
			if cited[anchor.Key] {
				r.Anchors = append(r.Anchors, anchor)
			}
		}
	}
	var selected string
	err = tx.QueryRowContext(ctx, "SELECT review_id,revision FROM semantic_review_acceptances WHERE project_id=? AND target_kind=? AND target_id=? ORDER BY revision DESC LIMIT 1", project, r.TargetKind, r.TargetID).Scan(&selected, &r.SelectionRevision)
	if err != nil && err != sql.ErrNoRows {
		return r, err
	}
	r.Selected = selected == r.ID
	if r.Selected {
		r.SelectionReady = s.validateReportAuthorityTx(ctx, tx, r) == nil
	}
	return r, nil
}

func (s *SemanticReviews) List(ctx context.Context, a domain.Actor, project, branch, kind, target string) ([]SemanticReview, error) {
	tx, _, _, err := s.Generations.IP.begin(ctx, a, project, false)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err = branchTx(ctx, tx, project, branch); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, "SELECT "+semanticReviewColumns+" FROM semantic_review_runs WHERE project_id=? AND branch_id=? AND target_kind=? AND target_id=? ORDER BY created_at DESC,id LIMIT 100", project, branch, kind, target)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []SemanticReview{}
	for rows.Next() {
		r, e := scanSemanticReview(rows)
		if e != nil && !errors.Is(e, domain.ErrMaterialExpired) {
			return nil, e
		}
		result = append(result, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	var selected string
	var revision int64
	err = tx.QueryRowContext(ctx, "SELECT review_id,revision FROM semantic_review_acceptances WHERE project_id=? AND target_kind=? AND target_id=? ORDER BY revision DESC LIMIT 1", project, kind, target).Scan(&selected, &revision)
	if err != nil && err != sql.ErrNoRows {
		return nil, err
	}
	for i := range result {
		r := &result[i]
		if len(r.expiredMaterial) != 0 {
			marker, err := runtimeRetentionTx(ctx, tx, project, "semantic_review_runs", r.ID, "", r.expiredMaterial)
			if err != nil {
				return nil, err
			}
			*r = SemanticReview{ID: r.ID, ProjectID: project, BranchID: branch, TargetKind: kind, TargetID: target, State: r.State, Verdict: r.Verdict, Retention: marker}
			continue
		}
		result[i].Selected = result[i].ID == selected
		result[i].SelectionRevision = revision
	}
	return result, nil
}
