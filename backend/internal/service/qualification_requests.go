package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"novelstudio/internal/domain"
	"novelstudio/internal/models"
	"novelstudio/internal/qualification"
)

// EvaluationReference has no JSON fields in GenerationInput. Only the durable
// evaluator can construct it; HTTP callers cannot supply prompts or answers.
type EvaluationReference struct{ RunID, StepID, Arm, CaseID string }

var errEvaluationPaused = errors.New("parent evaluation is paused")

func evaluationPausedTx(ctx context.Context, tx *sql.Tx, g Generation) error {
	if g.Frozen.Capability == nil || g.Frozen.Capability.Mode != "EVALUATION" {
		return nil
	}
	r, err := qualificationRunTx(ctx, tx, g.ProjectID, g.Frozen.Capability.EvaluationID)
	if err != nil {
		return err
	}
	if r.Paused && !r.Cancelled {
		return errEvaluationPaused
	}
	return nil
}

func (s *Capabilities) methodRequestTx(ctx context.Context, tx *sql.Tx, project, id string, input json.RawMessage, request models.Request) (models.Request, CapabilityManifest, error) {
	m, err := s.manifestTx(ctx, tx, project, id)
	if err != nil {
		return request, m, err
	}
	root, err := capabilityTx(ctx, tx, project, id)
	if err != nil {
		return request, m, err
	}
	if root.Definition.Kind != "agent" && root.Definition.Kind != "skill" {
		return request, m, domain.ErrValidation
	}
	for _, f := range m.Findings {
		if f.Code == "REVOKED" || f.Code == "TOOL_QUALIFICATION_REQUIRED" {
			return request, m, domain.ErrNotReady
		}
	}
	bundles, err := s.methodBundlesTx(ctx, tx, project, m.Versions)
	if err != nil {
		return request, m, err
	}
	request, err = models.AppendCapabilityMethod(request, root, m.Hash, m.Versions, bundles, input, false)
	if err != nil {
		return request, m, err
	}
	return request, m, nil
}

func (s *Capabilities) prepareEvaluation(ctx context.Context, a domain.Actor, project string, input GenerationInput) (models.Request, *CapabilityTrial, error) {
	var request models.Request
	ref := input.evaluation
	tx, _, _, err := s.IP.begin(ctx, a, project, true)
	if err != nil {
		return request, nil, err
	}
	defer tx.Rollback()
	r, err := qualificationRunTx(ctx, tx, project, ref.RunID)
	if err != nil {
		return request, nil, err
	}
	if r.ActorID != a.ID || r.BranchID != input.BranchID || r.Frozen.Input.Role != input.Role || r.Frozen.Input.PriceID != input.PriceID {
		return request, nil, domain.ErrConflict
	}
	if err = (&Qualifications{IP: s.IP}).guardRunTx(ctx, tx, r, false); err != nil {
		return request, nil, err
	}
	var stepArm, caseID string
	if err = tx.QueryRowContext(ctx, "SELECT arm,case_key FROM qualification_steps WHERE id=? AND run_id=?", ref.StepID, r.ID).Scan(&stepArm, &caseID); err != nil {
		return request, nil, err
	}
	if stepArm != ref.Arm || caseID != ref.CaseID {
		return request, nil, domain.ErrConflict
	}
	request, manifest, err := s.evaluationTaskRequestTx(ctx, tx, project, r.Frozen, ref.Arm, caseID)
	if err != nil {
		return request, nil, err
	}
	trial := &CapabilityTrial{Mode: "EVALUATION", EvaluationID: r.ID, EvaluationStepID: ref.StepID, TaskKind: r.Frozen.Suite.TaskKind, SuiteHash: r.Frozen.Suite.Hash, ConfigHash: capabilityDigest(r.Frozen.Config), MaxOutputTokens: request.MaxOutputTokens, AgentVersionID: manifest.Root.ID, ManifestHash: manifest.Hash}
	return request, trial, nil
}

func (s *Capabilities) evaluationTaskRequestTx(ctx context.Context, tx *sql.Tx, project string, frozen QualificationFrozen, arm, caseID string) (models.Request, CapabilityManifest, error) {
	var request models.Request
	var manifest CapabilityManifest
	suite, lookupErr := qualification.Lookup(frozen.Suite.ID)
	if lookupErr != nil {
		return request, manifest, domain.ErrValidation
	}
	entry := suite.Public()
	request, taskInput, err := qualification.EvaluationRequest(entry, caseID, frozen.Input.MaxOutputTokens)
	if err != nil {
		return request, manifest, err
	}
	var version domain.CapabilityRef
	if arm == "candidate" {
		version = frozen.Candidate
	} else if arm == "baseline" {
		version = frozen.Baseline
	} else if arm != "no_method" {
		return request, manifest, domain.ErrValidation
	}
	if version.ID != "" {
		var err error
		request, manifest, err = s.methodRequestTx(ctx, tx, project, version.ID, taskInput, request)
		if err != nil {
			return request, manifest, err
		}
	}
	return request, manifest, nil
}

func (s *Capabilities) prepareQualified(ctx context.Context, a domain.Actor, project string, input GenerationInput, request models.Request) (models.Request, *CapabilityTrial, error) {
	if input.AgentVersionID != "" || input.CapabilityTrial || len(input.AgentInput) == 0 || len(request.Schema) > 0 {
		return request, nil, domain.ErrValidation
	}
	tx, a, _, err := s.IP.begin(ctx, a, project, true)
	if err != nil {
		return request, nil, err
	}
	defer tx.Rollback()
	q := &Qualifications{IP: s.IP}
	b, err := bindingTx(ctx, tx, project, input.BindingID)
	if err != nil {
		return request, nil, err
	}
	if !b.Enabled || b.TaskKind != input.TaskKind {
		return request, nil, domain.ErrConflict
	}
	if err = requireCurrentBindingTx(ctx, tx, project, b.ID, b.TaskKind); err != nil {
		return request, nil, err
	}
	record, err := q.recordTx(ctx, tx, project, b.QualificationID)
	if err != nil {
		return request, nil, err
	}
	if err = q.validateRecordTx(ctx, tx, a, project, record); err != nil {
		return request, nil, err
	}
	if record.Frozen.Input.Role != input.Role || record.Frozen.Input.MaxOutputTokens != request.MaxOutputTokens {
		return request, nil, domain.ErrConflict
	}
	request, m, err := s.methodRequestTx(ctx, tx, project, record.Frozen.Candidate.ID, input.AgentInput, request)
	if err != nil {
		return request, nil, err
	}
	return request, &CapabilityTrial{Mode: "QUALIFIED", AgentVersionID: m.Root.ID, ManifestHash: m.Hash, BindingID: b.ID, QualificationID: record.ID, TaskKind: b.TaskKind, SuiteHash: record.Frozen.Suite.Hash, ConfigHash: capabilityDigest(record.Frozen.Config), MaxOutputTokens: request.MaxOutputTokens}, nil
}

func (s *Capabilities) validateEvaluationTx(ctx context.Context, tx *sql.Tx, project string, trial *CapabilityTrial) error {
	r, err := qualificationRunTx(ctx, tx, project, trial.EvaluationID)
	if err != nil {
		return err
	}
	if r.State == "FAILED" || r.State == "BLOCKED" || r.Cancelled {
		return domain.ErrConflict
	}
	if trial.TaskKind != r.Frozen.Suite.TaskKind || trial.SuiteHash != r.Frozen.Suite.Hash || trial.ConfigHash != capabilityDigest(r.Frozen.Config) || trial.MaxOutputTokens != r.Frozen.Input.MaxOutputTokens {
		return domain.ErrConflict
	}
	return (&Qualifications{IP: s.IP}).guardRunTx(ctx, tx, r, true)
}

func (s *Capabilities) validateQualifiedTx(ctx context.Context, tx *sql.Tx, project string, trial *CapabilityTrial) error {
	b, err := bindingTx(ctx, tx, project, trial.BindingID)
	if err != nil {
		return err
	}
	if !b.Enabled || b.QualificationID != trial.QualificationID || b.TaskKind != trial.TaskKind {
		return domain.ErrConflict
	}
	q := &Qualifications{IP: s.IP}
	record, err := q.recordTx(ctx, tx, project, b.QualificationID)
	if err != nil {
		return err
	}
	r, err := qualificationRunTx(ctx, tx, project, record.RunID)
	if err != nil {
		return err
	}
	a, err := s.IP.Workspace.actorTx(ctx, tx, r.ActorID)
	if err != nil {
		return err
	}
	if trial.AgentVersionID != record.Frozen.Candidate.ID || trial.ManifestHash != record.Frozen.CandidateManifest || trial.SuiteHash != record.Frozen.Suite.Hash || trial.ConfigHash != capabilityDigest(record.Frozen.Config) || trial.MaxOutputTokens != record.Frozen.Input.MaxOutputTokens {
		return domain.ErrConflict
	}
	return q.validateRecordTx(ctx, tx, a, project, record)
}

func (s *Capabilities) validateConfiguredTx(ctx context.Context, tx *sql.Tx, g Generation) error {
	trial := g.Frozen.Capability
	if trial == nil || trial.Mode == "TRIAL" {
		return nil
	}
	if capabilityDigest(g.Frozen.Config) != trial.ConfigHash {
		return domain.ErrConflict
	}
	return s.validateTrialTx(ctx, tx, g.ProjectID, trial)
}
