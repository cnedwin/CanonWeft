package domain

import (
	"encoding/json"
	"slices"
)

const SemanticReviewScope = "固定候选、完整当前设定与所列资料；不包含未提供的远端依赖或全书文学质量"

// CompleteSemanticReviewSnapshot derives coverage and evidence anchors from the
// fixed snapshot. It neither obtains external evidence nor grants review authority.
func CompleteSemanticReviewSnapshot(doc SemanticReviewDocument) (SemanticReviewDocument, error) {
	var err error
	if err = ValidateSnapshot(doc.Snapshot); err != nil {
		doc.HardFindings = append(doc.HardFindings, err.Error())
	}
	if err = ValidateRuleEvidence(doc.Snapshot); err != nil {
		doc.HardFindings = append(doc.HardFindings, err.Error())
	}
	// Every typed object is covered, including author-only truths and disabled
	// records; the reviewer must reason about labels rather than losing secrets.
	raw, _ := json.Marshal(doc.Snapshot)
	var collections map[string][]json.RawMessage
	if err = json.Unmarshal(raw, &collections); err != nil {
		return doc, err
	}
	kinds := map[string]string{"entities": "entity", "assertions": "assertion", "rules": "rule", "events": "event", "mappings": "mapping"}
	for collection, items := range collections {
		for _, item := range items {
			var identity struct {
				ID string `json:"id"`
			}
			if json.Unmarshal(item, &identity) != nil || identity.ID == "" {
				return doc, ErrNotReady
			}
			key := kinds[collection] + ":" + identity.ID
			doc.RequiredCoverage = append(doc.RequiredCoverage, key)
			doc.Anchors = append(doc.Anchors, SemanticReviewAnchor{Key: key, TargetKey: key, Text: string(item)})
		}
	}
	if len(doc.RequiredCoverage) == 0 {
		doc.RequiredCoverage = []string{"snapshot"}
		doc.Anchors = append(doc.Anchors, SemanticReviewAnchor{Key: "snapshot", TargetKey: "snapshot", Text: string(raw)})
	}
	slices.Sort(doc.RequiredCoverage)
	slices.SortFunc(doc.Anchors, func(a, b SemanticReviewAnchor) int {
		if a.Key < b.Key {
			return -1
		}
		if a.Key > b.Key {
			return 1
		}
		return 0
	})
	return doc, nil
}
