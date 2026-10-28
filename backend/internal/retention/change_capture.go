package retention

import (
	"context"

	"novelstudio/internal/domain"
)

func (index *SourceIndex) changeCaptureKeys(ctx context.Context, project string, row SourceRow, read ReadSourceObject) ([]SourceKey, error) {
	var plan domain.ChangePlan
	if contentJSON([]byte(row.Values["payload"]), &plan, row.Values["content_hash"]) != nil || plan.ID != row.Values["id"] || plan.BranchID != row.Values["branch_id"] {
		return nil, ErrSourceOwnership
	}
	var keys []SourceKey
	var material MaterialReference
	b := contextSourceBuilder{index: index, project: project, branch: plan.BranchID, m: &material}
	b.capture = func(source SourceRow) error {
		spec, ok := OwnerSpec(source.Table)
		if !ok || len(keys) >= 4096 {
			return ErrSourceOwnership
		}
		primary := make(map[string]string, len(spec.PrimaryKey))
		for _, key := range spec.PrimaryKey {
			primary[key.Name] = source.Values[key.Name]
		}
		keys = append(keys, SourceKey{Table: source.Table, Primary: primary})
		return nil
	}
	graph := changeSourceGraph{b: &b, read: read, nodes: map[domain.DependencyRef]changeNode{}, values: map[string]map[string]any{}}
	if err := graph.plan(ctx, plan); err != nil {
		return nil, err
	}
	return keys, nil
}
