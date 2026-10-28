package backup

import (
	"encoding/json"
	"fmt"

	"novelstudio/internal/domain"
	"novelstudio/internal/recipe"
	"novelstudio/internal/retention"
	"novelstudio/internal/service"
)

// These views are validation-only. Retired definitions supply independently
// proven relation hashes and closed roles, never executable replacement schemas,
// names, prompts or erased inputs. Their hashes remain the original identities.
func archiveRecipeCapability(index map[string]map[string]map[string]string, id, hash string, definitions map[string]retention.ExpiredProjection) (domain.CapabilityVersion, error) {
	row := index["capability_versions"][id]
	v := domain.CapabilityVersion{ID: id, Hash: hash, ParentID: row["parent_id"]}
	if proof, ok := definitions[id]; ok {
		source, err := retention.ParseSourceProjection([]byte(row["definition"]), "capability_versions.definition")
		s := proof.Summary.CapabilityContent
		if err != nil || s == nil || row["content_hash"] != hash || proof.Summary.InputHash != hash || !recipeJSONEqual(marshalRecipeProof(source), marshalRecipeProof(proof)) {
			return v, errExpiredArchiveEvidence
		}
		v.Definition.Kind = row["kind"]
		if row["kind"] == "agent" && s.AgentRole != "" {
			v.Definition.Role = s.AgentRole
			return v, nil
		}
		if row["kind"] != "recipe" || len(s.RecipeSteps) == 0 {
			return v, errExpiredArchiveEvidence
		}
		for _, step := range s.RecipeSteps {
			v.Definition.Steps = append(v.Definition.Steps, domain.RecipeStep{Key: step.KeyHash, AgentID: step.AgentID, After: step.AfterHashes, WaitForConfirmation: step.WaitForConfirmation})
		}
		return v, nil
	}
	if row["content_hash"] != hash || strictJSON([]byte(row["definition"]), &v.Definition) != nil || v.Definition.Validate() != nil {
		return v, errExpiredArchiveEvidence
	}
	return v, nil
}

func archivedRecipeRun(row map[string]string, proof retention.ExpiredProjection, index map[string]map[string]map[string]string, definitions map[string]retention.ExpiredProjection) (service.RecipeFrozen, error) {
	var f service.RecipeFrozen
	s := proof.Summary.RecipeRun
	source, err := retention.ParseSourceProjection([]byte(row["frozen"]), "recipe_runs.frozen")
	if err != nil || s == nil || s.ID != row["id"] || proof.OriginalPayloadHash != row["frozen_hash"] || !recipeJSONEqual(marshalRecipeProof(source), marshalRecipeProof(proof)) {
		return f, errExpiredArchiveEvidence
	}
	f.Definition, err = archiveRecipeCapability(index, s.DefinitionID, s.DefinitionHash, definitions)
	if err != nil {
		return f, err
	}
	f.Protocol, f.ManifestHash, f.BaseHead, f.Trial = 1, s.ManifestHash, s.BaseHead, s.Trial
	return f, nil
}

func marshalRecipeProof(p retention.ExpiredProjection) []byte { raw, _ := json.Marshal(p); return raw }

func archivedRecipeStep(row map[string]string, proof retention.ExpiredProjection, root service.RecipeFrozen, ordinal int, index map[string]map[string]map[string]string, definitions map[string]retention.ExpiredProjection) (service.RecipeStepFrozen, error) {
	var f service.RecipeStepFrozen
	s := proof.Summary.RecipeStep
	source, err := retention.ParseSourceProjection([]byte(row["frozen"]), "recipe_steps.frozen")
	if err != nil || s == nil || s.ID != row["id"] || s.RunID != row["run_id"] || s.KeyHash != retention.LabelIdentityHash(row["step_key"]) || proof.OriginalPayloadHash != row["frozen_hash"] || !recipeJSONEqual(marshalRecipeProof(source), marshalRecipeProof(proof)) {
		return f, errExpiredArchiveEvidence
	}
	f.Step = root.Definition.Definition.Steps[ordinal]
	raw, _ := json.Marshal(f.Step)
	wantHash := recipeJSONDigest(raw)
	if proof, ok := definitions[root.Definition.ID]; ok {
		wantHash = proof.Summary.CapabilityContent.RecipeSteps[ordinal].StepHash
	}
	if wantHash != s.StepHash {
		return f, errExpiredArchiveEvidence
	}
	f.Agent, err = archiveRecipeCapability(index, s.AgentID, s.AgentHash, definitions)
	if err != nil {
		return f, err
	}
	config := index["model_connection_versions"][fmt.Sprintf("%s:%d", s.ConfigID, s.ConfigRevision)]
	price := index["model_prices"][s.PriceID]
	if strictJSON([]byte(config["config"]), &f.Config) != nil || strictJSON([]byte(price["payload"]), &f.Price) != nil {
		return f, errExpiredArchiveEvidence
	}
	configRaw, _ := json.Marshal(f.Config)
	priceRaw, _ := json.Marshal(f.Price)
	if recipeJSONDigest(configRaw) != s.ConfigHash || recipeJSONDigest(priceRaw) != s.PriceHash {
		return f, errExpiredArchiveEvidence
	}
	f.ManifestHash = s.ManifestHash
	f.Input = recipe.RecipeStepInput{Key: f.Step.Key, PriceID: s.PriceID, MaxOutputTokens: s.MaxOutputTokens, ContextID: s.ContextID}
	if len(s.Sources) > 0 && len(s.Sources) != len(f.Step.After) {
		return f, errExpiredArchiveEvidence
	}
	for i, pin := range s.Sources {
		f.Sources = append(f.Sources, recipe.Source{Key: f.Step.After[i], StepID: pin.StepID, OutputHash: pin.OutputHash})
	}
	return f, nil
}

// A retired recipe layout already carries one-way key identities. Never hash
// those a second time or turn them back into executable names.
func archiveRecipeKeyHash(retired bool, key string) string {
	if retired {
		return key
	}
	return digest([]byte(key))
}
