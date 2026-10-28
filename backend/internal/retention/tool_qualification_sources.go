package retention

import (
	"context"
	"strconv"

	"novelstudio/internal/domain"
	"novelstudio/internal/isolation"
	"novelstudio/internal/tooltrial"
)

func (b *contextSourceBuilder) toolQualification(ctx context.Context, child SourceRow, use tooltrial.Frozen, request isolation.Request, read ReadSourceObject) error {
	evaluation := use.EvaluationID != "" || use.EvaluationStepID != ""
	qualified := use.BindingID != "" || use.QualificationID != "" || use.TaskKind != ""
	if evaluation && qualified {
		return ErrSourceOwnership
	}
	if qualified {
		binding, err := b.index.sourceRow("capability_bindings", map[string]string{"id": use.BindingID})
		if err != nil || binding.Values["project_id"] != b.project || binding.Values["qualification_id"] != use.QualificationID || binding.Values["task_kind"] != use.TaskKind {
			return ErrSourceOwnership
		}
		record, err := b.index.sourceRow("capability_qualifications", map[string]string{"id": use.QualificationID})
		if err != nil || record.Values["project_id"] != b.project {
			return ErrSourceOwnership
		}
		run, frozen, err := b.qualificationRun(record.Values["run_id"])
		if err != nil || frozen.Candidate.Kind != "tool" || frozen.Candidate.ID != use.VersionID || frozen.Candidate.Hash != use.VersionHash || frozen.CandidateManifest != use.ManifestHash || frozen.Suite.TaskKind != use.TaskKind || frozen.RuntimeHash != use.ImageHash {
			return ErrSourceOwnership
		}
		for _, field := range []string{"payload", "report_hash"} {
			if err := b.link(record, field, ""); err != nil {
				return err
			}
		}
		return b.link(run, "frozen", "")
	}
	if !evaluation {
		return nil
	}
	run, frozen, err := b.qualificationRun(use.EvaluationID)
	if err != nil || run.Values["actor_id"] != b.actor || frozen.Candidate.Kind != "tool" || frozen.RuntimeHash != use.ImageHash {
		return ErrSourceOwnership
	}
	step, err := b.index.sourceRow("qualification_steps", map[string]string{"id": use.EvaluationStepID})
	if err != nil || step.Values["run_id"] != run.Values["id"] || step.Values["child_id"] != child.Values["id"] || step.Values["child_kind"] != "tool" {
		return ErrSourceOwnership
	}
	repeat, err := strconv.Atoi(step.Values["repeat_index"])
	if err != nil || repeat < 0 || repeat >= frozen.Suite.Repeats {
		return ErrSourceOwnership
	}
	var ref domain.CapabilityRef
	var hash string
	switch step.Values["arm"] {
	case "candidate":
		ref, hash = frozen.Candidate, frozen.CandidateManifest
	case "baseline":
		ref, hash = frozen.Baseline, frozen.BaselineManifest
	case "no_method":
		ref = frozen.NoMethod
		manifest, err := b.capabilityClosure(ctx, ref, read)
		if err != nil {
			return err
		}
		hash = manifest.ManifestHash
	default:
		return ErrSourceOwnership
	}
	if ref.Kind != "tool" || ref.ID != use.VersionID || ref.Hash != use.VersionHash || hash != use.ManifestHash {
		return ErrSourceOwnership
	}
	matched := 0
	for _, c := range frozen.Suite.Cases {
		if c.ID == step.Values["case_key"] {
			// SQL JSON storage may reorder object keys; compare exact JSON values.
			if !toolCaseInputMatches(request.Input, c.Input) {
				return ErrSourceOwnership
			}
			matched++
		}
	}
	if matched != 1 {
		return ErrSourceOwnership
	}
	return b.link(run, "frozen", "")
}

func toolCaseInputMatches(actual, expected []byte) bool {
	var left, right any
	return strictJSONWithLimit(actual, &left, tooltrial.MaxInputBytes) == nil &&
		strictJSONWithLimit(expected, &right, tooltrial.MaxInputBytes) == nil &&
		equalContextJSON(left, right)
}
