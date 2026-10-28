package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"novelstudio/internal/domain"
	"novelstudio/internal/isolation"
	"novelstudio/internal/objectwriter"
	"novelstudio/internal/tooltrial"
	"slices"
)

type ToolTrials struct {
	IP          *IP
	RuntimeRoot string
	Heartbeat   func(context.Context)
}
type ToolTrialInput struct {
	BindingID   string `json:"binding_id,omitempty"`
	TaskKind    string `json:"task_kind,omitempty"`
	evaluation  *EvaluationReference
	OperationID string `json:"operation_id"`
	VersionID   string `json:"version_id"`
	InputText   string `json:"input_text"`
}
type ToolTrialFrozen = tooltrial.Frozen
type ToolTrial struct {
	Retention       *RuntimeRetention `json:"retention,omitempty"`
	expiredMaterial []byte
	ID              string           `json:"id"`
	ProjectID       string           `json:"project_id"`
	ActorID         string           `json:"-"`
	State           string           `json:"state"`
	Reason          string           `json:"reason"`
	Frozen          ToolTrialFrozen  `json:"frozen"`
	CanCancel       bool             `json:"can_cancel"`
	Stale           bool             `json:"stale"`
	ElapsedMS       int64            `json:"elapsed_ms"`
	Output          *ToolTrialOutput `json:"output,omitempty"`
	lease           string
	outputHash      string
}
type ToolTrialOutput = tooltrial.Output

const toolTrialColumns = "id,project_id,actor_id,state,reason,frozen,lease_id,output_hash,elapsed_ms"

func scanToolTrial(row interface{ Scan(...any) error }) (ToolTrial, error) {
	var job ToolTrial
	var raw []byte
	err := row.Scan(&job.ID, &job.ProjectID, &job.ActorID, &job.State, &job.Reason, &raw, &job.lease, &job.outputHash, &job.ElapsedMS)
	if err == sql.ErrNoRows {
		return job, domain.ErrForbidden
	}
	if err != nil {
		return job, err
	}
	if err := rejectExpiredRuntime(raw, "tool_trials.frozen"); err != nil {
		job.expiredMaterial = raw
		return job, err
	}
	if json.Unmarshal(raw, &job.Frozen) != nil {
		return job, domain.ErrNotReady
	}
	return job, nil
}
func toolTrialTx(ctx context.Context, tx *sql.Tx, project, id string) (ToolTrial, error) {
	job, err := scanToolTrial(tx.QueryRowContext(ctx, "SELECT "+toolTrialColumns+" FROM tool_trials WHERE project_id=? AND id=? FOR UPDATE", project, id))
	if err == nil {
		err = requireRuntimeObjectTx(ctx, tx, project, "tool_trials", id, "output_hash", job.outputHash)
	}
	return job, err
}
func toolTrialActive(job ToolTrial) bool {
	return slices.Contains([]string{"QUEUED", "RUNNING"}, job.State)
}
func canReadToolTrial(a domain.Actor, job ToolTrial) bool {
	return a.ID == job.ActorID || a.Role == "owner"
}

func (s *ToolTrials) capabilityTx(ctx context.Context, tx *sql.Tx, project, id string) (CapabilityVersion, CapabilityManifest, error) {
	var empty CapabilityManifest
	v, err := capabilityTx(ctx, tx, project, id)
	if err != nil {
		return v, empty, err
	}
	if v.Definition.Kind != "tool" {
		return v, empty, domain.ErrValidation
	}
	manifest, err := (&Capabilities{IP: s.IP}).manifestTx(ctx, tx, project, id)
	if err != nil {
		return v, manifest, err
	}
	for _, f := range manifest.Findings {
		if f.Code == "REVOKED" {
			return v, manifest, domain.ErrConflict
		}
	}
	return v, manifest, nil
}
func (s *ToolTrials) guardTx(ctx context.Context, tx *sql.Tx, job ToolTrial, complete bool) (domain.CapabilityDefinition, error) {
	if job.Frozen.BindingID != "" {
		record, err := s.qualifiedToolTx(ctx, tx, domain.Actor{ID: job.ActorID}, job.ProjectID, job.Frozen.BindingID, job.Frozen.TaskKind)
		if err != nil {
			return domain.CapabilityDefinition{}, err
		}
		if record.ID != job.Frozen.QualificationID || record.Frozen.Candidate.ID != job.Frozen.VersionID || record.Frozen.CandidateManifest != job.Frozen.ManifestHash {
			return domain.CapabilityDefinition{}, domain.ErrConflict
		}
	}
	if job.Frozen.EvaluationID != "" {
		r, err := qualificationRunTx(ctx, tx, job.ProjectID, job.Frozen.EvaluationID)
		if err != nil {
			return domain.CapabilityDefinition{}, err
		}
		if r.State == "FAILED" || r.State == "BLOCKED" || r.Cancelled {
			return domain.CapabilityDefinition{}, domain.ErrConflict
		}
		if err = (&Qualifications{IP: s.IP}).guardRunTx(ctx, tx, r, true); err != nil {
			return domain.CapabilityDefinition{}, err
		}
	}
	v, err := capabilityTx(ctx, tx, job.ProjectID, job.Frozen.VersionID)
	if err != nil {
		return v.Definition, err
	}
	if v.Definition.Kind != "tool" || v.Hash != job.Frozen.VersionHash || v.Definition.PackageID != job.Frozen.PackageID || v.Definition.PackageHash != job.Frozen.PackageHash || job.Frozen.ImageHash != isolation.ApprovedImageSHA256 {
		return v.Definition, domain.ErrConflict
	}
	available, err := capabilityAvailable(ctx, tx, job.ProjectID, v.ID)
	if err != nil {
		return v.Definition, err
	}
	if !available {
		return v.Definition, domain.ErrConflict
	}
	available, err = skillAvailable(ctx, tx, job.ProjectID, v.Definition.PackageID)
	if err != nil {
		return v.Definition, err
	}
	if !available {
		return v.Definition, domain.ErrConflict
	}
	if complete {
		_, m, readErr := s.capabilityTx(ctx, tx, job.ProjectID, v.ID)
		if readErr != nil {
			return v.Definition, readErr
		}
		if m.Hash != job.Frozen.ManifestHash {
			return v.Definition, domain.ErrConflict
		}
	}
	return v.Definition, nil
}
func (s *ToolTrials) Create(ctx context.Context, a domain.Actor, project string, input ToolTrialInput) (ToolTrial, error) {
	var job ToolTrial
	if !validName(input.OperationID, 120) || len(input.InputText) > tooltrial.MaxInputBytes {
		return job, domain.ErrValidation
	}
	tx, a, _, err := s.IP.begin(ctx, a, project, true)
	if err != nil {
		return job, err
	}
	var qualified CapabilityQualification
	if input.BindingID != "" {
		if input.VersionID != "" || input.evaluation != nil {
			tx.Rollback()
			return job, domain.ErrValidation
		}
		binding, readErr := bindingTx(ctx, tx, project, input.BindingID)
		if readErr != nil {
			tx.Rollback()
			return job, readErr
		}
		qualified, err = (&Qualifications{IP: s.IP}).recordTx(ctx, tx, project, binding.QualificationID)
		if err != nil {
			tx.Rollback()
			return job, err
		}
		input.VersionID = qualified.Frozen.Candidate.ID
	} else if input.TaskKind != "" {
		tx.Rollback()
		return job, domain.ErrValidation
	}
	fingerprint := capabilityDigest([]any{"tool.trial", project, input.VersionID, input.InputText})
	if input.BindingID != "" || input.evaluation != nil {
		fingerprint = capabilityDigest([]any{"scoped-tool-v1", project, input.VersionID, input.InputText, input.BindingID, input.TaskKind, input.evaluation})
	}
	var prior, id string
	prior, id, err = evaluationOperationReceiptTx(ctx, tx, a, input.OperationID, input.evaluation != nil)
	if err == nil {
		defer tx.Rollback()
		if prior != fingerprint {
			return job, domain.ErrConflict
		}
		return replayEvaluationToolTx(ctx, tx, project, id, input.evaluation)
	}
	if err != sql.ErrNoRows {
		tx.Rollback()
		return job, err
	}
	if input.BindingID != "" {
		qualified, err = s.qualifiedToolTx(ctx, tx, a, project, input.BindingID, input.TaskKind)
		if err != nil {
			tx.Rollback()
			return job, err
		}
		if err = requireCurrentBindingTx(ctx, tx, project, input.BindingID, input.TaskKind); err != nil {
			tx.Rollback()
			return job, err
		}
	}
	v, manifest, err := s.capabilityTx(ctx, tx, project, input.VersionID)
	if err != nil {
		tx.Rollback()
		return job, err
	}
	pkg, err := skillTx(ctx, tx, project, v.Definition.PackageID)
	if err != nil {
		tx.Rollback()
		return job, err
	}
	bundle, err := (&Capabilities{IP: s.IP}).packageTx(ctx, project, pkg)
	tx.Rollback()
	if err != nil {
		return job, err
	}
	request, err := tooltrial.Prepare(v.Definition, bundle, []byte(input.InputText))
	if err != nil {
		return job, err
	}
	raw, err := json.Marshal(request)
	if err != nil {
		return job, err
	}
	job = ToolTrial{ID: NewID(), ProjectID: project, ActorID: a.ID, State: "QUEUED", CanCancel: true, Frozen: ToolTrialFrozen{VersionID: v.ID, VersionHash: v.Hash, ManifestHash: manifest.Hash, PackageID: pkg.ID, PackageHash: pkg.Report.Hash, ImageHash: isolation.ApprovedImageSHA256, InputHash: digest(string(raw))}}
	if input.evaluation != nil {
		job.Frozen.EvaluationID = input.evaluation.RunID
		job.Frozen.EvaluationStepID = input.evaluation.StepID
	}
	if input.BindingID != "" {
		job.Frozen.BindingID = input.BindingID
		job.Frozen.QualificationID = qualified.ID
		job.Frozen.TaskKind = input.TaskKind
	}
	tx, a, _, err = s.IP.begin(ctx, a, project, true)
	if err != nil {
		return job, err
	}
	defer tx.Rollback()
	prior, id, err = evaluationOperationReceiptTx(ctx, tx, a, input.OperationID, input.evaluation != nil)
	if err == nil {
		if prior != fingerprint {
			return job, domain.ErrConflict
		}
		return replayEvaluationToolTx(ctx, tx, project, id, input.evaluation)
	}
	if err != sql.ErrNoRows {
		return job, err
	}
	if _, err = objectwriter.Write(ctx, tx, s.IP.Blobs, project, raw); err != nil {
		return job, err
	}
	if _, err = s.guardTx(ctx, tx, job, true); err != nil {
		return job, err
	}
	if input.BindingID != "" {
		if err = requireCurrentBindingTx(ctx, tx, project, input.BindingID, input.TaskKind); err != nil {
			return job, err
		}
	}
	frozen, _ := json.Marshal(job.Frozen)
	if _, err = tx.ExecContext(ctx, "INSERT INTO tool_trials(id,project_id,actor_id,capability_id,frozen) VALUES(?,?,?,?,?)", job.ID, project, a.ID, v.ID, frozen); err != nil {
		return job, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO operations(actor_id,operation_id,kind,request_hash,result_id) VALUES(?,?,'tool.trial',?,?)", a.ID, input.OperationID, fingerprint, job.ID); err != nil {
		return job, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO outbox(id,project_id,kind,resource_id,payload) VALUES(?,?,'tool.start',?,?)", NewID(), project, job.ID, `{}`); err != nil {
		return job, err
	}
	if input.evaluation != nil {
		if err = bindEvaluationChildTx(ctx, tx, project, input.evaluation.RunID, input.evaluation.StepID, job.ID, "tool"); err != nil {
			return job, err
		}
	}
	if err = audit(ctx, tx, a, "tool.trial", job.ID); err != nil {
		return job, err
	}
	if err = s.IP.captureToolSourceTx(ctx, tx, project, job.ID); err != nil {
		return job, err
	}
	return job, tx.Commit()
}
func (s *ToolTrials) Read(ctx context.Context, a domain.Actor, project, id string) (ToolTrial, error) {
	tx, a, _, err := s.IP.begin(ctx, a, project, false)
	if err != nil {
		return ToolTrial{}, err
	}
	defer tx.Rollback()
	job, err := toolTrialTx(ctx, tx, project, id)
	if err != nil {
		return job, err
	}
	if !canReadToolTrial(a, job) {
		return job, domain.ErrForbidden
	}
	job.CanCancel = toolTrialActive(job) && domain.CanEdit(a.Role)
	if _, err = s.guardTx(ctx, tx, job, true); err != nil {
		job.Stale = true
		return job, nil
	}
	if job.outputHash != "" && job.State != "CANCELLED" && job.State != "BLOCKED" {
		raw, readErr := s.IP.Blobs.ReadObject(ctx, project, job.outputHash)
		if readErr != nil || digest(string(raw)) != job.outputHash {
			return job, domain.ErrNotReady
		}
		var output ToolTrialOutput
		if json.Unmarshal(raw, &output) != nil {
			return job, domain.ErrNotReady
		}
		job.Output = &output
	}
	return job, nil
}
func (s *ToolTrials) List(ctx context.Context, a domain.Actor, project, after string) ([]ToolTrial, string, error) {
	tx, a, _, err := s.IP.begin(ctx, a, project, false)
	if err != nil {
		return nil, "", err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, "SELECT "+toolTrialColumns+" FROM tool_trials WHERE project_id=? AND id>? AND (actor_id=? OR ?='owner') ORDER BY id LIMIT 101", project, after, a.ID, a.Role)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	items := []ToolTrial{}
	for rows.Next() {
		job, readErr := scanToolTrial(rows)
		if readErr != nil && !errors.Is(readErr, domain.ErrMaterialExpired) {
			return nil, "", readErr
		}
		job.CanCancel = toolTrialActive(job) && domain.CanEdit(a.Role)
		items = append(items, job)
	}
	if err = rows.Err(); err != nil {
		return nil, "", err
	}
	rows.Close()
	for i := range items {
		job := &items[i]
		if len(job.expiredMaterial) == 0 {
			continue
		}
		marker, err := runtimeRetentionTx(ctx, tx, project, "tool_trials", job.ID, "", job.expiredMaterial)
		if err != nil {
			return nil, "", err
		}
		*job = ToolTrial{ID: job.ID, ProjectID: project, State: job.State, Retention: marker}
	}
	next := ""
	if len(items) > 100 {
		items = items[:100]
		next = items[99].ID
	}
	return items, next, nil
}
func (s *ToolTrials) Cancel(ctx context.Context, a domain.Actor, project, id string) (ToolTrial, error) {
	tx, a, _, err := s.IP.begin(ctx, a, project, true)
	if err != nil {
		return ToolTrial{}, err
	}
	defer tx.Rollback()
	job, err := toolTrialTx(ctx, tx, project, id)
	if err != nil {
		return job, err
	}
	if !canReadToolTrial(a, job) {
		return job, domain.ErrForbidden
	}
	if !toolTrialActive(job) {
		return job, nil
	}
	if err = captureDiagnosticSourceTx(ctx, tx, project, "tool_trials", id); err != nil {
		return job, err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE tool_trials SET state='CANCELLED',reason='已取消，不采用晚到结果' WHERE id=?", id); err != nil {
		return job, err
	}
	if err = captureDiagnosticSourceTx(ctx, tx, project, "tool_trials", id); err != nil {
		return job, err
	}
	if err = audit(ctx, tx, a, "tool.cancel", id); err != nil {
		return job, err
	}
	job.State = "CANCELLED"
	job.Reason = "已取消，不采用晚到结果"
	job.CanCancel = false
	return job, tx.Commit()
}
