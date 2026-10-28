package retention

import "context"

// Capture the immutable target actually reviewed. Current resource heads and
// later candidate revisions cannot replace the saved review's evidence.
func (index *SourceIndex) semanticCaptureKeys(ctx context.Context, project string, row SourceRow, read ReadSourceObject) ([]SourceKey, error) {
	if row.Table == "semantic_review_acceptances" {
		return []SourceKey{{Table: "semantic_review_runs", Primary: map[string]string{"id": row.Values["review_id"]}}}, nil
	}
	var keys []SourceKey
	var material MaterialReference
	b := contextSourceBuilder{index: index, project: project, branch: row.Values["branch_id"], actor: row.Values["actor_id"], m: &material}
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
	run, frozen, err := b.semanticRun(row.Values["id"])
	if err != nil {
		return nil, err
	}
	if _, err := b.semanticDocument(ctx, run, frozen, read); err != nil {
		return nil, err
	}
	// Prepared reviews have no child yet. Only a report consumes the completed
	// child and its actual receipt; observation at child creation records its age.
	if run.Values["report_hash"] != "" {
		keys = append(keys, SourceKey{Table: "generation_tasks", Primary: map[string]string{"id": run.Values["generation_id"]}})
	}
	return keys, nil
}
