package retention

import (
	"strings"
	"testing"
)

func TestExecutionObjectExpiryCatalog(t *testing.T) {
	for _, rule := range Rules() {
		parts := strings.Split(rule.Key, ".")
		if rule.Storage == Blob && rule.Class == RawRun && len(parts) == 2 {
			if !supportedExpiryField(parts[0], parts[1]) {
				t.Errorf("execution object has no expiry handler: %s", rule.Key)
			}
		}
	}
	for _, rule := range []string{"resources.content_hash", "knowledge_chunks.source_hash", "ingest_jobs.source_hash", "invented.output_hash"} {
		if DirectExecutionObjectRule(rule) {
			t.Errorf("author material or unknown field admitted as execution object: %s", rule)
		}
	}
	for _, key := range []string{"source_imports.source_hash", "ingest_jobs.source_hash"} {
		rule, ok := ConcreteRule(key)
		parts := strings.Split(key, ".")
		if !ok || !rule.Promotable || rule.Class != Candidate || !DirectImportObjectRule(key) || !supportedExpiryField(parts[0], parts[1]) {
			t.Fatal("import expiry lost candidate/content classification", key)
		}
	}
}

func TestTransientRunRecordsHaveExpiryHandlers(t *testing.T) {
	for _, key := range []string{"generation_previews.content", "ingest_jobs.progress", "skill_discovery_runs.inventory", "skill_discovery_runs.source_snapshot", "qualification_steps.metrics", "capability_qualifications.payload", "qualification_approvals.payload", "qualification_schedules.config", "working_indexes.manifest", "working_chunks.content", "working_chunks.vector"} {
		parts := strings.SplitN(key, ".", 2)
		if !supportedExpiryField(parts[0], parts[1]) {
			t.Errorf("transient execution material lacks handler: %s", key)
		}
	}
}

func TestSavedDraftsHaveExpiryHandlersWithoutLosingContentProtection(t *testing.T) {
	for _, table := range []string{"ip_drafts", "patch_drafts", "import_drafts", "resource_drafts", "resource_policy_drafts"} {
		field := draftField(table)
		rule, ok := ConcreteRule(table + "." + field)
		if !ok || !rule.Promotable || !supportedExpiryField(table, field) {
			t.Errorf("saved draft lifecycle missing: %s", table)
		}
	}
}

// Deliberately diagnostic: this is a bounded inventory of implementation gaps,
// never an assertion that all lifecycle handling has been completed.
func TestExpiryCatalogInventory(t *testing.T) {
	groups := map[string][]string{}
	for _, rule := range Rules() {
		if _, ok := ConcreteRule(rule.Key); !ok || rule.Storage == Metadata {
			continue
		}
		parts := strings.SplitN(rule.Key, ".", 2)
		if supportedExpiryField(parts[0], parts[1]) || requestKeyRule(rule.Key) {
			continue
		}
		groups[string(rule.Storage)] = append(groups[string(rule.Storage)], rule.Key)
	}
	for _, storage := range []string{string(Inline), string(Blob)} {
		t.Logf("%s pending (%d): %s", storage, len(groups[storage]), strings.Join(groups[storage], ", "))
	}
}
