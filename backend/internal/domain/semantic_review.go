package domain

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
)

type SemanticReviewTarget struct {
	Kind           string `json:"kind"`
	ID             string `json:"id"`
	Hash           string `json:"hash"`
	BranchID       string `json:"branch_id"`
	ExpectedIPHead string `json:"expected_ip_head"`
}

type SemanticReviewAnchor struct {
	Key       string `json:"key"`
	TargetKey string `json:"target_key"`
	Text      string `json:"text"`
}

type SemanticReviewArtifact struct {
	Target ArtifactReviewTarget `json:"target"`
	Text   string               `json:"text"`
}

// SemanticReviewDocument is assembled by the server from exact authorized
// snapshots. The model cannot nominate different targets or add evidence.
type SemanticReviewDocument struct {
	Schema           int                      `json:"schema"`
	Target           SemanticReviewTarget     `json:"target"`
	Snapshot         Snapshot                 `json:"snapshot"`
	Delta            *BranchDelta             `json:"delta,omitempty"`
	Artifacts        []SemanticReviewArtifact `json:"artifacts"`
	HardFindings     []string                 `json:"hard_findings"`
	RequiredCoverage []string                 `json:"required_coverage"`
	Anchors          []SemanticReviewAnchor   `json:"anchors"`
	Scope            string                   `json:"scope"`
}

// InputHash is the sole new input blob root. Other execution authority is kept
// in the linked Generation's frozen request, never duplicated as a client grant.
type SemanticReviewFrozen struct {
	ContentScopeHash    string   `json:"content_scope_hash,omitempty"`
	Schema              int      `json:"schema"`
	InputHash           string   `json:"input_hash"`
	ExpectedIPHead      string   `json:"expected_ip_head"`
	DeltaHash           string   `json:"delta_hash"`
	SourceGenerationIDs []string `json:"source_generation_ids"`
	ReviewerBindingID   string   `json:"reviewer_binding_id"`
	PriceID             string   `json:"price_id"`
	MaxOutputTokens     int      `json:"max_output_tokens"`
	Scope               string   `json:"scope"`
}

type SemanticReviewIssue struct {
	Category    string   `json:"category"`
	Severity    string   `json:"severity"`
	TargetKey   string   `json:"target_key"`
	AnchorKeys  []string `json:"anchor_keys"`
	Explanation string   `json:"explanation"`
}

type SemanticReviewReport struct {
	Schema     int                   `json:"schema"`
	TargetHash string                `json:"target_hash"`
	Verdict    string                `json:"verdict"`
	Coverage   []string              `json:"coverage"`
	Issues     []SemanticReviewIssue `json:"issues"`
	Summary    string                `json:"summary"`
}

func SemanticReviewCategories() []string {
	return []string{"world_rule", "identity", "fact_belief_claim", "temporal", "knowledge_visibility", "causal", "outline_chapter_consistency", "unsupported", "coverage"}
}

func (r SemanticReviewReport) Validate(input SemanticReviewDocument) error {
	if r.Schema != 1 || input.Schema != 1 || r.TargetHash != input.Target.Hash || !capabilityHash.MatchString(r.TargetHash) || !slices.Contains([]string{"PASS", "FAIL", "UNKNOWN"}, r.Verdict) || strings.TrimSpace(r.Summary) == "" || len(r.Summary) > 8000 {
		return ErrValidation
	}
	required := map[string]bool{}
	for _, key := range input.RequiredCoverage {
		if key == "" || required[key] {
			return ErrValidation
		}
		required[key] = true
	}
	covered := map[string]bool{}
	for _, key := range r.Coverage {
		if !required[key] || covered[key] {
			return fmt.Errorf("审查覆盖不匹配: %w", ErrValidation)
		}
		covered[key] = true
	}
	if len(covered) != len(required) {
		return fmt.Errorf("审查未覆盖全部固定目标: %w", ErrValidation)
	}
	anchors := map[string]SemanticReviewAnchor{}
	for _, anchor := range input.Anchors {
		if anchor.Key == "" || anchor.Text == "" || !required[anchor.TargetKey] {
			return ErrValidation
		}
		if _, exists := anchors[anchor.Key]; exists {
			return ErrValidation
		}
		anchors[anchor.Key] = anchor
	}
	hasBlocker, hasUnknown := false, false
	for _, issue := range r.Issues {
		if !slices.Contains(SemanticReviewCategories(), issue.Category) || !slices.Contains([]string{"blocker", "warning", "unknown"}, issue.Severity) || !required[issue.TargetKey] || strings.TrimSpace(issue.Explanation) == "" || len(issue.Explanation) > 8000 || len(issue.AnchorKeys) == 0 {
			return ErrValidation
		}
		cited := map[string]bool{}
		for _, key := range issue.AnchorKeys {
			if _, exists := anchors[key]; !exists || cited[key] {
				return fmt.Errorf("审查引用不存在或重复: %w", ErrValidation)
			}
			cited[key] = true
		}
		hasBlocker = hasBlocker || issue.Severity == "blocker"
		hasUnknown = hasUnknown || issue.Severity == "unknown" || issue.Category == "unsupported"
	}
	if (r.Verdict == "PASS" && (hasBlocker || hasUnknown || len(input.HardFindings) > 0)) || (r.Verdict == "FAIL" && !hasBlocker) || (r.Verdict == "UNKNOWN" && !hasUnknown) {
		return fmt.Errorf("结论与具体问题不一致: %w", ErrValidation)
	}
	return nil
}

func ParseSemanticReviewReport(raw []byte, input SemanticReviewDocument) (SemanticReviewReport, error) {
	var report SemanticReviewReport
	if len(raw) > 512*1024 {
		return report, ErrValidation
	}
	if err := validateReviewJSON(raw); err != nil {
		return report, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&report); err != nil {
		return report, ErrValidation
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return report, ErrValidation
	}
	return report, report.Validate(input)
}
