package backup

import (
	"encoding/json"
	"errors"
	"novelstudio/internal/domain"
	"novelstudio/internal/retention"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

var exclusions = []string{"API keys and credential references", "vault masters and ciphertexts", "password hashes and sessions", "project memberships and model bindings", "outbox and Temporal databases/history"}
var retentionExclusions = []string{"API keys and credential references", "vault masters and ciphertexts", "password hashes and sessions", "project memberships and model bindings", "pending outbox delivery and Temporal databases/history"}

func excludedTable(name string) bool {
	return slices.Contains([]string{"sessions", "project_members", "model_bindings", "outbox"}, name)
}

func excludedTableForSchema(name string, schema int) bool {
	return excludedTable(name) && !(hasRetentionSchema(schema) && name == "outbox")
}
func sanitize(d Data) {
	sanitizeWithSourceHistory(d, false)
}

func sanitizeWithSourceHistory(d Data, preserveHistory bool) {
	// Keep the original plan, state, receipts and clocks as historical evidence.
	// This independent runtime flag prevents an imported cleanup cursor from
	// becoming executable; recovery must obtain a fresh trusted inventory.
	for _, s := range retentionTableCatalog() {
		if s.name == "retention_jobs" || s.name == "retention_recovery_attempts" {
			for _, row := range d[s.name] {
				s.set(row, "restored_disabled", "1")
			}
		}
	}
	for _, s := range businessTableCatalog(d) {
		if excludedTable(s.name) && !(preserveHistory && s.name == "outbox") {
			d[s.name] = nil
			continue
		}
		for _, r := range d[s.name] {
			s.set(r, "credential_ref", "")
			switch s.name {
			case "outbox":
				// Retain exact event/source bytes but suppress delivery on every
				// restore. This marker is not a business execution receipt.
				// A deterministic value keeps snapshot and binlog images equal.
				if s.value(r, "processed_at") == "" {
					s.set(r, "processed_at", s.value(r, "created_at"))
				}
			case "recipe_runs":
				s.set(r, "restored_disabled", "1")
				s.set(r, "cancel_requested", "1")
				s.set(r, "paused", "0")
				if !slices.Contains([]string{"COMPLETED", "FAILED", "CANCELLED"}, s.value(r, "state")) {
					s.set(r, "state", "CANCELLED")
					if !preserveHistory {
						s.set(r, "reason", "RESTORED_DISABLED")
					}
				}
			case "ingest_jobs":
				s.set(r, "paused", "1")
				s.set(r, "cancel_requested", "1")
				if !slices.Contains([]string{"COMPLETED", "FAILED", "CANCELLED"}, s.value(r, "state")) {
					s.set(r, "state", "CANCELLED")
					s.set(r, "reason", "RESTORED_DISABLED")
				}
			case "semantic_review_runs":
				s.set(r, "restored_disabled", "1")
				s.set(r, "cancel_requested", "1")
				s.set(r, "paused", "0")
				if slices.Contains([]string{"PREPARED", "QUEUED", "RUNNING", "UNKNOWN", "BLOCKED"}, s.value(r, "state")) {
					s.set(r, "state", "CANCELLED")
				}
			case "users":
				s.set(r, "password_hash", "")
				s.set(r, "enabled", "0")
			case "model_budgets", "skill_discovery_sources", "working_indexes":
				s.set(r, "enabled", "0")
			case "capability_qualifications", "capability_bindings", "qualification_schedules", "qualification_week_grants":
				s.set(r, "enabled", "0")
			case "qualification_calls":
				if !preserveHistory && slices.Contains([]string{"RESERVED", "DISPATCHED", "UNKNOWN"}, s.value(r, "state")) {
					s.set(r, "state", "UNKNOWN")
				}
			case "qualification_runs":
				s.set(r, "state", "CANCELLED")
				s.set(r, "paused", "1")
				s.set(r, "cancelled", "1")
				if !preserveHistory {
					s.set(r, "reason", "Isolated restore: evaluation cancelled; qualification and binding revalidation required")
				}
			case "generation_tasks":
				s.set(r, "paused", "1")
				s.set(r, "cancel_requested", "1")
				if !slices.Contains([]string{"SUCCEEDED", "FAILED", "CANCELLED"}, s.value(r, "state")) {
					s.set(r, "state", "CANCELLED")
				}
			case "platform_tasks":
				s.set(r, "paused", "1")
				s.set(r, "cancelled", "1")
				if !slices.Contains([]string{"SUCCEEDED", "FAILED", "CANCELLED"}, s.value(r, "state")) {
					s.set(r, "state", "CANCELLED")
				}
			case "tool_trials", "skill_discovery_runs", "index_jobs":
				s.set(r, "lease_id", "")
				if s.name == "index_jobs" && s.value(r, "kind") == "working" || !slices.Contains([]string{"SUCCEEDED", "COMPLETED", "FAILED", "CANCELLED", "BLOCKED"}, s.value(r, "state")) {
					s.set(r, "state", "CANCELLED")
					if !preserveHistory {
						s.set(r, "reason", "Isolated restore: execution stopped; rebind and reconcile before new work")
					}
				}
			case "model_calls":
				s.set(r, "cancel_requested", "1")
				if !preserveHistory && slices.Contains([]string{"RESERVED", "DISPATCHED", "RUNNING"}, s.value(r, "state")) {
					s.set(r, "state", "UNKNOWN")
				}
			}
		}
	}
}
func validateData(d Data) error {
	return validateDataWithEvidence(d, qualificationArchiveEvidence{})
}

func validateDataWithEvidence(d Data, evidence qualificationArchiveEvidence) error {
	catalog := businessTableCatalog(d)
	if len(d) != len(catalog) {
		return errors.New("table catalog mismatch")
	}
	for _, s := range catalog {
		rows, exists := d[s.name]
		if !exists {
			return errors.New("missing table")
		}
		if excludedTable(s.name) && !(evidence.historicalOutbox && s.name == "outbox") && len(rows) > 0 {
			return errors.New("excluded data present")
		}
		for _, r := range rows {
			if len(r) != len(s.cols()) {
				return errors.New("row column mismatch")
			}
			if s.name == "outbox" && (s.value(r, "processed_at") == "" || s.value(r, "created_at") == "") {
				return errors.New("archive outbox delivery is not disabled")
			}
			if s.name == "generation_previews" {
				body := s.value(r, "content")
				seq, err := strconv.ParseInt(s.value(r, "sequence"), 10, 64)
				proof, expired := evidence.transients["generation_previews.content"][s.value(r, "generation_id")]
				validBody := len(body) <= 4*1024*1024 && utf8.ValidString(body) && digest([]byte(body)) == s.value(r, "content_hash")
				if expired {
					p, parseErr := retention.ParseSourceProjection([]byte(body), "generation_previews.content")
					validBody = parseErr == nil && p.OriginalPayloadHash == s.value(r, "content_hash") && recipeJSONEqual(marshalRecipeProof(p), marshalRecipeProof(proof))
				}
				if err != nil || seq < 1 || !validBody {
					return errors.New("invalid provisional preview")
				}
			}
			for i, column := range s.cols() {
				value := string(r[i])
				if s.name == "novel_settings_drafts" && column == "base_revision" {
					base, err := strconv.ParseInt(value, 10, 64)
					if err != nil || base < 1 {
						return errors.New("invalid novel settings base revision")
					}
					continue
				}
				if value == "" || column == "operation_id" || (column == "id" && s.name == "audit_events") {
					continue
				}
				isHashID := column == "index_id" || column == "source_key" || (column == "id" && slices.Contains([]string{"knowledge_indexes", "knowledge_chunks", "working_indexes", "working_chunks", "model_call_receipts"}, s.name))
				if isHashID || strings.HasSuffix(column, "_hash") {
					if !hex64.MatchString(value) {
						return errors.New("invalid hash identity")
					}
				} else if column == "id" || strings.HasSuffix(column, "_id") || slices.Contains([]string{"base_head", "head_revision", "parent_head", "old_base", "base_revision", "resource_revision"}, column) {
					if !hex32.MatchString(value) {
						return errors.New("invalid relation identity")
					}
				}
			}
			if s.value(r, "credential_ref") != "" || s.value(r, "password_hash") != "" {
				return errors.New("secret reference in archive")
			}
			if s.name == "users" && s.value(r, "enabled") != "0" {
				return errors.New("archive identity enabled")
			}
			if s.name == "working_indexes" && s.value(r, "enabled") != "0" {
				return errors.New("archive private index enabled")
			}
			if slices.Contains([]string{"capability_qualifications", "capability_bindings", "qualification_schedules", "qualification_week_grants"}, s.name) && s.value(r, "enabled") != "0" {
				return errors.New("archive qualification authority enabled")
			}
			if slices.Contains([]string{"qualification_week_grants", "qualification_calls"}, s.name) {
				for _, column := range []string{"cap_micros", "reserved_micros", "spent_micros", "held_micros", "charged_micros"} {
					if s.index(column) >= 0 {
						amount, parseErr := strconv.ParseInt(s.value(r, column), 10, 64)
						if parseErr != nil || amount < 0 {
							return errors.New("invalid qualification budget balance")
						}
					}
				}
			}
			if s.name == "index_jobs" && !slices.Contains([]string{"published", "working"}, s.value(r, "kind")) {
				return errors.New("invalid index job kind")
			}
			if s.name == "projects" && !hex32.MatchString(s.value(r, "id")) {
				return errors.New("invalid project identity")
			}
		}
	}
	if err := validateQualificationBudgetWithEvidence(d, evidence); err != nil {
		return err
	}
	if err := validateRecipeBackupWithAllEvidence(d, evidence.generations, evidence.recipeRuns, evidence.recipeSteps, evidence.transients["capability_versions.definition"]); err != nil {
		return err
	}
	if err := validateIngestBackupWithAllEvidence(d, evidence.transients["ingest_jobs.progress"], evidence.transients["import_drafts.payload"], evidence.transients["source_imports.report"]); err != nil {
		return err
	}
	if err := validatePatchDraftBackupWithEvidence(d, evidence.transients["patch_drafts.payload"]); err != nil {
		return err
	}
	if err := validateMemoryBackupWithDocuments(d, evidence.contexts, evidence.transients); err != nil {
		return err
	}
	return validateSemanticReviewBackup(d, evidence.transients["ip_branch_deltas.payload"])
}

// Enumerate actual object references, never all values called hash. Some hashes
// identify contracts, containers or SQL content rather than blob objects.
func blobReferences(d Data) (map[string]bool, error) {
	refs := map[string]bool{}
	branches := map[string]string{}
	indexes := map[string]string{}
	calls := map[string]string{}
	for _, s := range tableCatalog {
		for _, r := range d[s.name] {
			switch s.name {
			case "branches":
				branches[s.value(r, "id")] = s.value(r, "project_id")
			case "knowledge_indexes":
				indexes[s.value(r, "id")] = s.value(r, "project_id")
			case "model_calls":
				calls[s.value(r, "id")] = s.value(r, "project_id")
			}
		}
	}
	add := func(project, hash string) error {
		if hash == "" {
			return nil
		}
		if !hex32.MatchString(project) || !hex64.MatchString(hash) {
			return errors.New("invalid object identity")
		}
		refs["blobs/"+project+"/"+hash] = true
		return nil
	}
	for _, s := range tableCatalog {
		for _, r := range d[s.name] {
			project := s.value(r, "project_id")
			var hashes []string
			switch s.name {
			case "recipe_steps":
				hashes = append(hashes, s.value(r, "input_hash"), s.value(r, "output_hash"))
			case "semantic_review_runs":
				var frozen domain.SemanticReviewFrozen
				if err := strictJSON([]byte(s.value(r, "frozen")), &frozen); err != nil || frozen.Schema != 1 || !hex64.MatchString(frozen.InputHash) {
					return nil, errors.New("invalid frozen semantic review input")
				}
				hashes = append(hashes, frozen.InputHash, s.value(r, "report_hash"))
			case "qualification_runs", "capability_qualifications":
				hashes = append(hashes, s.value(r, "report_hash"))
			case "ip_revisions":
				project = branches[s.value(r, "branch_id")]
				hashes = append(hashes, s.value(r, "content_hash"))
			case "resources":
				hashes = append(hashes, s.value(r, "content_hash"))
			case "artifact_releases":
				hashes = append(hashes, s.value(r, "content_hash"))
				var m domain.Manifest
				if err := json.Unmarshal([]byte(s.value(r, "manifest")), &m); err != nil {
					return nil, errors.New("invalid release manifest")
				}
				for _, ref := range m.Artifacts {
					hashes = append(hashes, ref.Hash, ref.OriginalHash)
				}
			case "source_imports", "ingest_jobs":
				hashes = append(hashes, s.value(r, "source_hash"))
			case "skill_packages":
				hashes = append(hashes, s.value(r, "bundle_hash"))
				var source struct {
					ArchiveHash string `json:"archive_hash"`
				}
				if err := json.Unmarshal([]byte(s.value(r, "source")), &source); err != nil {
					return nil, errors.New("invalid skill source")
				}
				hashes = append(hashes, source.ArchiveHash)
			case "generation_tasks", "tool_trials":
				var frozen struct {
					InputHash string `json:"input_hash"`
				}
				if err := json.Unmarshal([]byte(s.value(r, "frozen")), &frozen); err != nil {
					return nil, errors.New("invalid frozen input")
				}
				hashes = append(hashes, frozen.InputHash, s.value(r, "output_hash"))
			case "model_calls":
				hashes = append(hashes, s.value(r, "output_hash"))
			case "model_call_receipts":
				// Settle records the original output before cancellation clears
				// the primary hash. Conflicting receipts likewise retain their
				// own output without replacing model_calls.output_hash.
				var exists bool
				project, exists = calls[s.value(r, "call_id")]
				if !exists {
					return nil, errors.New("receipt references missing call")
				}
				var receipt []json.RawMessage
				if err := json.Unmarshal([]byte(s.value(r, "payload")), &receipt); err != nil || len(receipt) != 4 {
					return nil, errors.New("invalid model receipt payload")
				}
				var outputHash *string
				if err := json.Unmarshal(receipt[3], &outputHash); err != nil || outputHash == nil {
					return nil, errors.New("invalid model receipt output reference")
				}
				hashes = append(hashes, *outputHash)
			case "knowledge_chunks":
				project = indexes[s.value(r, "index_id")]
				hashes = append(hashes, s.value(r, "source_hash"))
			case "platform_tasks":
				hashes = append(hashes, s.value(r, "input_hash"))
			}
			for _, hash := range hashes {
				if err := add(project, hash); err != nil {
					return nil, err
				}
			}
		}
	}
	return refs, nil
}
