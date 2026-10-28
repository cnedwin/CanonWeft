package service

import (
	"novelstudio/internal/domain"
	"testing"
)

func TestInspectionCoveragePreservesUncertaintyAndUnattemptedScope(t *testing.T) {
	refs := []domain.DependencyRef{{Kind: "resource", ID: "a", BranchID: "b", Revision: "1"}, {Kind: "resource", ID: "b", BranchID: "b", Revision: "1"}, {Kind: "resource", ID: "c", BranchID: "b", Revision: "1"}}
	plan := domain.ChangePlan{ID: "plan", BranchID: "b"}
	for _, ref := range refs {
		plan.Items = append(plan.Items, domain.ImpactItem{Node: ref})
	}
	coverage := newInspectionCoverage(plan, "b")
	coverage.add(Generation{ID: "unknown", State: "SUCCEEDED"}, ChangeInspection{Current: true, Counted: true, Items: []ChangeInspectionItem{{Ref: refs[0], State: "UNCERTAIN"}}}, nil)
	coverage.add(Generation{ID: "pass", State: "SUCCEEDED"}, ChangeInspection{Current: true, Counted: true, Items: []ChangeInspectionItem{{Ref: refs[0], State: "CHECKED_NO_ISSUE_REPORTED"}}}, nil)
	coverage.add(Generation{ID: "pending", State: "PREPARED"}, ChangeInspection{Current: true, Items: []ChangeInspectionItem{{Ref: refs[1], State: "UNCHECKED"}}}, nil)
	coverage.finish()
	if coverage.Checked != 1 || coverage.Uncertain != 1 || coverage.Pending != 1 || coverage.AllListedChecked || len(coverage.NextBatch) != 1 || coverage.NextBatch[0] != "c" {
		t.Fatalf("false coverage or repeated preparation: %+v", coverage)
	}
	if coverage.Items[0].State != "UNCERTAIN" || len(coverage.Items[0].Runs) != 2 {
		t.Fatal("PASS erased uncertainty", coverage.Items[0])
	}
	coverage.add(Generation{ID: "finding", State: "SUCCEEDED"}, ChangeInspection{Current: true, Counted: true, Items: []ChangeInspectionItem{{Ref: refs[0], State: "CHECKED_WITH_FINDINGS"}}}, nil)
	coverage.finish()
	if coverage.Findings != 1 || coverage.Uncertain != 1 {
		t.Fatal("uncertainty hid an existing finding", coverage)
	}
	coverage.add(Generation{ID: "expired"}, ChangeInspection{}, domain.ErrMaterialExpired)
	coverage.finish()
	if coverage.AutomaticReady || len(coverage.NextBatch) != 0 || coverage.UnreadableRuns != 1 {
		t.Fatal("unknown targets automatically retried", coverage)
	}
}

func TestInspectionCoverageRejectsOldRefsAndKeepsManualRetries(t *testing.T) {
	ref := domain.DependencyRef{Kind: "resource", ID: "a", BranchID: "b", Revision: "2"}
	plan := domain.ChangePlan{ID: "plan", BranchID: "b", Items: []domain.ImpactItem{{Node: ref}}}
	coverage := newInspectionCoverage(plan, "b")
	coverage.add(Generation{ID: "failed", State: "FAILED"}, ChangeInspection{Current: true, Items: []ChangeInspectionItem{{Ref: ref}}}, nil)
	coverage.finish()
	if coverage.NeedsAttention != 1 || len(coverage.NextBatch) != 0 || coverage.Items[0].State != "NEEDS_ATTENTION" {
		t.Fatal("failed batch silently retried", coverage)
	}
	old := ref
	old.Revision = "1"
	coverage.add(Generation{ID: "old", State: "SUCCEEDED"}, ChangeInspection{Current: true, Counted: true, Items: []ChangeInspectionItem{{Ref: old, State: "CHECKED_NO_ISSUE_REPORTED"}}}, nil)
	coverage.finish()
	if coverage.Checked != 0 || coverage.UnreadableRuns != 1 || coverage.AutomaticReady {
		t.Fatal("old revision counted", coverage)
	}
}
