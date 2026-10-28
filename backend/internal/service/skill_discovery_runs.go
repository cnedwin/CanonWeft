package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"novelstudio/internal/domain"
	"slices"
	"time"
)

// ErrDiscoverySuperseded means another activity advanced the same durable run.
// It is an execution race, not a permission or source-configuration failure.
var ErrDiscoverySuperseded = errors.New("发现活动进度已由替代活动推进")

func (s *SkillDiscovery) Start(ctx context.Context, a domain.Actor, project, sourceID, operation string) (DiscoveryRun, error) {
	var result DiscoveryRun
	if !validName(operation, 120) {
		return result, domain.ErrValidation
	}
	tx, a, _, err := s.IP.begin(ctx, a, project, true)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	hash := digest("discovery.run\x00" + project + "\x00" + sourceID)
	var stored, id string
	stored, id, err = operationReceiptTx(ctx, tx, a, operation)
	if err == nil {
		if stored != hash {
			return result, domain.ErrConflict
		}
		return discoveryRunTx(ctx, tx, project, id)
	}
	if err != sql.ErrNoRows {
		return result, err
	}
	source, err := discoverySourceTx(ctx, tx, project, sourceID)
	if err != nil {
		return result, err
	}
	if !source.Enabled {
		return result, domain.ErrConflict
	}
	var active, daily int
	if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM skill_discovery_runs WHERE source_id=? AND state IN ('QUEUED','RUNNING','PAUSED')", sourceID).Scan(&active); err != nil {
		return result, err
	}
	// Reserve the full configured allowance before any network work, including failures.
	if err = tx.QueryRowContext(ctx, "SELECT COALESCE(SUM(CAST(JSON_UNQUOTE(JSON_EXTRACT(source_snapshot,'$.limit')) AS UNSIGNED)),0) FROM skill_discovery_runs WHERE project_id=? AND created_at>=UTC_DATE()", project).Scan(&daily); err != nil {
		return result, err
	}
	if active > 0 || daily+source.Limit > 20 {
		return result, domain.ErrConflict
	}
	raw, _ := json.Marshal(source)
	id = NewID()
	if _, err = tx.ExecContext(ctx, "INSERT INTO skill_discovery_runs(id,project_id,source_id,actor_id,source_revision,source_snapshot) VALUES(?,?,?,?,?,?)", id, project, sourceID, a.ID, source.Revision, raw); err != nil {
		return result, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO operations(actor_id,operation_id,kind,request_hash,result_id) VALUES(?,?,'discovery.run',?,?)", a.ID, operation, hash, id); err != nil {
		return result, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO outbox(id,project_id,kind,resource_id,payload) VALUES(?,?,'discovery.start',?,'{}')", NewID(), project, id); err != nil {
		return result, err
	}
	if err = audit(ctx, tx, a, "discovery.run", id); err != nil {
		return result, err
	}
	if err = s.IP.captureDiscoverySourceTx(ctx, tx, project, "skill_discovery_runs", id); err != nil {
		return result, err
	}
	result, err = discoveryRunTx(ctx, tx, project, id)
	if err != nil {
		return result, err
	}
	return result, tx.Commit()
}
func (s *SkillDiscovery) Control(ctx context.Context, a domain.Actor, project, id, action string) (DiscoveryRun, error) {
	var result DiscoveryRun
	if action != "pause" && action != "resume" && action != "cancel" {
		return result, domain.ErrValidation
	}
	tx, a, _, err := s.IP.begin(ctx, a, project, true)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	result, err = discoveryRunTx(ctx, tx, project, id)
	if err != nil {
		return result, err
	}
	if result.State != "QUEUED" && result.State != "RUNNING" && result.State != "PAUSED" {
		return result, nil
	}
	state, reason := "PAUSED", "已暂停；已入库的候选保留"
	if action == "cancel" {
		state, reason = "CANCELLED", "已取消；不会提交后续候选"
	}
	if action == "resume" {
		source, e := discoverySourceTx(ctx, tx, project, result.SourceID)
		if e != nil {
			return result, e
		}
		if !source.Enabled || source.Revision != result.SourceRevision {
			return result, domain.ErrConflict
		}
		state, reason = "QUEUED", ""
	}
	if err = updateDiscoveryStateTx(ctx, tx, project, id, state, reason); err != nil {
		return result, err
	}
	if err = audit(ctx, tx, a, "discovery."+action, id); err != nil {
		return result, err
	}
	result.State = state
	result.Reason = reason
	return result, tx.Commit()
}
func (s *SkillDiscovery) finish(ctx context.Context, id, state, reason string) (string, error) {
	return s.finishStates(ctx, id, state, reason, "QUEUED", "RUNNING")
}

func (s *SkillDiscovery) finishStates(ctx context.Context, id, state, reason string, eligible ...string) (string, error) {
	var project string
	db := s.IP.Workspace.DB
	if err := db.QueryRowContext(ctx, "SELECT project_id FROM skill_discovery_runs WHERE id=?", id).Scan(&project); err != nil {
		return "", err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if err = lockReceiptProjectTx(ctx, tx, project); err != nil {
		return "", err
	}
	current, err := discoveryRunTx(ctx, tx, project, id)
	if err != nil {
		return "", err
	}
	if !slices.Contains(eligible, current.State) {
		return current.State, nil
	}
	if err = updateDiscoveryStateTx(ctx, tx, project, id, state, reason); err != nil {
		return "", err
	}
	return state, tx.Commit()
}
func (s *SkillDiscovery) Fail(ctx context.Context, id string) (string, error) {
	return s.finish(ctx, id, "FAILED", "公开来源读取多次未完成，请检查网络、文件限制或许可后重新发现；未调用模型")
}

// Schedule is invoked by a durable daily Temporal timer. Revoked permissions
// cannot be replaced by a system identity; the source owner's permission is read again.
func (s *SkillDiscovery) Schedule(ctx context.Context, sourceID, day string) error {
	source, err := scanDiscoverySource(s.IP.Workspace.DB.QueryRowContext(ctx, "SELECT "+discoverySourceColumns+" FROM skill_discovery_sources WHERE id=?", sourceID))
	if errors.Is(err, domain.ErrForbidden) {
		return nil
	}
	if err != nil {
		return err
	}
	if !source.Enabled {
		return nil
	}
	_, err = s.Start(ctx, domain.Actor{ID: source.ActorID}, source.ProjectID, source.ID, "daily-discovery-"+sourceID+"-"+day)
	if errors.Is(err, domain.ErrForbidden) || errors.Is(err, domain.ErrUnauthorized) || errors.Is(err, domain.ErrConflict) {
		return nil
	}
	return err
}
func (s *SkillDiscovery) ReadRun(ctx context.Context, a domain.Actor, project, id string) (DiscoveryRun, error) {
	tx, _, _, err := s.IP.begin(ctx, a, project, false)
	if err != nil {
		return DiscoveryRun{}, err
	}
	defer tx.Rollback()
	return discoveryRunTx(ctx, tx, project, id, true)
}
func (s *SkillDiscovery) guard(ctx context.Context, r DiscoveryRun) (*sql.Tx, DiscoveryRun, error) {
	tx, _, _, err := s.IP.begin(ctx, domain.Actor{ID: r.ActorID}, r.ProjectID, true)
	if err != nil {
		return nil, r, err
	}
	// Lock source before run in every execution transaction, matching source edits.
	source, err := discoverySourceTx(ctx, tx, r.ProjectID, r.SourceID)
	if err == nil {
		r, err = discoveryRunTx(ctx, tx, r.ProjectID, r.ID)
	}
	if err == nil && (!source.Enabled || source.Revision != r.SourceRevision) {
		err = domain.ErrConflict
	}
	if err == nil && r.State != "QUEUED" && r.State != "RUNNING" {
		err = domain.ErrConflict
	}
	if err != nil {
		tx.Rollback()
		return nil, r, err
	}
	return tx, r, nil
}
func (s *SkillDiscovery) readRawRun(ctx context.Context, id string) (DiscoveryRun, error) {
	return scanDiscoveryRun(s.IP.Workspace.DB.QueryRowContext(ctx, "SELECT "+discoveryRunColumns+" FROM skill_discovery_runs WHERE id=?", id))
}
func (s *SkillDiscovery) terminalError(ctx context.Context, r DiscoveryRun, err error) (string, error) {
	if ctx.Err() != nil {
		return "RUNNING", ctx.Err()
	}
	if errors.Is(err, domain.ErrForbidden) || errors.Is(err, domain.ErrUnauthorized) || errors.Is(err, domain.ErrConflict) {
		return s.finish(ctx, r.ID, "BLOCKED", "当前权限、来源版本或任务状态变化，后续候选未提交")
	}
	return "RUNNING", err
}

// DiscoveryDay defines UTC quota and schedule boundaries consistently.
func DiscoveryDay(now time.Time) string { return now.UTC().Format("2006-01-02") }
