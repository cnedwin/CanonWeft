package retention

import "strconv"

type KeyKind string

const (
	KeyID      KeyKind = "ID32"
	KeyHash    KeyKind = "HASH64"
	KeyInteger KeyKind = "INTEGER"
	KeyText    KeyKind = "TEXT"
)

type KeyColumn struct {
	Name     string
	FreeText bool
	Kind     KeyKind
}

// PrimaryKey returns the fixed schema PK order and encoding types. A nil result
// is unclassified and must prevent destructive planning. FreeText is determined
// by the column, never by whether a particular value happens to look like a hash.
func PrimaryKey(table string) []KeyColumn {
	col := func(name string, kind KeyKind) KeyColumn {
		return KeyColumn{Name: name, Kind: kind, FreeText: kind == KeyText}
	}
	switch table {
	case "workspaces", "users", "projects", "branches", "ip_revisions", "outbox", "resources", "artifact_releases", "model_connections", "model_calls", "platform_tasks", "context_packages", "source_imports", "model_prices", "generation_tasks", "skill_packages", "model_reconciliations", "change_plans", "index_jobs", "capability_versions", "skill_discovery_sources", "skill_discovery_runs", "skill_discovery_candidates", "tool_trials", "qualification_runs", "qualification_steps", "capability_qualifications", "capability_bindings", "qualification_schedules", "qualification_week_grants", "semantic_review_runs", "semantic_review_acceptances":
		return []KeyColumn{col("id", KeyID)}
	case "knowledge_indexes", "knowledge_chunks", "model_call_receipts", "working_indexes", "working_chunks":
		return []KeyColumn{col("id", KeyHash)}
	case "novel_setup_drafts", "novels":
		return []KeyColumn{col("id", KeyID)}
	case "novel_settings_versions":
		return []KeyColumn{col("novel_id", KeyID), col("revision", KeyInteger)}
	case "novel_settings_drafts":
		return []KeyColumn{col("novel_id", KeyID), col("actor_id", KeyID)}
	case "ingest_jobs", "recipe_runs", "recipe_steps", "memory_items", "memory_revisions", "memory_sessions":
		return []KeyColumn{col("id", KeyID)}
	case "import_drafts":
		return []KeyColumn{col("import_id", KeyID), col("actor_id", KeyID)}
	case "sessions":
		return []KeyColumn{col("token_hash", KeyHash)}
	case "project_members":
		return []KeyColumn{col("project_id", KeyID), col("user_id", KeyID)}
	case "operations":
		return []KeyColumn{col("actor_id", KeyID), col("operation_id", KeyText)}
	case "audit_events":
		return []KeyColumn{col("id", KeyInteger)}
	case "adoptions", "ip_merge_candidates", "ip_amendments", "ip_promotions", "ip_branch_deltas":
		return []KeyColumn{col("candidate_id", KeyID)}
	case "ip_drafts", "patch_drafts":
		return []KeyColumn{col("branch_id", KeyID), col("actor_id", KeyID)}
	case "resource_drafts", "resource_policy_drafts":
		return []KeyColumn{col("resource_id", KeyID), col("actor_id", KeyID)}
	case "model_connection_versions":
		return []KeyColumn{col("connection_id", KeyID), col("revision", KeyInteger)}
	case "model_bindings":
		return []KeyColumn{col("workspace_id", KeyID), col("role", KeyText)}
	case "model_budgets":
		return []KeyColumn{col("workspace_id", KeyID)}
	case "source_import_candidates":
		return []KeyColumn{col("import_id", KeyID), col("release_id", KeyID)}
	case "generation_previews":
		return []KeyColumn{col("generation_id", KeyID)}
	case "skill_revocations":
		return []KeyColumn{col("package_id", KeyID)}
	case "model_call_accounting", "qualification_calls":
		return []KeyColumn{col("call_id", KeyID)}
	case "change_plan_releases":
		return []KeyColumn{col("plan_id", KeyID)}
	case "capability_heads":
		return []KeyColumn{col("project_id", KeyID), col("capability_key", KeyText)}
	case "capability_revocations":
		return []KeyColumn{col("version_id", KeyID)}
	case "capability_preset_steps":
		return []KeyColumn{col("project_id", KeyID), col("step_key", KeyText)}
	case "capability_binding_heads":
		return []KeyColumn{col("project_id", KeyID), col("task_kind", KeyText)}
	case "qualification_approvals":
		return []KeyColumn{col("run_id", KeyID)}
	}
	return nil
}

// RowOwnerKey reads the original row's exact PK columns. Non-PK columns are
// ignored. Its result is shared by scanner and archive proof validation.
func RowOwnerKey(table string, row map[string]string) (string, error) {
	columns := PrimaryKey(table)
	if len(columns) == 0 {
		return "", ErrInvalidInventory
	}
	parts := make([]string, 0, len(columns))
	for _, column := range columns {
		value, ok := row[column.Name]
		if !ok {
			return "", ErrInvalidInventory
		}
		switch column.Kind {
		case KeyID:
			if !id32.MatchString(value) {
				return "", ErrInvalidInventory
			}
		case KeyHash:
			if !hash64.MatchString(value) {
				return "", ErrInvalidInventory
			}
		case KeyInteger:
			number, err := strconv.ParseInt(value, 10, 64)
			if err != nil || number < 0 || strconv.FormatInt(number, 10) != value {
				return "", ErrInvalidInventory
			}
		case KeyText:
			if table == "capability_heads" && column.Name == "capability_key" {
				value = "sha256:" + LabelIdentityHash(value)
			} else {
				value = DigestKeyPart(value)
			}
		default:
			return "", ErrInvalidInventory
		}
		parts = append(parts, value)
	}
	return OwnerKey(parts...)
}
