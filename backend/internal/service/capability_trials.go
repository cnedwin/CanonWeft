package service

import (
	"context"
	"database/sql"

	"novelstudio/internal/domain"
	"novelstudio/internal/models"
)

type CapabilityTrial = domain.CapabilityUse

func (s *Capabilities) prepareTrial(ctx context.Context, a domain.Actor, project string, input GenerationInput, request models.Request) (models.Request, *CapabilityTrial, error) {
	if input.evaluation != nil {
		return s.prepareEvaluation(ctx, a, project, input)
	}
	if input.BindingID != "" {
		return s.prepareQualified(ctx, a, project, input, request)
	}
	if input.TaskKind != "" {
		return request, nil, domain.ErrValidation
	}
	if input.AgentVersionID == "" {
		if input.CapabilityTrial || len(input.AgentInput) > 0 {
			return request, nil, domain.ErrValidation
		}
		return request, nil, nil
	}
	if !input.CapabilityTrial || len(input.AgentInput) == 0 || len(input.AgentInput) > 256*1024 {
		return request, nil, domain.ErrValidation
	}
	tx, _, _, err := s.IP.begin(ctx, a, project, true)
	if err != nil {
		return request, nil, err
	}
	defer tx.Rollback()
	manifest, err := s.manifestTx(ctx, tx, project, input.AgentVersionID)
	if err != nil {
		return request, nil, err
	}
	if !manifest.TrialReady {
		return request, nil, domain.ErrNotReady
	}
	root, err := capabilityTx(ctx, tx, project, input.AgentVersionID)
	if err != nil {
		return request, nil, err
	}
	if root.Definition.Role != input.Role || len(request.Schema) > 0 {
		return request, nil, domain.ErrValidation
	}
	bundles, err := s.methodBundlesTx(ctx, tx, project, manifest.Versions)
	if err != nil {
		return request, nil, err
	}
	request, err = models.AppendCapabilityMethod(request, root, manifest.Hash, manifest.Versions, bundles, input.AgentInput, true)
	if err != nil {
		return request, nil, err
	}
	return request, &CapabilityTrial{AgentVersionID: root.ID, ManifestHash: manifest.Hash, Mode: "TRIAL"}, nil
}

func (s *Capabilities) validateTrialTx(ctx context.Context, tx *sql.Tx, project string, trial *CapabilityTrial) error {
	if trial == nil {
		return nil
	}
	if trial.Mode == "EVALUATION" {
		return s.validateEvaluationTx(ctx, tx, project, trial)
	}
	if trial.Mode == "QUALIFIED" {
		return s.validateQualifiedTx(ctx, tx, project, trial)
	}
	if trial.Mode != "TRIAL" {
		return domain.ErrConflict
	}
	manifest, err := s.manifestTx(ctx, tx, project, trial.AgentVersionID)
	if err != nil {
		return err
	}
	if !manifest.TrialReady || manifest.Hash != trial.ManifestHash {
		return domain.ErrConflict
	}
	return nil
}
