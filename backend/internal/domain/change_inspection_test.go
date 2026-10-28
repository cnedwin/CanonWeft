package domain

import (
	"strings"
	"testing"
)

func inspectionFixture() (ChangeReviewContext, Snapshot) {
	target := ChangeReviewTarget{Ref: DependencyRef{Kind: "resource", BranchID: "branch", ID: "chapter", Revision: "revision"}, Name: "场景", Text: "他仍用银针救人。"}
	target.BodyHash = changeInspectionBodyHash(target.Text)
	target.Ref.Hash = ArtifactEvidenceHash(struct {
		Hash         string
		Policy       *KnowledgePolicy
		Dependencies *DependencyPolicy
	}{target.BodyHash, nil, nil})
	plan := ChangePlan{ID: "plan", BranchID: "branch", BaseHead: "head", GraphHash: strings.Repeat("a", 64), Items: []ImpactItem{{Node: target.Ref}}, Changes: []DependencyChange{{Before: DependencyRef{Kind: "entity", BranchID: "branch", ID: "hero"}}}, Differences: []DependencyDifference{{Ref: DependencyRef{Kind: "entity", ID: "hero"}, Fields: []ChangedField{{Name: "description", Before: "医师", After: "失去医术"}}}}}
	return ChangeReviewContext{Plan: plan, FocusTargets: []DependencyRef{target.Ref}, Targets: []ChangeReviewTarget{target}}, Snapshot{Entities: []Entity{{ID: "hero", Kind: "character", Name: "林舟"}}}
}

func TestChangeInspectionPinsSourcesAndRequiresCompleteCoverage(t *testing.T) {
	source, snapshot := inspectionFixture()
	doc, err := BuildChangeInspectionDocument(source, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Target.Kind != "change_plan" || len(doc.Artifacts) != 1 || doc.Artifacts[0].Target.ReleaseID != "" {
		t.Fatal("inspection became publication", doc)
	}
	report := SemanticReviewReport{Schema: 1, TargetHash: doc.Target.Hash, Verdict: "PASS", Coverage: doc.RequiredCoverage, Issues: []SemanticReviewIssue{}, Summary: "只检查固定材料"}
	if err = report.Validate(doc); err != nil {
		t.Fatal(err)
	}
	report.Coverage = report.Coverage[1:]
	if report.Validate(doc) == nil {
		t.Fatal("omitted target accepted")
	}
	source.Plan.Differences[0].Fields[0].After = "已恢复医术"
	changed, err := BuildChangeInspectionDocument(source, snapshot)
	if err != nil || changed.Target.Hash == doc.Target.Hash {
		t.Fatal("change text not pinned", err)
	}
	source.Plan.Differences = nil
	incomplete, err := BuildChangeInspectionDocument(source, snapshot)
	report.TargetHash, report.Coverage = incomplete.Target.Hash, incomplete.RequiredCoverage
	if err != nil || report.Validate(incomplete) == nil {
		t.Fatal("missing changes reported pass", err)
	}
}

func TestChangeInspectionRejectsScopeAndBodySubstitution(t *testing.T) {
	source, snapshot := inspectionFixture()
	for _, mutate := range []func(*ChangeReviewContext){
		func(s *ChangeReviewContext) { s.FocusTargets = append(s.FocusTargets, s.FocusTargets[0]) },
		func(s *ChangeReviewContext) { s.FocusTargets[0].Revision = "old" },
		func(s *ChangeReviewContext) { s.Targets[0].Text = "替换正文" },
		func(s *ChangeReviewContext) { s.FocusBranchID = "other" },
		func(s *ChangeReviewContext) { s.Targets = nil },
	} {
		source, snapshot = inspectionFixture()
		mutate(&source)
		if _, err := BuildChangeInspectionDocument(source, snapshot); err == nil {
			t.Fatal("invalid inspection accepted", source)
		}
	}
}
