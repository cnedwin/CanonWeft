package backup

import (
	"fmt"
	"novelstudio/internal/retention"
	"slices"
)

// Export and import use the same source/metadata/ownership checks. This only
// selects required immutable files; it grants no restore or cleanup authority.
func validatedRetentionReferences(a *Archive, business, metadata Data) (map[string]bool, error) {
	if err := validateArchiveRecoveryAttempts(business, metadata, a.Manifest.SnapshotAt); err != nil {
		return nil, err
	}
	history, err := readArchiveReferenceHistory(metadata, a.Manifest.SnapshotAt)
	if err != nil {
		return nil, err
	}
	if err := validateArchiveSourceHistory(metadata, history, a.Manifest.SnapshotAt); err != nil {
		return nil, err
	}
	aliases, err := readArchiveRequestIdentities(business, metadata, a.Manifest.SnapshotAt)
	if err != nil {
		return nil, err
	}
	materials, err := archiveRetentionMaterials(business, history, aliases...)
	if err != nil {
		return nil, fmt.Errorf("archive retained materials: %w", err)
	}
	lifecycles, err := readArchiveLifecycles(business, metadata, a.Manifest)
	if err != nil {
		return nil, err
	}
	if err := validateArchiveLifecycleHistory(metadata, history, lifecycles); err != nil {
		return nil, err
	}
	deadlines, err := archiveEffectiveDeadlines(history, lifecycles)
	if err != nil {
		return nil, err
	}
	if err := validateArchiveObjects(business, metadata, materials, a.Manifest.SnapshotAt); err != nil {
		return nil, err
	}
	if err := validateArchiveCleanupJobs(business, metadata, history, lifecycles, deadlines, a.Manifest.SnapshotAt); err != nil {
		return nil, err
	}
	generationEvidence, err := readArchiveGenerationEvidence(business, history)
	if err != nil {
		return nil, err
	}
	runEvidence, err := readArchiveFrozenEvidence(business, history, "qualification_runs")
	if err != nil {
		return nil, err
	}
	contextEvidence, err := readArchivePayloadEvidence(business, history, "context_packages", "payload")
	if err != nil {
		return nil, fmt.Errorf("archive context evidence: %w", err)
	}
	recipeRuns, err := readArchiveFrozenEvidence(business, history, "recipe_runs")
	if err != nil {
		return nil, err
	}
	recipeSteps, err := readArchiveFrozenEvidence(business, history, "recipe_steps")
	if err != nil {
		return nil, err
	}
	transients := map[string]map[string]retention.ExpiredProjection{}
	transients["source_imports.report"], err = readArchivePayloadEvidence(business, history, "source_imports", "report")
	if err != nil {
		return nil, err
	}
	transients["ip_branch_deltas.payload"], err = readArchivePayloadEvidence(business, history, "ip_branch_deltas", "payload")
	if err != nil {
		return nil, err
	}
	for table, field := range map[string]string{"capability_versions": "definition", "ip_drafts": "payload", "resource_drafts": "body", "resource_policy_drafts": "payload", "patch_drafts": "payload", "import_drafts": "payload", "memory_sessions": "document", "memory_items": "document", "memory_revisions": "document"} {
		transients[table+"."+field], err = readArchivePayloadEvidence(business, history, table, field)
		if err != nil {
			return nil, err
		}
	}
	for _, source := range []struct{ table, field string }{{"generation_previews", "content"}, {"ingest_jobs", "progress"}, {"skill_discovery_runs", "inventory"}, {"skill_discovery_runs", "source_snapshot"}, {"qualification_steps", "metrics"}, {"capability_qualifications", "payload"}, {"qualification_approvals", "payload"}, {"qualification_schedules", "config"}, {"working_indexes", "manifest"}, {"working_chunks", "content"}, {"working_chunks", "vector"}} {
		transients[source.table+"."+source.field], err = readArchivePayloadEvidence(business, history, source.table, source.field)
		if err != nil {
			return nil, err
		}
	}
	if err := validateDataWithEvidence(business, qualificationArchiveEvidence{transients: transients, runs: runEvidence, generations: generationEvidence, contexts: contextEvidence, recipeRuns: recipeRuns, recipeSteps: recipeSteps, aliases: aliases, historicalOutbox: slices.Equal(a.Manifest.Exclusions, retentionExclusions)}); err != nil {
		return nil, fmt.Errorf("archive business validation: %w", err)
	}
	return archiveRetainedBlobReferences(materials)
}
