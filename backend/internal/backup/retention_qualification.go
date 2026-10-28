package backup

import (
	"encoding/json"
	"errors"
	"novelstudio/internal/qualificationledger"
	"novelstudio/internal/retention"
)

// Supplied only after independent archive source, journal and alias binding.
type qualificationArchiveEvidence struct {
	transients              map[string]map[string]retention.ExpiredProjection
	recipeRuns, recipeSteps map[string]retention.ExpiredProjection
	historicalOutbox        bool
	contexts                map[string]retention.ExpiredProjection
	runs, generations       map[string]retention.ExpiredProjection
	aliases                 []retention.RequestAliasRecord
}

type qualificationRunFrozen struct {
	Weekly *qualificationArchiveGrant `json:"weekly_grant"`
	Price  qualificationArchivePrice  `json:"price"`
}

type qualificationCapability struct {
	Mode string `json:"mode"`
	Run  string `json:"evaluation_id"`
	Step string `json:"evaluation_step_id"`
}

type qualificationGenerationFrozen struct {
	Capability *qualificationCapability  `json:"capability"`
	InputHash  string                    `json:"input_hash"`
	Estimate   int64                     `json:"estimate_micros"`
	Price      qualificationArchivePrice `json:"price"`
}

func (g qualificationBudgetGraph) bindFrozenProof(table string, row Row, proof retention.ExpiredProjection) error {
	key, err := retention.RowOwnerKey(table, map[string]string{"id": g.value(table, row, "id")})
	if err != nil {
		return err
	}
	ref, err := retention.ReferenceID(g.value(table, row, "project_id"), table, key, "frozen")
	raw, marshalErr := json.Marshal(proof)
	if err != nil || marshalErr != nil || proof.ReferenceID != ref || retention.ValidateProjectionForRule(proof, table+".frozen") != nil || !recipeJSONEqual(raw, []byte(g.value(table, row, "frozen"))) {
		return errExpiredArchiveEvidence
	}
	return nil
}

func decodeQualificationFrozen(raw string, target any) error {
	var header struct {
		State string `json:"state"`
	}
	if json.Unmarshal([]byte(raw), &header) != nil || header.State == "EXPIRED" || json.Unmarshal([]byte(raw), target) != nil {
		return errors.New("invalid or unproven expired qualification frozen input")
	}
	return nil
}

func (g qualificationBudgetGraph) runFrozen(row Row) (qualificationRunFrozen, error) {
	var result qualificationRunFrozen
	proof, ok := g.evidence.runs[g.value("qualification_runs", row, "id")]
	if !ok {
		return result, decodeQualificationFrozen(g.value("qualification_runs", row, "frozen"), &result)
	}
	if err := g.bindFrozenProof("qualification_runs", row, proof); err != nil {
		return result, err
	}
	if proof.Summary.Price == nil {
		return result, errExpiredArchiveEvidence
	}
	result.Price.Currency = proof.Summary.Price.Currency
	if ref := proof.Summary.WeeklyGrant; ref != nil {
		result.Weekly = &qualificationArchiveGrant{ID: ref.ID, ScheduleID: ref.ScheduleID, Revision: ref.Revision, Week: ref.Week, Cap: ref.CapMicros}
	}
	return result, nil
}

func (g qualificationBudgetGraph) generationFrozen(row Row) (qualificationGenerationFrozen, error) {
	var result qualificationGenerationFrozen
	proof, ok := g.evidence.generations[g.value("generation_tasks", row, "id")]
	if !ok {
		return result, decodeQualificationFrozen(g.value("generation_tasks", row, "frozen"), &result)
	}
	if err := g.bindFrozenProof("generation_tasks", row, proof); err != nil {
		return result, err
	}
	if proof.Summary.CallID != g.value("generation_tasks", row, "call_id") {
		return result, errExpiredArchiveEvidence
	}
	if cap := proof.Summary.Capability; cap != nil {
		result.Capability = &qualificationCapability{Mode: cap.Mode, Run: cap.EvaluationID, Step: cap.EvaluationStepID}
	}
	if price := proof.Summary.Price; price != nil {
		result.Price.Currency = price.Currency
	}
	result.InputHash, result.Estimate = proof.Summary.InputHash, proof.Summary.EstimateMicros
	return result, nil
}

func (g qualificationBudgetGraph) generationOperationMatches(row Row, step string) bool {
	want := "evaluation-" + step
	actual := g.value("generation_tasks", row, "operation_id")
	if actual == want {
		return true
	}
	alias, ok := g.operationAliases[g.value("generation_tasks", row, "id")]
	return ok && alias.Identity.ActorID == g.value("generation_tasks", row, "actor_id") && alias.AnonymousKey == actual && alias.OriginalKeyHash == digest([]byte(want))
}

// Legacy conversion binds the complete run call set. Never validate only one
// selected member, or release a hold because its original input was erased.
func (g *qualificationBudgetGraph) validateExpiredLegacyBatches(links, runs, steps, calls, accounting map[string]Row) error {
	g.expiredLegacy = map[string]bool{}
	batches := map[string][]qualificationledger.ExpiredLegacyCall{}
	for callID, link := range links {
		runID := g.value("qualification_calls", link, "run_id")
		stepID := g.value("qualification_calls", link, "step_id")
		generations := g.generations[callID]
		if len(generations) != 1 {
			return errors.New("qualification call does not identify one generation")
		}
		generation := generations[0]
		runProof, runExpired := g.evidence.runs[runID]
		generationProof, generationExpired := g.evidence.generations[g.value("generation_tasks", generation, "id")]
		origin := g.value("qualification_calls", link, "origin")
		legacy := origin == "LEGACY_28_DB" || origin == "LEGACY_28_ARCHIVE"
		if !legacy || (!runExpired && !generationExpired) {
			continue
		}
		if !runExpired || !generationExpired || runProof.Summary.LegacyEvidenceHash == "" || generationProof.Summary.LegacyEvidenceHash == "" || !g.generationOperationMatches(generation, stepID) {
			return errors.New("incomplete expired legacy qualification run")
		}
		record := g.record(link, runs[runID], steps[stepID], calls[callID], generation, accounting[callID])
		record.Operation = "evaluation-" + stepID
		batches[runID] = append(batches[runID], qualificationledger.ExpiredLegacyCall{Record: record, Origin: origin, Proof: []byte(g.value("qualification_calls", link, "legacy_source")), GenerationProjection: generationProof})
	}
	for runID, proof := range g.evidence.runs {
		if proof.Summary.LegacyEvidenceHash != "" && len(batches[runID]) == 0 {
			return errors.New("expired legacy run missing its complete accounting history")
		}
	}
	for runID, batch := range batches {
		mappings, err := qualificationledger.ValidateExpiredLegacyRun(batch, g.evidence.runs[runID])
		if err != nil {
			return err
		}
		for i, mapping := range mappings {
			id := batch[i].Record.CallID
			link := links[id]
			if mapping.State != g.value("qualification_calls", link, "state") || mapping.Reserved != parseQualificationAmount(g.value("qualification_calls", link, "reserved_micros")) || mapping.Held != parseQualificationAmount(g.value("qualification_calls", link, "held_micros")) || mapping.Charged != parseQualificationAmount(g.value("qualification_calls", link, "charged_micros")) {
				return errors.New("expired legacy qualification amount mismatch")
			}
			g.expiredLegacy[id] = true
		}
	}
	return nil
}
