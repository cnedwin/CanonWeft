package service

import (
	"context"
	"database/sql"
	"novelstudio/internal/authority"
	"novelstudio/internal/domain"
	"os"
	"strconv"
)

func protectedLedger() authority.Ledger {
	schema := os.Getenv("STUDIO_AUTHORITY_SCHEMA")
	if schema == "" {
		schema = "studio_authority"
	}
	return authority.Ledger{Schema: schema}
}

// DeleteProject performs T0 access revocation. History and blobs remain intact;
// later retention/physical purge and restore-as-new are separate operations.
func (s *Workspace) DeleteProject(ctx context.Context, actorID, id, confirmation string, revision int64, operation string) error {
	if revision < 1 || !validName(confirmation, 120) || !validName(operation, 120) {
		return domain.ErrValidation
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	a, err := s.actorTx(ctx, tx, actorID)
	if err != nil {
		return err
	}
	if !domain.CanEdit(a.Role) {
		return domain.ErrForbidden
	}
	fingerprint := digest("project.delete\x00" + id + "\x00" + confirmation + "\x00" + strconv.FormatInt(revision, 10))
	var stored, result string
	stored, result, err = operationReceiptTx(ctx, tx, a, operation)
	if err == nil {
		if stored != fingerprint || result != id {
			return domain.ErrConflict
		}
		deleted, err := protectedLedger().DeletionRecordedTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if !deleted {
			return domain.ErrNotReady
		}
		if err = registerProjectDeletionLifecycleTx(ctx, tx, id); err != nil {
			return err
		}
		return tx.Commit()
	}
	if err != sql.ErrNoRows {
		return err
	}
	p, err := projectTx(ctx, tx, a, id)
	if err != nil {
		return err
	}
	if p.Role != "owner" {
		return domain.ErrForbidden
	}
	if confirmation != p.Name {
		return domain.ErrValidation
	}
	if p.Revision != revision {
		return domain.ErrConflict
	}
	if _, err = tx.ExecContext(ctx, "UPDATE projects SET deleted_at=UTC_TIMESTAMP(6),revision=revision+1 WHERE id=?", id); err != nil {
		return err
	}
	if err = protectedLedger().AppendTx(ctx, tx, authority.Event{Kind: authority.ProjectDeleted, ProjectID: id, ResourceID: id, ActorID: a.ID, Reason: "owner confirmed project deletion"}); err != nil {
		return err
	}
	if err = registerProjectDeletionLifecycleTx(ctx, tx, id); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO operations(actor_id,operation_id,kind,request_hash,result_id) VALUES(?,?,'project.delete',?,?)", a.ID, operation, fingerprint, id); err != nil {
		return err
	}
	if err = audit(ctx, tx, a, "project.delete", id); err != nil {
		return err
	}
	return tx.Commit()
}
