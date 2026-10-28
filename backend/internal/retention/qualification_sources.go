package retention

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"unicode/utf8"

	"novelstudio/internal/domain"
	"novelstudio/internal/models"
	"novelstudio/internal/qualification"
	"novelstudio/internal/skills"
	"novelstudio/internal/tooltrial"
)

func (b *contextSourceBuilder) qualificationRun(id string) (SourceRow, qualification.QualificationFrozen, error) {
	row, err := b.index.sourceRow("qualification_runs", map[string]string{"id": id})
	var frozen qualification.QualificationFrozen
	if err != nil || row.Values["project_id"] != b.project || authorDocument(row.Values["frozen"], &frozen) != nil || frozen.Input.BranchID != row.Values["branch_id"] || frozen.Input.VersionID != frozen.Candidate.ID || frozen.Input.BaselineID != frozen.Baseline.ID || frozen.Input.SuiteID != frozen.Suite.ID {
		return row, frozen, ErrSourceOwnership
	}
	return row, frozen, nil
}

func (index *SourceIndex) attachQualificationSources(ctx context.Context, project string, materials []ObservedMaterial, read ReadSourceObject) error {
	for i := range materials {
		m := &materials[i]
		if m.Table != "qualification_runs" && m.Table != "qualification_steps" && m.Table != "capability_qualifications" && m.Table != "qualification_approvals" {
			continue
		}
		if m.Field != "frozen" && m.Field != "payload" && m.Field != "metrics" && m.Field != "report_hash" {
			continue
		}
		row := index.rows[m.Table][m.OwnerKey]
		runID := row.Values["run_id"]
		if row.Table == "qualification_runs" {
			runID = row.Values["id"]
		}
		b := contextSourceBuilder{index: index, project: project, m: &m.MaterialReference}
		run, frozen, err := b.qualificationRun(runID)
		if err != nil {
			return err
		}
		b.branch, b.actor = run.Values["branch_id"], run.Values["actor_id"]
		switch {
		case m.Table == "qualification_approvals":
			var approval qualification.ExecutableApproval
			if authorDocument(row.Values["payload"], &approval) != nil || read == nil || frozen.Candidate.Kind != "tool" || approval.Policy != qualification.ExecutableApprovalPolicy || approval.ReportHash != run.Values["report_hash"] || approval.ManifestHash != frozen.CandidateManifest || approval.RuntimeHash != frozen.RuntimeHash || approval.SuiteHash != frozen.Suite.Hash || strings.TrimSpace(approval.Note) == "" || utf8.RuneCountInString(approval.Note) > 2000 {
				return ErrSourceOwnership
			}
			data, err := read(ctx, project, approval.ReportHash)
			var report qualification.QualificationReport
			if err != nil || contentJSON(data, &report, approval.ReportHash) != nil || report.RunID != runID || !report.Passed || report.ProfessionalQualified || capabilityProjectionHash(report.Frozen) != capabilityProjectionHash(frozen) {
				return ErrSourceOwnership
			}
			for _, field := range []string{"frozen", "report_hash"} {
				if err := b.link(run, field, ""); err != nil {
					return err
				}
			}
		case m.Table == "qualification_runs" && m.Field == "frozen":
			for _, ref := range []struct {
				ref  domain.CapabilityRef
				hash string
			}{{frozen.Candidate, frozen.CandidateManifest}, {frozen.Baseline, frozen.BaselineManifest}} {
				if _, err := b.capabilityManifest(ctx, ref.ref, ref.hash, read); err != nil {
					return err
				}
			}
			if _, err := b.ip(frozen.Input.ExpectedIPHead, b.branch); err != nil {
				return err
			}
			trusted, err := qualification.Lookup(frozen.Suite.ID)
			if err != nil || trusted.Public().Hash != frozen.Suite.Hash {
				m.UnboundSources++
			} else if capabilityProjectionHash(trusted.Public()) != capabilityProjectionHash(frozen.Suite) {
				return ErrSourceOwnership
			}
			if frozen.NoMethod.ID != "" {
				if frozen.Candidate.Kind != "tool" || frozen.NoMethod.Kind != "tool" {
					return ErrSourceOwnership
				}
				if _, err := b.capabilityClosure(ctx, frozen.NoMethod, read); err != nil {
					return err
				}
			}
		case m.Table == "qualification_steps":
			if err := b.qualificationMetrics(ctx, row, run, read); err != nil {
				return err
			}
		case m.Table == "capability_qualifications":
			var copied qualification.QualificationFrozen
			if index.replacedColumn(project, row, "payload") {
				proof, err := ParseSourceProjection([]byte(row.Values["payload"]), "capability_qualifications.payload")
				if err != nil || proof.Summary.InputHash != capabilityProjectionHash(frozen) {
					return ErrSourceOwnership
				}
			} else if authorDocument(row.Values["payload"], &copied) != nil || capabilityProjectionHash(copied) != capabilityProjectionHash(frozen) {
				return ErrSourceOwnership
			}
			if row.Values["report_hash"] != run.Values["report_hash"] {
				return ErrSourceOwnership
			}
			if err := b.link(run, "frozen", ""); err != nil {
				return err
			}
			if err := b.link(run, "report_hash", ""); err != nil {
				return err
			}
		case m.Table == "qualification_runs" && m.Field == "report_hash":
			data, err := read(ctx, project, row.Values["report_hash"])
			var report qualification.QualificationReport
			if err != nil || contentJSON(data, &report, row.Values["report_hash"]) != nil || report.RunID != runID || capabilityProjectionHash(report.Frozen) != capabilityProjectionHash(frozen) {
				return ErrSourceOwnership
			}
			if err := b.link(run, "frozen", ""); err != nil {
				return err
			}
			for _, step := range report.Steps {
				source, err := index.sourceRow("qualification_steps", map[string]string{"id": step.ID})
				if err != nil || source.Values["run_id"] != runID || source.Values["arm"] != step.Arm || source.Values["case_key"] != step.CaseID || source.Values["child_id"] != step.ChildID || source.Values["child_kind"] != step.ChildKind {
					return ErrSourceOwnership
				}
				var current qualification.EvaluationMetrics
				if authorDocument(source.Values["metrics"], &current) != nil {
					return ErrSourceOwnership
				}
				if capabilityProjectionHash(current) != capabilityProjectionHash(step.Metrics) {
					// A later invoice may legitimately refresh current metrics. An
					// unchanged report retains its already journaled original pin;
					// without that pin, no current or guessed hash proves the past.
					key, err := RowOwnerKey(source.Table, source.Values)
					if err != nil {
						return err
					}
					id, err := ReferenceID(project, source.Table, key, "metrics")
					if err != nil {
						return err
					}
					if err := appendSourceRequest(&m.MaterialReference, MaterialSource{ReferenceID: id, Unresolved: true}); err != nil {
						return err
					}
					continue
				}
				if err := b.link(source, "metrics", ""); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (b *contextSourceBuilder) qualificationMetrics(ctx context.Context, row, run SourceRow, read ReadSourceObject) error {
	if err := b.link(run, "frozen", ""); err != nil {
		return err
	}
	var metrics qualification.EvaluationMetrics
	if authorDocument(row.Values["metrics"], &metrics) != nil {
		return ErrSourceOwnership
	}
	if metrics.OutputHash == "" {
		return nil
	}
	if row.Values["child_kind"] != "generation" {
		if row.Values["child_kind"] != "tool" {
			return ErrSourceOwnership
		}
		child, err := b.index.sourceRow("tool_trials", map[string]string{"id": row.Values["child_id"]})
		var frozen tooltrial.Frozen
		if err != nil || child.Values["project_id"] != b.project || child.Values["actor_id"] != b.actor || authorDocument(child.Values["frozen"], &frozen) != nil || frozen.EvaluationID != run.Values["id"] || frozen.EvaluationStepID != row.Values["id"] {
			return ErrSourceOwnership
		}
		data, err := read(ctx, b.project, metrics.OutputHash)
		var output tooltrial.Output
		elapsed, timeErr := strconv.ParseInt(child.Values["elapsed_ms"], 10, 64)
		if err != nil || contentJSON(data, &output, metrics.OutputHash) != nil || output.Stdout != metrics.Output || metrics.InputTokens != 0 || metrics.OutputTokens != 0 || metrics.Cost != 0 || timeErr != nil || metrics.DurationMS != elapsed {
			return ErrSourceOwnership
		}
		if err := b.link(child, "frozen", ""); err != nil {
			return err
		}
		return b.link(child, "output_hash", financialDigest([]byte(metrics.OutputHash)))
	}
	child, err := b.index.sourceRow("generation_tasks", map[string]string{"id": row.Values["child_id"]})
	if err != nil || child.Values["project_id"] != b.project || child.Values["actor_id"] != b.actor || child.Values["branch_id"] != b.branch {
		return ErrSourceOwnership
	}
	call, err := b.index.sourceRow("model_calls", map[string]string{"id": child.Values["call_id"]})
	if err != nil || call.Values["project_id"] != b.project {
		return ErrSourceOwnership
	}
	data, err := read(ctx, b.project, metrics.OutputHash)
	var output models.Result
	if err != nil || contentJSON(data, &output, metrics.OutputHash) != nil || output.Text != metrics.Output || output.InputTokens != metrics.InputTokens || output.OutputTokens != metrics.OutputTokens {
		return ErrSourceOwnership
	}
	if err := b.link(child, "frozen", ""); err != nil {
		return err
	}
	if call.Values["output_hash"] == "" && call.Values["cancel_requested"] == "1" && call.Values["settlement_hash"] != "" {
		id := financialDigest([]byte(call.Values["id"] + "\x00" + call.Values["settlement_hash"]))
		receipt, err := b.index.sourceRow("model_call_receipts", map[string]string{"id": id})
		if err != nil || receipt.Values["call_id"] != call.Values["id"] {
			return ErrSourceOwnership
		}
		evidence, err := ReceiptEvidence(call.Values["id"], id, []byte(receipt.Values["payload"]))
		if err != nil || evidence.OutputHash != metrics.OutputHash {
			return ErrSourceOwnership
		}
		return b.link(receipt, "payload.output_hash", financialDigest([]byte(metrics.OutputHash)))
	}
	return b.link(call, "output_hash", financialDigest([]byte(metrics.OutputHash)))
}

func (b *contextSourceBuilder) qualificationUse(ctx context.Context, child SourceRow, use *domain.CapabilityUse, request models.Request, inputHash string, read ReadSourceObject) error {
	if use == nil || use.Mode == "TRIAL" {
		return nil
	}
	var run SourceRow
	var frozen qualification.QualificationFrozen
	var err error
	if use.Mode == "QUALIFIED" {
		binding, e := b.index.sourceRow("capability_bindings", map[string]string{"id": use.BindingID})
		if e != nil || binding.Values["project_id"] != b.project || binding.Values["qualification_id"] != use.QualificationID || binding.Values["task_kind"] != use.TaskKind {
			return ErrSourceOwnership
		}
		record, e := b.index.sourceRow("capability_qualifications", map[string]string{"id": use.QualificationID})
		if e != nil || record.Values["project_id"] != b.project {
			return ErrSourceOwnership
		}
		run, frozen, err = b.qualificationRun(record.Values["run_id"])
		if err != nil || frozen.Candidate.ID != use.AgentVersionID || frozen.CandidateManifest != use.ManifestHash {
			return ErrSourceOwnership
		}
		if err := b.link(record, "payload", ""); err != nil {
			return err
		}
		if err := b.link(record, "report_hash", ""); err != nil {
			return err
		}
	} else if use.Mode == "EVALUATION" {
		run, frozen, err = b.qualificationRun(use.EvaluationID)
		if err != nil || run.Values["branch_id"] != b.branch || run.Values["actor_id"] != b.actor {
			return ErrSourceOwnership
		}
		step, e := b.index.sourceRow("qualification_steps", map[string]string{"id": use.EvaluationStepID})
		if e != nil || step.Values["run_id"] != run.Values["id"] || step.Values["child_id"] != child.Values["id"] || step.Values["child_kind"] != "generation" {
			return ErrSourceOwnership
		}
		want, input, e := qualification.EvaluationRequest(frozen.Suite, step.Values["case_key"], frozen.Input.MaxOutputTokens)
		if e != nil {
			return ErrSourceOwnership
		}
		ref, hash := domain.CapabilityRef{}, ""
		switch step.Values["arm"] {
		case "candidate":
			ref, hash = frozen.Candidate, frozen.CandidateManifest
		case "baseline":
			ref, hash = frozen.Baseline, frozen.BaselineManifest
		case "no_method":
		default:
			return ErrSourceOwnership
		}
		if ref.ID != use.AgentVersionID || hash != use.ManifestHash {
			return ErrSourceOwnership
		}
		if ref.ID != "" {
			manifest, e := b.capabilityManifest(ctx, ref, hash, read)
			if e != nil {
				return e
			}
			bundles := map[string]skills.Bundle{}
			for _, v := range manifest.Versions {
				if v.Definition.Kind == "skill" {
					bundle, e := b.capabilityPackage(ctx, v.Definition.PackageID, v.Definition.PackageHash, read)
					if e != nil {
						return e
					}
					bundles[v.Definition.PackageID] = bundle
				}
			}
			want, e = models.AppendCapabilityMethod(want, manifest.CapabilityVersion, hash, manifest.Versions, bundles, input, false)
			if e != nil {
				return e
			}
		}
		actual, _ := json.Marshal(request)
		if !exactJSONBytes(want, actual) {
			return ErrSourceOwnership
		}
		matched := 0
		for _, quote := range frozen.RequestQuotes {
			if quote.Arm == step.Values["arm"] && quote.CaseID == step.Values["case_key"] && quote.RequestHash == inputHash && quote.InputBytes == len(actual) {
				matched++
			}
		}
		if matched != 1 {
			return ErrSourceOwnership
		}
		repeat, e := strconv.Atoi(step.Values["repeat_index"])
		if e != nil || repeat < 0 || repeat >= frozen.Suite.Repeats {
			return ErrSourceOwnership
		}
	} else {
		return ErrSourceOwnership
	}
	if use.TaskKind != frozen.Suite.TaskKind || use.SuiteHash != frozen.Suite.Hash || use.ConfigHash != capabilityProjectionHash(frozen.Config) || use.MaxOutputTokens != frozen.Input.MaxOutputTokens || request.MaxOutputTokens != use.MaxOutputTokens {
		return ErrSourceOwnership
	}
	var childConfig struct {
		Config domain.ModelConfig `json:"config"`
		Role   string             `json:"role"`
	}
	if json.Unmarshal([]byte(child.Values["frozen"]), &childConfig) != nil || capabilityProjectionHash(childConfig.Config) != use.ConfigHash || childConfig.Role != frozen.Input.Role {
		return ErrSourceOwnership
	}
	return b.link(run, "frozen", "")
}
