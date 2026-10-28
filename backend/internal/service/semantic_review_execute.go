package service

import (
	"context"
	"encoding/json"
	"errors"
	"novelstudio/internal/domain"
	"novelstudio/internal/objectwriter"
)

func (s *SemanticReviews) setState(ctx context.Context, r SemanticReview, state, reason string) (string, error) {
	tx, err := s.Generations.IP.Workspace.DB.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if err = lockReceiptProjectTx(ctx, tx, r.ProjectID); err != nil {
		return "", err
	}
	current, err := semanticReviewTx(ctx, tx, r.ProjectID, r.ID)
	if err != nil {
		return "", err
	}
	if current.CancelRequested || current.RestoredDisabled {
		return "CANCELLED", nil
	}
	if current.State == "COMPLETED" {
		return current.State, nil
	}
	if err = captureDiagnosticSourceTx(ctx, tx, r.ProjectID, "semantic_review_runs", r.ID); err != nil {
		return "", err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE semantic_review_runs SET state=?,reason=?,updated_at=UTC_TIMESTAMP(6) WHERE id=?", state, reason, r.ID); err != nil {
		return "", err
	}
	if err = captureDiagnosticSourceTx(ctx, tx, r.ProjectID, "semantic_review_runs", r.ID); err != nil {
		return "", err
	}
	return state, tx.Commit()
}

func reviewBlockedReason(err error) string {
	switch {
	case errors.Is(err, ErrBudgetClosed):
		return "费用开关尚未开启；候选与审查请求已保留。检查预算后明确继续。"
	case errors.Is(err, domain.ErrForbidden):
		return "审查作者或来源权限已变化；请恢复权限或由有权限的成员重新准备审查。"
	case errors.Is(err, domain.ErrConflict):
		return "固定目标、模型或资格已变化；请查看当前候选并重新准备审查。"
	default:
		return "审查条件尚未就绪；请检查独立审查资格、模型、价格与预算后继续。"
	}
}

// Advance only prepares/starts the existing Generation child or records its
// result. Provider execution belongs exclusively to GenerationWorkflow.
func (s *SemanticReviews) Advance(ctx context.Context, id string) (string, error) {
	r, err := scanSemanticReview(s.Generations.IP.Workspace.DB.QueryRowContext(ctx, "SELECT "+semanticReviewColumns+" FROM semantic_review_runs WHERE id=?", id))
	if err != nil {
		return "", err
	}
	actor := domain.Actor{ID: r.ActorID}
	if r.CancelRequested || r.RestoredDisabled || r.State == "CANCELLED" {
		if r.GenerationID != "" {
			if _, err = s.Generations.Control(ctx, actor, r.ProjectID, r.GenerationID, "cancel"); err != nil && !errors.Is(err, domain.ErrForbidden) {
				return "", err
			}
			// Receipt recovery is system bookkeeping and remains available after
			// an author is disabled; it never authorizes or resends a request.
			if _, err = s.Generations.Recover(ctx, r.GenerationID); err != nil {
				return "", err
			}
		}
		return "CANCELLED", nil
	}
	if r.State == "COMPLETED" || r.State == "FAILED" || r.State == "PREPARED" || r.State == "BLOCKED" {
		return r.State, nil
	}
	if r.Paused {
		return "PAUSED", nil
	}
	if r.State == "UNKNOWN" && r.GenerationID != "" {
		if _, err = s.Generations.Recover(ctx, r.GenerationID); err != nil {
			return "", err
		}
	}
	child, err := s.prepareChild(ctx, r)
	if err != nil {
		return s.setState(ctx, r, "BLOCKED", reviewBlockedReason(err))
	}
	if child.State == "PREPARED" {
		child, err = s.Generations.Start(ctx, actor, r.ProjectID, child.ID)
		if err != nil {
			return s.setState(ctx, r, "BLOCKED", reviewBlockedReason(err))
		}
	}
	switch child.State {
	case "QUEUED", "RUNNING":
		return s.setState(ctx, r, "RUNNING", "")
	case "UNKNOWN":
		return s.setState(ctx, r, "UNKNOWN", "请求结果未知；保留费用占用，不自动重发。核对服务商回执后恢复本地记录。")
	case "SUCCEEDED":
		r.GenerationID = child.ID
		return s.finalize(ctx, r, child)
	case "PREPARED":
		return "QUEUED", nil
	default:
		return s.setState(ctx, r, "FAILED", "独立生成未成功完成，未授予审查通过。请查看执行记录并重新准备。")
	}
}

func (s *SemanticReviews) finalize(ctx context.Context, r SemanticReview, g Generation) (string, error) {
	if g.Output == nil {
		return s.setState(ctx, r, "BLOCKED", "审查输出尚不可读取；请恢复对象存储或核对执行回执。")
	}
	doc, err := s.document(ctx, r)
	if err != nil {
		return "", err
	}
	report, err := domain.ParseSemanticReviewReport([]byte(g.Output.Text), doc)
	if err != nil {
		return s.setState(ctx, r, "FAILED", "审查报告与固定目标、引用或覆盖契约不符；未授予通过。")
	}
	tx, _, _, err := s.Generations.IP.begin(ctx, domain.Actor{ID: r.ActorID}, r.ProjectID, true)
	if err != nil {
		return s.setState(ctx, r, "BLOCKED", reviewBlockedReason(err))
	}
	defer tx.Rollback()
	current, err := semanticReviewTx(ctx, tx, r.ProjectID, r.ID)
	if err != nil {
		return "", err
	}
	if current.CancelRequested || current.RestoredDisabled {
		return "CANCELLED", nil
	}
	if current.Paused {
		return "PAUSED", nil
	}
	if current.State == "COMPLETED" {
		return "COMPLETED", nil
	}
	if current.GenerationID != g.ID {
		return "", domain.ErrConflict
	}
	if err = s.validateTargetTx(ctx, tx, current); err != nil {
		return "", err
	}
	locked, err := generationTx(ctx, tx, r.ProjectID, g.ID)
	if err != nil {
		return "", err
	}
	if locked.Cancelled || locked.State != "SUCCEEDED" || locked.Frozen.SemanticReviewID != r.ID {
		return "", domain.ErrConflict
	}
	if err = s.Generations.validateFrozen(ctx, tx, locked); err != nil {
		return "", err
	}
	object, err := objectwriter.Write(ctx, tx, s.Generations.IP.Blobs, r.ProjectID, []byte(g.Output.Text))
	if err != nil {
		return "", err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE semantic_review_runs SET state='COMPLETED',verdict=?,report_hash=?,reason='',updated_at=UTC_TIMESTAMP(6) WHERE id=?", report.Verdict, object.Hash, r.ID); err != nil {
		return "", err
	}
	if err = s.Generations.IP.captureSemanticSourceTx(ctx, tx, r.ProjectID, r.ID, ""); err != nil {
		return "", err
	}
	return "COMPLETED", tx.Commit()
}

func (s *SemanticReviews) Fail(ctx context.Context, id string) (string, error) {
	r, err := scanSemanticReview(s.Generations.IP.Workspace.DB.QueryRowContext(ctx, "SELECT "+semanticReviewColumns+" FROM semantic_review_runs WHERE id=?", id))
	if err != nil {
		return "", err
	}
	return s.setState(ctx, r, "BLOCKED", "任务暂未完成；固定输入与已有执行保留，可检查后继续，不会重复发送未知请求。")
}

// The estimate is the linked child's existing frozen quote, not another budget.
func (s *SemanticReviews) estimate(ctx context.Context, r SemanticReview) (int64, string, error) {
	if r.GenerationID == "" {
		return 0, "", nil
	}
	var frozen []byte
	if err := s.Generations.IP.Workspace.DB.QueryRowContext(ctx, "SELECT frozen FROM generation_tasks WHERE id=? AND project_id=?", r.GenerationID, r.ProjectID).Scan(&frozen); err != nil {
		return 0, "", err
	}
	var value GenerationFrozen
	if json.Unmarshal(frozen, &value) != nil {
		return 0, "", domain.ErrNotReady
	}
	return value.Estimate, value.Price.Currency, nil
}
