package backup

import (
	"encoding/json"
	"errors"
	"novelstudio/internal/retention"
	"slices"
)

var semanticReviewTables = []string{"ip_branch_deltas", "semantic_review_runs", "semantic_review_acceptances"}

func upgradeSemanticReviews(a *Archive) error {
	if a.Manifest.Schema >= 31 {
		return nil
	}
	for _, name := range semanticReviewTables {
		if _, exists := a.Data[name]; exists {
			return errors.New("unexpected semantic review table in legacy archive")
		}
		a.Data[name] = nil
	}
	return nil
}

func validateSemanticReviewBackup(d Data, deltaEvidence ...map[string]retention.ExpiredProjection) error {
	var deltas map[string]retention.ExpiredProjection
	if len(deltaEvidence) > 0 {
		deltas = deltaEvidence[0]
	}
	runs := map[string]map[string]string{}
	for _, s := range tableCatalog {
		for _, r := range d[s.name] {
			switch s.name {
			case "ip_branch_deltas":
				payload := []byte(s.value(r, "payload"))
				key, err := retention.RowOwnerKey(s.name, map[string]string{"candidate_id": s.value(r, "candidate_id")})
				if err != nil {
					return err
				}
				if proof, ok := deltas[key]; ok {
					actual, err := retention.ParseSourceProjection(payload, "ip_branch_deltas.payload")
					if err != nil || actual.OriginalPayloadHash != s.value(r, "delta_hash") || !recipeJSONEqual(marshalRecipeProof(actual), marshalRecipeProof(proof)) {
						return errors.New("invalid expired branch delta evidence")
					}
				} else if !json.Valid(payload) || digest(payload) != s.value(r, "delta_hash") {
					return errors.New("invalid branch delta payload")
				}
			case "semantic_review_runs":
				if s.value(r, "restored_disabled") != "1" || s.value(r, "cancel_requested") != "1" || s.value(r, "paused") != "0" || !slices.Contains([]string{"COMPLETED", "FAILED", "CANCELLED"}, s.value(r, "state")) {
					return errors.New("archive semantic review authority enabled")
				}
				id := s.value(r, "id")
				if _, exists := runs[id]; exists {
					return errors.New("duplicate semantic review identity")
				}
				row := map[string]string{}
				for _, c := range []string{"project_id", "target_kind", "target_id", "target_hash", "report_hash"} {
					row[c] = s.value(r, c)
				}
				runs[id] = row
			}
		}
	}
	for _, s := range tableCatalog {
		if s.name == "semantic_review_acceptances" {
			for _, r := range d[s.name] {
				run, exists := runs[s.value(r, "review_id")]
				if !exists || s.value(r, "report_hash") == "" {
					return errors.New("semantic acceptance has no report")
				}
				for _, c := range []string{"project_id", "target_kind", "target_id", "target_hash", "report_hash"} {
					if s.value(r, c) != run[c] {
						return errors.New("semantic acceptance target or report mismatch")
					}
				}
			}
		}
	}
	return nil
}
