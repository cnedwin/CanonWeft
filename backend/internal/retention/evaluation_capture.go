package retention

import (
	"novelstudio/internal/qualification"
	"novelstudio/internal/tooltrial"
)

// Producers record their evidence before dependent consumers. Do not expand a
// step back into its run's report: later receipt refreshes must retain the old
// report's pinned metrics instead of rebinding it to the current step version.
func evaluationCaptureKeys(row SourceRow) ([]SourceKey, error) {
	var keys []SourceKey
	add := func(table, id string) {
		if id != "" {
			keys = append(keys, SourceKey{Table: table, Primary: map[string]string{"id": id}})
		}
	}
	switch row.Table {
	case "qualification_approvals":
		add("qualification_runs", row.Values["run_id"])
	case "tool_trials":
		var frozen tooltrial.Frozen
		if authorDocument(row.Values["frozen"], &frozen) != nil {
			return nil, ErrSourceOwnership
		}
		add("capability_versions", frozen.VersionID)
	case "qualification_runs":
		var frozen qualification.QualificationFrozen
		if authorDocument(row.Values["frozen"], &frozen) != nil {
			return nil, ErrSourceOwnership
		}
		add("capability_versions", frozen.Candidate.ID)
		add("capability_versions", frozen.Baseline.ID)
		add("capability_versions", frozen.NoMethod.ID)
		add("ip_revisions", frozen.Input.ExpectedIPHead)
	case "qualification_steps":
		var metrics qualification.EvaluationMetrics
		if authorDocument(row.Values["metrics"], &metrics) != nil {
			return nil, ErrSourceOwnership
		}
		if metrics.OutputHash == "" {
			return nil, nil
		}
		switch row.Values["child_kind"] {
		case "generation":
			add("generation_tasks", row.Values["child_id"])
		case "tool":
			add("tool_trials", row.Values["child_id"])
		default:
			return nil, ErrSourceOwnership
		}
	}
	return keys, nil
}
