package retention

import (
	"slices"
	"strings"
)

// Schema-31 scan coverage. Even tables with no object reference must be covered:
// they can hold inline content or determine roots, permissions, and accounting.
// This independent list is checked against the real archive catalog in tests.
func KnownTables() []string {
	return strings.Fields(`workspaces users sessions projects project_members operations audit_events
branches ip_revisions adoptions outbox ip_drafts ip_merge_candidates resources artifact_releases resource_drafts ip_amendments
model_connections model_connection_versions model_bindings model_budgets model_calls ip_promotions platform_tasks
knowledge_indexes knowledge_chunks context_packages source_imports source_import_candidates model_prices generation_tasks generation_previews
resource_policy_drafts skill_packages skill_revocations model_call_accounting model_call_receipts model_reconciliations change_plans change_plan_releases
index_jobs working_indexes working_chunks capability_versions capability_heads capability_revocations capability_preset_steps
skill_discovery_sources skill_discovery_runs skill_discovery_candidates tool_trials qualification_runs qualification_steps capability_qualifications
capability_bindings capability_binding_heads qualification_approvals qualification_schedules qualification_week_grants qualification_calls
ip_branch_deltas semantic_review_runs semantic_review_acceptances patch_drafts ingest_jobs import_drafts recipe_runs recipe_steps memory_sessions memory_items memory_revisions`)
}

// The legacy table set remains frozen for schema36 archives and cleanup plans.
// Later tables are never silently ignored when reading an older database.
func KnownTablesForSchema(schema int) []string {
	tables := KnownTables()
	if schema >= 37 {
		tables = append(tables, "novel_setup_drafts", "novels", "novel_settings_versions")
	}
	if schema == 38 {
		tables = append(tables, "novel_settings_drafts")
	}
	return tables
}

// Rules classify actual references, not every column ending in _hash.
// Raw requests/reports may never acquire ContentRoot through publication.
func Rules() []ReferenceRule { return slices.Clone(referenceRules) }

var referenceRules = buildReferenceRules()

func buildReferenceRules() []ReferenceRule {
	var rules []ReferenceRule
	add := func(storage StorageKind, class Class, promotable bool, keys string) {
		for _, key := range strings.Fields(keys) {
			rules = append(rules, ReferenceRule{key, storage, class, promotable})
		}
	}
	add(Blob, Candidate, true, `ip_revisions.content_hash resources.content_hash artifact_releases.content_hash
artifact_releases.manifest.artifacts.hash artifact_releases.manifest.artifacts.original_hash
source_imports.source_hash ingest_jobs.source_hash skill_packages.bundle_hash skill_packages.source.archive_hash knowledge_chunks.source_hash`)
	add(Blob, RawRun, false, `generation_tasks.frozen.input_hash tool_trials.frozen.input_hash tool_trials.output_hash
model_calls.output_hash model_call_receipts.payload.output_hash platform_tasks.input_hash qualification_runs.report_hash
capability_qualifications.report_hash semantic_review_runs.frozen.input_hash semantic_review_runs.report_hash semantic_review_acceptances.report_hash recipe_steps.input_hash recipe_steps.output_hash`)
	add(Inline, RawRun, false, `generation_tasks.frozen generation_previews.content qualification_steps.metrics qualification_runs.frozen
capability_qualifications.payload qualification_approvals.payload tool_trials.frozen semantic_review_runs.frozen
context_packages.payload model_reconciliations.payload model_call_receipts.payload working_chunks.content working_chunks.vector working_indexes.manifest ingest_jobs.progress recipe_runs.frozen recipe_steps.frozen memory_sessions.document`)
	add(Inline, Content, false, `source_imports.filename ingest_jobs.filename knowledge_chunks.resource_name`)
	add(Inline, RawRun, false, `working_chunks.source_name`)
	add(Inline, Candidate, true, `novel_settings_drafts.payload novel_setup_drafts.payload novel_settings_versions.payload ip_revisions.payload ip_drafts.payload patch_drafts.payload import_drafts.payload resource_drafts.body resource_policy_drafts.payload
artifact_releases.manifest ip_promotions.payload ip_branch_deltas.payload change_plans.payload
source_imports.report knowledge_indexes.manifest knowledge_chunks.content knowledge_chunks.vector
capability_versions.definition skill_packages.report skill_packages.source memory_items.document memory_revisions.document`)
	add(Inline, Debug, false, `platform_tasks.reason index_jobs.reason skill_discovery_runs.reason skill_discovery_runs.inventory
tool_trials.reason qualification_runs.reason semantic_review_runs.reason recipe_runs.reason`)
	add(Metadata, Audit, false, `audit_events.metadata model_calls.accounting model_call_accounting.metadata model_call_receipts.metadata
model_reconciliations.accounting qualification_calls.accounting qualification_week_grants.accounting`)
	// The independently reviewed physical-column catalog covers private scalars
	// and keys too. Classification does not install a materializer: unique keys
	// still require their dedicated anonymization and replay-proof handlers.
	known := map[string]bool{}
	for _, rule := range rules {
		known[rule.Key] = true
	}
	for _, spec := range reviewedColumnContracts {
		if spec.Scope == ScopeWorkspace {
			continue
		}
		for _, column := range spec.Columns {
			key := spec.Table + "." + column.Column
			if !known[key] && (column.Privacy == PrivateBody || column.Privacy == PrivateText || column.Privacy == PrivateKey) {
				rules = append(rules, ReferenceRule{Key: key, Storage: Inline, Class: column.Class})
				known[key] = true
			}
		}
	}
	return rules
}

func lookupRule(key string) (ReferenceRule, bool) {
	parts := strings.Split(key, ".")
	if len(parts) == 5 && parts[0] == "artifact_releases" && parts[1] == "manifest" && parts[2] == "artifacts" && id32.MatchString(parts[3]) && (parts[4] == "hash" || parts[4] == "original_hash") {
		return ReferenceRule{Key: key, Storage: Blob, Class: Candidate, Promotable: true}, true
	}
	for _, rule := range referenceRules {
		if rule.Key == key {
			return rule, true
		}
	}
	return ReferenceRule{}, false
}

// ConcreteRule refuses array templates; a stored reference always identifies
// one immutable artifact resource ID, never an ordinal or the whole array.
func ConcreteRule(key string) (ReferenceRule, bool) {
	if key == "artifact_releases.manifest.artifacts.hash" || key == "artifact_releases.manifest.artifacts.original_hash" {
		return ReferenceRule{}, false
	}
	return lookupRule(key)
}

func ArtifactField(resourceID string, original bool) (string, error) {
	if !id32.MatchString(resourceID) {
		return "", ErrInvalidInventory
	}
	suffix := "hash"
	if original {
		suffix = "original_hash"
	}
	return "manifest.artifacts." + resourceID + "." + suffix, nil
}
