package service

import (
	"context"
	"time"

	"novelstudio/internal/authority"
	"novelstudio/internal/domain"
)

// RecycleProject describes a time window, not permission to restore content.
// Adoption must recheck authority and content availability in its own transaction.
type RecycleProject struct {
	ID           string     `json:"id"`
	Name         string     `json:"name"`
	Revision     int64      `json:"revision"`
	DeletedAt    *time.Time `json:"deleted_at,omitempty"`
	RecoverUntil *time.Time `json:"recover_until,omitempty"`
	CheckedAt    time.Time  `json:"checked_at"`
	Window       string     `json:"window"`
}

type RecycleProjectPage struct {
	Items      []RecycleProject `json:"items"`
	NextCursor string           `json:"next_cursor"`
}

func applyRecoveryWindow(item *RecycleProject, evidence authority.DeletionEvidence) error {
	if evidence.Sequence == 0 || evidence.CheckedAt.IsZero() || evidence.DeletedAt.IsZero() {
		return domain.ErrNotReady
	}
	item.CheckedAt = evidence.CheckedAt
	item.Window = "UNVERIFIED_CLOCK"
	if !evidence.VerifiedUTC {
		return nil
	}
	if evidence.DeletedAt.After(evidence.CheckedAt) {
		return domain.ErrNotReady
	}
	deadline := evidence.DeletedAt.Add(7 * 24 * time.Hour)
	item.DeletedAt, item.RecoverUntil = &evidence.DeletedAt, &deadline
	item.Window = "EXPIRED"
	if evidence.CheckedAt.Before(deadline) {
		item.Window = "WITHIN_WINDOW"
	}
	return nil
}

func (s *Workspace) RecycleProjects(ctx context.Context, actorID, after string) (RecycleProjectPage, error) {
	page := RecycleProjectPage{Items: []RecycleProject{}}
	if after != "" && !lowerHex32(after) {
		return page, domain.ErrValidation
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return page, err
	}
	defer tx.Rollback()
	actor, err := s.actorTx(ctx, tx, actorID)
	if err != nil {
		return page, err
	}
	if actor.Role != "owner" {
		return page, domain.ErrForbidden
	}
	rows, err := tx.QueryContext(ctx, "SELECT id,name,revision FROM projects WHERE workspace_id=? AND deleted_at IS NOT NULL AND id>? ORDER BY id LIMIT 26 FOR SHARE", actor.WorkspaceID, after)
	if err != nil {
		return page, err
	}
	for rows.Next() {
		var item RecycleProject
		if err := rows.Scan(&item.ID, &item.Name, &item.Revision); err != nil {
			rows.Close()
			return RecycleProjectPage{}, err
		}
		page.Items = append(page.Items, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return RecycleProjectPage{}, err
	}
	if len(page.Items) > 25 {
		page.Items = page.Items[:25]
		page.NextCursor = page.Items[24].ID
	}
	for i := range page.Items {
		evidence, err := protectedLedger().DeletionEvidenceTx(ctx, tx, page.Items[i].ID)
		if err != nil || applyRecoveryWindow(&page.Items[i], evidence) != nil {
			return RecycleProjectPage{}, domain.ErrNotReady
		}
	}
	if err := tx.Commit(); err != nil {
		return RecycleProjectPage{}, err
	}
	return page, nil
}
