package qualification

import (
	"encoding/json"
	"testing"

	"novelstudio/internal/domain"
)

func TestNovelSemanticCorpusHasDistinctBalancedCases(t *testing.T) {
	suite, err := Lookup("novel-semantic-acceptance-v1")
	if err != nil {
		t.Fatal(err)
	}
	entry := suite.Public()
	if len(entry.Cases) != 200 || entry.Repeats != 1 || entry.Professional || entry.Scope != "bounded.novel-semantic" {
		t.Fatal("the full bounded novel corpus must have 200 different cases, not repeated small cases")
	}
	seen := map[string]bool{}
	passed, failed := 0, 0
	for _, c := range entry.Cases {
		var input domain.SemanticReviewDocument
		if err := json.Unmarshal(c.Input, &input); err != nil {
			t.Fatal(err)
		}
		fingerprint := Hash(input.Anchors)
		if seen[fingerprint] {
			t.Fatal("duplicate scenario", c.ID)
		}
		seen[fingerprint] = true
		result, _ := json.Marshal(domain.SemanticReviewReport{Schema: 1, TargetHash: input.Target.Hash, Verdict: "PASS", Coverage: input.RequiredCoverage, Issues: []domain.SemanticReviewIssue{}, Summary: "未发现违反给定设定的内容"})
		if suite.Score(c.ID, result).Passed {
			passed++
		} else {
			failed++
		}
	}
	if passed != 100 || failed != 100 {
		t.Fatalf("constant PASS must reveal all 100 defects: passed=%d failed=%d", passed, failed)
	}
}

func TestNovelSuitePreservesExistingQualificationHashes(t *testing.T) {
	for id, expected := range map[string]string{
		"shared-contract-v1":        "9990b14427fd1646b83932bee119a0abe3098d562163cd5dac828b59d967887e",
		"source-ledger-v1":          "4d455a8c65a9306dacd3180120f0b0543cc8dc19dd6cc28b3ee0bdab7ec65804",
		"shared-semantic-review-v1": "d5e7b7e4c1bd95291aaa8f5555c12333fa97120ed882c2c0d0e908d5372d9706",
		"shared-change-impact-v1":   "bae4edd73057c74f9879e8b8ec49fbaa14da9ce4243a666618efac38ce616ae5",
	} {
		suite, err := Lookup(id)
		if err != nil {
			t.Fatal(err)
		}
		if suite.Public().Hash != expected {
			t.Fatal("existing qualification hash changed", id)
		}
	}
}
