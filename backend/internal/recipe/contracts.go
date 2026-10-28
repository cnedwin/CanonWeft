package recipe

import (
	"encoding/json"
	"novelstudio/internal/domain"
	"novelstudio/internal/models"
)

type RecipeStepInput struct {
	NovelChapter    *domain.NovelChapterSource  `json:"novel_chapter,omitempty"`
	NovelDetailID   string                      `json:"novel_detail_id,omitempty"`
	Key             string                      `json:"key"`
	PriceID         string                      `json:"price_id"`
	MaxOutputTokens int                         `json:"max_output_tokens"`
	ContextID       string                      `json:"context_id"`
	BindingID       string                      `json:"binding_id,omitempty"`
	TaskKind        string                      `json:"task_kind,omitempty"`
	Input           json.RawMessage             `json:"input"`
	InputText       string                      `json:"input_text,omitempty"`
	Bindings        []domain.RecipeValueBinding `json:"bindings"`
}

type RecipeFrozen struct {
	Protocol     int                      `json:"protocol"`
	Definition   domain.CapabilityVersion `json:"definition"`
	ManifestHash string                   `json:"manifest_hash"`
	BaseHead     string                   `json:"base_head"`
	Input        json.RawMessage          `json:"input"`
	Trial        bool                     `json:"trial"`
}

type RecipeStepFrozen struct {
	Step         domain.RecipeStep        `json:"step"`
	Input        RecipeStepInput          `json:"input"`
	ManifestHash string                   `json:"manifest_hash"`
	Agent        domain.CapabilityVersion `json:"agent"`
	Config       domain.ModelConfig       `json:"config"`
	Price        models.PriceCard         `json:"price"`
	Sources      []Source                 `json:"sources,omitempty"`
}

// Source identifies the exact predecessor attempt consumed at input capture.
// It is written by the scheduler, never accepted as user-provided routing.
type Source struct {
	Key        string `json:"key"`
	StepID     string `json:"step_id"`
	OutputHash string `json:"output_hash"`
}
