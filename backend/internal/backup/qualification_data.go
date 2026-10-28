package backup

import (
	"errors"
	"novelstudio/internal/retention"
	"strconv"
)

// Weekly amounts must be derived from the same durable call history that will
// be restored. A well-formed ZIP cannot replace an unknown hold with free funds.
func validateQualificationBudget(d Data) error {
	return validateQualificationBudgetWithEvidence(d, qualificationArchiveEvidence{})
}

func validateQualificationBudgetWithEvidence(d Data, evidence qualificationArchiveEvidence) error {
	specs := map[string]tableSpec{}
	for _, spec := range tableCatalog {
		specs[spec.name] = spec
	}
	value := func(table string, row Row, column string) string {
		if len(row) == 0 {
			return ""
		}
		return specs[table].value(row, column)
	}
	var indexErr error
	index := func(table, key string) map[string]Row {
		result := map[string]Row{}
		for _, row := range d[table] {
			id := value(table, row, key)
			if id == "" || result[id] != nil {
				indexErr = errors.New("duplicate or missing qualification accounting identity")
			}
			result[id] = row
		}
		return result
	}
	grants := index("qualification_week_grants", "id")
	runs := index("qualification_runs", "id")
	steps := index("qualification_steps", "id")
	calls := index("model_calls", "id")
	schedules := index("qualification_schedules", "id")
	accounting := index("model_call_accounting", "call_id")
	links := index("qualification_calls", "call_id")
	if indexErr != nil {
		return indexErr
	}
	for table, columns := range map[string][]string{
		"model_calls":               {"reserved_micros", "charged_micros"},
		"qualification_calls":       {"reserved_micros", "held_micros", "charged_micros"},
		"qualification_week_grants": {"cap_micros", "reserved_micros", "spent_micros"},
	} {
		for _, row := range d[table] {
			for _, column := range columns {
				n, err := strconv.ParseInt(value(table, row, column), 10, 64)
				if err != nil || n < 0 {
					return errors.New("invalid qualification accounting amount")
				}
			}
		}
	}
	graph := qualificationBudgetGraph{specs: specs, grants: grants, schedules: schedules, generations: map[string][]Row{}, evidence: evidence}
	graph.operationAliases = map[string]retention.RequestAliasRecord{}
	for _, alias := range evidence.aliases {
		if alias.Table != "generation_tasks" {
			continue
		}
		if _, exists := graph.operationAliases[alias.Identity.SourceID]; exists {
			return errors.New("duplicate qualification generation operation alias")
		}
		graph.operationAliases[alias.Identity.SourceID] = alias
	}
	for _, row := range d["generation_tasks"] {
		call := value("generation_tasks", row, "call_id")
		if call != "" {
			graph.generations[call] = append(graph.generations[call], row)
		}
	}
	if err := graph.validateReverse(d, links, runs, steps); err != nil {
		return err
	}
	if err := graph.validateExpiredLegacyBatches(links, runs, steps, calls, accounting); err != nil {
		return err
	}
	held, spent := map[string]int64{}, map[string]int64{}
	amount := func(table string, row Row, column string) int64 {
		n, _ := strconv.ParseInt(value(table, row, column), 10, 64)
		return n
	}
	for _, row := range d["qualification_calls"] {
		project, runID, stepID, callID, grantID := value("qualification_calls", row, "project_id"), value("qualification_calls", row, "run_id"), value("qualification_calls", row, "step_id"), value("qualification_calls", row, "call_id"), value("qualification_calls", row, "grant_id")
		run, rok := runs[runID]
		step, sok := steps[stepID]
		call, cok := calls[callID]
		if !rok || !sok || !cok || value("qualification_runs", run, "project_id") != project || value("qualification_steps", step, "run_id") != runID || value("qualification_steps", step, "project_id") != project || value("model_calls", call, "project_id") != project {
			return errors.New("qualification accounting scope mismatch")
		}
		reserved, hold, charge := amount("qualification_calls", row, "reserved_micros"), amount("qualification_calls", row, "held_micros"), amount("qualification_calls", row, "charged_micros")
		if reserved != amount("model_calls", call, "reserved_micros") || charge != amount("model_calls", call, "charged_micros") || hold > reserved {
			return errors.New("qualification call amount mismatch")
		}
		callState := value("model_calls", call, "state")
		unresolved := callState == "RESERVED" || callState == "DISPATCHED" || callState == "UNKNOWN"
		if (unresolved && hold != reserved) || (!unresolved && hold != 0) || value("qualification_calls", row, "state") != callState {
			return errors.New("qualification call outstanding hold/state mismatch")
		}
		if err := graph.validateBinding(row, run, step, call, accounting[callID]); err != nil {
			return err
		}
		if grantID != "" {
			grant, ok := grants[grantID]
			if !ok || value("qualification_week_grants", grant, "project_id") != project {
				return errors.New("qualification weekly grant scope mismatch")
			}
			if value("model_call_accounting", accounting[callID], "currency") != value("qualification_week_grants", grant, "currency") {
				return errors.New("qualification weekly currency mismatch")
			}
			var err error
			held[grantID], err = addQualificationAmount(held[grantID], hold)
			if err != nil {
				return err
			}
			spent[grantID], err = addQualificationAmount(spent[grantID], charge)
			if err != nil {
				return err
			}
		}
	}
	for id, row := range grants {
		schedule, ok := schedules[value("qualification_week_grants", row, "schedule_id")]
		if !ok || value("qualification_schedules", schedule, "project_id") != value("qualification_week_grants", row, "project_id") || held[id] != amount("qualification_week_grants", row, "reserved_micros") || spent[id] != amount("qualification_week_grants", row, "spent_micros") {
			return errors.New("qualification weekly history balance mismatch")
		}
	}
	return nil
}
