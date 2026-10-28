package qualification

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strconv"

	"novelstudio/internal/domain"
)

type SemanticCounts struct {
	Cases            int     `json:"cases"`
	Defects          int     `json:"defects"`
	Allowed          int     `json:"allowed"`
	Observed         int     `json:"observed"`
	TruePositive     int     `json:"true_positive"`
	FalsePositive    int     `json:"false_positive"`
	TrueNegative     int     `json:"true_negative"`
	FalseNegative    int     `json:"false_negative"`
	Invalid          int     `json:"invalid"`
	Abstained        int     `json:"abstained"`
	StrictPassed     int     `json:"strict_passed"`
	SupportedDefects int     `json:"supported_defects"`
	Precision        float64 `json:"precision"`
	PrecisionDefined bool    `json:"precision_defined"`
	Recall           float64 `json:"recall"`
	RecallDefined    bool    `json:"recall_defined"`
	StrictAccuracy   float64 `json:"strict_accuracy"`
	SupportedRecall  float64 `json:"supported_recall"`
}

type SemanticBreakdown struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
	SemanticCounts
}

type NovelSemanticArm struct {
	Arm            string              `json:"arm"`
	Pending        int                 `json:"pending"`
	DuplicateSteps int                 `json:"duplicate_steps"`
	UnknownSteps   int                 `json:"unknown_steps"`
	Unsettled      int                 `json:"unsettled"`
	Passed         bool                `json:"passed"`
	Breakdown      []SemanticBreakdown `json:"breakdown"`
	SemanticCounts
}

type NovelSemanticSummary struct {
	SuiteHash  string             `json:"suite_hash"`
	Threshold  float64            `json:"threshold"`
	Annotation string             `json:"annotation"`
	Boundary   string             `json:"boundary"`
	Arms       []NovelSemanticArm `json:"arms"`
}

func ratio(numerator, denominator int) float64 {
	if denominator == 0 {
		return 0
	}
	return float64(numerator) / float64(denominator)
}

func (counts *SemanticCounts) rates() {
	counts.PrecisionDefined = counts.TruePositive+counts.FalsePositive > 0
	counts.RecallDefined = counts.Defects > 0
	counts.Precision = ratio(counts.TruePositive, counts.TruePositive+counts.FalsePositive)
	counts.Recall = ratio(counts.TruePositive, counts.Defects)
	counts.StrictAccuracy = ratio(counts.StrictPassed, counts.Cases)
	counts.SupportedRecall = ratio(counts.SupportedDefects, counts.Defects)
}

func addSemanticObservation(counts *SemanticCounts, defect, observed, valid, predicted, abstained, strict bool) {
	counts.Cases++
	if defect {
		counts.Defects++
	} else {
		counts.Allowed++
	}
	if !observed {
		return
	}
	counts.Observed++
	if !valid {
		counts.Invalid++
		// Invalid outputs remain in the denominator. Their unknown decision
		// counts against the true class, rather than vanishing from the report.
		if defect {
			counts.FalseNegative++
		} else {
			counts.FalsePositive++
		}
		return
	}
	if abstained {
		counts.Abstained++
	}
	switch {
	case defect && predicted:
		counts.TruePositive++
	case defect:
		counts.FalseNegative++
	case predicted:
		counts.FalsePositive++
	default:
		counts.TrueNegative++
	}
	if strict {
		counts.StrictPassed++
		if defect {
			counts.SupportedDefects++
		}
	}
}

// NovelSemanticMetrics recalculates from trusted cases and actual child output.
// It never accepts a model's self-reported score or caller-supplied aggregates.
func (s Suite) NovelSemanticMetrics(steps []QualificationStep) *NovelSemanticSummary {
	if s.entry.ID != NovelSemanticSuiteID {
		return nil
	}
	result := &NovelSemanticSummary{
		SuiteHash: s.entry.Hash, Threshold: 0.9,
		Annotation: "repository-authored; independent-human-review:NOT_RUN",
		Boundary:   "200份固定工程场景；精确率/召回率、严格结论与证据分别统计。不代表文学质量、全书远端漏检率或真人审读。未完成样例不从分母删去。",
		Arms:       []NovelSemanticArm{},
	}
	for _, armName := range []string{"no_method", "baseline", "candidate"} {
		arm := NovelSemanticArm{Arm: armName, Breakdown: []SemanticBreakdown{}}
		byCase := map[string]QualificationStep{}
		for _, step := range steps {
			if step.Arm != armName {
				continue
			}
			if _, exists := s.gold[step.CaseID]; !exists || step.Repeat < 0 || step.Repeat >= s.entry.Repeats {
				arm.UnknownSteps++
				continue
			}
			key := step.CaseID + "/" + strconv.Itoa(step.Repeat)
			if _, exists := byCase[key]; exists {
				arm.DuplicateSteps++
				continue
			}
			byCase[key] = step
		}
		groups := map[string]*SemanticBreakdown{}
		for _, c := range s.entry.Cases {
			input, gold, _ := s.novelSemanticCase(c.ID)
			for repeat := 0; repeat < s.entry.Repeats; repeat++ {
				step, exists := byCase[c.ID+"/"+strconv.Itoa(repeat)]
				observed := exists && slices.Contains([]string{"SUCCEEDED", "FAILED_CHECK", "INVALID_OUTPUT", "REJECTED", "FAILED"}, step.State)
				var report domain.SemanticReviewReport
				var err error
				if observed {
					report, err = domain.ParseSemanticReviewReport([]byte(step.Metrics.Output), input)
					if !step.Metrics.UsageKnown || !step.Metrics.CostKnown || !step.Metrics.DurationKnown {
						arm.Unsettled++
					}
				}
				// OutputHash identifies the complete models.Result envelope, not
				// its Text member. The source registry verifies that blob and its
				// exact Text/usage linkage before reports can acquire authority.
				_, hashErr := hex.DecodeString(step.Metrics.OutputHash)
				valid := observed && slices.Contains([]string{"SUCCEEDED", "FAILED_CHECK"}, step.State) && err == nil && len(step.Metrics.OutputHash) == 64 && hashErr == nil
				strict := valid && s.Score(c.ID, []byte(step.Metrics.Output)).Passed
				predicted := report.Verdict == "FAIL"
				for _, issue := range report.Issues {
					predicted = predicted || issue.Category != "unsupported"
				}
				abstained := report.Verdict == "UNKNOWN"
				defect := gold.Summary == "FAIL"
				addSemanticObservation(&arm.SemanticCounts, defect, observed, valid, predicted, abstained, strict)
				for _, label := range []struct{ kind, name string }{{"topic", gold.Novel.Topic}, {"issue_category", gold.Plan}} {
					key := label.kind + "/" + label.name
					if groups[key] == nil {
						groups[key] = &SemanticBreakdown{Kind: label.kind, Name: label.name}
					}
					addSemanticObservation(&groups[key].SemanticCounts, defect, observed, valid, predicted, abstained, strict)
				}
			}
		}
		arm.rates()
		arm.Pending = arm.Cases - arm.Observed
		arm.Passed = arm.Pending == 0 && arm.DuplicateSteps == 0 && arm.UnknownSteps == 0 && arm.Unsettled == 0 && arm.Invalid == 0 && arm.Precision >= result.Threshold && arm.Recall >= result.Threshold && arm.StrictAccuracy >= result.Threshold && arm.SupportedRecall >= result.Threshold
		keys := make([]string, 0, len(groups))
		for key := range groups {
			keys = append(keys, key)
		}
		slices.Sort(keys)
		for _, key := range keys {
			groups[key].rates()
			arm.Breakdown = append(arm.Breakdown, *groups[key])
		}
		result.Arms = append(result.Arms, arm)
	}
	return result
}

func semanticTextHash(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}
