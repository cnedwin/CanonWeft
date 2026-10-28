package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"novelstudio/internal/domain"
	"novelstudio/internal/isolation"
	"novelstudio/internal/qualification"
	"time"
)

type CapabilityQualification struct {
	Retention  *RuntimeRetention   `json:"retention,omitempty"`
	ID         string              `json:"id"`
	RunID      string              `json:"run_id"`
	Frozen     QualificationFrozen `json:"frozen"`
	ReportHash string              `json:"report_hash"`
	Enabled    bool                `json:"enabled"`
	ExpiresAt  string              `json:"expires_at"`
}

type QualifiedBinding struct {
	ID              string                   `json:"id"`
	TaskKind        string                   `json:"task_kind"`
	QualificationID string                   `json:"qualification_id"`
	PreviousID      string                   `json:"previous_id"`
	Enabled         bool                     `json:"enabled"`
	Current         bool                     `json:"current"`
	Valid           bool                     `json:"valid"`
	Qualification   *CapabilityQualification `json:"qualification,omitempty"`
}

type BindingInput struct {
	OperationID     string `json:"operation_id"`
	QualificationID string `json:"qualification_id"`
	ExpectedBinding string `json:"expected_binding"`
	TaskKind        string `json:"task_kind"`
	Rollback        bool   `json:"rollback"`
}

func requireCurrentBindingTx(ctx context.Context, tx *sql.Tx, project, id, task string) error {
	head, err := bindingHeadTx(ctx, tx, project, task)
	if err != nil {
		return err
	}
	if head != id {
		return domain.ErrConflict
	}
	return nil
}

func bindingTx(ctx context.Context, tx *sql.Tx, project, id string) (QualifiedBinding, error) {
	var b QualifiedBinding
	err := tx.QueryRowContext(ctx, "SELECT id,task_kind,qualification_id,previous_id,enabled FROM capability_bindings WHERE project_id=? AND id=? FOR UPDATE", project, id).Scan(&b.ID, &b.TaskKind, &b.QualificationID, &b.PreviousID, &b.Enabled)
	if err == sql.ErrNoRows {
		return b, domain.ErrForbidden
	}
	return b, err
}

func (s *Qualifications) recordTx(ctx context.Context, tx *sql.Tx, project, id string, history ...bool) (CapabilityQualification, error) {
	var record CapabilityQualification
	var raw []byte
	var expires string
	err := tx.QueryRowContext(ctx, "SELECT id,run_id,payload,report_hash,enabled,CAST(expires_at AS CHAR) FROM capability_qualifications WHERE project_id=? AND id=? FOR UPDATE", project, id).Scan(&record.ID, &record.RunID, &raw, &record.ReportHash, &record.Enabled, &expires)
	if err == sql.ErrNoRows {
		return record, domain.ErrForbidden
	}
	if err != nil {
		return record, err
	}
	historyOnly := len(history) > 0 && history[0]
	if expired := rejectExpiredRuntime(raw, "capability_qualifications.payload"); expired != nil {
		if !historyOnly || !errors.Is(expired, domain.ErrMaterialExpired) {
			return record, expired
		}
		record.Retention, err = runtimeTransientTx(ctx, tx, project, "capability_qualifications", "payload", id)
		if err != nil {
			return record, err
		}
	} else if json.Unmarshal(raw, &record.Frozen) != nil {
		return record, domain.ErrNotReady
	}
	date, err := time.Parse("2006-01-02 15:04:05.999999", expires)
	if err != nil {
		return record, domain.ErrNotReady
	}
	record.ExpiresAt = date.UTC().Format(time.RFC3339)
	reportRetention, err := runtimeObjectRetentionTx(ctx, tx, project, "capability_qualifications", id, "report_hash", record.ReportHash)
	if err != nil {
		return record, err
	}
	if reportRetention != nil {
		if !historyOnly {
			return record, domain.ErrMaterialExpired
		}
		record.Retention = reportRetention
	}
	return record, nil
}

func (s *Qualifications) validateRecordTx(ctx context.Context, tx *sql.Tx, a domain.Actor, project string, record CapabilityQualification) error {
	if record.Retention != nil {
		return domain.ErrMaterialExpired
	}
	date, err := time.Parse(time.RFC3339, record.ExpiresAt)
	if err != nil || !record.Enabled || !date.After(time.Now()) {
		return domain.ErrConflict
	}
	suite, err := qualification.Lookup(record.Frozen.Suite.ID)
	if err != nil || suite.Public().Hash != record.Frozen.Suite.Hash {
		return domain.ErrConflict
	}
	m, err := (&Capabilities{IP: s.IP}).manifestTx(ctx, tx, project, record.Frozen.Candidate.ID)
	if err != nil {
		return err
	}
	if m.Root != record.Frozen.Candidate || m.Hash != record.Frozen.CandidateManifest {
		return domain.ErrConflict
	}
	for _, f := range m.Findings {
		if f.Code == "REVOKED" {
			return domain.ErrConflict
		}
	}
	if record.Frozen.Candidate.Kind == "tool" {
		if record.Frozen.RuntimeHash != isolation.ApprovedImageSHA256 {
			return domain.ErrConflict
		}
	} else {
		config, _, readErr := modelTx(ctx, tx, a.WorkspaceID, record.Frozen.Config.ID)
		if readErr != nil {
			return readErr
		}
		if !config.Enabled || capabilityDigest(config) != capabilityDigest(record.Frozen.Config) {
			return domain.ErrConflict
		}
		var connection string
		var revision int64
		if err = tx.QueryRowContext(ctx, "SELECT connection_id,connection_revision FROM model_bindings WHERE workspace_id=? AND role=?", a.WorkspaceID, record.Frozen.Input.Role).Scan(&connection, &revision); err != nil {
			return err
		}
		if connection != config.ID || revision != config.Revision {
			return domain.ErrConflict
		}
	}
	raw, err := s.IP.Blobs.ReadObject(ctx, project, record.ReportHash)
	if err != nil || digest(string(raw)) != record.ReportHash {
		return domain.ErrNotReady
	}
	var report QualificationReport
	if json.Unmarshal(raw, &report) != nil || report.RunID != record.RunID || capabilityDigest(report.Frozen) != capabilityDigest(record.Frozen) || !report.Passed {
		return domain.ErrNotReady
	}
	if !reportComplete(report, suite) {
		return domain.ErrConflict
	}
	return nil
}

func (s *Qualifications) Activate(ctx context.Context, a domain.Actor, project string, input BindingInput) (QualifiedBinding, error) {
	var b QualifiedBinding
	if !validName(input.OperationID, 120) {
		return b, domain.ErrValidation
	}
	tx, a, p, err := s.IP.begin(ctx, a, project, false)
	if err != nil {
		return b, err
	}
	defer tx.Rollback()
	if a.Role != "owner" || p.Role != "owner" {
		return b, domain.ErrForbidden
	}
	fingerprint := capabilityDigest([]any{"qualification.bind", project, input})
	var prior, id string
	prior, id, err = operationReceiptTx(ctx, tx, a, input.OperationID)
	if err == nil {
		if prior != fingerprint {
			return b, domain.ErrConflict
		}
		b, err = bindingTx(ctx, tx, project, id)
		if err != nil {
			return b, err
		}
		record, readErr := s.recordTx(ctx, tx, project, b.QualificationID)
		if readErr != nil {
			return b, readErr
		}
		return b, s.validateRecordTx(ctx, tx, a, project, record)
	}
	if err != sql.ErrNoRows {
		return b, err
	}
	record, err := s.recordTx(ctx, tx, project, input.QualificationID)
	if err != nil {
		return b, err
	}
	if record.Frozen.Suite.TaskKind != input.TaskKind {
		return b, domain.ErrConflict
	}
	if err = s.validateRecordTx(ctx, tx, a, project, record); err != nil {
		return b, err
	}
	head, err := bindingHeadTx(ctx, tx, project, input.TaskKind)
	if err != nil {
		return b, err
	}
	if head != input.ExpectedBinding {
		return b, domain.ErrConflict
	}
	if !input.Rollback {
		r, readErr := qualificationRunTx(ctx, tx, project, record.RunID)
		if readErr != nil {
			return b, readErr
		}
		if r.State != "PASSED" || r.QualificationID != record.ID {
			return b, domain.ErrConflict
		}
		if err = s.guardRunTx(ctx, tx, r, true); err != nil {
			return b, err
		}
	} else {
		var count int
		if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM capability_bindings WHERE project_id=? AND task_kind=? AND qualification_id=?", project, input.TaskKind, record.ID).Scan(&count); err != nil {
			return b, err
		}
		if count == 0 {
			return b, domain.ErrConflict
		}
	}
	b = QualifiedBinding{ID: NewID(), TaskKind: input.TaskKind, QualificationID: record.ID, PreviousID: head, Enabled: true, Current: true, Valid: true, Qualification: &record}
	if _, err = tx.ExecContext(ctx, "INSERT INTO capability_bindings(id,project_id,task_kind,qualification_id,previous_id,created_by) VALUES(?,?,?,?,?,?)", b.ID, project, b.TaskKind, record.ID, head, a.ID); err != nil {
		return b, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO capability_binding_heads(project_id,task_kind,binding_id) VALUES(?,?,?) ON DUPLICATE KEY UPDATE binding_id=VALUES(binding_id),revision=revision+1", project, b.TaskKind, b.ID); err != nil {
		return b, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO operations(actor_id,operation_id,kind,request_hash,result_id) VALUES(?,?,'qualification.bind',?,?)", a.ID, input.OperationID, fingerprint, b.ID); err != nil {
		return b, err
	}
	if err = audit(ctx, tx, a, "qualification.bind", b.ID); err != nil {
		return b, err
	}
	return b, tx.Commit()
}

func (s *Qualifications) Bindings(ctx context.Context, a domain.Actor, project string) ([]QualifiedBinding, error) {
	tx, a, _, err := s.IP.begin(ctx, a, project, false)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, "SELECT id,task_kind,qualification_id,previous_id,enabled FROM capability_bindings WHERE project_id=? ORDER BY created_at DESC,id LIMIT 200", project)
	if err != nil {
		return nil, err
	}
	items := []QualifiedBinding{}
	for rows.Next() {
		var b QualifiedBinding
		if err = rows.Scan(&b.ID, &b.TaskKind, &b.QualificationID, &b.PreviousID, &b.Enabled); err != nil {
			rows.Close()
			return nil, err
		}
		items = append(items, b)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for i := range items {
		b := &items[i]
		head, readErr := bindingHeadTx(ctx, tx, project, b.TaskKind)
		if readErr != nil {
			return nil, readErr
		}
		b.Current = head == b.ID
		record, readErr := s.recordTx(ctx, tx, project, b.QualificationID, true)
		if readErr != nil {
			return nil, readErr
		}
		b.Qualification = &record
		b.Valid = b.Enabled && s.validateRecordTx(ctx, tx, a, project, record) == nil
	}
	return items, nil
}

func (s *Qualifications) DisableBinding(ctx context.Context, a domain.Actor, project, id string) error {
	tx, a, p, err := s.IP.begin(ctx, a, project, false)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if a.Role != "owner" || p.Role != "owner" {
		return domain.ErrForbidden
	}
	if _, err = bindingTx(ctx, tx, project, id); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE capability_bindings SET enabled=FALSE WHERE project_id=? AND id=?", project, id); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM capability_binding_heads WHERE project_id=? AND binding_id=?", project, id); err != nil {
		return err
	}
	if err = audit(ctx, tx, a, "qualification.binding.disable", id); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Qualifications) RevokeVersion(ctx context.Context, a domain.Actor, project, qualificationID, reason string) error {
	tx, _, _, err := s.IP.begin(ctx, a, project, false)
	if err != nil {
		return err
	}
	record, err := s.recordTx(ctx, tx, project, qualificationID)
	tx.Rollback()
	if err != nil {
		return err
	}
	return (&Capabilities{IP: s.IP}).Revoke(ctx, a, project, record.Frozen.Candidate.ID, reason)
}
