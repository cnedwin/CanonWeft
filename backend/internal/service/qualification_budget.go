package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"novelstudio/internal/domain"
	"novelstudio/internal/qualification"
	"time"
)

// Only Schedule can create this reference. It is not a public input field.
type qualificationScheduleRef = qualification.ScheduleRef
type QualificationWeeklyGrant = qualification.QualificationWeeklyGrant
type qualificationWeekBalance struct {
	QualificationWeeklyGrant
	Currency        string
	Reserved, Spent int64
	Enabled         bool
}

func qualificationWeekTx(ctx context.Context, tx *sql.Tx, project, week string) (qualificationWeekBalance, error) {
	var b qualificationWeekBalance
	err := tx.QueryRowContext(ctx, "SELECT id,schedule_id,schedule_revision,week_key,cap_micros,currency,reserved_micros,spent_micros,enabled FROM qualification_week_grants WHERE project_id=? AND week_key=? FOR UPDATE", project, week).Scan(&b.ID, &b.ScheduleID, &b.Revision, &b.Week, &b.Cap, &b.Currency, &b.Reserved, &b.Spent, &b.Enabled)
	return b, err
}

func scheduleAuthorityTx(ctx context.Context, tx *sql.Tx, actor, project string, ref qualificationScheduleRef) (QualificationSchedule, error) {
	s, err := scanQualificationSchedule(tx.QueryRowContext(ctx, "SELECT id,project_id,actor_id,config,enabled,revision,last_week,reason FROM qualification_schedules WHERE id=? AND project_id=? FOR UPDATE", ref.ID, project))
	if err != nil {
		return s, err
	}
	if !s.Enabled || s.ActorID != actor || s.Revision != ref.Revision || ref.Week != QualificationWeek(time.Now()) {
		return s, domain.ErrConflict
	}
	var role, projectRole string
	err = tx.QueryRowContext(ctx, "SELECT u.role,IF(u.role='owner','owner',m.role) FROM users u LEFT JOIN project_members m ON m.user_id=u.id AND m.project_id=? WHERE u.id=? AND u.enabled=TRUE", project, actor).Scan(&role, &projectRole)
	if err != nil {
		return s, err
	}
	if role != "owner" || projectRole != "owner" {
		return s, domain.ErrForbidden
	}
	return s, nil
}

func (s *Qualifications) freezeWeeklyTx(ctx context.Context, tx *sql.Tx, actor domain.Actor, project string, q *QualificationQuote, ref *qualificationScheduleRef, create bool) error {
	if ref == nil {
		return nil
	}
	if _, err := budgetTx(ctx, tx, actor.WorkspaceID); err != nil {
		return err
	}
	schedule, err := scheduleAuthorityTx(ctx, tx, actor.ID, project, *ref)
	if err != nil {
		return err
	}
	b, err := qualificationWeekTx(ctx, tx, project, ref.Week)
	if err == sql.ErrNoRows {
		b = qualificationWeekBalance{QualificationWeeklyGrant: QualificationWeeklyGrant{ID: NewID(), ScheduleID: ref.ID, Revision: ref.Revision, Week: ref.Week, Cap: schedule.BudgetLimit}, Currency: q.Frozen.Price.Currency, Enabled: true}
		if create {
			_, err = tx.ExecContext(ctx, "INSERT INTO qualification_week_grants(id,project_id,week_key,schedule_id,schedule_revision,currency,cap_micros,enabled) VALUES(?,?,?,?,?,?,?,TRUE)", b.ID, project, b.Week, b.ScheduleID, b.Revision, b.Currency, b.Cap)
		} else {
			err = nil
		}
	}
	if err != nil {
		return err
	}
	// A new schedule revision changes authority, never erases this week's spend or holds.
	if create {
		// A free tool plan has no currency. Initialize it only when the owner
		// explicitly authorizes a ready model scope and no financial history exists.
		if q.Ready && b.Currency == "" && b.Reserved == 0 && b.Spent == 0 {
			b.Currency = q.Frozen.Price.Currency
		}
		if _, err = tx.ExecContext(ctx, "UPDATE qualification_week_grants SET schedule_id=?,schedule_revision=?,cap_micros=?,currency=?,enabled=TRUE WHERE id=?", ref.ID, ref.Revision, schedule.BudgetLimit, b.Currency, b.ID); err != nil {
			return err
		}
	}
	b.Cap = schedule.BudgetLimit
	b.ScheduleID = ref.ID
	b.Revision = ref.Revision
	q.Frozen.Weekly = &b.QualificationWeeklyGrant
	if (b.Currency != "" && b.Currency != q.Frozen.Price.Currency) || q.Frozen.Estimate > b.Cap-b.Spent-b.Reserved {
		q.Ready = false
		q.Reasons = append(q.Reasons, "项目当周费用授权不足；已发生费用和未知占用不会因修改设置而清零")
	}
	return nil
}

func validateWeeklyTx(ctx context.Context, tx *sql.Tx, r QualificationRun, amount int64, reserving bool) error {
	ref := r.Frozen.Weekly
	if ref == nil {
		return nil
	}
	var workspace string
	if err := tx.QueryRowContext(ctx, "SELECT workspace_id FROM projects WHERE id=?", r.ProjectID).Scan(&workspace); err != nil {
		return err
	}
	if _, err := budgetTx(ctx, tx, workspace); err != nil {
		return err
	}
	if _, err := scheduleAuthorityTx(ctx, tx, r.ActorID, r.ProjectID, qualificationScheduleRef{ID: ref.ScheduleID, Revision: ref.Revision, Week: ref.Week}); err != nil {
		return err
	}
	b, err := qualificationWeekTx(ctx, tx, r.ProjectID, ref.Week)
	if err != nil {
		return err
	}
	if b.ID != ref.ID || !b.Enabled || b.Revision != ref.Revision || b.Cap != ref.Cap || b.Currency != r.Frozen.Price.Currency {
		return domain.ErrConflict
	}
	available := b.Cap - b.Spent - b.Reserved
	if (reserving && amount > available) || (!reserving && available < 0) {
		return ErrBudgetExceeded
	}
	return nil
}

func reserveQualificationTx(ctx context.Context, tx *sql.Tx, g Generation) error {
	trial := g.Frozen.Capability
	if trial == nil || trial.Mode != "EVALUATION" {
		return nil
	}
	r, err := qualificationRunTx(ctx, tx, g.ProjectID, trial.EvaluationID)
	if err != nil {
		return err
	}
	var arm, caseID string
	if err = tx.QueryRowContext(ctx, "SELECT arm,case_key FROM qualification_steps WHERE id=? AND run_id=?", trial.EvaluationStepID, r.ID).Scan(&arm, &caseID); err != nil {
		return err
	}
	matched := false
	for _, quote := range r.Frozen.RequestQuotes {
		if quote.Arm == arm && quote.CaseID == caseID {
			matched = quote.RequestHash == g.Frozen.InputHash && quote.Estimate == g.Frozen.Estimate
		}
	}
	if !matched || r.Frozen.QuoteProfile != "complete-request-v1" {
		return domain.ErrConflict
	}
	return validateWeeklyTx(ctx, tx, r, g.Frozen.Estimate, true)
}

func attachQualificationCallTx(ctx context.Context, tx *sql.Tx, g Generation, call Call) error {
	trial := g.Frozen.Capability
	if trial == nil || trial.Mode != "EVALUATION" {
		return nil
	}
	r, err := qualificationRunTx(ctx, tx, g.ProjectID, trial.EvaluationID)
	if err != nil {
		return err
	}
	grant := ""
	if r.Frozen.Weekly != nil {
		grant = r.Frozen.Weekly.ID
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO qualification_calls(call_id,project_id,run_id,step_id,grant_id,reserved_micros,held_micros,origin) VALUES(?,?,?,?,?,?,?,'NATIVE_30')", call.ID, g.ProjectID, r.ID, trial.EvaluationStepID, grant, call.Reserved, call.Reserved); err != nil {
		return err
	}
	if grant != "" {
		_, err = tx.ExecContext(ctx, "UPDATE qualification_week_grants SET reserved_micros=reserved_micros+? WHERE id=?", call.Reserved, grant)
	}
	if err != nil {
		return err
	}
	return bindEvaluationChildTx(ctx, tx, g.ProjectID, r.ID, trial.EvaluationStepID, g.ID, "generation")
}

// Lock order: project, workspace budget, weekly grant, model call. No permission
// check here: an already-issued bill must remain recordable after revocation.
func lockReceiptProjectTx(ctx context.Context, tx *sql.Tx, project string) error {
	var id string
	return tx.QueryRowContext(ctx, "SELECT id FROM projects WHERE id=? FOR UPDATE", project).Scan(&id)
}
func lockQualificationCallTx(ctx context.Context, tx *sql.Tx, call string) error {
	var grant string
	err := tx.QueryRowContext(ctx, "SELECT grant_id FROM qualification_calls WHERE call_id=?", call).Scan(&grant)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	if grant == "" {
		return nil
	}
	var id string
	return tx.QueryRowContext(ctx, "SELECT id FROM qualification_week_grants WHERE id=? FOR UPDATE", grant).Scan(&id)
}

func accountQualificationCallTx(ctx context.Context, tx *sql.Tx, call, state string, charged int64, release bool) error {
	var grant, project, run, priorState string
	var held, previous int64
	err := tx.QueryRowContext(ctx, "SELECT grant_id,project_id,run_id,held_micros,charged_micros,state FROM qualification_calls WHERE call_id=? FOR UPDATE", call).Scan(&grant, &project, &run, &held, &previous, &priorState)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	released := int64(0)
	if release {
		released = held
	}
	if priorState == state && charged == previous && released == 0 {
		return nil
	}
	if grant != "" {
		if _, err = tx.ExecContext(ctx, "UPDATE qualification_week_grants SET reserved_micros=reserved_micros-?,spent_micros=spent_micros+? WHERE id=?", released, charged-previous, grant); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, "UPDATE qualification_calls SET held_micros=held_micros-?,charged_micros=?,state=? WHERE call_id=?", released, charged, state, call); err != nil {
		return err
	}
	return qualificationReceiptOutboxTx(ctx, tx, project, run)
}

func qualificationReceiptOutboxTx(ctx context.Context, tx *sql.Tx, project, run string) error {
	event := NewID()
	raw, _ := json.Marshal(map[string]string{"run_id": run})
	_, err := tx.ExecContext(ctx, "INSERT INTO outbox(id,project_id,kind,resource_id,payload) VALUES(?,?,'qualification.receipt',?,?)", event, project, event, raw)
	return err
}

func qualificationCallReceiptOutboxTx(ctx context.Context, tx *sql.Tx, call string) error {
	var project, run string
	err := tx.QueryRowContext(ctx, "SELECT project_id,run_id FROM qualification_calls WHERE call_id=?", call).Scan(&project, &run)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	return qualificationReceiptOutboxTx(ctx, tx, project, run)
}
