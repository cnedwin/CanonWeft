package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"novelstudio/internal/domain"
)

type Task struct {
	ID          string `json:"id"`
	ProjectID   string `json:"project_id"`
	BranchID    string `json:"branch_id"`
	CandidateID string `json:"candidate_id"`
	InputHash   string `json:"input_hash"`
	BaseHead    string `json:"base_head"`
	ActorID     string `json:"-"`
	State       string `json:"state"`
	Reason      string `json:"reason"`
	Paused      bool   `json:"paused"`
	Cancelled   bool   `json:"cancelled"`
	Revision    int64  `json:"revision"`
}
type Tasks struct{ IP *IP }

const taskColumns = "id,project_id,branch_id,candidate_id,input_hash,base_head,actor_id,state,reason,paused,cancelled,revision"

func scanTask(row interface{ Scan(...any) error }) (Task, error) {
	var t Task
	err := row.Scan(&t.ID, &t.ProjectID, &t.BranchID, &t.CandidateID, &t.InputHash, &t.BaseHead, &t.ActorID, &t.State, &t.Reason, &t.Paused, &t.Cancelled, &t.Revision)
	if err == sql.ErrNoRows {
		err = domain.ErrForbidden
	}
	return t, err
}
func taskTx(ctx context.Context, tx *sql.Tx, project, id string) (Task, error) {
	task, err := scanTask(tx.QueryRowContext(ctx, "SELECT "+taskColumns+" FROM platform_tasks WHERE project_id=? AND id=? FOR UPDATE", project, id))
	if err == nil {
		err = requireRuntimeObjectTx(ctx, tx, project, "platform_tasks", id, "input_hash", task.InputHash)
	}
	return task, err
}
func (s *Tasks) Read(ctx context.Context, a domain.Actor, project, id string) (Task, error) {
	tx, _, _, err := s.IP.begin(ctx, a, project, false)
	if err != nil {
		return Task{}, err
	}
	defer tx.Rollback()
	return taskTx(ctx, tx, project, id)
}
func (s *Tasks) List(ctx context.Context, a domain.Actor, project string) ([]Task, error) {
	tx, _, _, err := s.IP.begin(ctx, a, project, false)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, "SELECT "+taskColumns+" FROM platform_tasks WHERE project_id=? ORDER BY created_at DESC LIMIT 100", project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Task{}
	for rows.Next() {
		t, readErr := scanTask(rows)
		if readErr != nil {
			return nil, readErr
		}
		result = append(result, t)
	}
	return result, rows.Err()
}

func (s *Tasks) StartReview(ctx context.Context, a domain.Actor, project, branch, candidate, operation string) (Task, error) {
	var task Task
	if !validName(operation, 120) {
		return task, domain.ErrValidation
	}
	tx, a, _, err := s.IP.begin(ctx, a, project, true)
	if err != nil {
		return task, err
	}
	defer tx.Rollback()
	fingerprint := digest("task.review\x00" + project + "\x00" + branch + "\x00" + candidate)
	var hash, id string
	hash, id, err = operationReceiptTx(ctx, tx, a, operation)
	if err == nil {
		if hash != fingerprint {
			return task, domain.ErrConflict
		}
		return taskTx(ctx, tx, project, id)
	}
	if err != sql.ErrNoRows {
		return task, err
	}
	b, err := branchTx(ctx, tx, project, branch)
	if err != nil {
		return task, err
	}
	c, err := candidateTx(ctx, tx, branch, candidate)
	if err != nil {
		return task, err
	}
	if c.BaseHead != b.Head || c.Status == "PUBLISHED" {
		return task, domain.ErrConflict
	}
	task = Task{ID: NewID(), ProjectID: project, BranchID: branch, CandidateID: candidate, InputHash: c.Hash, BaseHead: c.BaseHead, ActorID: a.ID, State: "QUEUED", Revision: 1}
	_, err = tx.ExecContext(ctx, "INSERT INTO platform_tasks(id,project_id,branch_id,candidate_id,input_hash,base_head,actor_id) VALUES(?,?,?,?,?,?,?)", task.ID, project, branch, candidate, c.Hash, c.BaseHead, a.ID)
	if err != nil {
		return task, err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO operations(actor_id,operation_id,kind,request_hash,result_id) VALUES(?,?,'task.review',?,?)", a.ID, operation, fingerprint, task.ID)
	if err != nil {
		return task, err
	}
	payload, _ := json.Marshal(map[string]string{"task_id": task.ID})
	_, err = tx.ExecContext(ctx, "INSERT INTO outbox(id,project_id,kind,resource_id,payload) VALUES(?,?,'task.start',?,?)", NewID(), project, task.ID, string(payload))
	if err != nil {
		return task, err
	}
	if err = audit(ctx, tx, a, "task.create", task.ID); err != nil {
		return task, err
	}
	if err = s.IP.captureReviewTaskSourceTx(ctx, tx, project, task.ID); err != nil {
		return task, err
	}
	return task, tx.Commit()
}

func terminalTask(state string) bool {
	return state == "COMPLETED" || state == "CANCELLED" || state == "STALE"
}
func (s *Tasks) Control(ctx context.Context, a domain.Actor, project, id, action string, revision int64) (Task, error) {
	tx, a, _, err := s.IP.begin(ctx, a, project, true)
	if err != nil {
		return Task{}, err
	}
	defer tx.Rollback()
	task, err := taskTx(ctx, tx, project, id)
	if err != nil {
		return task, err
	}
	if task.ActorID != a.ID && a.Role != "owner" {
		return task, domain.ErrForbidden
	}
	if task.Revision != revision || terminalTask(task.State) {
		return task, domain.ErrConflict
	}
	switch action {
	case "pause":
		task.Paused = true
		task.State = "PAUSED"
		task.Reason = "用户暂停"
	case "resume":
		task.Paused = false
		task.State = "QUEUED"
		task.Reason = "等待重新检查"
	case "cancel":
		task.Cancelled = true
		task.State = "CANCELLED"
		task.Reason = "用户取消；未改变已发布内容"
	case "refresh":
	default:
		return task, domain.ErrValidation
	}
	task.Revision++
	if err = captureDiagnosticSourceTx(ctx, tx, project, "platform_tasks", id); err != nil {
		return task, err
	}
	_, err = tx.ExecContext(ctx, "UPDATE platform_tasks SET paused=?,cancelled=?,state=?,reason=?,revision=? WHERE id=?", task.Paused, task.Cancelled, task.State, task.Reason, task.Revision, id)
	if err != nil {
		return task, err
	}
	if err = audit(ctx, tx, a, "task."+action, id); err != nil {
		return task, err
	}
	if err = captureDiagnosticSourceTx(ctx, tx, project, "platform_tasks", id); err != nil {
		return task, err
	}
	return task, tx.Commit()
}

func (s *Tasks) blockRevokedReview(ctx context.Context, frozen Task) (string, error) {
	tx, err := s.IP.Workspace.DB.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if err = lockReceiptProjectTx(ctx, tx, frozen.ProjectID); err != nil {
		return "", err
	}
	current, err := taskTx(ctx, tx, frozen.ProjectID, frozen.ID)
	if err != nil {
		return "", err
	}
	if terminalTask(current.State) {
		return current.State, nil
	}
	const reason = "发起者的访问权限已撤销"
	if current.State == "BLOCKED" && current.Reason == reason {
		return current.State, nil
	}
	if err = captureDiagnosticSourceTx(ctx, tx, frozen.ProjectID, "platform_tasks", frozen.ID); err != nil {
		return "", err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE platform_tasks SET state='BLOCKED',reason=?,revision=revision+1 WHERE id=?", reason, frozen.ID); err != nil {
		return "", err
	}
	if err = captureDiagnosticSourceTx(ctx, tx, frozen.ProjectID, "platform_tasks", frozen.ID); err != nil {
		return "", err
	}
	return "BLOCKED", tx.Commit()
}

// Inspect is called only by a registered Temporal Activity. Signals carry no authority.
func (s *Tasks) Inspect(ctx context.Context, id string) (string, error) {
	frozen, err := scanTask(s.IP.Workspace.DB.QueryRowContext(ctx, "SELECT "+taskColumns+" FROM platform_tasks WHERE id=?", id))
	if err != nil {
		return "", err
	}
	tx, _, _, err := s.IP.begin(ctx, domain.Actor{ID: frozen.ActorID}, frozen.ProjectID, false)
	if errors.Is(err, domain.ErrForbidden) {
		return s.blockRevokedReview(ctx, frozen)
	}
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	task, err := taskTx(ctx, tx, frozen.ProjectID, id)
	if err != nil {
		return "", err
	}
	if terminalTask(task.State) {
		return task.State, nil
	}
	state, reason := "WAITING_ADOPTION", "确定性检查通过，等待用户确认采用；未进行语义模型评审"
	if task.Cancelled {
		state = "CANCELLED"
		reason = "用户取消"
	} else if task.Paused {
		state = "PAUSED"
		reason = "用户暂停"
	} else {
		branch, readErr := branchTx(ctx, tx, task.ProjectID, task.BranchID)
		if readErr != nil {
			return "", readErr
		}
		candidate, readErr := candidateTx(ctx, tx, task.BranchID, task.CandidateID)
		if readErr != nil {
			return "", readErr
		}
		switch {
		case candidate.Hash != task.InputHash:
			state = "BLOCKED"
			reason = "固定输入哈希不匹配"
		case candidate.Status == "PUBLISHED":
			state = "COMPLETED"
			reason = "用户确认的版本已正式发布"
		case branch.Head != task.BaseHead:
			state = "STALE"
			reason = "基础版本已经变化，请基于新版创建候选"
		default:
			checkErr := domain.ValidateSnapshot(candidate.Snapshot)
			if checkErr == nil {
				checkErr = domain.ValidateRuleEvidence(candidate.Snapshot)
			}
			if checkErr != nil {
				state = "BLOCKED"
				reason = "设定存在冲突或缺少证据，请查看候选检查结果"
			} else if _, checkErr = s.IP.Blobs.ReadObject(ctx, task.ProjectID, task.InputHash); checkErr != nil {
				state = "BLOCKED"
				reason = "候选内容对象缺失或完整性校验失败"
			} else if candidate.Status == "READY" {
				state = "WAITING_PUBLICATION"
				reason = "用户已确认采用，等待具有发布权限的用户发布"
			}
		}
	}
	if task.State != state || task.Reason != reason {
		if err = captureDiagnosticSourceTx(ctx, tx, task.ProjectID, "platform_tasks", id); err != nil {
			return "", err
		}
		_, err = tx.ExecContext(ctx, "UPDATE platform_tasks SET state=?,reason=?,revision=revision+1 WHERE id=?", state, reason, id)
		if err != nil {
			return "", err
		}
		if err = captureDiagnosticSourceTx(ctx, tx, task.ProjectID, "platform_tasks", id); err != nil {
			return "", err
		}
	}
	return state, tx.Commit()
}
