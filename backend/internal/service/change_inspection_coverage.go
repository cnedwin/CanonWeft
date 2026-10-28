package service

import (
	"context"
	"errors"
	"slices"

	"novelstudio/internal/domain"
)

type InspectionCoverageItem struct {
	Ref       domain.DependencyRef `json:"ref"`
	State     string               `json:"state"`
	Runs      []string             `json:"runs"`
	Checked   bool                 `json:"checked"`
	Pending   bool                 `json:"pending"`
	Findings  bool                 `json:"findings"`
	Uncertain bool                 `json:"uncertain"`
}

type InspectionCoverage struct {
	PlanID           string                   `json:"plan_id"`
	BranchID         string                   `json:"branch_id"`
	Scope            string                   `json:"scope"`
	Items            []InspectionCoverageItem `json:"items"`
	Checked          int                      `json:"checked"`
	Pending          int                      `json:"pending"`
	Findings         int                      `json:"findings"`
	Uncertain        int                      `json:"uncertain"`
	NeedsAttention   int                      `json:"needs_attention"`
	UnreadableRuns   int                      `json:"unreadable_runs"`
	OutsideScope     int                      `json:"outside_scope"`
	Untracked        int                      `json:"untracked"`
	AutomaticReady   bool                     `json:"automatic_ready"`
	AllListedChecked bool                     `json:"all_listed_checked"`
	NextBatch        []string                 `json:"next_batch"`
	positions        map[domain.DependencyRef]int
}

func newInspectionCoverage(plan domain.ChangePlan, branch string) InspectionCoverage {
	result := InspectionCoverage{PlanID: plan.ID, BranchID: branch, Scope: domain.ChangeInspectionScope, Items: []InspectionCoverageItem{}, NextBatch: []string{}, positions: map[domain.DependencyRef]int{}}
	for _, item := range plan.Items {
		if item.Node.Kind != "resource" || item.Node.BranchID != branch {
			continue
		}
		if _, exists := result.positions[item.Node]; exists {
			continue
		}
		result.positions[item.Node] = len(result.Items)
		result.Items = append(result.Items, InspectionCoverageItem{Ref: item.Node, State: "UNCHECKED", Runs: []string{}})
	}
	for _, ref := range plan.OutsideRecordedClosure {
		if ref.Kind == "resource" && ref.BranchID == branch {
			result.OutsideScope++
		}
	}
	for _, ref := range plan.Untracked {
		if ref.Kind == "resource" && ref.BranchID == branch {
			result.Untracked++
		}
	}
	return result
}

func (c *InspectionCoverage) add(g Generation, report ChangeInspection, err error) {
	// Unknown scope must never turn into an automatic retry of a possibly live call.
	if err != nil || len(report.Items) == 0 {
		c.UnreadableRuns++
		return
	}
	for _, item := range report.Items {
		if _, exists := c.positions[item.Ref]; !exists {
			c.UnreadableRuns++
			return
		}
	}
	for _, item := range report.Items {
		dst := &c.Items[c.positions[item.Ref]]
		if !slices.Contains(dst.Runs, g.ID) {
			dst.Runs = append(dst.Runs, g.ID)
		}
		if report.Current && !g.Cancelled && slices.Contains([]string{"PREPARED", "QUEUED", "RUNNING", "UNKNOWN"}, g.State) {
			dst.Pending = true
		}
		if !report.Current || !report.Counted {
			continue
		}
		dst.Checked = true
		dst.Findings = dst.Findings || item.State == "CHECKED_WITH_FINDINGS"
		dst.Uncertain = dst.Uncertain || item.State == "UNCERTAIN"
		for _, issue := range item.Issues {
			if issue.Severity != "unknown" && issue.Category != "unsupported" {
				dst.Findings = true
			}
		}
		// An additional PASS cannot erase an unresolved finding or unknown on this revision.
		if inspectionStateRank(item.State) > inspectionStateRank(dst.State) {
			dst.State = item.State
		}
	}
}

func inspectionStateRank(state string) int {
	switch state {
	case "CHECKED_NO_ISSUE_REPORTED":
		return 1
	case "CHECKED_WITH_FINDINGS":
		return 2
	case "UNCERTAIN":
		return 3
	default:
		return 0
	}
}

func (c *InspectionCoverage) finish() {
	c.Checked, c.Pending, c.Findings, c.Uncertain, c.NeedsAttention = 0, 0, 0, 0, 0
	c.AutomaticReady = c.UnreadableRuns == 0
	c.NextBatch = []string{}
	for i := range c.Items {
		item := &c.Items[i]
		if item.Pending {
			c.Pending++
		}
		if item.Checked {
			c.Checked++
			if item.Findings {
				c.Findings++
			}
			if item.Uncertain {
				c.Uncertain++
			}
		} else if item.Pending {
			item.State = "PENDING"
		} else if len(item.Runs) > 0 {
			item.State = "NEEDS_ATTENTION"
			c.NeedsAttention++
		} else {
			item.State = "UNCHECKED"
			if c.AutomaticReady && len(c.NextBatch) < 16 {
				c.NextBatch = append(c.NextBatch, item.Ref.ID)
			}
		}
	}
	c.AllListedChecked = len(c.Items) > 0 && c.Checked == len(c.Items) && c.UnreadableRuns == 0
}

// Scan all attempts, not just the UI's first page. A single transaction reuses
// the actual report validator and locks; unreadable attempts block auto-selection.
// Retained summaries are included even after private frozen inputs were erased.
func (s *Generations) ChangeInspectionCoverage(ctx context.Context, a domain.Actor, project, planID, branch string) (InspectionCoverage, error) {
	tx, a, _, err := s.IP.begin(ctx, a, project, false)
	if err != nil {
		return InspectionCoverage{}, err
	}
	defer tx.Rollback()
	plan, err := currentChangePlanTx(ctx, tx, project, branch, planID)
	if err != nil {
		return InspectionCoverage{}, err
	}
	result := newInspectionCoverage(plan, branch)
	after := ""
	for {
		rows, err := tx.QueryContext(ctx, `SELECT id FROM generation_tasks WHERE project_id=? AND branch_id=? AND (
   (JSON_UNQUOTE(JSON_EXTRACT(frozen,'$.change_plan_id'))=? AND JSON_UNQUOTE(JSON_EXTRACT(frozen,'$.capability.task_kind'))='shared.change-impact')
   OR (JSON_UNQUOTE(JSON_EXTRACT(frozen,'$.summary.generation.change_plan_id'))=? AND JSON_UNQUOTE(JSON_EXTRACT(frozen,'$.summary.generation.role'))='critic')
  ) AND id>? ORDER BY id LIMIT 100`, project, branch, planID, planID, after)
		if err != nil {
			return result, err
		}
		ids := []string{}
		for rows.Next() {
			var id string
			if err = rows.Scan(&id); err != nil {
				rows.Close()
				return result, err
			}
			ids = append(ids, id)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return result, err
		}
		for _, id := range ids {
			g, readErr := generationTx(ctx, tx, project, id)
			if readErr == nil && !canControlGeneration(a, g) {
				readErr = domain.ErrForbidden
			}
			var report ChangeInspection
			if readErr == nil {
				report, readErr = s.readChangeInspectionTx(ctx, tx, a, project, id)
			}
			// Operational failures must not masquerade as a complete coverage response.
			if readErr != nil && !errors.Is(readErr, domain.ErrForbidden) && !errors.Is(readErr, domain.ErrMaterialExpired) && !errors.Is(readErr, domain.ErrNotReady) && !errors.Is(readErr, domain.ErrConflict) && !errors.Is(readErr, domain.ErrValidation) {
				return result, readErr
			}
			result.add(g, report, readErr)
		}
		if len(ids) < 100 {
			break
		}
		after = ids[len(ids)-1]
	}
	result.finish()
	return result, nil
}
