package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"novelstudio/internal/domain"
	"novelstudio/internal/models"
	"slices"
)

// RefreshReceipts performs local accounting only. It cannot issue a request,
// score a new candidate, change a report object or grant a qualification.
func (s *Qualifications) RefreshReceipts(ctx context.Context, id string) error {
	r, err := scanQualificationRun(s.IP.Workspace.DB.QueryRowContext(ctx, "SELECT "+qualificationRunColumns+" FROM qualification_runs WHERE id=?", id))
	if err != nil {
		return err
	}
	if slices.Contains([]string{"QUEUED", "RUNNING", "PAUSED"}, r.State) {
		return nil
	}
	if err = s.recoverEvaluationChildLinks(ctx, r); err != nil {
		return err
	}
	if slices.Contains([]string{"CANCELLED", "BLOCKED", "FAILED"}, r.State) {
		if _, err = s.cancelChildren(ctx, r); err != nil {
			return err
		}
	}
	tx, err := s.IP.Workspace.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = lockReceiptProjectTx(ctx, tx, r.ProjectID); err != nil {
		return err
	}
	steps, err := scanQualificationSteps(ctx, tx, id)
	if err != nil {
		return err
	}
	for _, step := range steps {
		if step.ChildID == "" {
			continue
		}
		if err = s.IP.captureEvaluationSourceTx(ctx, tx, r.ProjectID, "", step.ID, ""); err != nil {
			return err
		}
		metrics := step.Metrics
		state := step.State
		if step.ChildKind == "generation" {
			g, readErr := generationTx(ctx, tx, r.ProjectID, step.ChildID)
			if readErr != nil {
				return readErr
			}
			state = g.State
			if g.CallID == "" {
				state = "NOT_RUN"
			} else {
				var call Call
				var settlement string
				if err = tx.QueryRowContext(ctx, "SELECT id,state,reserved_micros,charged_micros,cancel_requested,output_hash,settlement_hash FROM model_calls WHERE id=?", g.CallID).Scan(&call.ID, &call.State, &call.Reserved, &call.Charged, &call.CancelRequested, &call.OutputHash, &settlement); err != nil {
					return err
				}
				metrics.Cost = call.Charged
				if !slices.Contains([]string{"RESERVED", "DISPATCHED", "UNKNOWN"}, call.State) {
					state = call.State
				}
				metrics.CostKnown = settlement != ""
				metrics.OutputHash = call.OutputHash
				// Cancellation suppresses adoptable output on the call, while the
				// exact accepted receipt still retains its diagnostic blob.
				receiptID := digest(call.ID + "\x00" + settlement)
				if metrics.OutputHash == "" && settlement != "" {
					var payload []byte
					e := tx.QueryRowContext(ctx, "SELECT payload FROM model_call_receipts WHERE id=? AND call_id=?", receiptID, call.ID).Scan(&payload)
					if e != nil && e != sql.ErrNoRows {
						return e
					}
					if e == nil {
						var receipt []json.RawMessage
						if json.Unmarshal(payload, &receipt) != nil || len(receipt) != 4 || json.Unmarshal(receipt[3], &metrics.OutputHash) != nil {
							return domain.ErrNotReady
						}
					}
				}
				if err = tx.QueryRowContext(ctx, "SELECT revision,(SELECT COUNT(*) FROM model_call_receipts WHERE call_id=?) FROM model_call_accounting WHERE call_id=?", call.ID, call.ID).Scan(&metrics.AccountingRevision, &metrics.ReceiptCount); err != nil {
					return err
				}
				if call.State == "UNKNOWN" || call.State == "DISPATCHED" {
					state = "UNKNOWN"
					metrics.UsageKnown = false
					metrics.CostKnown = false
				}
				if call.State == "RECONCILED" {
					state = "RECONCILED"
					metrics.UsageKnown = false
				}
				if call.State == "CANCELLED" && settlement == "" {
					state = "NOT_RUN"
					metrics.CostKnown = true
				}
				if metrics.OutputHash != "" {
					raw, e := s.IP.Blobs.ReadObject(ctx, r.ProjectID, metrics.OutputHash)
					if e != nil || digest(string(raw)) != metrics.OutputHash {
						return domain.ErrNotReady
					}
					var output models.Result
					if json.Unmarshal(raw, &output) != nil {
						return domain.ErrNotReady
					}
					metrics.Output = output.Text
					metrics.InputTokens = output.InputTokens
					metrics.OutputTokens = output.OutputTokens
					metrics.UsageKnown = true
				}
				if metrics.CostKnown && state != "NOT_RUN" && metrics.OutputHash != "" && !metrics.DurationKnown {
					if err = tx.QueryRowContext(ctx, "SELECT GREATEST(0,TIMESTAMPDIFF(MICROSECOND,g.created_at,c.created_at) DIV 1000) FROM generation_tasks g JOIN model_call_receipts c ON c.call_id=g.call_id WHERE g.id=? AND c.id=?", g.ID, receiptID).Scan(&metrics.DurationMS); err != nil {
						if err != sql.ErrNoRows {
							return err
						}
					} else {
						metrics.DurationKnown = true
					}
				}
			}
			// Preserve the independent checker result for a completed successful arm.
			if step.State == "FAILED_CHECK" && state == "SUCCEEDED" {
				state = step.State
			}
		} else {
			job, e := toolTrialTx(ctx, tx, r.ProjectID, step.ChildID)
			if e != nil {
				return e
			}
			state = job.State
			metrics.CostKnown = true
			if toolTrialActive(job) {
				state = "UNKNOWN"
			}
			if job.outputHash != "" {
				raw, e := s.IP.Blobs.ReadObject(ctx, r.ProjectID, job.outputHash)
				if e != nil || digest(string(raw)) != job.outputHash {
					return domain.ErrNotReady
				}
				var output ToolTrialOutput
				if json.Unmarshal(raw, &output) != nil {
					return domain.ErrNotReady
				}
				metrics.OutputHash = job.outputHash
				metrics.Output = output.Stdout
				metrics.DurationMS = job.ElapsedMS
				metrics.DurationKnown = true
				metrics.UsageKnown = true
			}
			if step.State == "FAILED_CHECK" && state == "SUCCEEDED" {
				state = step.State
			}
		}
		metrics.Reason = "当前执行回执和账单状态；原报告保持冻结，此刷新不重新授予资格"
		raw, _ := json.Marshal(metrics)
		if _, err = tx.ExecContext(ctx, "UPDATE qualification_steps SET state=?,metrics=? WHERE id=?", state, raw, step.ID); err != nil {
			return err
		}
		if err = s.IP.captureEvaluationSourceTx(ctx, tx, r.ProjectID, "", step.ID, ""); err != nil {
			return err
		}
	}
	return tx.Commit()
}
