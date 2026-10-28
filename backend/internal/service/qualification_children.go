package service

import (
	"context"
	"database/sql"
	"novelstudio/internal/domain"
)

func bindEvaluationChildTx(ctx context.Context, tx *sql.Tx, project, run, step, child, kind string) error {
	var prior string
	if err := tx.QueryRowContext(ctx, "SELECT child_id FROM qualification_steps WHERE id=? AND run_id=? AND project_id=? FOR UPDATE", step, run, project).Scan(&prior); err != nil {
		return err
	}
	if prior != "" {
		if prior == child {
			return nil
		}
		return domain.ErrConflict
	}
	_, err := tx.ExecContext(ctx, "UPDATE qualification_steps SET child_id=?,child_kind=?,state='RUNNING' WHERE id=?", child, kind, step)
	return err
}

func replayEvaluationToolTx(ctx context.Context, tx *sql.Tx, project, id string, ref *EvaluationReference) (ToolTrial, error) {
	job, err := toolTrialTx(ctx, tx, project, id)
	if err != nil || ref == nil {
		return job, err
	}
	if job.Frozen.EvaluationID != ref.RunID || (job.Frozen.EvaluationStepID != "" && job.Frozen.EvaluationStepID != ref.StepID) {
		return job, domain.ErrConflict
	}
	if err = bindEvaluationChildTx(ctx, tx, project, ref.RunID, ref.StepID, id, "tool"); err != nil {
		return job, err
	}
	return job, tx.Commit()
}

// Older jobs could commit their child before the step link. Resolve only the
// server's exact actor/project/evaluation operation, never an HTTP supplied ID.
func (s *Qualifications) recoverEvaluationChildLinks(ctx context.Context, r QualificationRun) error {
	tx, err := s.IP.Workspace.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	a, err := s.IP.Workspace.actorTx(ctx, tx, r.ActorID)
	if err != nil {
		return err
	}
	if _, _, err = requestIdentityPolicyTx(ctx, tx, a.WorkspaceID); err != nil {
		return err
	}
	if err = lockReceiptProjectTx(ctx, tx, r.ProjectID); err != nil {
		return err
	}
	steps, err := scanQualificationSteps(ctx, tx, r.ID)
	if err != nil {
		return err
	}
	for _, step := range steps {
		if step.ChildID != "" {
			continue
		}
		var id, kind string
		if r.Frozen.Candidate.Kind == "tool" {
			var receipt requestIdentity
			receipt, err = requestIdentityTx(ctx, tx, a, "operations", "evaluation-"+step.ID, true)
			if err == sql.ErrNoRows {
				continue
			}
			if err != nil {
				return err
			}
			if receipt.Kind != "tool.trial" {
				return domain.ErrNotReady
			}
			id = receipt.ID
			job, e := toolTrialTx(ctx, tx, r.ProjectID, id)
			if e != nil {
				return e
			}
			if job.ActorID != r.ActorID || job.Frozen.EvaluationID != r.ID || (job.Frozen.EvaluationStepID != "" && job.Frozen.EvaluationStepID != step.ID) {
				return domain.ErrNotReady
			}
			kind = "tool"
		} else {
			err = tx.QueryRowContext(ctx, "SELECT g.id FROM qualification_calls q JOIN generation_tasks g ON g.call_id=q.call_id WHERE q.run_id=? AND q.step_id=? AND g.project_id=? AND g.actor_id=?", r.ID, step.ID, r.ProjectID, r.ActorID).Scan(&id)
			if err == sql.ErrNoRows {
				id, _, err = requestReceiptTx(ctx, tx, a, "generation_tasks", "evaluation-"+step.ID, true)
			}
			if err == sql.ErrNoRows {
				continue
			}
			if err != nil {
				return err
			}
			g, e := generationTx(ctx, tx, r.ProjectID, id)
			if e != nil {
				return e
			}
			trial := g.Frozen.Capability
			if trial == nil || trial.Mode != "EVALUATION" || trial.EvaluationID != r.ID || (trial.EvaluationStepID != "" && trial.EvaluationStepID != step.ID) {
				return domain.ErrNotReady
			}
			kind = "generation"
		}
		if err = bindEvaluationChildTx(ctx, tx, r.ProjectID, r.ID, step.ID, id, kind); err != nil {
			return err
		}
	}
	return tx.Commit()
}
