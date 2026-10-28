package service

import (
	"context"
	"database/sql"
	"fmt"
	"novelstudio/internal/domain"
)

func (s *IP) novelWritingBasisTx(ctx context.Context, tx *sql.Tx, project, branch, role, detail string) (*domain.DependencyRef, error) {
	if role != "writer" {
		if detail != "" {
			return nil, domain.ErrValidation
		}
		return nil, nil
	}
	var count int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM novels WHERE project_id=? AND branch_id=?", project, branch).Scan(&count); err != nil {
		return nil, err
	}
	if count == 0 {
		if detail != "" {
			return nil, domain.ErrValidation
		}
		return nil, nil
	}
	if count != 1 || detail == "" {
		return nil, fmt.Errorf("%w：请先确认世界与故事、全书章节安排及本章细纲，再生成正文", domain.ErrConflict)
	}
	c, err := newPlanningCheck(ctx, tx, s, project, branch)
	if err != nil {
		return nil, err
	}
	r, err := resourceTx(ctx, tx, project, detail)
	if err != nil {
		return nil, err
	}
	kind, scope, err := planIdentityFromResource(r)
	if err != nil || kind != "detail" || r.BranchID != branch {
		return nil, domain.ErrValidation
	}
	current, _, err := c.current(kind, scope)
	if err != nil {
		return nil, err
	}
	if current.ID != r.ID {
		return nil, domain.ErrConflict
	}
	pin := planPin(current)
	return &pin, nil
}

func (s *IP) validateNovelWritingBasisTx(ctx context.Context, tx *sql.Tx, g Generation) error {
	detail := ""
	if g.Frozen.NovelDetail != nil {
		detail = g.Frozen.NovelDetail.ID
	}
	current, err := s.novelWritingBasisTx(ctx, tx, g.ProjectID, g.BranchID, g.Frozen.Role, detail)
	if err != nil {
		return err
	}
	if (current == nil) != (g.Frozen.NovelDetail == nil) || (current != nil && *current != *g.Frozen.NovelDetail) {
		return fmt.Errorf("%w：本章细纲已变化，需重新准备生成", domain.ErrConflict)
	}
	return nil
}
