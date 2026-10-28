package backup

import (
	"encoding/json"
	"errors"
	"novelstudio/internal/qualificationledger"
	"strconv"
	"strings"
	"time"
)

func (g qualificationBudgetGraph) validateReverse(d Data, links, runs, steps map[string]Row) error {
	generations := map[string]Row{}
	for _, row := range d["generation_tasks"] {
		id, call := g.value("generation_tasks", row, "id"), g.value("generation_tasks", row, "call_id")
		if id == "" || generations[id] != nil {
			return errors.New("duplicate or missing generation identity")
		}
		generations[id] = row
		if call == "" {
			continue
		}
		frozen, err := g.generationFrozen(row)
		if err != nil {
			return err
		}
		evaluation := frozen.Capability != nil && frozen.Capability.Mode == "EVALUATION"
		operation := g.value("generation_tasks", row, "operation_id")
		if strings.HasPrefix(operation, "evaluation-") {
			if step, ok := steps[strings.TrimPrefix(operation, "evaluation-")]; ok {
				run := runs[g.value("qualification_steps", step, "run_id")]
				if g.value("generation_tasks", row, "project_id") == g.value("qualification_steps", step, "project_id") && g.value("generation_tasks", row, "actor_id") == g.value("qualification_runs", run, "actor_id") {
					evaluation = true
				}
			}
		}
		if evaluation {
			if _, ok := links[call]; !ok {
				return errors.New("evaluation generation missing durable call accounting mapping")
			}
		}
	}
	for _, row := range d["qualification_steps"] {
		if g.value("qualification_steps", row, "child_kind") != "generation" {
			continue
		}
		id := g.value("qualification_steps", row, "child_id")
		if id == "" {
			continue
		}
		generation, ok := generations[id]
		if !ok {
			return errors.New("evaluation step generation missing")
		}
		call := g.value("generation_tasks", generation, "call_id")
		if call != "" {
			if _, ok := links[call]; !ok {
				return errors.New("evaluation step call missing accounting mapping")
			}
		}
	}
	return nil
}

// Original schema28 has no durable call-link table. Only that explicit archive
// source may be converted; schema29/30 missing mappings remain corruption.
func upgradeQualificationAccounting(a *Archive) error {
	if a.Manifest.Schema == 29 {
		for index, row := range a.Data["qualification_calls"] {
			if len(row) != 10 {
				return errors.New("schema29 qualification column mismatch")
			}
			a.Data["qualification_calls"][index] = append(row, []byte("NATIVE_29"), nil)
		}
	}
	if a.Manifest.Schema != 28 {
		return nil
	}
	specs := map[string]tableSpec{}
	for _, spec := range tableCatalog {
		specs[spec.name] = spec
	}
	graph := qualificationBudgetGraph{specs: specs}
	value := graph.value
	index := func(table, key string) map[string]Row {
		result := map[string]Row{}
		for _, row := range a.Data[table] {
			result[value(table, row, key)] = row
		}
		return result
	}
	calls, accounting, runs, steps := index("model_calls", "id"), index("model_call_accounting", "call_id"), index("qualification_runs", "id"), index("qualification_steps", "id")
	manifest, _ := json.Marshal(a.Manifest)
	source := qualificationledger.Source{Schema: 28, Kind: "ARCHIVE", Boundary: a.Manifest.SnapshotAt.UTC().Format(time.RFC3339Nano), ManifestHash: digest(manifest)}
	spec := specs["qualification_calls"]
	for _, generation := range a.Data["generation_tasks"] {
		callID := value("generation_tasks", generation, "call_id")
		if callID == "" {
			continue
		}
		var frozen struct {
			Capability *struct {
				Mode string `json:"mode"`
				Run  string `json:"evaluation_id"`
			} `json:"capability"`
		}
		if json.Unmarshal([]byte(value("generation_tasks", generation, "frozen")), &frozen) != nil {
			return errors.New("invalid legacy generation input")
		}
		if frozen.Capability == nil || frozen.Capability.Mode != "EVALUATION" {
			continue
		}
		stepID := strings.TrimPrefix(value("generation_tasks", generation, "operation_id"), "evaluation-")
		run, step, call := runs[frozen.Capability.Run], steps[stepID], calls[callID]
		link := make(Row, len(spec.cols()))
		spec.set(link, "project_id", value("generation_tasks", generation, "project_id"))
		record := graph.record(link, run, step, call, generation, accounting[callID])
		mapping, err := qualificationledger.Convert(record, source)
		if err != nil {
			return err
		}
		fields := map[string]string{"call_id": record.CallID, "project_id": record.Project, "run_id": record.RunID, "step_id": record.StepID, "grant_id": "", "reserved_micros": strconv.FormatInt(mapping.Reserved, 10), "held_micros": strconv.FormatInt(mapping.Held, 10), "charged_micros": strconv.FormatInt(mapping.Charged, 10), "state": mapping.State, "created_at": value("model_calls", call, "created_at"), "origin": mapping.Origin, "legacy_source": string(mapping.Provenance)}
		for column, raw := range fields {
			spec.set(link, column, raw)
		}
		a.Data["qualification_calls"] = append(a.Data["qualification_calls"], link)
	}
	return nil
}
