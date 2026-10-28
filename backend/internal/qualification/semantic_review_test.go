package qualification

import (
	"encoding/json"
	"novelstudio/internal/domain"
	"testing"
)

func TestSemanticReviewSuiteRequiresIndependentFindingNotSelfScore(t *testing.T) {
	s, err := Lookup("shared-semantic-review-v1")
	if err != nil {
		t.Fatal(err)
	}
	if s.Public().TaskKind != "shared.semantic-review" || s.Public().Professional || s.Public().Scope != "bounded.shared-consistency" {
		t.Fatal("wrong qualification scope")
	}
	if len(s.Public().Cases) < 6 {
		t.Fatal("semantic counterexamples missing")
	}
	for _, c := range s.Public().Cases {
		var input domain.SemanticReviewDocument
		if err = json.Unmarshal(c.Input, &input); err != nil {
			t.Fatal(err)
		}
		report := domain.SemanticReviewReport{Schema: 1, TargetHash: input.Target.Hash, Verdict: "PASS", Coverage: input.RequiredCoverage, Issues: []domain.SemanticReviewIssue{}, Summary: "完成"}
		raw, _ := json.Marshal(report)
		if s.Score(c.ID, raw).Passed == c.Negative {
			t.Fatalf("constant PASS has wrong outcome for %s", c.ID)
		}
	}
}

func TestSemanticReviewSuiteGoldIsFixedAndUsesActualAnchors(t *testing.T) {
	s, err := Lookup("shared-semantic-review-v1")
	if err != nil {
		t.Fatal(err)
	}
	var input domain.SemanticReviewDocument
	for _, c := range s.Public().Cases {
		if c.ID == "belief" {
			if err = json.Unmarshal(c.Input, &input); err != nil {
				t.Fatal(err)
			}
		}
	}
	report := domain.SemanticReviewReport{Schema: 1, TargetHash: input.Target.Hash, Verdict: "FAIL", Coverage: input.RequiredCoverage, Issues: []domain.SemanticReviewIssue{{Category: "fact_belief_claim", Severity: "blocker", TargetKey: "resource:scene", AnchorKeys: []string{"source"}, Explanation: "事实尚未成立，角色的信念不等于作者真相"}}, Summary: "存在冲突"}
	raw, _ := json.Marshal(report)
	if !s.Score("belief", raw).Passed {
		t.Fatal("specific supported finding rejected")
	}
	report.Issues[0].AnchorKeys = []string{"invented"}
	raw, _ = json.Marshal(report)
	if s.Score("belief", raw).Passed {
		t.Fatal("invented citation qualified")
	}
}
