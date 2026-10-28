package retention

import (
	"context"
	"database/sql"
	"strings"

	"novelstudio/internal/domain"
)

// Discover only the original rows named by this saved package. Reuse the same
// source validator as full registration; neither current heads nor arbitrary
// foreign-key traversal may substitute a source selected by the author.
func (index *SourceIndex) contextCaptureKeys(ctx context.Context, project string, row SourceRow, read ReadSourceObject) ([]SourceKey, error) {
	var pack domain.ContextPackage
	if contextJSON([]byte(row.Values["payload"]), &pack, row.Values["content_hash"]) != nil || pack.ID != row.Values["id"] || pack.IndexID != row.Values["index_id"] || !id32.MatchString(pack.ID) {
		return nil, ErrSourceOwnership
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
	if err := b.collect(ctx, pack, read); err != nil {
		return nil, err
	}
	return keys, nil
}

// Private-copy certification covers one selected private index in full, not
// the entire project's public index or unrelated contexts. The existing row,
// byte and reference caps also apply to these records.
func capturePrivateChunkKeys(ctx context.Context, tx *sql.Tx, indexID string) ([]SourceKey, error) {
	rows, err := tx.QueryContext(ctx, "SELECT id FROM working_chunks WHERE index_id=? ORDER BY id LIMIT 4097 FOR SHARE", indexID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var keys []SourceKey
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		if len(keys) >= 4096 {
			return nil, ErrSourceOwnership
		}
		keys = append(keys, SourceKey{Table: "working_chunks", Primary: map[string]string{"id": id}})
	}
	return keys, rows.Err()
}

func privateManifestCaptureKeys(project string, row SourceRow) ([]SourceKey, error) {
	var manifest domain.PrivateManifest
	if authorDocument(row.Values["manifest"], &manifest) != nil || stableHash(manifest) != row.Values["id"] || manifest.Project != project || manifest.Branch != row.Values["branch_id"] || manifest.Actor != row.Values["actor_id"] || len(manifest.Sources) > 4096 {
		return nil, ErrSourceOwnership
	}
	var keys []SourceKey
	seen := map[string]bool{}
	for _, pin := range manifest.Sources {
		var key SourceKey
		switch pin.Ref.Kind {
		case "resource_draft":
			key = SourceKey{Table: "resource_drafts", Primary: map[string]string{"resource_id": pin.Ref.ID, "actor_id": manifest.Actor}}
			if pin.Part == 1 {
				key.Table = "resource_policy_drafts"
			}
		case "ip_draft":
			key = SourceKey{Table: "ip_drafts", Primary: map[string]string{"branch_id": pin.Ref.ID, "actor_id": manifest.Actor}}
		case "ip_candidate":
			key = SourceKey{Table: "ip_revisions", Primary: map[string]string{"id": pin.Ref.ID}}
		case "release_candidate":
			key = SourceKey{Table: "artifact_releases", Primary: map[string]string{"id": pin.Ref.ID}}
		default:
			return nil, ErrSourceOwnership
		}
		owner, err := RowOwnerKey(key.Table, key.Primary)
		if err != nil {
			return nil, err
		}
		identity := key.Table + ":" + owner
		if !seen[identity] {
			seen[identity] = true
			keys = append(keys, key)
		}
	}
	return keys, nil
}

// Explicit consumer and receipt sources use the same field catalog as inventory.
// This is deliberately limited to selected rows, not all lazily loaded owners.
func (g *materialCollector) captureRow(row SourceRow) error {
	switch row.Table {
	case "generation_previews":
		if !validPreviewSource(row) {
			return ErrSourceOwnership
		}
	case "operations", "projects", "branches", "index_jobs", "qualification_schedules", "skill_discovery_sources", "skill_discovery_runs", "skill_discovery_candidates", "ingest_jobs", "source_imports":
		// Names and scalar diagnostic reasons need no object reads.
	case "platform_tasks", "tool_trials", "qualification_runs", "qualification_steps", "capability_qualifications", "qualification_approvals", "change_plans":
		// Original execution inputs, measurements and qualification reports.
	case "context_packages", "resources", "knowledge_indexes", "knowledge_chunks", "working_indexes", "working_chunks", "memory_items", "memory_revisions", "skill_packages", "capability_versions", "model_calls", "model_call_receipts", "model_reconciliations", "recipe_runs", "recipe_steps", "generation_tasks", "semantic_review_runs", "semantic_review_acceptances":
		// These are validated context inputs and raw call/receipt materials.
		// Other writers keep their dedicated projection/financial collectors.
	default:
		return nil
	}
	nested := false
	for _, rule := range referenceRules {
		if rule.Storage == Metadata || !strings.HasPrefix(rule.Key, row.Table+".") {
			continue
		}
		field := strings.TrimPrefix(rule.Key, row.Table+".")
		if g.index.replacedColumn(g.project, row, field) {
			continue
		}
		if strings.Contains(field, ".") {
			if !nested {
				if err := g.nested(row); err != nil {
					return err
				}
				nested = true
			}
			continue
		}
		value, exists := row.Values[field]
		if !exists {
			return ErrSourceOwnership
		}
		if row.Nulls[field] || (rule.Storage == Blob && value == "") {
			continue
		}
		if err := g.add(row, field, value); err != nil {
			return err
		}
	}
	return nil
}
