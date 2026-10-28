package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"novelstudio/internal/domain"
	"novelstudio/internal/qualification"
	"regexp"
	"time"
)

type QualificationScheduleInput struct {
	Input       QualificationInput `json:"input"`
	Enabled     bool               `json:"enabled"`
	BudgetLimit int64              `json:"budget_limit_micros"`
	Revision    int64              `json:"revision"`
}
type QualificationSchedule struct {
	ID          string             `json:"id"`
	ProjectID   string             `json:"project_id"`
	ActorID     string             `json:"-"`
	Input       QualificationInput `json:"input"`
	Enabled     bool               `json:"enabled"`
	BudgetLimit int64              `json:"budget_limit_micros"`
	Revision    int64              `json:"revision"`
	LastWeek    string             `json:"last_week"`
	Reason      string             `json:"reason"`
}

func scanQualificationSchedule(row interface{ Scan(...any) error }) (QualificationSchedule, error) {
	var schedule QualificationSchedule
	var raw []byte
	err := row.Scan(&schedule.ID, &schedule.ProjectID, &schedule.ActorID, &raw, &schedule.Enabled, &schedule.Revision, &schedule.LastWeek, &schedule.Reason)
	if err != nil {
		return schedule, err
	}
	var config QualificationScheduleInput
	if err := rejectExpiredRuntime(raw, "qualification_schedules.config"); err != nil {
		return schedule, err
	}
	if json.Unmarshal(raw, &config) != nil {
		return schedule, domain.ErrNotReady
	}
	schedule.Input = config.Input
	schedule.BudgetLimit = config.BudgetLimit
	return schedule, nil
}

func (s *Qualifications) ReadSchedule(ctx context.Context, a domain.Actor, project string) (QualificationSchedule, error) {
	tx, _, _, err := s.IP.begin(ctx, a, project, false)
	if err != nil {
		return QualificationSchedule{}, err
	}
	defer tx.Rollback()
	result, err := scanQualificationSchedule(tx.QueryRowContext(ctx, "SELECT id,project_id,actor_id,config,enabled,revision,last_week,reason FROM qualification_schedules WHERE project_id=?", project))
	if err == sql.ErrNoRows {
		return QualificationSchedule{}, nil
	}
	return result, err
}

func (s *Qualifications) SaveSchedule(ctx context.Context, a domain.Actor, project string, input QualificationScheduleInput) (QualificationSchedule, error) {
	var schedule QualificationSchedule
	if input.BudgetLimit < 0 || input.BudgetLimit > 1000000000000 {
		return schedule, domain.ErrValidation
	}
	tx, a, p, err := s.IP.begin(ctx, a, project, false)
	if err != nil {
		return schedule, err
	}
	defer tx.Rollback()
	if a.Role != "owner" || p.Role != "owner" {
		return schedule, domain.ErrForbidden
	}
	current, err := scanQualificationSchedule(tx.QueryRowContext(ctx, "SELECT id,project_id,actor_id,config,enabled,revision,last_week,reason FROM qualification_schedules WHERE project_id=? FOR UPDATE", project))
	if err != nil && err != sql.ErrNoRows {
		return schedule, err
	}
	if current.Revision != input.Revision {
		return schedule, domain.ErrConflict
	}
	if current.ID != "" {
		if err = captureMetadataSourceTx(ctx, tx, project, "qualification_schedules", current.ID); err != nil {
			return schedule, err
		}
	}
	if !input.Enabled && input.Input.VersionID == "" && current.ID != "" {
		input.Input = current.Input
	}
	if _, err = qualification.Lookup(input.Input.SuiteID); err != nil {
		return schedule, domain.ErrValidation
	}
	if input.Enabled {
		if _, err = branchTx(ctx, tx, project, input.Input.BranchID); err != nil {
			return schedule, err
		}
		if _, err = capabilityTx(ctx, tx, project, input.Input.VersionID); err != nil {
			return schedule, err
		}
	}
	schedule = QualificationSchedule{ID: current.ID, ProjectID: project, ActorID: a.ID, Input: input.Input, Enabled: input.Enabled, BudgetLimit: input.BudgetLimit, Revision: current.Revision + 1}
	if schedule.ID == "" {
		schedule.ID = NewID()
	}
	input.Input.OperationID = ""
	raw, _ := json.Marshal(input)
	if _, err = tx.ExecContext(ctx, "INSERT INTO qualification_schedules(id,project_id,actor_id,config,enabled,revision) VALUES(?,?,?,?,?,?) ON DUPLICATE KEY UPDATE actor_id=VALUES(actor_id),config=VALUES(config),enabled=VALUES(enabled),revision=VALUES(revision),last_week=''", schedule.ID, project, a.ID, raw, input.Enabled, schedule.Revision); err != nil {
		return schedule, err
	}
	if input.Enabled {
		event := NewID()
		payload, _ := json.Marshal(map[string]string{"schedule_id": schedule.ID})
		if _, err = tx.ExecContext(ctx, "INSERT INTO outbox(id,project_id,kind,resource_id,payload) VALUES(?,?,'qualification.schedule',?,?)", event, project, event, payload); err != nil {
			return schedule, err
		}
	}
	if _, err = budgetTx(ctx, tx, a.WorkspaceID); err != nil {
		return schedule, err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE qualification_week_grants SET schedule_revision=?,cap_micros=?,enabled=? WHERE project_id=? AND week_key=?", schedule.Revision, schedule.BudgetLimit, schedule.Enabled, project, QualificationWeek(time.Now())); err != nil {
		return schedule, err
	}
	if err = audit(ctx, tx, a, "qualification.schedule.configure", schedule.ID); err != nil {
		return schedule, err
	}
	if err = captureMetadataSourceTx(ctx, tx, project, "qualification_schedules", schedule.ID); err != nil {
		return schedule, err
	}
	return schedule, tx.Commit()
}

func QualificationWeek(now time.Time) string {
	year, week := now.UTC().ISOWeek()
	return fmt.Sprintf("%04d-W%02d", year, week)
}

func (s *Qualifications) Schedule(ctx context.Context, id, week string) error {
	if !regexp.MustCompile(`^\d{4}-W\d{2}$`).MatchString(week) {
		return domain.ErrValidation
	}
	schedule, err := scanQualificationSchedule(s.IP.Workspace.DB.QueryRowContext(ctx, "SELECT id,project_id,actor_id,config,enabled,revision,last_week,reason FROM qualification_schedules WHERE id=?", id))
	if err != nil {
		return err
	}
	if !schedule.Enabled || schedule.LastWeek == week {
		return nil
	}
	input := schedule.Input
	input.OperationID = fmt.Sprintf("weekly-%s-%s-%d", id, week, schedule.Revision)
	input.Schedule = &qualificationScheduleRef{ID: id, Revision: schedule.Revision, Week: week}
	// A schedule does not acquire a system identity or rewrite a stale branch or
	// baseline. The saved owner and every frozen condition are checked again.
	r, err := s.Create(ctx, domain.Actor{ID: schedule.ActorID}, schedule.ProjectID, input)
	if err != nil {
		if toolTrialGuardFailure(err) {
			return s.recordScheduleResult(ctx, schedule, week, "当前权限、分支、基线或配置已有变化；请检查保存的评测范围，未发送请求")
		}
		return err
	}
	return s.recordScheduleResult(ctx, schedule, week, r.Reason)
}

// A background result may record why it stopped after authority was revoked;
// it cannot rewrite a new schedule revision or acquire execution authority.
func (s *Qualifications) recordScheduleResult(ctx context.Context, schedule QualificationSchedule, week, reason string) error {
	tx, err := s.IP.Workspace.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = lockReceiptProjectTx(ctx, tx, schedule.ProjectID); err != nil {
		return err
	}
	var revision int64
	if err = tx.QueryRowContext(ctx, "SELECT revision FROM qualification_schedules WHERE id=? AND project_id=? FOR UPDATE", schedule.ID, schedule.ProjectID).Scan(&revision); err != nil {
		return err
	}
	if revision != schedule.Revision {
		return nil
	}
	if err = captureDiagnosticSourceTx(ctx, tx, schedule.ProjectID, "qualification_schedules", schedule.ID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE qualification_schedules SET last_week=?,reason=? WHERE id=? AND revision=?", week, reason, schedule.ID, schedule.Revision); err != nil {
		return err
	}
	if err = captureDiagnosticSourceTx(ctx, tx, schedule.ProjectID, "qualification_schedules", schedule.ID); err != nil {
		return err
	}
	return tx.Commit()
}
