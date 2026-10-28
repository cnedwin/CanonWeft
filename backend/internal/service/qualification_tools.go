package service

import (
	"context"
	"database/sql"
	"novelstudio/internal/domain"
	"novelstudio/internal/isolation"
)

func (s *ToolTrials) qualifiedToolTx(ctx context.Context, tx *sql.Tx, a domain.Actor, project, bindingID, task string) (CapabilityQualification, error) {
	var empty CapabilityQualification
	a, err := s.IP.Workspace.actorTx(ctx, tx, a.ID)
	if err != nil {
		return empty, err
	}
	b, err := bindingTx(ctx, tx, project, bindingID)
	if err != nil {
		return empty, err
	}
	if !b.Enabled || b.TaskKind != task {
		return empty, domain.ErrConflict
	}
	q := &Qualifications{IP: s.IP}
	record, err := q.recordTx(ctx, tx, project, b.QualificationID)
	if err != nil {
		return record, err
	}
	if record.Frozen.Candidate.Kind != "tool" || record.Frozen.RuntimeHash != isolation.ApprovedImageSHA256 {
		return record, domain.ErrConflict
	}
	return record, q.validateRecordTx(ctx, tx, a, project, record)
}
