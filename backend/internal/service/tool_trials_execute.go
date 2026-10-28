package service

import (
	"context"
	"encoding/json"
	"errors"
	"novelstudio/internal/domain"
	"novelstudio/internal/isolation"
	"novelstudio/internal/objectwriter"
	"novelstudio/internal/tooltrial"
	"time"
)

func toolTrialGuardFailure(err error) bool {
	return errors.Is(err, domain.ErrForbidden) || errors.Is(err, domain.ErrUnauthorized) || errors.Is(err, domain.ErrConflict) || errors.Is(err, domain.ErrNotReady) || errors.Is(err, domain.ErrValidation)
}

func (s *ToolTrials) finish(ctx context.Context, job ToolTrial, state, reason string) (string, error) {
	tx, err := s.IP.Workspace.DB.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if err = lockReceiptProjectTx(ctx, tx, job.ProjectID); err != nil {
		return "", err
	}
	if err = captureDiagnosticSourceTx(ctx, tx, job.ProjectID, "tool_trials", job.ID); err != nil {
		return "", err
	}
	_, err = tx.ExecContext(ctx, "UPDATE tool_trials SET state=?,reason=? WHERE id=? AND lease_id=? AND state IN ('QUEUED','RUNNING')", state, reason, job.ID, job.lease)
	if err != nil {
		return "", err
	}
	if err = captureDiagnosticSourceTx(ctx, tx, job.ProjectID, "tool_trials", job.ID); err != nil {
		return "", err
	}
	var actual string
	err = tx.QueryRowContext(ctx, "SELECT state FROM tool_trials WHERE id=?", job.ID).Scan(&actual)
	if err != nil {
		return "", err
	}
	if job.Frozen.EvaluationID != "" {
		if err = qualificationReceiptOutboxTx(ctx, tx, job.ProjectID, job.Frozen.EvaluationID); err != nil {
			return "", err
		}
	}
	return actual, tx.Commit()
}
func (s *ToolTrials) Fail(ctx context.Context, id string) (string, error) {
	job, err := scanToolTrial(s.IP.Workspace.DB.QueryRowContext(ctx, "SELECT "+toolTrialColumns+" FROM tool_trials WHERE id=?", id))
	if err != nil {
		return "", err
	}
	return s.finish(ctx, job, "FAILED", "本地执行多次未完成；原资料保持不变，可重新准备试评")
}

// The watchdog checks authorization and revocation while the guest is running.
// It can only cancel. The final transaction repeats all guards before attaching output.
func (s *ToolTrials) watch(ctx context.Context, job ToolTrial, cancel context.CancelFunc, done chan<- error) {
	defer close(done)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if s.Heartbeat != nil {
				s.Heartbeat(ctx)
			}
			check, stop := context.WithTimeout(ctx, 3*time.Second)
			tx, _, _, err := s.IP.begin(check, domain.Actor{ID: job.ActorID}, job.ProjectID, true)
			if err == nil {
				current, readErr := toolTrialTx(check, tx, job.ProjectID, job.ID)
				err = readErr
				if err == nil && (current.State != "RUNNING" || current.lease != job.lease) {
					err = domain.ErrConflict
				}
				if err == nil {
					_, err = s.guardTx(check, tx, job, false)
				}
				tx.Rollback()
			}
			stop()
			if err != nil {
				// The runner also cancels this monitor on normal completion.
				// A query interrupted by that shutdown is not a database failure.
				if ctx.Err() != nil {
					return
				}
				done <- err
				cancel()
				return
			}
		}
	}
}
func (s *ToolTrials) Execute(ctx context.Context, id string) (string, error) {
	job, err := scanToolTrial(s.IP.Workspace.DB.QueryRowContext(ctx, "SELECT "+toolTrialColumns+" FROM tool_trials WHERE id=?", id))
	if err != nil {
		return "", err
	}
	if !toolTrialActive(job) {
		return job.State, nil
	}
	tx, _, _, err := s.IP.begin(ctx, domain.Actor{ID: job.ActorID}, job.ProjectID, true)
	if err != nil {
		if !toolTrialGuardFailure(err) {
			return "", err
		}
		return s.finish(ctx, job, "BLOCKED", "项目权限已变化，未执行工具")
	}
	current, err := toolTrialTx(ctx, tx, job.ProjectID, id)
	if err != nil {
		tx.Rollback()
		return "", err
	}
	if !toolTrialActive(current) {
		tx.Rollback()
		return current.State, nil
	}
	if current.Frozen.EvaluationID != "" {
		parent, readErr := qualificationRunTx(ctx, tx, current.ProjectID, current.Frozen.EvaluationID)
		if readErr != nil {
			tx.Rollback()
			return "", readErr
		}
		if parent.Paused && !parent.Cancelled {
			tx.Rollback()
			return "PAUSED", nil
		}
	}
	definition, err := s.guardTx(ctx, tx, current, true)
	if err != nil {
		tx.Rollback()
		if !toolTrialGuardFailure(err) {
			return "", err
		}
		return s.finish(ctx, current, "BLOCKED", "能力版本或来源已变更、撤销，未执行工具")
	}
	job = current
	job.lease = NewID()
	if err = captureDiagnosticSourceTx(ctx, tx, job.ProjectID, "tool_trials", id); err != nil {
		tx.Rollback()
		return "", err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE tool_trials SET state='RUNNING',lease_id=?,reason='' WHERE id=?", job.lease, id); err != nil {
		tx.Rollback()
		return "", err
	}
	if err = captureDiagnosticSourceTx(ctx, tx, job.ProjectID, "tool_trials", id); err != nil {
		tx.Rollback()
		return "", err
	}
	if err = tx.Commit(); err != nil {
		return "", err
	}
	raw, err := s.IP.Blobs.ReadObject(ctx, job.ProjectID, job.Frozen.InputHash)
	if err != nil || digest(string(raw)) != job.Frozen.InputHash {
		return s.finish(ctx, job, "BLOCKED", "已固定的试评输入不完整，未执行工具")
	}
	var request isolation.Request
	if json.Unmarshal(raw, &request) != nil {
		return s.finish(ctx, job, "BLOCKED", "已固定的试评输入无法读取，未执行工具")
	}
	runner, err := isolation.NewRunner(s.RuntimeRoot, job.Frozen.ImageHash)
	if err != nil {
		return s.finish(ctx, job, "BLOCKED", "受限 Linux 环境缺失或校验失败，未执行工具")
	}
	runContext, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go s.watch(runContext, job, cancel, done)
	started := time.Now()
	result, runErr := runner.Run(runContext, request)
	elapsed := time.Since(started).Milliseconds()
	cancel()
	watchErr := <-done
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	// Re-read even after cancellation or a runner error to distinguish a deliberate
	// cancel/revocation from failed execution and never attach a late result.
	tx, a, _, err := s.IP.begin(ctx, domain.Actor{ID: job.ActorID}, job.ProjectID, true)
	if err != nil {
		if !toolTrialGuardFailure(err) {
			return "", err
		}
		return s.finish(ctx, job, "BLOCKED", "项目权限已变化，结果未采用")
	}
	current, err = toolTrialTx(ctx, tx, job.ProjectID, id)
	if err != nil {
		tx.Rollback()
		return "", err
	}
	if current.State != "RUNNING" || current.lease != job.lease {
		tx.Rollback()
		return current.State, nil
	}
	if _, err = s.guardTx(ctx, tx, job, true); err != nil {
		tx.Rollback()
		if !toolTrialGuardFailure(err) {
			return "", err
		}
		return s.finish(ctx, job, "BLOCKED", "能力版本或来源已变化，结果未采用")
	}
	if runErr != nil {
		tx.Rollback()
		if watchErr != nil && !toolTrialGuardFailure(watchErr) {
			return "", watchErr
		}
		return s.finish(ctx, job, "FAILED", "隔离执行未完成；请检查运行环境或重新准备试评")
	}
	if watchErr != nil && !toolTrialGuardFailure(watchErr) {
		tx.Rollback()
		return "", watchErr
	}
	if result.ImageHash != job.Frozen.ImageHash {
		tx.Rollback()
		return s.finish(ctx, job, "BLOCKED", "执行环境版本与冻结版本不一致，结果未采用")
	}
	output := ToolTrialOutput{Result: result, Stdout: string(result.Stdout), Stderr: string(result.Stderr), Valid: tooltrial.ValidateOutput(definition, result) == nil}
	state, reason := "SUCCEEDED", "隔离试评与输出契约通过，仍未取得生产资格"
	if !output.Valid {
		state = "FAILED"
		reason = "工具退出、输出完整性或结果契约未通过；保留诊断，不授予资格"
	}
	payload, _ := json.Marshal(output)
	object, err := objectwriter.Write(ctx, tx, s.IP.Blobs, job.ProjectID, payload)
	if err != nil {
		tx.Rollback()
		return "", err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE tool_trials SET state=?,reason=?,output_hash=?,elapsed_ms=? WHERE id=? AND lease_id=?", state, reason, object.Hash, elapsed, id, job.lease); err != nil {
		tx.Rollback()
		return "", err
	}
	if err = audit(ctx, tx, a, "tool.result", id); err != nil {
		tx.Rollback()
		return "", err
	}
	if err = s.IP.captureToolSourceTx(ctx, tx, job.ProjectID, id); err != nil {
		tx.Rollback()
		return "", err
	}
	if job.Frozen.EvaluationID != "" {
		if err = qualificationReceiptOutboxTx(ctx, tx, job.ProjectID, job.Frozen.EvaluationID); err != nil {
			tx.Rollback()
			return "", err
		}
	}
	return state, tx.Commit()
}
