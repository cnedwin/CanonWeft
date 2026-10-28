package retention

import (
	"encoding/json"

	"novelstudio/internal/recipe"
)

// These summaries retain immutable identities and digests, never instructions,
// input bodies, routing pointers, model endpoints or generated output text.
type RecipeRunSummary struct {
	ID             string `json:"id"`
	DefinitionID   string `json:"definition_id"`
	DefinitionHash string `json:"definition_hash"`
	ManifestHash   string `json:"manifest_hash"`
	BaseHead       string `json:"base_head,omitempty"`
	InputHash      string `json:"input_hash"`
	Trial          bool   `json:"trial"`
}

type RecipeSourceSummary struct {
	StepID     string `json:"step_id"`
	OutputHash string `json:"output_hash"`
}

type RecipeStepSummary struct {
	ID                string                `json:"id"`
	RunID             string                `json:"run_id"`
	KeyHash           string                `json:"key_hash"`
	StepHash          string                `json:"step_hash"`
	AgentID           string                `json:"agent_id"`
	AgentHash         string                `json:"agent_hash"`
	ManifestHash      string                `json:"manifest_hash"`
	ContextID         string                `json:"context_id,omitempty"`
	ConfigID          string                `json:"config_id"`
	ConfigRevision    int64                 `json:"config_revision"`
	ConfigHash        string                `json:"config_hash"`
	PriceID           string                `json:"price_id"`
	PriceHash         string                `json:"price_hash"`
	InputMetadataHash string                `json:"input_metadata_hash"`
	MaxOutputTokens   int                   `json:"max_output_tokens"`
	Sources           []RecipeSourceSummary `json:"sources,omitempty"`
}

func validRecipeRunSummary(s RecipeRunSummary) bool {
	return id32.MatchString(s.ID) && id32.MatchString(s.DefinitionID) && hash64.MatchString(s.DefinitionHash) &&
		hash64.MatchString(s.ManifestHash) && hash64.MatchString(s.InputHash) && (s.BaseHead == "" || id32.MatchString(s.BaseHead))
}

func validRecipeStepSummary(s RecipeStepSummary) bool {
	for _, id := range []string{s.ID, s.RunID, s.AgentID, s.ConfigID, s.PriceID} {
		if !id32.MatchString(id) {
			return false
		}
	}
	for _, hash := range []string{s.KeyHash, s.StepHash, s.AgentHash, s.ManifestHash, s.ConfigHash, s.PriceHash, s.InputMetadataHash} {
		if !hash64.MatchString(hash) {
			return false
		}
	}
	if s.ConfigRevision < 1 || s.MaxOutputTokens < 1 || s.MaxOutputTokens > 131072 || (s.ContextID != "" && !id32.MatchString(s.ContextID)) || len(s.Sources) > 64 {
		return false
	}
	seen := map[string]bool{}
	for _, source := range s.Sources {
		if !id32.MatchString(source.StepID) || !hash64.MatchString(source.OutputHash) || seen[source.StepID] {
			return false
		}
		seen[source.StepID] = true
	}
	return true
}

func RecipeEvidence(table, id, run string, raw []byte, originalHash string) (EvidenceSummary, error) {
	if financialDigest(raw) != originalHash {
		return EvidenceSummary{}, ErrInvalidInventory
	}
	if table == "recipe_runs" {
		var f recipe.RecipeFrozen
		if authorDocument(string(raw), &f) != nil || f.Protocol != 1 || f.Definition.Definition.Kind != "recipe" || f.Definition.Definition.Validate() != nil || !recipeSchemaAccepts(f.Definition.Definition.InputSchema, f.Input) {
			return EvidenceSummary{}, ErrInvalidInventory
		}
		s := RecipeRunSummary{ID: id, DefinitionID: f.Definition.ID, DefinitionHash: f.Definition.Hash, ManifestHash: f.ManifestHash, BaseHead: f.BaseHead, InputHash: financialDigest(f.Input), Trial: f.Trial}
		if !validRecipeRunSummary(s) {
			return EvidenceSummary{}, ErrInvalidInventory
		}
		return EvidenceSummary{RecipeRun: &s}, nil
	}
	if table == "recipe_steps" {
		var f recipe.RecipeStepFrozen
		if authorDocument(string(raw), &f) != nil || f.Agent.Definition.Kind != "agent" || f.Agent.Definition.Validate() != nil || f.Step.AgentID != f.Agent.ID || f.Step.Key != f.Input.Key || f.Input.PriceID != f.Price.ID || f.Input.InputText != "" {
			return EvidenceSummary{}, ErrInvalidInventory
		}
		s := RecipeStepSummary{ID: id, RunID: run, KeyHash: financialDigest([]byte(f.Step.Key)), StepHash: capabilityProjectionHash(f.Step), AgentID: f.Agent.ID, AgentHash: f.Agent.Hash, ManifestHash: f.ManifestHash,
			ContextID: f.Input.ContextID, ConfigID: f.Config.ID, ConfigRevision: f.Config.Revision, ConfigHash: capabilityProjectionHash(f.Config), PriceID: f.Price.ID, PriceHash: capabilityProjectionHash(f.Price),
			InputMetadataHash: capabilityProjectionHash(f.Input), MaxOutputTokens: f.Input.MaxOutputTokens}
		for _, source := range f.Sources {
			s.Sources = append(s.Sources, RecipeSourceSummary{StepID: source.StepID, OutputHash: source.OutputHash})
		}
		if !validRecipeStepSummary(s) {
			return EvidenceSummary{}, ErrInvalidInventory
		}
		return EvidenceSummary{RecipeStep: &s}, nil
	}
	return EvidenceSummary{}, ErrInvalidInventory
}

func validateRecipeProjectionSource(rule string, raw []byte, summary EvidenceSummary) error {
	var table, id, run string
	if rule == "recipe_runs.frozen" && summary.RecipeRun != nil {
		table, id = "recipe_runs", summary.RecipeRun.ID
	} else if rule == "recipe_steps.frozen" && summary.RecipeStep != nil {
		table, id, run = "recipe_steps", summary.RecipeStep.ID, summary.RecipeStep.RunID
	} else if rule == "recipe_runs.frozen" || rule == "recipe_steps.frozen" || summary.RecipeRun != nil || summary.RecipeStep != nil {
		return ErrInvalidInventory
	} else {
		return nil
	}
	expected, err := RecipeEvidence(table, id, run, raw, financialDigest(raw))
	a, _ := json.Marshal(expected)
	b, _ := json.Marshal(summary)
	if err != nil || string(a) != string(b) {
		return ErrInvalidInventory
	}
	return nil
}

// Frozen payloads do not contain their SQL owner ID. Bind that separately on
// both initial erasure and replay, rather than trusting the supplied summary.
func validateRecipeProjectionOwner(row SourceRow, proof ExpiredProjection) error {
	if binding := proof.Summary.RecipeRun; binding != nil {
		if row.Table != "recipe_runs" || binding.ID != row.Values["id"] || proof.OriginalPayloadHash != row.Values["frozen_hash"] {
			return ErrInvalidInventory
		}
	}
	if binding := proof.Summary.RecipeStep; binding != nil {
		if row.Table != "recipe_steps" || binding.ID != row.Values["id"] || binding.RunID != row.Values["run_id"] || binding.KeyHash != LabelIdentityHash(row.Values["step_key"]) || proof.OriginalPayloadHash != row.Values["frozen_hash"] {
			return ErrInvalidInventory
		}
	}
	return nil
}
