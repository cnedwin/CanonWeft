package backup

import (
	"encoding/json"
	"errors"
	"math"
	"novelstudio/internal/qualificationledger"
	"novelstudio/internal/retention"
	"regexp"
	"strconv"
)

type qualificationBudgetGraph struct {
	specs             map[string]tableSpec
	grants, schedules map[string]Row
	generations       map[string][]Row
	evidence          qualificationArchiveEvidence
	expiredLegacy     map[string]bool
	operationAliases  map[string]retention.RequestAliasRecord
}

func (g qualificationBudgetGraph) record(link, run, step, call, generation, accounting Row) qualificationledger.Record {
	v := g.value
	return qualificationledger.Record{
		Project: v("qualification_calls", link, "project_id"), Actor: v("qualification_runs", run, "actor_id"), RunID: v("qualification_runs", run, "id"), StepID: v("qualification_steps", step, "id"), GenerationID: v("generation_tasks", generation, "id"), CallID: v("model_calls", call, "id"),
		RunProject: v("qualification_runs", run, "project_id"), StepProject: v("qualification_steps", step, "project_id"), StepRun: v("qualification_steps", step, "run_id"), GenerationProject: v("generation_tasks", generation, "project_id"), GenerationActor: v("generation_tasks", generation, "actor_id"), CallProject: v("model_calls", call, "project_id"), CallActor: v("model_calls", call, "actor_id"),
		ChildID: v("qualification_steps", step, "child_id"), ChildKind: v("qualification_steps", step, "child_kind"), Operation: v("generation_tasks", generation, "operation_id"), InputHash: v("generation_tasks", generation, "input_hash"),
		RunCreated: v("qualification_runs", run, "created_at"), GenerationCreated: v("generation_tasks", generation, "created_at"), CallCreated: v("model_calls", call, "created_at"),
		RunFrozen: []byte(v("qualification_runs", run, "frozen")), GenerationFrozen: []byte(v("generation_tasks", generation, "frozen")), State: v("model_calls", call, "state"), Currency: v("model_call_accounting", accounting, "currency"), Reserved: parseQualificationAmount(v("model_calls", call, "reserved_micros")), Charged: parseQualificationAmount(v("model_calls", call, "charged_micros")),
	}
}
func parseQualificationAmount(raw string) int64 {
	amount, _ := strconv.ParseInt(raw, 10, 64)
	return amount
}
func (g qualificationBudgetGraph) value(table string, row Row, column string) string {
	if len(row) == 0 {
		return ""
	}
	return g.specs[table].value(row, column)
}

type qualificationArchivePrice struct {
	Currency string `json:"currency"`
}
type qualificationArchiveGrant struct {
	ID         string `json:"id"`
	ScheduleID string `json:"schedule_id"`
	Revision   int64  `json:"revision"`
	Week       string `json:"week"`
	Cap        int64  `json:"cap_micros"`
}

func (g qualificationBudgetGraph) validateBinding(link, run, step, call, accounting Row) error {
	project, runID, stepID, callID, grantID := g.value("qualification_calls", link, "project_id"), g.value("qualification_calls", link, "run_id"), g.value("qualification_calls", link, "step_id"), g.value("qualification_calls", link, "call_id"), g.value("qualification_calls", link, "grant_id")
	frozen, err := g.runFrozen(run)
	if err != nil {
		return err
	}
	generations := g.generations[callID]
	if len(generations) != 1 {
		return errors.New("qualification call does not identify one generation")
	}
	generation := generations[0]
	origin := g.value("qualification_calls", link, "origin")
	provenance := g.value("qualification_calls", link, "legacy_source")
	switch origin {
	case "NATIVE_29", "NATIVE_30":
		if (provenance != "" && provenance != "null") || g.evidence.runs[runID].Summary.LegacyEvidenceHash != "" || g.evidence.generations[g.value("generation_tasks", generation, "id")].Summary.LegacyEvidenceHash != "" {
			return errors.New("native qualification has unexpected legacy provenance")
		}
	case "LEGACY_28_DB", "LEGACY_28_ARCHIVE":
		if !g.expiredLegacy[callID] {
			record := g.record(link, run, step, call, generation, accounting)
			if g.generationOperationMatches(generation, stepID) {
				record.Operation = "evaluation-" + stepID
			}
			if err := qualificationledger.ValidateProof(record, origin, []byte(provenance)); err != nil {
				return err
			}
		}
	default:
		return errors.New("unknown qualification accounting origin")
	}
	actor := g.value("qualification_runs", run, "actor_id")
	child := g.value("qualification_steps", step, "child_id")
	childKind := g.value("qualification_steps", step, "child_kind")
	if actor == "" || g.value("model_calls", call, "actor_id") != actor || g.value("generation_tasks", generation, "actor_id") != actor || g.value("generation_tasks", generation, "project_id") != project || !g.generationOperationMatches(generation, stepID) {
		return errors.New("qualification generation owner or internal operation mismatch")
	}
	if (child != "" && (child != g.value("generation_tasks", generation, "id") || childKind != "generation")) || (child == "" && childKind != "") {
		return errors.New("qualification step child-call mismatch")
	}
	generationFrozen, err := g.generationFrozen(generation)
	if err != nil {
		return err
	}
	trial := generationFrozen.Capability
	reserved, err := strconv.ParseInt(g.value("qualification_calls", link, "reserved_micros"), 10, 64)
	if err != nil || trial == nil || trial.Mode != "EVALUATION" || trial.Run != runID || (trial.Step != "" && trial.Step != stepID) || generationFrozen.InputHash != g.value("generation_tasks", generation, "input_hash") || generationFrozen.Estimate != reserved {
		return errors.New("qualification generation evaluation/call scope mismatch")
	}
	currency := g.value("model_call_accounting", accounting, "currency")
	if (currency != "USD" && currency != "CNY") || generationFrozen.Price.Currency != currency || frozen.Price.Currency != currency {
		return errors.New("qualification frozen accounting currency mismatch")
	}
	if frozen.Weekly == nil {
		if grantID != "" {
			return errors.New("manual qualification call has unexpected weekly grant")
		}
		return nil
	}
	ref := frozen.Weekly
	grant, ok := g.grants[grantID]
	if grantID == "" || !ok || ref.ID != grantID || ref.ScheduleID != g.value("qualification_week_grants", grant, "schedule_id") || ref.Week != g.value("qualification_week_grants", grant, "week_key") || g.value("qualification_week_grants", grant, "currency") != currency || ref.Revision < 1 || ref.Cap < 0 || ref.Cap > 1000000000000 || !regexp.MustCompile(`^\d{4}-W(0[1-9]|[1-4]\d|5[0-3])$`).MatchString(ref.Week) {
		return errors.New("qualification frozen weekly ownership mismatch")
	}
	revision, err := strconv.ParseInt(g.value("qualification_week_grants", grant, "schedule_revision"), 10, 64)
	if err != nil {
		return err
	}
	cap, err := strconv.ParseInt(g.value("qualification_week_grants", grant, "cap_micros"), 10, 64)
	if err != nil {
		return err
	}
	schedule := g.schedules[ref.ScheduleID]
	scheduleRevision, err := strconv.ParseInt(g.value("qualification_schedules", schedule, "revision"), 10, 64)
	if err != nil {
		return err
	}
	// Same-week edits retain one shared financial row. Its latest cap/revision
	// may differ from the immutable authorization captured by an older run.
	if revision < ref.Revision || scheduleRevision < revision || (revision == ref.Revision && cap != ref.Cap) {
		return errors.New("qualification weekly authorization revision mismatch")
	}
	if revision == scheduleRevision {
		if proof, expired := g.evidence.transients["qualification_schedules.config"][ref.ScheduleID]; expired {
			if proof.Summary.Schedule == nil || proof.Summary.Schedule.BudgetLimit != cap {
				return errors.New("expired qualification schedule cap mismatch")
			}
			return nil
		}
		var config struct {
			Budget int64 `json:"budget_limit_micros"`
		}
		if json.Unmarshal([]byte(g.value("qualification_schedules", schedule, "config")), &config) != nil || config.Budget != cap {
			return errors.New("qualification latest weekly cap mismatch")
		}
	}
	return nil
}

func addQualificationAmount(left, right int64) (int64, error) {
	if left < 0 || right < 0 || right > math.MaxInt64-left {
		return 0, errors.New("qualification weekly amount overflow")
	}
	return left + right, nil
}
