package novel

import (
	"encoding/json"
	"testing"
)

func TestBookReviewEvidenceAndCoverage(t *testing.T) {
	r := BookReviewResult{Coverage: []string{"a", "b"}, Checks: map[string]string{}, Findings: []BookReviewFinding{{ID: "f1", Category: "causality", Severity: "major", Title: "钥匙去向", Explanation: "两章状态不同", Suggestion: "补足钥匙交接", Evidence: []BookReviewEvidence{{ChapterID: "a", Quote: "他把钥匙交给林舟。"}, {ChapterID: "b", Quote: "钥匙仍在他口袋里。"}}}}, Carry: []BookReviewCarry{}, Limits: []string{}}
	for _, key := range BookReviewCategories {
		r.Checks[key] = "issues_found"
	}
	corpus := map[string]string{"a": "他把钥匙交给林舟。", "b": "钥匙仍在他口袋里。"}
	raw, _ := json.Marshal(r)
	if _, e := ReadBookReview(raw, []string{"a", "b"}, corpus); e != nil {
		t.Fatal(e)
	}
	r.Findings[0].Evidence[1].Quote = "林舟把钥匙还给他。"
	raw, _ = json.Marshal(r)
	if _, e := ReadBookReview(raw, []string{"a", "b"}, corpus); e == nil {
		t.Fatal("fabricated quotation accepted")
	}
	r.Findings = nil
	r.Coverage = []string{"a"}
	raw, _ = json.Marshal(r)
	if _, e := ReadBookReview(raw, []string{"a", "b"}, corpus); e == nil {
		t.Fatal("missing chapter treated as reviewed")
	}
}
