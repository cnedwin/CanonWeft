package retention

import (
	"slices"

	"novelstudio/internal/domain"
)

// GenerationBindingSummary retains execution relations needed after private
// frozen input is erased. IDs and hashes never restore an executable request.
// Model configuration, price source text, prompts and input bodies stay absent.
type GenerationBindingSummary struct {
	NovelChapter     *domain.NovelChapterSource `json:"novel_chapter,omitempty"`
	NovelDetail      *domain.DependencyRef      `json:"novel_detail,omitempty"`
	Schema           int                        `json:"schema"`
	RecipeRunID      string                     `json:"recipe_run_id,omitempty"`
	RecipeStepID     string                     `json:"recipe_step_id,omitempty"`
	RecipeInputHash  string                     `json:"recipe_input_hash,omitempty"`
	SemanticReviewID string                     `json:"semantic_review_id,omitempty"`
	ChangePlanID     string                     `json:"change_plan_id,omitempty"`
	BaseHead         string                     `json:"base_head,omitempty"`
	ContextID        string                     `json:"context_id,omitempty"`
	Role             string                     `json:"role,omitempty"`
	AgentVersionID   string                     `json:"agent_version_id,omitempty"`
	ManifestHash     string                     `json:"manifest_hash,omitempty"`
	PriceHash        string                     `json:"price_hash"`
}

func validGenerationBinding(binding GenerationBindingSummary) bool {
	if pin := binding.NovelChapter; pin != nil {
		if (binding.Role != "writer" && binding.Role != "bookkeeper") || !id32.MatchString(pin.BookID) || !id32.MatchString(pin.ResourceID) || pin.Revision < 1 || !hash64.MatchString(pin.Hash) || (pin.BaseHead != "" && !id32.MatchString(pin.BaseHead)) {
			return false
		}
	}
	if pin := binding.NovelDetail; pin != nil {
		if binding.Role != "writer" || pin.Kind != "resource" || !id32.MatchString(pin.ID) || !id32.MatchString(pin.BranchID) || !id32.MatchString(pin.Revision) || !hash64.MatchString(pin.Hash) {
			return false
		}
	}
	if binding.Schema != 1 || !hash64.MatchString(binding.PriceHash) || (binding.Role != "" && !slices.Contains(domain.ModelRoles(), binding.Role)) {
		return false
	}
	for _, id := range []string{binding.RecipeRunID, binding.RecipeStepID, binding.SemanticReviewID, binding.ChangePlanID, binding.BaseHead, binding.ContextID, binding.AgentVersionID} {
		if id != "" && !id32.MatchString(id) {
			return false
		}
	}
	for _, hash := range []string{binding.RecipeInputHash, binding.ManifestHash} {
		if hash != "" && !hash64.MatchString(hash) {
			return false
		}
	}
	if (binding.AgentVersionID == "") != (binding.ManifestHash == "") {
		return false
	}
	if binding.RecipeRunID != "" || binding.RecipeStepID != "" || binding.RecipeInputHash != "" {
		if binding.RecipeRunID == "" || binding.RecipeStepID == "" || binding.RecipeInputHash == "" || binding.Role == "" || binding.AgentVersionID == "" {
			return false
		}
	}
	return true
}
