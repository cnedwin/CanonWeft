package qualification

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"novelstudio/internal/domain"
)

func novelTestSteps(t *testing.T, suite Suite, change func(heldout, *domain.SemanticReviewReport)) []QualificationStep {
	t.Helper()
	steps := []QualificationStep{}
	for _, c := range suite.entry.Cases {
		input, gold, _ := suite.novelSemanticCase(c.ID)
		report := domain.SemanticReviewReport{Schema: 1, TargetHash: input.Target.Hash, Verdict: gold.Summary, Coverage: input.RequiredCoverage, Issues: []domain.SemanticReviewIssue{}, Summary: "本地测试输出，仅检验固定评分和证据契约"}
		if gold.Summary == "FAIL" {
			report.Issues = []domain.SemanticReviewIssue{{Category: gold.Plan, Severity: "blocker", TargetKey: "resource:scene", AnchorKeys: []string{"canon", "source"}, Explanation: gold.Novel.Rationale}}
		}
		if change != nil {
			change(gold, &report)
		}
		raw, err := json.Marshal(report)
		if err != nil {
			t.Fatal(err)
		}
		steps = append(steps, QualificationStep{ID: c.ID, Arm: "candidate", CaseID: c.ID, ChildID: "child-" + c.ID, State: "SUCCEEDED", Metrics: EvaluationMetrics{Output: string(raw), OutputHash: semanticTextHash(string(raw)), UsageKnown: true, CostKnown: true, DurationKnown: true}})
	}
	return steps
}

func TestNovelSemanticMetricsThresholdsAndCounterexamples(t *testing.T) {
	suite, err := Lookup(NovelSemanticSuiteID)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name                string
		missed, falseAlarms int
		pass                bool
	}{
		{"all-correct", 0, 0, true}, {"recall-exactly-90", 10, 0, true}, {"recall-below-90", 11, 0, false},
		{"precision-above-90", 0, 11, true}, {"precision-below-90", 0, 12, false}, {"constant-pass", 100, 0, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			missed, falseAlarms := test.missed, test.falseAlarms
			steps := novelTestSteps(t, suite, func(gold heldout, report *domain.SemanticReviewReport) {
				if gold.Summary == "FAIL" && missed > 0 {
					missed--
					report.Verdict, report.Issues = "PASS", []domain.SemanticReviewIssue{}
				} else if gold.Summary == "PASS" && falseAlarms > 0 {
					falseAlarms--
					report.Verdict = "FAIL"
					report.Issues = []domain.SemanticReviewIssue{{Category: gold.Plan, Severity: "blocker", TargetKey: "resource:scene", AnchorKeys: []string{"canon", "source"}, Explanation: "故意制造的测试误报"}}
				}
			})
			arm := suite.NovelSemanticMetrics(steps).Arms[2]
			if arm.Passed != test.pass || arm.Cases != 200 || arm.Observed != 200 || arm.Defects != 100 || arm.Allowed != 100 || arm.TruePositive != 100-test.missed || arm.FalseNegative != test.missed || arm.FalsePositive != test.falseAlarms || arm.TrueNegative != 100-test.falseAlarms {
				t.Fatalf("wrong independent counts: %+v", arm)
			}
			if test.missed == 100 && arm.PrecisionDefined {
				t.Fatal("zero positive predictions must not yield a perfect precision")
			}
			if len(arm.Breakdown) < 17 {
				t.Fatal("missing topic and issue-category disclosures")
			}
		})
	}
}

func TestNovelSemanticMetricsRejectMissingForgedAndUnsettledEvidence(t *testing.T) {
	suite, _ := Lookup(NovelSemanticSuiteID)
	for _, mutation := range []string{"missing", "duplicate", "unknown-case", "wrong-hash", "unsettled", "invalid-json", "wrong-category", "only-one-anchor", "copied-self-score"} {
		t.Run(mutation, func(t *testing.T) {
			steps := novelTestSteps(t, suite, func(gold heldout, report *domain.SemanticReviewReport) {
				if gold.Summary != "FAIL" {
					return
				}
				if mutation == "wrong-category" {
					report.Issues[0].Category = "coverage"
				}
				if mutation == "only-one-anchor" {
					report.Issues[0].AnchorKeys = []string{"source"}
				}
			})
			switch mutation {
			case "missing":
				steps = steps[1:]
			case "duplicate":
				steps = append(steps, steps[0])
			case "unknown-case":
				steps = append(steps, QualificationStep{Arm: "candidate", CaseID: "made-up", State: "SUCCEEDED"})
			case "wrong-hash":
				steps[0].Metrics.OutputHash = strings.Repeat("x", 64)
			case "unsettled":
				steps[0].Metrics.CostKnown = false
			case "invalid-json":
				steps[0].Metrics.Output = `{"passed":true}`
				steps[0].Metrics.OutputHash = semanticTextHash(steps[0].Metrics.Output)
			case "copied-self-score":
				steps[0].Metrics.Output = `{}`
				steps[0].Metrics.OutputHash = semanticTextHash(`{}`)
				steps[0].Metrics.Score = Score{Passed: true}
			}
			arm := suite.NovelSemanticMetrics(steps).Arms[2]
			if arm.Passed || arm.Cases != 200 {
				t.Fatal("fabricated/incomplete evidence passed or changed denominator", arm)
			}
			if (mutation == "wrong-category" || mutation == "only-one-anchor") && (arm.Precision != 1 || arm.Recall != 1 || arm.SupportedRecall != 0) {
				t.Fatal("unsupported finding must not hide behind binary detection", arm)
			}
		})
	}
}

func TestNovelCorpusAnnotationsAndRequestsAreSeparated(t *testing.T) {
	suite, _ := Lookup(NovelSemanticSuiteID)
	entry := suite.Public()
	for _, c := range entry.Cases {
		input, gold, ok := suite.novelSemanticCase(c.ID)
		if !ok || gold.Novel == nil || gold.Novel.Topic == "" || gold.Novel.Rationale == "" {
			t.Fatal("annotation missing", c.ID)
		}
		source := input.Anchors[1].Text
		if gold.Novel.SourceHash != semanticTextHash(source) || gold.Novel.Quote != source || gold.Novel.StartRune != 0 || gold.Novel.EndRune != utf8.RuneCountInString(source) {
			t.Fatal("annotation no longer locates the exact source", c.ID)
		}
		request, _, err := EvaluationRequest(entry, c.ID, 1024)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(request.Prompt, gold.Novel.Rationale) || strings.Contains(request.Prompt, `"negative":true`) || strings.Contains(request.Prompt, `"rationale"`) || strings.Contains(request.Prompt, `"expected_verdict"`) || c.Negative {
			t.Fatal("answer metadata leaked into candidate task", c.ID)
		}
	}
	entry.Cases[0].Input = json.RawMessage(`{"source":"changed by author"}`)
	again, _ := Lookup(NovelSemanticSuiteID)
	if strings.Contains(string(again.Public().Cases[0].Input), "changed by author") || again.Public().Hash != suite.Public().Hash {
		t.Fatal("public catalog could mutate trusted cases")
	}
}
