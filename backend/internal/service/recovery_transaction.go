package service

import (
	"context"
	"database/sql"
	"novelstudio/internal/domain"
	"sort"
)

// Every recovery component uses current database authority inside the owning
// transaction; a previously displayed recycle-bin window is never a grant.
func recoveryWindowTx(ctx context.Context, tx *sql.Tx, source string) error {
	evidence, err := protectedLedger().DeletionEvidenceTx(ctx, tx, source)
	var item RecycleProject
	if err != nil || applyRecoveryWindow(&item, evidence) != nil {
		return domain.ErrNotReady
	}
	if item.Window != "WITHIN_WINDOW" {
		return domain.ErrForbidden
	}
	return nil
}

func (s *Workspace) lockRecoveryProjectsTx(ctx context.Context, tx *sql.Tx, actorID, source, target string) (domain.Actor, error) {
	if tx == nil || !lowerHex32(source) || !lowerHex32(target) || source == target {
		return domain.Actor{}, domain.ErrValidation
	}
	actor, err := s.actorTx(ctx, tx, actorID)
	if err != nil {
		return actor, err
	}
	if actor.Role != "owner" {
		return actor, domain.ErrForbidden
	}
	projects := []string{source, target}
	sort.Strings(projects)
	for _, project := range projects {
		var deleted bool
		var role string
		if err = tx.QueryRowContext(ctx, "SELECT p.deleted_at IS NOT NULL,m.role FROM projects p JOIN project_members m ON m.project_id=p.id AND m.user_id=? WHERE p.id=? AND p.workspace_id=? FOR UPDATE", actor.ID, project, actor.WorkspaceID).Scan(&deleted, &role); err != nil {
			if err == sql.ErrNoRows {
				err = domain.ErrForbidden
			}
			return actor, err
		}
		if role != "owner" || deleted != (project == source) {
			return actor, domain.ErrForbidden
		}
	}
	if active, e := protectedLedger().ProjectActiveTx(ctx, tx, target); e != nil || !active {
		return actor, domain.ErrNotReady
	}
	return actor, recoveryWindowTx(ctx, tx, source)
}

func validRecoveryIDs(ids map[string]string) bool {
	seen := map[string]bool{}
	for source, target := range ids {
		if !lowerHex32(source) || !lowerHex32(target) || source == target || seen[target] || ids[target] != "" {
			return false
		}
		seen[target] = true
	}
	return true
}
