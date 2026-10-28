package retention

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"novelstudio/internal/domain"
	"novelstudio/internal/models"
)

// A declared context ID alone proves no copy. The immutable request must have
// the exact canonical context suffix written by Generations.Prepare. This reads
// local bytes only, without reconstructing prompts or invoking any provider.
func (index *SourceIndex) attachGenerationContextSources(ctx context.Context, project string, materials []ObservedMaterial, read ReadSourceObject) error {
	for i := range materials {
		m := &materials[i]
		if m.Table != "generation_tasks" || (m.Field != "frozen" && m.Field != "frozen.input_hash") {
			continue
		}
		row := index.rows[m.Table][m.OwnerKey]
		hash, err := materialFrozenInput(row.Table, row.Values["frozen"])
		if err != nil {
			return err
		}
		var frozen struct {
			Creative         *models.CreativeSource     `json:"creative"`
			Image            *models.CharacterImageTask `json:"image"`
			NovelChapter     *domain.NovelChapterSource `json:"novel_chapter"`
			Capability       *domain.CapabilityUse      `json:"capability"`
			ContextID        string                     `json:"context_id"`
			BaseHead         string                     `json:"base_head"`
			RecipeRunID      string                     `json:"recipe_run_id"`
			RecipeStepID     string                     `json:"recipe_step_id"`
			RecipeInputHash  string                     `json:"recipe_input_hash"`
			SemanticReviewID string                     `json:"semantic_review_id"`
			ChangePlanID     string                     `json:"change_plan_id"`
			Role             string                     `json:"role"`
		}
		if json.Unmarshal([]byte(row.Values["frozen"]), &frozen) != nil {
			return ErrSourceOwnership
		}
		b := contextSourceBuilder{index: index, project: project, branch: row.Values["branch_id"], actor: row.Values["actor_id"], m: &m.MaterialReference}
		if m.Field == "frozen" {
			if err := b.link(row, "frozen.input_hash", financialDigest([]byte(hash))); err != nil {
				return err
			}
			continue
		}
		data, err := read(ctx, project, hash)
		var request models.Request
		if err != nil || len(data) > 1<<20 || contentJSON(data, &request, hash) != nil {
			return ErrSourceOwnership
		}
		if (request.Image == nil) != (frozen.Image == nil) {
			return ErrSourceOwnership
		}
		if (request.Creative == nil) != (frozen.Creative == nil) {
			return ErrSourceOwnership
		}
		if request.Creative != nil {
			left, _ := json.Marshal(request.Creative)
			right, _ := json.Marshal(frozen.Creative)
			if string(left) != string(right) {
				return ErrSourceOwnership
			}
			var references []models.ImageReference
			switch request.Creative.Kind {
			case "character_visual", "novel_visual":
				if request.Creative.Visual == nil || frozen.Role != "asset_designer" {
					return ErrSourceOwnership
				}
				references = request.Creative.Visual.References
				if (request.Creative.Kind == "novel_visual") != (request.Creative.Visual.Novel != nil) {
					return ErrSourceOwnership
				}
				if err := b.novelVisual(ctx, request.Creative.Visual, read); err != nil {
					return fmt.Errorf("novel visual basis: %w", err)
				}
				if request.Creative.Kind == "novel_visual" {
					if frozen.Capability == nil {
						return ErrSourceOwnership
					}
					open, close, _ := models.CapabilityMethodFrame(frozen.Capability.Mode == "TRIAL")
					start := strings.LastIndex(request.Prompt, open)
					if start < 0 {
						return ErrSourceOwnership
					}
					raw := request.Prompt[start+len(open):]
					end := strings.Index(raw, close)
					var payload capabilityPromptData
					if end < 0 || json.Unmarshal([]byte(raw[:end]), &payload) != nil {
						return ErrSourceOwnership
					}
					var input struct {
						Novel *models.NovelImageTarget `json:"novel"`
						Basis json.RawMessage          `json:"basis"`
					}
					if json.Unmarshal(payload.Input, &input) != nil || stableHash(input.Novel) != stableHash(request.Creative.Visual.Novel) || !novelVisualBasisEqual(input.Basis, []byte(request.Creative.Visual.Basis)) {
						return fmt.Errorf("novel visual prompt basis: %w", ErrSourceOwnership)
					}
				}
			case "novel_setup":
				if frozen.Role != "architect" || !id32.MatchString(request.Creative.SetupID) || !hash64.MatchString(request.Creative.Hash) || request.Creative.Revision < 1 || frozen.ContextID == "" {
					return ErrSourceOwnership
				}
			default:
				return ErrSourceOwnership
			}
			for _, ref := range references {
				source, err := index.sourceRow("source_imports", map[string]string{"id": ref.ID})
				if err != nil || source.Values["project_id"] != project || source.Values["branch_id"] != b.branch || source.Values["source_hash"] != ref.Hash {
					return ErrSourceOwnership
				}
				if err = b.link(source, "report", ""); err != nil {
					return err
				}
				if err = b.link(source, "source_hash", financialDigest([]byte(ref.Hash))); err != nil {
					return err
				}
			}
		}
		if request.Image != nil {
			left, _ := json.Marshal(request.Image)
			right, _ := json.Marshal(frozen.Image)
			if string(left) != string(right) || frozen.Role != "image_generator" {
				return ErrSourceOwnership
			}
			if err := b.novelVisual(ctx, request.Image, read); err != nil {
				return err
			}
			if request.Image.Novel != nil && !strings.Contains(request.Prompt, "\n"+request.Image.Basis+"\n") {
				return ErrSourceOwnership
			}
			if id := request.Image.VisualGenerationID; id != "" {
				origin, err := index.sourceRow("generation_tasks", map[string]string{"id": id})
				if err != nil || origin.Values["project_id"] != project || origin.Values["branch_id"] != b.branch || origin.Values["actor_id"] != b.actor {
					return ErrSourceOwnership
				}
				call, err := index.sourceRow("model_calls", map[string]string{"id": origin.Values["call_id"]})
				if err != nil || call.Values["output_hash"] == "" {
					return ErrSourceOwnership
				}
				if err = b.link(call, "output_hash", financialDigest([]byte(call.Values["output_hash"]))); err != nil {
					return err
				}
			}
			for _, ref := range request.Image.References {
				source, err := index.sourceRow("source_imports", map[string]string{"id": ref.ID})
				if err != nil || source.Values["project_id"] != project || source.Values["branch_id"] != b.branch || source.Values["source_hash"] != ref.Hash {
					return ErrSourceOwnership
				}
				if err = b.link(source, "source_hash", financialDigest([]byte(ref.Hash))); err != nil {
					return err
				}
				if err = b.link(source, "report", ""); err != nil {
					return err
				}
			}
		}
		methodInputHash := frozen.RecipeInputHash
		if frozen.SemanticReviewID != "" {
			if methodInputHash != "" || frozen.RecipeRunID != "" || frozen.RecipeStepID != "" {
				return ErrSourceOwnership
			}
			methodInputHash, err = b.semanticGeneration(row, frozen.SemanticReviewID)
			if err != nil {
				return err
			}
		}
		if err := b.capabilityPrompt(ctx, frozen.Capability, request, methodInputHash, read); err != nil {
			return fmt.Errorf("generation method source: %w", err)
		}
		if err := b.qualificationUse(ctx, row, frozen.Capability, request, hash, read); err != nil {
			return fmt.Errorf("generation qualification source: %w", err)
		}
		if err := b.novelChapterPrompt(ctx, frozen.NovelChapter, frozen.Role, frozen.Capability, request, read); err != nil {
			return fmt.Errorf("generation chapter source: %w", err)
		}
		if err := b.changePrompt(ctx, frozen.ChangePlanID, frozen.Role, frozen.BaseHead, request, read); err != nil {
			return fmt.Errorf("generation change source: %w", err)
		}
		if frozen.RecipeRunID != "" || frozen.RecipeStepID != "" || frozen.RecipeInputHash != "" {
			if err := b.recipeChildInput(row, frozen.RecipeRunID, frozen.RecipeStepID, frozen.RecipeInputHash, frozen.BaseHead, frozen.ContextID); err != nil {
				return err
			}
		}
		if frozen.ContextID == "" {
			continue
		}
		if !id32.MatchString(frozen.ContextID) {
			return ErrSourceOwnership
		}
		source, err := index.sourceRow("context_packages", map[string]string{"id": frozen.ContextID})
		var p domain.ContextPackage
		if err != nil || source.Values["project_id"] != project || source.Values["branch_id"] != b.branch || source.Values["actor_id"] != b.actor || contextJSON([]byte(source.Values["payload"]), &p, source.Values["content_hash"]) != nil || p.ID != frozen.ContextID || p.IPHead != frozen.BaseHead {
			return ErrSourceOwnership
		}
		canonical, err := json.Marshal(p)
		if err != nil || !strings.HasSuffix(request.Prompt, "\n\n<reference-data>\n"+string(canonical)+"\n</reference-data>") {
			return ErrSourceOwnership
		}
		if err := b.link(source, "payload", ""); err != nil {
			return err
		}
	}
	return nil
}
