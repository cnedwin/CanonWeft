package retention

import (
	"encoding/json"
	"strconv"
	"strings"
	"unicode/utf8"

	"novelstudio/internal/domain"
	"novelstudio/internal/memory"
	"novelstudio/internal/qualification"
	"novelstudio/internal/retrieval"
	"novelstudio/internal/skills"
)

// The closed identity contract deliberately excludes mutable execution flags.
// Its digest contains no recoverable preview, extracted text or remote paths.
func TransientColumns(key string) []string {
	switch key {
	case "outbox.payload":
		return strings.Fields("id project_id resource_id kind")
	case "branches.name":
		return strings.Fields("id project_id parent_id base_revision")
	case "recipe_steps.step_key":
		return strings.Fields("id project_id run_id ordinal attempt frozen_hash")
	case "capability_versions.capability_key":
		return strings.Fields("id project_id kind parent_id content_hash created_by")
	case "capability_heads.capability_key":
		return strings.Fields("project_id version_id")
	case "skill_discovery_candidates.source_path":
		return strings.Fields("id project_id source_id run_id source_commit package_id")
	case "capability_versions.definition":
		return strings.Fields("id project_id kind parent_id content_hash created_by")
	case "skill_packages.report", "skill_packages.source":
		return strings.Fields("id project_id parent_id upstream_id bundle_hash created_by")
	case "ip_revisions.payload":
		return strings.Fields("id branch_id base_head content_hash created_by")
	case "artifact_releases.manifest":
		return strings.Fields("id project_id branch_id content_hash created_by")
	case "generation_previews.content":
		return strings.Fields("generation_id sequence content_hash")
	case "ingest_jobs.progress":
		return strings.Fields("id project_id branch_id actor_id source_hash source_size parser_revision ocr_mode progress_hash revision")
	case "source_imports.report":
		return strings.Fields("id project_id branch_id actor_id source_hash source_size request_hash")
	case "ip_promotions.payload":
		return strings.Fields("candidate_id")
	case "ip_branch_deltas.payload":
		return strings.Fields("candidate_id project_id branch_id base_revision parent_revision delta_hash created_by")
	case "change_plans.payload":
		return strings.Fields("id project_id branch_id candidate_id content_hash created_by")
	case "skill_discovery_runs.inventory", "skill_discovery_runs.source_snapshot":
		return strings.Fields("id project_id source_id actor_id source_revision")
	case "qualification_steps.metrics":
		return strings.Fields("id project_id run_id ordinal arm case_key repeat_index child_id child_kind")
	case "capability_qualifications.payload":
		return strings.Fields("id project_id run_id report_hash")
	case "qualification_approvals.payload":
		return strings.Fields("run_id project_id actor_id")
	case "qualification_schedules.config":
		return strings.Fields("id project_id actor_id revision")
	case "working_indexes.manifest":
		return strings.Fields("id project_id branch_id actor_id model_revision chunks")
	case "knowledge_indexes.manifest":
		return strings.Fields("id project_id branch_id ip_head model_revision chunks")
	case "knowledge_chunks.content", "knowledge_chunks.vector":
		return strings.Fields("id index_id resource_id resource_revision source_hash start_rune end_rune vector_hash")
	case "working_chunks.content", "working_chunks.vector":
		return strings.Fields("id index_id source_key ref_kind ref_id ref_version part_number source_hash start_rune end_rune vector_hash")
	case "ip_drafts.payload":
		return strings.Fields("branch_id actor_id revision base_head")
	case "resource_drafts.body":
		return strings.Fields("resource_id actor_id revision base_head")
	case "resource_policy_drafts.payload":
		return strings.Fields("resource_id actor_id revision")
	case "patch_drafts.payload":
		return strings.Fields("branch_id actor_id project_id revision content_hash")
	case "import_drafts.payload":
		return strings.Fields("import_id actor_id project_id revision content_hash")
	case "memory_sessions.document":
		return strings.Fields("id project_id branch_id actor_id revision")
	case "memory_items.document":
		return strings.Fields("id project_id branch_id head_revision kind state actor_id session_id")
	case "memory_revisions.document":
		return strings.Fields("id project_id item_id parent_id actor_id")
	}
	return nil
}

func TransientIdentity(key string, values map[string]string) (string, error) {
	columns := TransientColumns(key)
	if len(columns) == 0 {
		return "", ErrInvalidInventory
	}
	identity := map[string]string{"rule": key}
	for _, column := range columns {
		value, exists := values[column]
		optional := key == "qualification_steps.metrics" && (column == "child_id" || column == "child_kind")
		optional = optional || (key == "branches.name" && (column == "parent_id" || column == "base_revision")) || (key == "capability_versions.capability_key" && column == "parent_id")
		optional = optional || (column == "base_head" && (key == "ip_drafts.payload" || key == "resource_drafts.body"))
		optional = optional || (key == "memory_items.document" && (column == "actor_id" || column == "session_id")) || (key == "memory_revisions.document" && column == "parent_id")
		optional = optional || (key == "ip_branch_deltas.payload" && (column == "base_revision" || column == "parent_revision")) || (key == "change_plans.payload" && column == "candidate_id")
		optional = optional || (key == "knowledge_indexes.manifest" && column == "ip_head")
		optional = optional || (key == "ip_revisions.payload" && column == "base_head")
		optional = optional || ((key == "capability_versions.definition" || key == "skill_packages.report" || key == "skill_packages.source") && (column == "parent_id" || column == "upstream_id"))
		if !exists || (value == "" && !optional) {
			return "", ErrInvalidInventory
		}
		identity[column] = value
	}
	raw, err := json.Marshal(identity)
	if err != nil {
		return "", err
	}
	return financialDigest(raw), nil
}

func TransientEvidence(row SourceRow, field string) (EvidenceSummary, error) {
	key := row.Table + "." + field
	hash, err := TransientIdentity(key, row.Values)
	if err != nil || row.Nulls[field] {
		return EvidenceSummary{}, ErrInvalidInventory
	}
	summary := EvidenceSummary{ContentScopeHash: hash}
	raw := row.Values[field]
	if AnonymizedLabelAllowed(key) {
		if raw == "" || strings.HasPrefix(raw, retiredLabelPrefix) {
			return EvidenceSummary{}, ErrInvalidInventory
		}
		return summary, nil
	}
	switch key {
	case "outbox.payload":
		id, err := eventSubject(row)
		if err != nil || row.Nulls["processed_at"] || row.Values["processed_at"] == "" {
			return EvidenceSummary{}, ErrInvalidInventory
		}
		summary.Event = &EventExpirySummary{SubjectID: id}
	case "capability_versions.definition":
		var d domain.CapabilityDefinition
		if authorDocument(raw, &d) != nil || d.Validate() != nil || d.Kind != row.Values["kind"] || d.Key != row.Values["capability_key"] || authorCapabilityHash(row.Values["parent_id"], d) != row.Values["content_hash"] {
			return EvidenceSummary{}, ErrInvalidInventory
		}
		summary.InputHash = row.Values["content_hash"]
		summary.CapabilityContent = capabilityContentSummary(d)
	case "skill_packages.report":
		var report skills.Report
		if authorDocument(raw, &report) != nil || report.Hash != row.Values["bundle_hash"] {
			return EvidenceSummary{}, ErrInvalidInventory
		}
		summary.InputHash = report.Hash
	case "skill_packages.source":
		var source authorSkillSource
		if authorDocument(raw, &source) != nil || source.Kind == "" || (source.ArchiveHash != "" && !hash64.MatchString(source.ArchiveHash)) {
			return EvidenceSummary{}, ErrInvalidInventory
		}
		summary.InputHash = source.ArchiveHash
	case "ip_revisions.payload":
		var snapshot domain.Snapshot
		if contentJSON([]byte(raw), &snapshot, row.Values["content_hash"]) != nil || domain.ValidateSnapshot(snapshot) != nil {
			return EvidenceSummary{}, ErrInvalidInventory
		}
		summary.InputHash = row.Values["content_hash"]
	case "artifact_releases.manifest":
		summary.Release, err = releaseExpirySummary(row)
		if err != nil {
			return EvidenceSummary{}, err
		}
		summary.InputHash = row.Values["content_hash"]
	case "ip_promotions.payload":
		var input domain.PromotionInput
		if authorDocument(raw, &input) != nil {
			return EvidenceSummary{}, ErrInvalidInventory
		}
	case "ip_branch_deltas.payload":
		var delta domain.BranchDelta
		if authorDocument(raw, &delta) != nil || delta.Schema != 1 || financialDigest([]byte(raw)) != row.Values["delta_hash"] {
			return EvidenceSummary{}, ErrInvalidInventory
		}
	case "change_plans.payload":
		var plan domain.ChangePlan
		if contentJSON([]byte(raw), &plan, row.Values["content_hash"]) != nil || plan.ID != row.Values["id"] || plan.BranchID != row.Values["branch_id"] || plan.CandidateID != row.Values["candidate_id"] || !hash64.MatchString(plan.GraphHash) {
			return EvidenceSummary{}, ErrInvalidInventory
		}
	case "source_imports.report":
		var report retrieval.ImportReport
		if authorDocument(raw, &report) != nil {
			return EvidenceSummary{}, ErrInvalidInventory
		}
		summary.ReportHash = stableHash(report)
	case "memory_items.document", "memory_revisions.document":
		var item memory.Item
		if authorDocument(raw, &item) != nil || memory.ValidateItem(item) != nil || item.Project != row.Values["project_id"] {
			return EvidenceSummary{}, ErrInvalidInventory
		}
		if row.Table == "memory_items" && (item.ID != row.Values["id"] || item.Revision != row.Values["head_revision"] || item.Branch != row.Values["branch_id"] || item.Kind != row.Values["kind"] || item.State != row.Values["state"] || item.Actor != row.Values["actor_id"] || item.Session != row.Values["session_id"]) {
			return EvidenceSummary{}, ErrInvalidInventory
		}
		if row.Table == "memory_revisions" && (item.ID != row.Values["item_id"] || item.Revision != row.Values["id"]) {
			return EvidenceSummary{}, ErrInvalidInventory
		}
		summary.InputHash = stableHash(item)
	case "memory_sessions.document":
		var checkpoint memoryCheckpoint
		if authorDocument(raw, &checkpoint) != nil || checkpoint.FocusIDs == nil {
			return EvidenceSummary{}, ErrInvalidInventory
		}
	case "ip_drafts.payload", "resource_drafts.body", "resource_policy_drafts.payload", "patch_drafts.payload", "import_drafts.payload":
		// Saved drafts need not be publishable. Erasure proves their actual
		// bytes and revision, never repairs or interprets incomplete author work.
		if !utf8.ValidString(raw) {
			return EvidenceSummary{}, ErrInvalidInventory
		}
		if (row.Table == "patch_drafts" || row.Table == "import_drafts") && financialDigest([]byte(raw)) != row.Values["content_hash"] {
			return EvidenceSummary{}, ErrInvalidInventory
		}
	case "generation_previews.content":
		if !utf8.ValidString(raw) || len(raw) > 4*1024*1024 || financialDigest([]byte(raw)) != row.Values["content_hash"] {
			return EvidenceSummary{}, ErrInvalidInventory
		}
	case "ingest_jobs.progress":
		var p struct {
			Plan  *retrieval.ImportPlan  `json:"plan"`
			Units []retrieval.ImportUnit `json:"units"`
		}
		if financialDigest([]byte(raw)) != row.Values["progress_hash"] || authorDocument(raw, &p) != nil {
			return EvidenceSummary{}, ErrInvalidInventory
		}
		if row.Values["state"] == "COMPLETED" {
			if p.Plan == nil || len(p.Units) != p.Plan.Count {
				return EvidenceSummary{}, ErrInvalidInventory
			}
			report, err := retrieval.CompleteImportReport(p.Plan.Parser, p.Units)
			if err != nil {
				return EvidenceSummary{}, ErrInvalidInventory
			}
			encoded, err := json.Marshal(report)
			if err != nil {
				return EvidenceSummary{}, err
			}
			summary.ReportHash = financialDigest(encoded)
		}
	case "skill_discovery_runs.inventory":
		var inventory skills.DiscoveryInventory
		if authorDocument(raw, &inventory) != nil {
			return EvidenceSummary{}, ErrInvalidInventory
		}
	case "skill_discovery_runs.source_snapshot":
		var snapshot struct {
			ID         string `json:"id"`
			Repository string `json:"repository"`
			Ref        string `json:"ref"`
			Path       string `json:"path"`
			Keyword    string `json:"keyword"`
			Enabled    bool   `json:"enabled"`
			Limit      int    `json:"limit"`
			Revision   int64  `json:"revision"`
		}
		if authorDocument(raw, &snapshot) != nil || snapshot.ID != row.Values["source_id"] || !snapshot.Enabled {
			return EvidenceSummary{}, ErrInvalidInventory
		}
	case "qualification_steps.metrics":
		var metrics qualification.EvaluationMetrics
		if authorDocument(raw, &metrics) != nil {
			return EvidenceSummary{}, ErrInvalidInventory
		}
	case "capability_qualifications.payload":
		var frozen qualification.QualificationFrozen
		if authorDocument(raw, &frozen) != nil {
			return EvidenceSummary{}, ErrInvalidInventory
		}
		summary.InputHash = capabilityProjectionHash(frozen)
	case "qualification_approvals.payload":
		var approval qualification.ExecutableApproval
		if authorDocument(raw, &approval) != nil || approval.Policy != qualification.ExecutableApprovalPolicy {
			return EvidenceSummary{}, ErrInvalidInventory
		}
	case "qualification_schedules.config":
		var config struct {
			Input       qualification.QualificationInput `json:"input"`
			Enabled     bool                             `json:"enabled"`
			BudgetLimit int64                            `json:"budget_limit_micros"`
			Revision    int64                            `json:"revision"`
		}
		if authorDocument(raw, &config) != nil || config.BudgetLimit < 0 || config.BudgetLimit > 1000000000000 {
			return EvidenceSummary{}, ErrInvalidInventory
		}
		summary.Schedule = &ScheduleExpirySummary{BudgetLimit: config.BudgetLimit}
	case "working_indexes.manifest":
		var manifest domain.PrivateManifest
		if authorDocument(raw, &manifest) != nil || stableHash(manifest) != row.Values["id"] || manifest.Project != row.Values["project_id"] || manifest.Branch != row.Values["branch_id"] || manifest.Actor != row.Values["actor_id"] || manifest.Model != row.Values["model_revision"] {
			return EvidenceSummary{}, ErrInvalidInventory
		}
	case "knowledge_indexes.manifest":
		var manifest domain.KnowledgeIndexManifest
		if authorDocument(raw, &manifest) != nil || manifest.Branch != row.Values["branch_id"] || manifest.IPHead != row.Values["ip_head"] || manifest.Model != row.Values["model_revision"] || manifest.Parser != "unicode-512-overlap32-v1" {
			return EvidenceSummary{}, ErrInvalidInventory
		}
		canonical, err := json.Marshal(manifest)
		if err != nil || financialDigest(append([]byte(row.Values["project_id"]+"\x00"), canonical...)) != row.Values["id"] {
			return EvidenceSummary{}, ErrInvalidInventory
		}
	case "working_chunks.content", "knowledge_chunks.content":
		start, startErr := strconv.Atoi(row.Values["start_rune"])
		end, endErr := strconv.Atoi(row.Values["end_rune"])
		if startErr != nil || endErr != nil || start < 0 || end <= start || !utf8.ValidString(raw) || utf8.RuneCountInString(raw) != end-start {
			return EvidenceSummary{}, ErrInvalidInventory
		}
	case "working_chunks.vector", "knowledge_chunks.vector":
		if _, err := retrieval.UnpackVector([]byte(raw)); err != nil || financialDigest([]byte(raw)) != row.Values["vector_hash"] {
			return EvidenceSummary{}, ErrInvalidInventory
		}
	}
	return summary, nil
}

func validTransientSummary(key string, s EvidenceSummary) bool {
	want := EvidenceSummary{ContentScopeHash: s.ContentScopeHash}
	if key == "capability_versions.definition" {
		if !validCapabilityContentExpiry(s.CapabilityContent) || !hash64.MatchString(s.InputHash) {
			return false
		}
		want.CapabilityContent, want.InputHash = s.CapabilityContent, s.InputHash
	}
	if key == "skill_packages.report" || key == "skill_packages.source" {
		if (key == "skill_packages.report" || s.InputHash != "") && !hash64.MatchString(s.InputHash) {
			return false
		}
		want.InputHash = s.InputHash
	}
	if key == "ingest_jobs.progress" || key == "source_imports.report" {
		want.ReportHash = s.ReportHash
	}
	if key == "source_imports.report" && !hash64.MatchString(s.ReportHash) {
		return false
	}
	if key == "capability_qualifications.payload" || key == "memory_items.document" || key == "memory_revisions.document" || key == "ip_revisions.payload" || key == "artifact_releases.manifest" {
		if !hash64.MatchString(s.InputHash) {
			return false
		}
		want.InputHash = s.InputHash
	}
	if key == "artifact_releases.manifest" {
		if !validReleaseExpirySummary(s.Release) {
			return false
		}
		want.Release = s.Release
	}
	if key == "qualification_schedules.config" {
		if s.Schedule == nil || s.Schedule.BudgetLimit < 0 || s.Schedule.BudgetLimit > 1000000000000 {
			return false
		}
		want.Schedule = s.Schedule
	}
	if key == "outbox.payload" {
		if s.Event == nil || !id32.MatchString(s.Event.SubjectID) {
			return false
		}
		want.Event = s.Event
	}
	return hash64.MatchString(s.ContentScopeHash) && s == want && (s.ReportHash == "" || hash64.MatchString(s.ReportHash))
}

// A retained budget ceiling is evidence, never permission to schedule a call.
type ScheduleExpirySummary struct {
	BudgetLimit int64 `json:"budget_limit_micros"`
}

func validateTransientOwner(row SourceRow, field string, p ExpiredProjection) error {
	key := row.Table + "." + field
	if key == "outbox.payload" && (row.Nulls["processed_at"] || row.Values["processed_at"] == "") {
		return ErrInvalidInventory
	}
	if len(TransientColumns(key)) == 0 {
		return nil
	}
	hash, err := TransientIdentity(key, row.Values)
	if err != nil || !validTransientSummary(key, p.Summary) || hash != p.Summary.ContentScopeHash {
		return ErrInvalidInventory
	}
	// At the first transition, derive every retained value from the actual
	// source again. A caller cannot forge a budget ceiling or frozen-input hash
	// merely by supplying the right row identity and original payload digest.
	// Replays instead bind the erased envelope to the independent journal.
	if AnonymizedLabelAllowed(key) {
		if hash, marked := RetiredLabelHash(row.Values[field]); marked {
			if p.Materialization != AnonymizedLabel || hash != p.OriginalPayloadHash {
				return ErrInvalidInventory
			}
			return nil
		}
	}
	if _, expired := ParseSourceProjection([]byte(row.Values[field]), key); expired != nil {
		expected, err := TransientEvidence(row, field)
		if err != nil || stableHash(expected) != stableHash(p.Summary) || financialDigest([]byte(row.Values[field])) != p.OriginalPayloadHash {
			return ErrInvalidInventory
		}
	}
	if key == "generation_previews.content" && row.Values["content_hash"] != p.OriginalPayloadHash {
		return ErrInvalidInventory
	}
	if key == "ingest_jobs.progress" && row.Values["progress_hash"] != p.OriginalPayloadHash {
		return ErrInvalidInventory
	}
	if (key == "working_chunks.vector" || key == "knowledge_chunks.vector") && row.Values["vector_hash"] != p.OriginalPayloadHash {
		return ErrInvalidInventory
	}
	return nil
}
