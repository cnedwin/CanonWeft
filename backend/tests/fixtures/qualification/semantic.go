package main

import (
	"encoding/json"
	"novelstudio/internal/domain"
	"strings"
)

// Browser-fixture-only answers. This is never linked into the studio server.
func semanticFixtureOutput(prompt string) ([]byte, bool) {
	var input domain.SemanticReviewDocument
	method := strings.Contains(prompt, "<fixed-method-data>\n")
	if method {
		start := strings.Index(prompt, "<fixed-method-data>\n")
		var payload struct {
			Input domain.SemanticReviewDocument `json:"input"`
		}
		if json.NewDecoder(strings.NewReader(prompt[start+len("<fixed-method-data>\n"):])).Decode(&payload) != nil {
			return nil, false
		}
		input = payload.Input
	} else {
		start := strings.Index(prompt, "Input JSON:\n")
		if start < 0 || json.NewDecoder(strings.NewReader(prompt[start+len("Input JSON:\n"):])).Decode(&input) != nil {
			return nil, false
		}
	}
	if input.Schema != 1 || input.Target.Hash == "" {
		return nil, false
	}
	report := domain.SemanticReviewReport{Schema: 1, TargetHash: input.Target.Hash, Verdict: "PASS", Coverage: input.RequiredCoverage, Issues: []domain.SemanticReviewIssue{}, Summary: "测试协议已检查固定对象；这不是实际模型语义质量证明。"}
	if method {
		category := map[string]string{"belief": "fact_belief_claim", "temporal": "temporal", "secret": "knowledge_visibility", "world": "world_rule", "ambiguous": "unsupported", "injection": "fact_belief_claim"}[input.Target.ID]
		if category != "" {
			report.Verdict = "FAIL"
			severity := "blocker"
			if category == "unsupported" {
				report.Verdict = "UNKNOWN"
				severity = "unknown"
			}
			report.Issues = []domain.SemanticReviewIssue{{Category: category, Severity: severity, TargetKey: "resource:scene", AnchorKeys: []string{"source"}, Explanation: "测试协议标记固定反例"}}
		}
	}
	raw, _ := json.Marshal(report)
	return raw, true
}
