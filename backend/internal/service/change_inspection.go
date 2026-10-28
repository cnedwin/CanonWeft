package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"slices"

	"novelstudio/internal/domain"
	"novelstudio/internal/models"
)

type ChangeInspectionInput struct {
	BranchID        string   `json:"branch_id"`
	ExpectedIPHead  string   `json:"expected_ip_head"`
	ResourceIDs     []string `json:"resource_ids"`
	BindingID       string   `json:"binding_id"`
	PriceID         string   `json:"price_id"`
	MaxOutputTokens int      `json:"max_output_tokens"`
	OperationID     string   `json:"operation_id"`
}

type ChangeInspectionItem struct {
	Ref    domain.DependencyRef         `json:"ref"`
	Name   string                       `json:"name"`
	State  string                       `json:"state"`
	Issues []domain.SemanticReviewIssue `json:"issues"`
}

type ChangeInspection struct {
	GenerationID string                        `json:"generation_id"`
	PlanID       string                        `json:"plan_id"`
	State        string                        `json:"state"`
	Current      bool                          `json:"current"`
	Counted      bool                          `json:"counted"`
	Reason       string                        `json:"reason"`
	Scope        string                        `json:"scope"`
	Items        []ChangeInspectionItem        `json:"items"`
	Report       *domain.SemanticReviewReport  `json:"report,omitempty"`
	Anchors      []domain.SemanticReviewAnchor `json:"anchors,omitempty"`
}

func (s *Generations) PrepareChangeInspection(ctx context.Context, a domain.Actor, project, plan string, input ChangeInspectionInput) (Generation, error) {
	if plan == "" || input.BindingID == "" || len(input.ResourceIDs) == 0 || len(input.ResourceIDs) > 16 {
		return Generation{}, domain.ErrValidation
	}
	ids := slices.Clone(input.ResourceIDs)
	slices.Sort(ids)
	for i, id := range ids {
		if id == "" || (i > 0 && id == ids[i-1]) {
			return Generation{}, domain.ErrValidation
		}
	}
	return s.Prepare(ctx, a, project, GenerationInput{
		inspectionTargets: ids, ChangePlanID: plan, BranchID: input.BranchID, ExpectedIPHead: input.ExpectedIPHead,
		Role: "critic", TaskKind: "shared.change-impact", BindingID: input.BindingID, PriceID: input.PriceID, OperationID: input.OperationID,
		Request: models.Request{Prompt: "独立检查固定变更对本批资料的影响。比较变更前后与原文，逐项覆盖且只引用给定锚点。不能确定则UNKNOWN，不能宣称批次外内容已检查；只输出审查JSON，不改写或发布。", MaxOutputTokens: input.MaxOutputTokens},
	})
}

// Reports are read against live authority, not accepted as release approvals.
// Stale or revoked runs stay visible as unchecked; no historical PASS is reused.
func (s *Generations) ReadChangeInspection(ctx context.Context, a domain.Actor, project, id string) (ChangeInspection, error) {
	tx, a, _, err := s.IP.begin(ctx, a, project, false)
	if err != nil {
		return ChangeInspection{}, err
	}
	defer tx.Rollback()
	return s.readChangeInspectionTx(ctx, tx, a, project, id)
}

func (s *Generations) readChangeInspectionTx(ctx context.Context, tx *sql.Tx, a domain.Actor, project, id string) (ChangeInspection, error) {
	result := ChangeInspection{GenerationID: id, Scope: domain.ChangeInspectionScope, Items: []ChangeInspectionItem{}}
	g, err := generationTx(ctx, tx, project, id)
	if err != nil {
		return result, err
	}
	if !canControlGeneration(a, g) {
		return result, domain.ErrForbidden
	}
	use := g.Frozen.Capability
	if g.Frozen.Role != "critic" || g.Frozen.ChangePlanID == "" || g.Frozen.SemanticReviewID != "" || use == nil || use.Mode != "QUALIFIED" || use.TaskKind != "shared.change-impact" {
		return result, domain.ErrValidation
	}
	result.State, result.PlanID = g.State, g.Frozen.ChangePlanID
	raw, err := s.IP.Blobs.ReadObject(ctx, project, g.Frozen.InputHash)
	if err != nil || digest(string(raw)) != g.Frozen.InputHash {
		return result, domain.ErrNotReady
	}
	var request models.Request
	if json.Unmarshal(raw, &request) != nil {
		return result, domain.ErrNotReady
	}
	source, actual, err := domain.ParseChangeInspectionInput(request.Prompt)
	if err != nil || source.Plan.ID != result.PlanID {
		return result, domain.ErrValidation
	}
	plan, err := readChangePlanTx(ctx, tx, project, result.PlanID)
	if err != nil {
		return result, err
	}
	if valueHash(plan) != valueHash(source.Plan) {
		return result, domain.ErrConflict
	}
	branch, head := source.Plan.BranchID, source.Plan.BaseHead
	if source.FocusBranchID != "" {
		branch, head = source.FocusBranchID, source.FocusBaseHead
	}
	if branch != g.BranchID || head != g.Frozen.BaseHead {
		return result, domain.ErrConflict
	}
	snapshot, err := snapshotTx(ctx, tx, project, head)
	if err != nil {
		return result, err
	}
	doc, err := domain.BuildChangeInspectionDocument(source, snapshot)
	if err != nil || valueHash(doc) != valueHash(actual) {
		return result, domain.ErrConflict
	}
	for _, target := range source.Targets {
		result.Items = append(result.Items, ChangeInspectionItem{Ref: target.Ref, Name: target.Name, State: "UNCHECKED", Issues: []domain.SemanticReviewIssue{}})
	}
	result.Anchors = doc.Anchors
	result.Reason = "资料版本、执行权限或审查资格已改变；本结果不计入当前覆盖。"
	author, err := s.IP.releaseSourceActorTx(ctx, tx, g.ActorID)
	if err != nil {
		return result, nil
	}
	permission, err := projectTx(ctx, tx, author, project)
	if err != nil || !domain.CanEdit(author.Role) || !domain.CanEdit(permission.Role) {
		return result, nil
	}
	if s.IP.lockReleaseQualificationActorTx(ctx, tx, g) != nil || s.validateFrozen(ctx, tx, g) != nil {
		return result, nil
	}
	result.Current = true
	result.Reason = "任务尚未产生可计入覆盖的完整审查。"
	if g.State != "SUCCEEDED" || g.Cancelled || g.Paused || g.CallID == "" {
		return result, nil
	}
	call, err := callTx(ctx, tx, g.WorkspaceID, project, g.CallID)
	if err != nil {
		return result, err
	}
	if call.State != "SUCCEEDED" || call.CancelRequested || call.OutputHash == "" {
		return result, domain.ErrConflict
	}
	expired, err := runtimeObjectRetentionTx(ctx, tx, project, "model_calls", call.ID, "output_hash", call.OutputHash)
	if err != nil {
		return result, err
	}
	if expired != nil {
		return result, domain.ErrMaterialExpired
	}
	var actor, operation, fingerprint string
	err = tx.QueryRowContext(ctx, "SELECT actor_id,operation_id,input_hash FROM model_calls WHERE id=? AND project_id=?", g.CallID, project).Scan(&actor, &operation, &fingerprint)
	if err != nil {
		return result, err
	}
	if actor != g.ActorID || operation != "generation-"+g.ID || fingerprint != modelCallFingerprint(project, g.Frozen.Estimate, g.Frozen.Price.Currency, g.Frozen.InputHash) {
		return result, domain.ErrConflict
	}
	raw, err = s.IP.Blobs.ReadObject(ctx, project, call.OutputHash)
	if err != nil || digest(string(raw)) != call.OutputHash {
		return result, domain.ErrNotReady
	}
	var output models.Result
	if json.Unmarshal(raw, &output) != nil {
		return result, domain.ErrNotReady
	}
	report, err := domain.ParseSemanticReviewReport([]byte(output.Text), doc)
	if err != nil {
		result.Reason = "审查输出格式、覆盖或证据不完整；尚不能计入覆盖。"
		return result, nil
	}
	result.Counted, result.Report, result.Reason = true, &report, "仅计入本批固定资料的审查记录；发现问题和不确定不代表已修复。"
	for i := range result.Items {
		item := &result.Items[i]
		key := "resource:" + item.Ref.ID
		item.State = "CHECKED_NO_ISSUE_REPORTED"
		if report.Verdict == "FAIL" {
			item.State = "CHECKED_WITH_FINDINGS"
		}
		if report.Verdict == "UNKNOWN" {
			item.State = "UNCERTAIN"
		}
		for _, issue := range report.Issues {
			if issue.TargetKey == key || slices.Contains(issue.AnchorKeys, key) || issue.TargetKey == "change" {
				item.Issues = append(item.Issues, issue)
				if item.State != "UNCERTAIN" {
					item.State = "CHECKED_WITH_FINDINGS"
				}
				if issue.Severity == "unknown" || issue.Category == "unsupported" {
					item.State = "UNCERTAIN"
				}
			}
		}
	}
	return result, nil
}
