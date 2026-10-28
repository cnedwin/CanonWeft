package retention

import (
	"context"
	"database/sql"
	"encoding/json"

	"novelstudio/internal/domain"
	"novelstudio/internal/recipe"
)

func (index *SourceIndex) recipeCaptureKeys(ctx context.Context, tx *sql.Tx, row SourceRow) ([]SourceKey, error) {
	var keys []SourceKey
	add := func(table, id string) {
		if id != "" {
			keys = append(keys, SourceKey{Table: table, Primary: map[string]string{"id": id}})
		}
	}
	if row.Table == "generation_tasks" {
		var frozen struct {
			Capability   *domain.CapabilityUse `json:"capability"`
			ContextID    string                `json:"context_id"`
			ChangePlanID string                `json:"change_plan_id"`
		}
		if json.Unmarshal([]byte(row.Values["frozen"]), &frozen) != nil {
			return nil, ErrSourceOwnership
		}
		if frozen.Capability != nil {
			add("capability_versions", frozen.Capability.AgentVersionID)
		}
		add("context_packages", frozen.ContextID)
		add("change_plans", frozen.ChangePlanID)
		add("model_calls", row.Values["call_id"])
		return keys, nil
	}
	if row.Table == "recipe_steps" {
		var frozen recipe.RecipeStepFrozen
		if recipeFrozenDocument(row, &frozen) != nil {
			return nil, ErrSourceOwnership
		}
		add("recipe_runs", row.Values["run_id"])
		add("capability_versions", frozen.Agent.ID)
		add("context_packages", frozen.Input.ContextID)
		for _, source := range frozen.Sources {
			add("recipe_steps", source.StepID)
		}
		if row.Values["output_hash"] != "" {
			add("generation_tasks", row.Values["generation_id"])
		}
		return keys, nil
	}
	var frozen recipe.RecipeFrozen
	if recipeFrozenDocument(row, &frozen) != nil {
		return nil, ErrSourceOwnership
	}
	add("capability_versions", frozen.Definition.ID)
	add("ip_revisions", frozen.BaseHead)
	// Inactive attempts are loaded only when explicitly named by an original
	// predecessor pin. A retry must not inherit all previous attempts' inputs.
	rows, err := tx.QueryContext(ctx, "SELECT id FROM recipe_steps WHERE run_id=? AND active=TRUE ORDER BY ordinal LIMIT 33 FOR SHARE", row.Values["id"])
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		count++
		if count > 32 {
			return nil, ErrSourceOwnership
		}
		add("recipe_steps", id)
	}
	if err := rows.Err(); err != nil || count != len(frozen.Definition.Definition.Steps) {
		return nil, ErrSourceOwnership
	}
	return keys, nil
}
