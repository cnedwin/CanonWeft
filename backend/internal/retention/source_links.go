package retention

// Each link is an explicit schema relationship. No *_id inference is allowed:
// some source IDs are polymorphic, composite, or intentionally workspace-wide.
type sourceLink struct {
	column, table string
	required      bool
}

var linkedSourceColumns = map[string][]sourceLink{
	"capability_heads":           {{"version_id", "capability_versions", true}},
	"capability_revocations":     {{"version_id", "capability_versions", true}},
	"skill_revocations":          {{"package_id", "skill_packages", true}},
	"skill_discovery_runs":       {{"source_id", "skill_discovery_sources", true}},
	"skill_discovery_candidates": {{"source_id", "skill_discovery_sources", true}, {"run_id", "skill_discovery_runs", true}, {"package_id", "skill_packages", true}, {"previous_id", "skill_packages", false}},
	"tool_trials":                {{"capability_id", "capability_versions", true}},
	"capability_bindings":        {{"qualification_id", "capability_qualifications", true}, {"previous_id", "capability_bindings", false}},
	"capability_binding_heads":   {{"binding_id", "capability_bindings", true}},
	"qualification_week_grants":  {{"schedule_id", "qualification_schedules", true}},
	"qualification_calls":        {{"step_id", "qualification_steps", true}, {"grant_id", "qualification_week_grants", false}},
}

func (index *SourceIndex) validateSourceLinks(row SourceRow, owner SourceOwner, path map[string]bool) error {
	for _, link := range linkedSourceColumns[row.Table] {
		id := row.Values[link.column]
		if id == "" && !link.required {
			continue
		}
		related, err := index.single(link.table, id, path)
		if err != nil || related != owner {
			return ErrSourceOwnership
		}
	}
	match := func(table, id, targetColumn, expected string) bool {
		target, err := index.sourceRow(table, map[string]string{"id": id})
		return err == nil && target.Values[targetColumn] == expected
	}
	switch row.Table {
	case "novel_setup_drafts", "novels", "novel_settings_versions", "novel_settings_drafts":
		return index.validateNovelLinks(row, owner)
	case "memory_items", "memory_revisions", "memory_sessions":
		return index.validateMemoryLinks(row, owner)
	case "branches", "ip_revisions":
		return index.validateRevisionPointers(row, owner)
	case "semantic_review_runs", "semantic_review_acceptances":
		return index.validateSemanticTarget(row, owner, path)
	case "capability_heads":
		target, err := index.sourceRow("capability_versions", map[string]string{"id": row.Values["version_id"]})
		if err != nil || LabelIdentityHash(target.Values["capability_key"]) != LabelIdentityHash(row.Values["capability_key"]) {
			return ErrSourceOwnership
		}
	case "capability_binding_heads":
		if !match("capability_bindings", row.Values["binding_id"], "task_kind", row.Values["task_kind"]) {
			return ErrSourceOwnership
		}
	case "skill_discovery_candidates":
		if !match("skill_discovery_runs", row.Values["run_id"], "source_id", row.Values["source_id"]) {
			return ErrSourceOwnership
		}
	case "qualification_calls":
		if !match("qualification_steps", row.Values["step_id"], "run_id", row.Values["run_id"]) {
			return ErrSourceOwnership
		}
	case "model_bindings", "model_prices":
		primary := map[string]string{"connection_id": row.Values["connection_id"], "revision": row.Values["connection_revision"]}
		key, err := RowOwnerKey("model_connection_versions", primary)
		if err != nil {
			return ErrSourceOwnership
		}
		if _, err := index.sourceRow("model_connection_versions", primary); err != nil {
			return ErrSourceOwnership
		}
		related, err := index.owner("model_connection_versions", key, path)
		if err != nil || !related.Shared || related.WorkspaceID != owner.WorkspaceID {
			return ErrSourceOwnership
		}
	}
	return nil
}
