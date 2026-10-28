package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"novelstudio/internal/domain"
	"novelstudio/internal/qualification"
)

func novelReportFixture(t *testing.T) (QualificationReport, qualification.Suite) {
	t.Helper()
	suite, err := qualification.Lookup(qualification.NovelSemanticSuiteID)
	if err != nil {
		t.Fatal(err)
	}
	entry := suite.Public()
	report := QualificationReport{Frozen: QualificationFrozen{Suite: entry}, Steps: []QualificationStep{}}
	for _, c := range entry.Cases {
		var input domain.SemanticReviewDocument
		if err = json.Unmarshal(c.Input, &input); err != nil {
			t.Fatal(err)
		}
		output := domain.SemanticReviewReport{Schema: 1, TargetHash: input.Target.Hash, Verdict: "PASS", Coverage: input.RequiredCoverage, Issues: []domain.SemanticReviewIssue{}, Summary: "Unit fixture for authority gate only"}
		raw, _ := json.Marshal(output)
		if !suite.Score(c.ID, raw).Passed {
			found := false
			for _, category := range domain.SemanticReviewCategories() {
				output.Verdict = "FAIL"
				output.Issues = []domain.SemanticReviewIssue{{Category: category, Severity: "blocker", TargetKey: "resource:scene", AnchorKeys: []string{"canon", "source"}, Explanation: "Local fixture"}}
				raw, _ = json.Marshal(output)
				if suite.Score(c.ID, raw).Passed {
					found = true
					break
				}
			}
			if !found {
				t.Fatal("fixture has no valid output", c.ID)
			}
		}
		sum := sha256.Sum256(raw)
		for _, arm := range []string{"no_method", "baseline", "candidate"} {
			report.Steps = append(report.Steps, qualification.QualificationStep{ID: arm + "-" + c.ID, Arm: arm, CaseID: c.ID, ChildID: "child-" + arm + "-" + c.ID, State: "SUCCEEDED", Metrics: qualification.EvaluationMetrics{Output: string(raw), OutputHash: hex.EncodeToString(sum[:]), UsageKnown: true, CostKnown: true, DurationKnown: true}})
		}
	}
	return report, suite
}

func TestNovelSemanticQualificationRecomputesGateFromAllActualOutputs(t *testing.T) {
	for _, missed := range []int{0, 10, 11, 100} {
		t.Run("missed-"+strconv.Itoa(missed), func(t *testing.T) {
			report, suite := novelReportFixture(t)
			remaining := missed
			for i := range report.Steps {
				step := &report.Steps[i]
				var output domain.SemanticReviewReport
				_ = json.Unmarshal([]byte(step.Metrics.Output), &output)
				if step.Arm != "candidate" || output.Verdict != "FAIL" || remaining == 0 {
					continue
				}
				remaining--
				output.Verdict, output.Issues = "PASS", []domain.SemanticReviewIssue{}
				raw, _ := json.Marshal(output)
				sum := sha256.Sum256(raw)
				step.Metrics.Output, step.Metrics.OutputHash = string(raw), hex.EncodeToString(sum[:])
				step.State = "FAILED_CHECK"
			}
			report.NovelSemantics = &qualification.NovelSemanticSummary{Arms: []qualification.NovelSemanticArm{{Arm: "candidate", Passed: true}}}
			if reportComplete(report, suite) != (missed <= 10) {
				t.Fatal("claimed summary replaced actual threshold", missed)
			}
		})
	}
}

func TestNovelSemanticQualificationCannotIgnoreFrozenIdentityOrUnknownCharges(t *testing.T) {
	for _, mutation := range []string{"suite-hash", "missing-step", "unknown-charge", "failed-state", "professional"} {
		t.Run(mutation, func(t *testing.T) {
			report, suite := novelReportFixture(t)
			switch mutation {
			case "suite-hash":
				report.Frozen.Suite.Hash = strings.Repeat("a", 64)
			case "missing-step":
				report.Steps = report.Steps[1:]
			case "unknown-charge":
				report.Steps[0].Metrics.CostKnown = false
			case "failed-state":
				report.Steps[2].State = "INVALID_OUTPUT"
			case "professional":
				report.ProfessionalQualified = true
			}
			if reportComplete(report, suite) {
				t.Fatal("unqualified evidence granted authority", mutation)
			}
		})
	}
}
