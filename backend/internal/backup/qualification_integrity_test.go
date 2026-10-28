package backup

import (
	"encoding/json"
	"strings"
	"testing"
)

func archiveQualificationCell(a *Archive, table string, index int, column, value string) {
	for _, spec := range tableCatalog {
		if spec.name == table {
			spec.set(a.Data[table][index], column, value)
			return
		}
	}
}
func weeklyBudgetArchive(t *testing.T) *Archive {
	t.Helper()
	a := emptyArchive(t)
	project, run, step, call, grant, schedule, actor, generation := strings.Repeat("a", 32), strings.Repeat("b", 32), strings.Repeat("c", 32), strings.Repeat("d", 32), strings.Repeat("e", 32), strings.Repeat("f", 32), strings.Repeat("1", 32), strings.Repeat("2", 32)
	frozen, _ := json.Marshal(map[string]any{"weekly_grant": map[string]any{"id": grant, "schedule_id": schedule, "revision": 1, "week": "2026-W43", "cap_micros": 100}, "price": map[string]any{"currency": "USD"}})
	inputHash := strings.Repeat("3", 64)
	generationFrozen, _ := json.Marshal(map[string]any{"capability": map[string]any{"mode": "EVALUATION", "evaluation_id": run, "evaluation_step_id": step}, "input_hash": inputHash, "estimate_micros": 50, "price": map[string]any{"currency": "USD"}})
	testRow(a, "projects", map[string]string{"id": project})
	testRow(a, "qualification_runs", map[string]string{"id": run, "project_id": project, "actor_id": actor, "frozen": string(frozen)})
	testRow(a, "qualification_steps", map[string]string{"id": step, "run_id": run, "project_id": project, "child_id": generation, "child_kind": "generation"})
	testRow(a, "qualification_schedules", map[string]string{"id": schedule, "project_id": project, "enabled": "0", "revision": "1", "config": `{"budget_limit_micros":100}`})
	testRow(a, "model_calls", map[string]string{"id": call, "project_id": project, "actor_id": actor, "state": "UNKNOWN", "reserved_micros": "50", "charged_micros": "0"})
	testRow(a, "generation_tasks", map[string]string{"id": generation, "project_id": project, "actor_id": actor, "call_id": call, "operation_id": "evaluation-" + step, "input_hash": inputHash, "frozen": string(generationFrozen)})
	testRow(a, "model_call_accounting", map[string]string{"call_id": call, "currency": "USD"})
	testRow(a, "qualification_calls", map[string]string{"call_id": call, "project_id": project, "run_id": run, "step_id": step, "grant_id": grant, "reserved_micros": "50", "held_micros": "50", "charged_micros": "0", "state": "UNKNOWN", "origin": "NATIVE_30"})
	testRow(a, "qualification_week_grants", map[string]string{"id": grant, "project_id": project, "schedule_id": schedule, "week_key": "2026-W43", "schedule_revision": "1", "currency": "USD", "cap_micros": "100", "reserved_micros": "50", "spent_micros": "0", "enabled": "0"})
	return a
}

func TestArchiveWeeklyGrantCannotBeRemovedOrShiftedToAnotherWeek(t *testing.T) {
	for _, action := range []string{"empty", "last-week"} {
		t.Run(action, func(t *testing.T) {
			a := weeklyBudgetArchive(t)
			archiveQualificationCell(a, "qualification_week_grants", 0, "reserved_micros", "0")
			grant := ""
			if action == "last-week" {
				grant = strings.Repeat("9", 32)
				testRow(a, "qualification_week_grants", map[string]string{"id": grant, "project_id": strings.Repeat("a", 32), "schedule_id": strings.Repeat("f", 32), "week_key": "2026-W42", "schedule_revision": "1", "currency": "USD", "cap_micros": "100", "reserved_micros": "50", "spent_micros": "0", "enabled": "0"})
			}
			archiveQualificationCell(a, "qualification_calls", 0, "grant_id", grant)
			if err := validateData(a.Data); err == nil {
				t.Fatal("frozen weekly call escaped current week", action)
			}
		})
	}
}

func TestArchiveRejectsDeletedEvaluationCallMappingEvenWhenTotalsAreReduced(t *testing.T) {
	a := weeklyBudgetArchive(t)
	a.Data["qualification_calls"] = nil
	archiveQualificationCell(a, "qualification_week_grants", 0, "reserved_micros", "0")
	if err := validateData(a.Data); err == nil {
		t.Fatal("issued evaluation call disappeared from weekly accounting by deleting its mapping")
	}
}

func TestArchiveQualificationChildMustMatchExactGenerationCall(t *testing.T) {
	for _, action := range []string{"wrong-child", "wrong-call", "wrong-evaluation"} {
		t.Run(action, func(t *testing.T) {
			a := weeklyBudgetArchive(t)
			switch action {
			case "wrong-child":
				archiveQualificationCell(a, "qualification_steps", 0, "child_id", strings.Repeat("8", 32))
			case "wrong-call":
				archiveQualificationCell(a, "generation_tasks", 0, "call_id", strings.Repeat("8", 32))
			case "wrong-evaluation":
				var frozen map[string]any
				_ = json.Unmarshal(a.Data["generation_tasks"][0][tableIndex("generation_tasks", "frozen")], &frozen)
				frozen["capability"].(map[string]any)["evaluation_id"] = strings.Repeat("8", 32)
				raw, _ := json.Marshal(frozen)
				archiveQualificationCell(a, "generation_tasks", 0, "frozen", string(raw))
			}
			if err := validateData(a.Data); err == nil {
				t.Fatal("unrelated child/call accepted", action)
			}
		})
	}
}

func tableIndex(table, column string) int {
	for _, spec := range tableCatalog {
		if spec.name == table {
			return spec.index(column)
		}
	}
	return -1
}

func TestArchiveWeeklyTotalsRejectIntegerOverflow(t *testing.T) {
	for _, column := range []string{"held_micros", "charged_micros"} {
		t.Run(column, func(t *testing.T) {
			a := weeklyBudgetArchive(t)
			// Three individually valid signed amounts overflow to the plausible total2.
			for n, value := range []string{"9223372036854775807", "9223372036854775807", "4"} {
				if n > 0 {
					for _, table := range []string{"qualification_calls", "model_calls", "model_call_accounting", "generation_tasks", "qualification_steps"} {
						row := make(Row, len(a.Data[table][0]))
						for i, cell := range a.Data[table][0] {
							row[i] = append([]byte(nil), cell...)
						}
						a.Data[table] = append(a.Data[table], row)
					}
					id := strings.Repeat(string(rune('5'+n)), 32)
					step := strings.Repeat(string(rune('3'+n)), 32)
					gen := strings.Repeat(string(rune('6'+n)), 32)
					archiveQualificationCell(a, "qualification_calls", n, "call_id", id)
					archiveQualificationCell(a, "qualification_calls", n, "step_id", step)
					archiveQualificationCell(a, "model_calls", n, "id", id)
					archiveQualificationCell(a, "model_call_accounting", n, "call_id", id)
					archiveQualificationCell(a, "generation_tasks", n, "id", gen)
					archiveQualificationCell(a, "generation_tasks", n, "call_id", id)
					archiveQualificationCell(a, "generation_tasks", n, "operation_id", "evaluation-"+step)
					archiveQualificationCell(a, "qualification_steps", n, "id", step)
					archiveQualificationCell(a, "qualification_steps", n, "child_id", gen)
					var frozen map[string]any
					_ = json.Unmarshal(a.Data["generation_tasks"][n][tableIndex("generation_tasks", "frozen")], &frozen)
					frozen["capability"].(map[string]any)["evaluation_step_id"] = step
					raw, _ := json.Marshal(frozen)
					archiveQualificationCell(a, "generation_tasks", n, "frozen", string(raw))
				}
				if column == "held_micros" {
					archiveQualificationCell(a, "qualification_calls", n, "reserved_micros", value)
					archiveQualificationCell(a, "qualification_calls", n, "held_micros", value)
					archiveQualificationCell(a, "model_calls", n, "reserved_micros", value)
					var frozen map[string]any
					_ = json.Unmarshal(a.Data["generation_tasks"][n][tableIndex("generation_tasks", "frozen")], &frozen)
					frozen["estimate_micros"] = json.RawMessage(value)
					raw, _ := json.Marshal(frozen)
					archiveQualificationCell(a, "generation_tasks", n, "frozen", string(raw))
				} else {
					archiveQualificationCell(a, "qualification_calls", n, "held_micros", "0")
					archiveQualificationCell(a, "qualification_calls", n, "charged_micros", value)
					archiveQualificationCell(a, "qualification_calls", n, "state", "SUCCEEDED")
					archiveQualificationCell(a, "model_calls", n, "charged_micros", value)
					archiveQualificationCell(a, "model_calls", n, "state", "SUCCEEDED")
				}
			}
			if column == "held_micros" {
				archiveQualificationCell(a, "qualification_week_grants", 0, "reserved_micros", "2")
			} else {
				archiveQualificationCell(a, "qualification_week_grants", 0, "reserved_micros", "0")
				archiveQualificationCell(a, "qualification_week_grants", 0, "spent_micros", "2")
			}
			if err := validateData(a.Data); err == nil {
				t.Fatal("weekly ledger wrapped integer total", column)
			}
		})
	}
}
