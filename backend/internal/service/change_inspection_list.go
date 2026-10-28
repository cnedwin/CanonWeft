package service

import (
	"context"
	"encoding/hex"
	"strings"

	"novelstudio/internal/domain"
)

type ChangeInspectionPage struct {
	Items []Generation `json:"items"`
	Next  string       `json:"next,omitempty"`
}

// This is a page of readable runs, never a whole-plan coverage certificate.
// Each opened report revalidates its own versions and qualified execution.
func (s *Generations) ChangeInspections(ctx context.Context, a domain.Actor, project, planID, branch, after string) (ChangeInspectionPage, error) {
	result := ChangeInspectionPage{Items: []Generation{}}
	if after != "" {
		if len(after) != 32 || strings.ToLower(after) != after {
			return result, domain.ErrValidation
		}
		if _, err := hex.DecodeString(after); err != nil {
			return result, domain.ErrValidation
		}
	}
	tx, a, _, err := s.IP.begin(ctx, a, project, false)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	if _, err = branchTx(ctx, tx, project, branch); err != nil {
		return result, err
	}
	if _, err = readChangePlanTx(ctx, tx, project, planID); err != nil {
		return result, err
	}
	rows, err := tx.QueryContext(ctx, "SELECT "+generationColumns+" FROM generation_tasks WHERE project_id=? AND branch_id=? AND (actor_id=? OR ?) AND JSON_UNQUOTE(JSON_EXTRACT(frozen,'$.change_plan_id'))=? AND JSON_UNQUOTE(JSON_EXTRACT(frozen,'$.capability.task_kind'))='shared.change-impact' AND (?='' OR id<?) ORDER BY id DESC LIMIT 21", project, branch, a.ID, a.Role == "owner", planID, after, after)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		g, readErr := scanGeneration(rows)
		if readErr != nil {
			return result, readErr
		}
		result.Items = append(result.Items, g)
	}
	if err = rows.Err(); err != nil {
		return result, err
	}
	if len(result.Items) > 20 {
		result.Items = result.Items[:20]
		result.Next = result.Items[19].ID
	}
	return result, nil
}
