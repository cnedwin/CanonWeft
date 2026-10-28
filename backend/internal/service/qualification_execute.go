package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"novelstudio/internal/domain"
	"novelstudio/internal/models"
	"novelstudio/internal/objectwriter"
	"novelstudio/internal/qualification"
	"slices"
	"time"
)

type QualificationReport = qualification.QualificationReport

func reportComplete(report QualificationReport, suite qualification.Suite) bool {
	entry := suite.Public()
	if report.ProfessionalQualified || len(report.Steps) != len(entry.Cases)*entry.Repeats*3 {
		return false
	}
	seen := map[string]bool{}
	for _, step := range report.Steps {
		if !slices.Contains([]string{"no_method", "baseline", "candidate"}, step.Arm) || step.Repeat < 0 || step.Repeat >= entry.Repeats || step.ChildID == "" || !step.Metrics.UsageKnown || !step.Metrics.CostKnown || !step.Metrics.DurationKnown {
			return false
		}
		key := step.Arm + "/" + step.CaseID + "/" + string(rune('0'+step.Repeat))
		if seen[key] {
			return false
		}
		seen[key] = true
		validCase := false
		for _, c := range entry.Cases {
			if c.ID == step.CaseID {
				validCase = true
			}
		}
		if !validCase {
			return false
		}
		if !slices.Contains([]string{"SUCCEEDED", "FAILED_CHECK", "INVALID_OUTPUT", "REJECTED", "FAILED"}, step.State) {
			return false
		}
		if step.Arm == "candidate" && entry.ID != qualification.NovelSemanticSuiteID && (step.State != "SUCCEEDED" || !suite.Score(step.CaseID, []byte(step.Metrics.Output)).Passed || step.Metrics.OutputHash == "") {
			return false
		}
	}
	if entry.ID == qualification.NovelSemanticSuiteID {
		if report.Frozen.Suite.Hash != entry.Hash {
			return false
		}
		for _, arm := range suite.NovelSemanticMetrics(report.Steps).Arms {
			if arm.Arm == "candidate" {
				return arm.Passed
			}
		}
		return false
	}
	return true
}

func (s *Qualifications) finish(ctx context.Context, id, state, reason string) (string, error) {
	var project string
	if err := s.IP.Workspace.DB.QueryRowContext(ctx, "SELECT project_id FROM qualification_runs WHERE id=?", id).Scan(&project); err != nil {
		return "", err
	}
	tx, err := s.IP.Workspace.DB.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if err = lockReceiptProjectTx(ctx, tx, project); err != nil {
		return "", err
	}
	if err = captureDiagnosticSourceTx(ctx, tx, project, "qualification_runs", id); err != nil {
		return "", err
	}
	_, err = tx.ExecContext(ctx, "UPDATE qualification_runs SET state=?,reason=? WHERE id=? AND state IN ('QUEUED','RUNNING')", state, reason, id)
	if err != nil {
		return "", err
	}
	if err = captureDiagnosticSourceTx(ctx, tx, project, "qualification_runs", id); err != nil {
		return "", err
	}
	var actual string
	err = tx.QueryRowContext(ctx, "SELECT state FROM qualification_runs WHERE id=?", id).Scan(&actual)
	if err != nil {
		return "", err
	}
	if err = qualificationReceiptOutboxTx(ctx, tx, project, id); err != nil {
		return "", err
	}
	return actual, tx.Commit()
}

func (s *Qualifications) Fail(ctx context.Context, id string) (string, error) {
	r, err := scanQualificationRun(s.IP.Workspace.DB.QueryRowContext(ctx, "SELECT "+qualificationRunColumns+" FROM qualification_runs WHERE id=?", id))
	if err != nil {
		return "", err
	}
	if r.Cancelled {
		return "CANCELLED", s.RefreshReceipts(ctx, id)
	}
	return s.finish(ctx, id, "FAILED", "评测推进多次未完成；已有调用与费用保留，未知请求不会自动重发")
}

func (s *Qualifications) Advance(ctx context.Context, id string) (string, error) {
	r, err := scanQualificationRun(s.IP.Workspace.DB.QueryRowContext(ctx, "SELECT "+qualificationRunColumns+" FROM qualification_runs WHERE id=?", id))
	if err != nil {
		return "", err
	}
	if r.Cancelled {
		return "CANCELLED", s.RefreshReceipts(ctx, id)
	}
	if r.Paused {
		return "PAUSED", nil
	}
	if !slices.Contains([]string{"QUEUED", "RUNNING"}, r.State) {
		return r.State, s.RefreshReceipts(ctx, id)
	}
	tx, a, _, err := s.IP.begin(ctx, domain.Actor{ID: r.ActorID}, r.ProjectID, true)
	if err != nil {
		if toolTrialGuardFailure(err) {
			return s.finish(ctx, id, "BLOCKED", "当前操作者或项目权限已变化；未执行后续评测")
		}
		return "", err
	}
	r, err = qualificationRunTx(ctx, tx, r.ProjectID, id)
	if err != nil {
		tx.Rollback()
		return "", err
	}
	if r.Paused || r.Cancelled {
		tx.Rollback()
		return r.State, nil
	}
	if err = s.guardRunTx(ctx, tx, r, false); err != nil {
		tx.Rollback()
		if toolTrialGuardFailure(err) || errors.Is(err, sql.ErrNoRows) || errors.Is(err, ErrBudgetExceeded) || errors.Is(err, ErrBudgetClosed) {
			return s.finish(ctx, id, "BLOCKED", "基线、分支、模型配置、来源或评测政策已变化；评测已停止")
		}
		return "", err
	}
	r.Steps, err = scanQualificationSteps(ctx, tx, r.ID)
	if err != nil {
		tx.Rollback()
		return "", err
	}
	if err = captureDiagnosticSourceTx(ctx, tx, r.ProjectID, "qualification_runs", r.ID); err != nil {
		tx.Rollback()
		return "", err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE qualification_runs SET state='RUNNING',reason='' WHERE id=? AND state IN ('QUEUED','RUNNING')", r.ID); err != nil {
		tx.Rollback()
		return "", err
	}
	if err = captureDiagnosticSourceTx(ctx, tx, r.ProjectID, "qualification_runs", r.ID); err != nil {
		tx.Rollback()
		return "", err
	}
	if err = tx.Commit(); err != nil {
		return "", err
	}
	for _, step := range r.Steps {
		if slices.Contains([]string{"SUCCEEDED", "FAILED_CHECK", "INVALID_OUTPUT", "REJECTED", "FAILED"}, step.State) {
			continue
		}
		if step.ChildID == "" {
			return s.startStep(ctx, a, r, step)
		}
		return s.collectStep(ctx, a, r, step)
	}
	return s.finalize(ctx, a, r)
}

func (s *Qualifications) startStep(ctx context.Context, a domain.Actor, r QualificationRun, step QualificationStep) (string, error) {
	var input json.RawMessage
	for _, c := range r.Frozen.Suite.Cases {
		if c.ID == step.CaseID {
			input = c.Input
		}
	}
	if len(input) == 0 {
		return s.finish(ctx, r.ID, "BLOCKED", "固定的评测任务不存在，未执行")
	}
	version := r.Frozen.Candidate.ID
	if step.Arm == "baseline" {
		version = r.Frozen.Baseline.ID
	} else if step.Arm == "no_method" {
		version = r.Frozen.NoMethod.ID
	}
	ref := &EvaluationReference{RunID: r.ID, StepID: step.ID, Arm: step.Arm, CaseID: step.CaseID}
	if r.Frozen.Candidate.Kind == "tool" {
		job, err := s.toolService().Create(ctx, a, r.ProjectID, ToolTrialInput{OperationID: "evaluation-" + step.ID, VersionID: version, InputText: string(input), evaluation: ref})
		if err != nil {
			return s.stepError(ctx, r, err)
		}
		_ = job
	} else {
		job, err := s.generationService().Prepare(ctx, a, r.ProjectID, GenerationInput{BranchID: r.BranchID, ExpectedIPHead: r.Frozen.Input.ExpectedIPHead, Role: r.Frozen.Input.Role, PriceID: r.Frozen.Input.PriceID, OperationID: "evaluation-" + step.ID, Request: models.Request{Prompt: "server evaluation", MaxOutputTokens: r.Frozen.Input.MaxOutputTokens}, evaluation: ref})
		if err != nil {
			return s.stepError(ctx, r, err)
		}
		if _, err = s.generationService().Start(ctx, a, r.ProjectID, job.ID); err != nil {
			return s.stepError(ctx, r, err)
		}
	}
	return "RUNNING", nil
}

func (s *Qualifications) stepError(ctx context.Context, r QualificationRun, err error) (string, error) {
	if toolTrialGuardFailure(err) || errors.Is(err, ErrBudgetClosed) || errors.Is(err, ErrBudgetExceeded) {
		return s.finish(ctx, r.ID, "BLOCKED", "调用条件或费用授权未满足；未发送新请求，请检查配置和专用预算")
	}
	return "", err
}

func (s *Qualifications) collectStep(ctx context.Context, a domain.Actor, r QualificationRun, step QualificationStep) (string, error) {
	metrics := EvaluationMetrics{}
	state := ""
	if step.ChildKind == "generation" {
		job, err := s.generationService().Read(ctx, a, r.ProjectID, step.ChildID)
		if err != nil {
			return s.stepError(ctx, r, err)
		}
		if slices.Contains([]string{"PREPARED", "QUEUED", "RUNNING"}, job.State) {
			return "RUNNING", nil
		}
		if job.State == "UNKNOWN" {
			return s.blockStep(ctx, r, step, "UNKNOWN", "供应商回执未知；费用占用保留，禁止自动重发。请先对账")
		}
		if job.State == "BLOCKED" || job.State == "CANCELLED" {
			return s.blockStep(ctx, r, step, "NOT_RUN", "生成对照未执行或已取消；NOT_RUN，不能取得资格")
		}
		state = job.State
		if job.Call != nil {
			metrics.Cost = job.Call.Charged
			metrics.OutputHash = job.Call.OutputHash
			metrics.UsageKnown = job.Call.State != "UNKNOWN" && job.Call.State != "DISPATCHED"
			metrics.CostKnown = metrics.UsageKnown
		}
		if job.Output != nil {
			metrics.Output = job.Output.Text
			metrics.InputTokens = job.Output.InputTokens
			metrics.OutputTokens = job.Output.OutputTokens
		}
		if job.State == "INVALID_OUTPUT" && metrics.OutputHash != "" {
			raw, readErr := s.IP.Blobs.ReadObject(ctx, r.ProjectID, metrics.OutputHash)
			if readErr != nil || digest(string(raw)) != metrics.OutputHash {
				return "", domain.ErrNotReady
			}
			var diagnostic models.Result
			if json.Unmarshal(raw, &diagnostic) != nil {
				return "", domain.ErrNotReady
			}
			metrics.Output = diagnostic.Text
			metrics.InputTokens = diagnostic.InputTokens
			metrics.OutputTokens = diagnostic.OutputTokens
		}
		if job.State == "INVALID_OUTPUT" && metrics.OutputHash == "" {
			metrics.UsageKnown = false
			metrics.Reason = "实际费用已保留，但失败产物与原始使用量证据未取得"
		}
		if err = s.IP.Workspace.DB.QueryRowContext(ctx, "SELECT GREATEST(0,TIMESTAMPDIFF(MICROSECOND,g.created_at,c.updated_at) DIV 1000) FROM generation_tasks g JOIN model_calls c ON c.id=g.call_id WHERE g.id=?", job.ID).Scan(&metrics.DurationMS); err != nil {
			return "", err
		}
		metrics.DurationKnown = true
	} else {
		job, err := s.toolService().Read(ctx, a, r.ProjectID, step.ChildID)
		if err != nil {
			return s.stepError(ctx, r, err)
		}
		if toolTrialActive(job) {
			return "RUNNING", nil
		}
		if job.Stale || job.State == "BLOCKED" || job.State == "CANCELLED" {
			return s.blockStep(ctx, r, step, "NOT_RUN", "隔离对照不可用或已撤销；NOT_RUN，不能取得资格")
		}
		if job.Output == nil {
			return s.blockStep(ctx, r, step, "NOT_RUN", "隔离对照未取得实际执行回执；NOT_RUN，不能将环境故障当作已完成比较")
		}
		state = job.State
		metrics.DurationMS = job.ElapsedMS
		metrics.DurationKnown = true
		metrics.UsageKnown = true
		metrics.CostKnown = true
		metrics.OutputHash = job.outputHash
		if job.Output != nil {
			metrics.Output = job.Output.Stdout
			metrics.Reason = job.Output.Result.Reason
		}
	}
	suite, _ := qualification.Lookup(r.Frozen.Suite.ID)
	metrics.Score = suite.Score(step.CaseID, []byte(metrics.Output))
	if state != "SUCCEEDED" {
		metrics.Score.Passed = false
		metrics.Score.Checks = append(metrics.Score.Checks, qualification.Check{Code: "EXECUTION_COMPLETED", Passed: false, Detail: "外部执行未完整成功；结构符合也不能替代实际完成回执"})
	}
	if state == "SUCCEEDED" && !metrics.Score.Passed {
		state = "FAILED_CHECK"
	}
	metrics.Reason += "；自动流程未进行人工修复，manual_repairs=0；全部三臂使用独立上下文"
	err := s.saveStepMetrics(ctx, r, step, state, metrics)
	return "RUNNING", err
}

func (s *Qualifications) blockStep(ctx context.Context, r QualificationRun, step QualificationStep, state, reason string) (string, error) {
	metrics := EvaluationMetrics{Reason: reason, Score: qualification.Score{Checks: []qualification.Check{{Code: "EXECUTION_RECEIPT", Passed: false, Detail: reason}}}}
	if err := s.saveStepMetrics(ctx, r, step, state, metrics); err != nil {
		return "", err
	}
	return s.finish(ctx, r.ID, "BLOCKED", reason)
}

func (s *Qualifications) saveStepMetrics(ctx context.Context, run QualificationRun, step QualificationStep, state string, metrics EvaluationMetrics) error {
	tx, err := s.IP.Workspace.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = lockReceiptProjectTx(ctx, tx, run.ProjectID); err != nil {
		return err
	}
	var child string
	if err = tx.QueryRowContext(ctx, "SELECT child_id FROM qualification_steps WHERE id=? AND run_id=? AND project_id=? FOR UPDATE", step.ID, run.ID, run.ProjectID).Scan(&child); err != nil {
		return err
	}
	if child != step.ChildID {
		return domain.ErrConflict
	}
	if err = s.IP.captureEvaluationSourceTx(ctx, tx, run.ProjectID, "", step.ID, ""); err != nil {
		return err
	}
	raw, err := json.Marshal(metrics)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE qualification_steps SET state=?,metrics=? WHERE id=? AND child_id=?", state, raw, step.ID, step.ChildID); err != nil {
		return err
	}
	if err = s.IP.captureEvaluationSourceTx(ctx, tx, run.ProjectID, "", step.ID, ""); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Qualifications) finalize(ctx context.Context, a domain.Actor, r QualificationRun) (string, error) {
	suite, _ := qualification.Lookup(r.Frozen.Suite.ID)
	report := QualificationReport{RunID: r.ID, Frozen: r.Frozen, Steps: r.Steps, Boundary: "仅授予指定任务、清单、模型/工具配置和评测集的机械资格；未授予文学、专业泛化、视觉或试玩质量资格。"}
	for _, step := range r.Steps {
		report.Cost += step.Metrics.Cost
		report.DurationMS += step.Metrics.DurationMS
		report.ManualRepairs += step.Metrics.ManualRepairs
	}
	report.Passed = reportComplete(report, suite)
	report.NovelSemantics = suite.NovelSemanticMetrics(report.Steps)
	raw, _ := json.Marshal(report)
	tx, a, _, err := s.IP.begin(ctx, a, r.ProjectID, true)
	if err != nil {
		return s.stepError(ctx, r, err)
	}
	defer tx.Rollback()
	current, err := qualificationRunTx(ctx, tx, r.ProjectID, r.ID)
	if err != nil {
		return "", err
	}
	if current.Paused || current.Cancelled || !slices.Contains([]string{"QUEUED", "RUNNING"}, current.State) {
		return current.State, nil
	}
	if err = s.guardRunTx(ctx, tx, current, true); err != nil {
		return "", err
	}
	object, err := objectwriter.Write(ctx, tx, s.IP.Blobs, r.ProjectID, raw)
	if err != nil {
		return "", err
	}
	state, reason := "FAILED", "候选未通过全部独立契约与负例检查；保留全部三臂证据，不能启用"
	if r.Frozen.Suite.ID == qualification.NovelSemanticSuiteID {
		reason = "小说语义验收未满足完整性、精确率/召回率、严格证据或90%阈值；保留全部三组结果，不能启用"
	}
	if report.Passed {
		state = "PASSED"
		reason = "三臂真实执行与独立机械检查通过；专业质量仍未取得资格"
		if r.Frozen.Suite.ID == qualification.NovelSemanticSuiteID {
			reason = "200份小说工程场景的三组执行与90%语义/证据门通过；独立人工标注和文学质量仍待验"
		}
		if r.Frozen.Candidate.Kind == "tool" {
			state = "REVIEW_REQUIRED"
			reason = "独立机械检查通过；新增可执行依赖须由维护角色检查完整隔离证据后明确批准"
		}
	}
	if _, err = tx.ExecContext(ctx, "UPDATE qualification_runs SET state=?,reason=?,report_hash=? WHERE id=?", state, reason, object.Hash, r.ID); err != nil {
		return "", err
	}
	if err = s.IP.captureEvaluationSourceTx(ctx, tx, r.ProjectID, r.ID, "", ""); err != nil {
		return "", err
	}
	if state == "PASSED" {
		if _, err = s.publishQualificationTx(ctx, tx, a, r, object.Hash); err != nil {
			return "", err
		}
	}
	if err = tx.Commit(); err != nil {
		return "", err
	}
	return state, nil
}

func (s *Qualifications) publishQualificationTx(ctx context.Context, tx *sql.Tx, a domain.Actor, r QualificationRun, reportHash string) (string, error) {
	var existing string
	err := tx.QueryRowContext(ctx, "SELECT id FROM capability_qualifications WHERE run_id=?", r.ID).Scan(&existing)
	if err == nil {
		return existing, nil
	}
	if err != sql.ErrNoRows {
		return "", err
	}
	id := NewID()
	raw, _ := json.Marshal(r.Frozen)
	if _, err = tx.ExecContext(ctx, "INSERT INTO capability_qualifications(id,project_id,run_id,payload,report_hash,expires_at) VALUES(?,?,?,?,?,?)", id, r.ProjectID, r.ID, raw, reportHash, time.Now().UTC().Add(30*24*time.Hour)); err != nil {
		return "", err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE qualification_runs SET qualification_id=?,state='PASSED' WHERE id=?", id, r.ID); err != nil {
		return "", err
	}
	if err = audit(ctx, tx, a, "qualification.mechanical.passed", id); err != nil {
		return "", err
	}
	if err = s.IP.captureEvaluationSourceTx(ctx, tx, r.ProjectID, "", "", id); err != nil {
		return "", err
	}
	return id, nil
}

func (s *Qualifications) ApproveExecutable(ctx context.Context, a domain.Actor, project, id, reportHash, note string) (QualificationRun, error) {
	var r QualificationRun
	if !validName(note, 2000) {
		return r, domain.ErrValidation
	}
	tx, a, p, err := s.IP.begin(ctx, a, project, false)
	if err != nil {
		return r, err
	}
	defer tx.Rollback()
	if a.Role != "owner" || p.Role != "owner" {
		return r, domain.ErrForbidden
	}
	r, err = qualificationRunTx(ctx, tx, project, id)
	if err != nil {
		return r, err
	}
	if r.State != "REVIEW_REQUIRED" || r.ReportHash != reportHash || r.Frozen.Candidate.Kind != "tool" {
		return r, domain.ErrConflict
	}
	if err = s.guardRunTx(ctx, tx, r, true); err != nil {
		return r, err
	}
	raw, err := s.IP.Blobs.ReadObject(ctx, project, reportHash)
	if err != nil || digest(string(raw)) != reportHash {
		return r, domain.ErrNotReady
	}
	var report QualificationReport
	suite, _ := qualification.Lookup(r.Frozen.Suite.ID)
	if json.Unmarshal(raw, &report) != nil || !report.Passed || !reportComplete(report, suite) {
		return r, domain.ErrConflict
	}
	approval, _ := json.Marshal(qualification.ExecutableApproval{
		ReportHash: reportHash, ManifestHash: r.Frozen.CandidateManifest,
		RuntimeHash: r.Frozen.RuntimeHash, SuiteHash: r.Frozen.Suite.Hash,
		Note: note, Policy: qualification.ExecutableApprovalPolicy,
	})
	if _, err = tx.ExecContext(ctx, "INSERT INTO qualification_approvals(run_id,project_id,actor_id,payload) VALUES(?,?,?,?)", r.ID, project, a.ID, approval); err != nil {
		return r, err
	}
	r.QualificationID, err = s.publishQualificationTx(ctx, tx, a, r, reportHash)
	if err != nil {
		return r, err
	}
	r.State = "PASSED"
	r.Reason = "维护角色已批准固定执行依赖；仅可启用此机械用途资格"
	if _, err = tx.ExecContext(ctx, "UPDATE qualification_runs SET reason=? WHERE id=?", r.Reason, r.ID); err != nil {
		return r, err
	}
	if err = s.IP.captureApprovalSourceTx(ctx, tx, project, r.ID); err != nil {
		return r, err
	}
	return r, tx.Commit()
}
