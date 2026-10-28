package semanticreview

import (
	"encoding/json"
	"novelstudio/internal/domain"
	"strings"
	"testing"
)

func reviewDocument() domain.SemanticReviewDocument {
	return domain.SemanticReviewDocument{Schema: 1, Target: domain.SemanticReviewTarget{Kind: "ip_candidate", ID: "candidate", Hash: strings.Repeat("a", 64), BranchID: "branch", ExpectedIPHead: "base"}, RequiredCoverage: []string{"entity:hero", "assertion:fact"}, Anchors: []domain.SemanticReviewAnchor{{Key: "fact", TargetKey: "assertion:fact", Text: "他相信自己不会死亡"}}}
}

func TestSemanticReportRequiresExactTargetCompleteCoverageAndRealAnchors(t *testing.T) {
	input := reviewDocument()
	valid := domain.SemanticReviewReport{Schema: 1, TargetHash: input.Target.Hash, Verdict: "PASS", Coverage: []string{"assertion:fact", "entity:hero"}, Issues: []domain.SemanticReviewIssue{}, Summary: "检查完成"}
	if err := valid.Validate(input); err != nil {
		t.Fatal(err)
	}
	for _, mutation := range []func(*domain.SemanticReviewReport){
		func(r *domain.SemanticReviewReport) { r.TargetHash = strings.Repeat("b", 64) },
		func(r *domain.SemanticReviewReport) { r.Coverage = []string{"entity:hero"} },
		func(r *domain.SemanticReviewReport) { r.Coverage = []string{"entity:hero", "entity:hero"} },
		func(r *domain.SemanticReviewReport) {
			r.Issues = []domain.SemanticReviewIssue{{Category: "fact_belief_claim", Severity: "blocker", TargetKey: "assertion:fact", AnchorKeys: []string{"fact"}, Explanation: "信念不能当事实"}}
		},
		func(r *domain.SemanticReviewReport) {
			r.Verdict = "FAIL"
			r.Issues = []domain.SemanticReviewIssue{{Category: "identity", Severity: "blocker", TargetKey: "entity:hero", AnchorKeys: []string{"fake"}, Explanation: "虚构引用"}}
		},
		func(r *domain.SemanticReviewReport) {
			r.Verdict = "FAIL"
			r.Issues = []domain.SemanticReviewIssue{{Category: "invented", Severity: "blocker", TargetKey: "entity:hero", AnchorKeys: []string{"fact"}, Explanation: "不存在类别"}}
		},
	} {
		r := valid
		mutation(&r)
		if r.Validate(input) == nil {
			t.Fatalf("invalid report accepted: %+v", r)
		}
	}
}

func TestSemanticReportUnknownCannotBecomePassAndStrictDecode(t *testing.T) {
	input := reviewDocument()
	report := domain.SemanticReviewReport{Schema: 1, TargetHash: input.Target.Hash, Verdict: "UNKNOWN", Coverage: input.RequiredCoverage, Issues: []domain.SemanticReviewIssue{{Category: "unsupported", Severity: "unknown", TargetKey: "assertion:fact", AnchorKeys: []string{"fact"}, Explanation: "语义不足，无法确定"}}, Summary: "需要补充条件"}
	if err := report.Validate(input); err != nil {
		t.Fatal(err)
	}
	report.Verdict = "PASS"
	if report.Validate(input) == nil {
		t.Fatal("unknown promoted to pass")
	}
	report.Verdict = "UNKNOWN"
	raw, _ := json.Marshal(report)
	if _, err := domain.ParseSemanticReviewReport(append(raw, []byte(" {}")...), input); err == nil {
		t.Fatal("trailing JSON accepted")
	}
	raw = []byte(strings.Replace(string(raw), `"schema":1`, `"approved":true,"schema":1`, 1))
	if _, err := domain.ParseSemanticReviewReport(raw, input); err == nil {
		t.Fatal("undeclared approval accepted")
	}
	valid, _ := json.Marshal(report)
	duplicate := []byte(strings.Replace(string(valid), `"verdict":"UNKNOWN"`, `"verdict":"PASS","verdict":"UNKNOWN"`, 1))
	if _, err := domain.ParseSemanticReviewReport(duplicate, input); err == nil {
		t.Fatal("duplicate verdict accepted")
	}
}
