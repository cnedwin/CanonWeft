package backup

import (
	"encoding/json"
	"errors"
	"novelstudio/internal/domain"
	"novelstudio/internal/retention"
	"novelstudio/internal/retrieval"
	"slices"
	"strconv"
)

var ingestTables = []string{"ingest_jobs", "import_drafts"}

func upgradeIngestJobs(a *Archive) error {
	if a.Manifest.Schema >= 33 {
		return nil
	}
	for _, name := range ingestTables {
		if _, ok := a.Data[name]; ok {
			return errors.New("unexpected ingest table in legacy archive")
		}
		a.Data[name] = nil
	}
	return nil
}
func validateIngestBackup(d Data) error {
	return validateIngestBackupWithEvidence(d, nil)
}
func validateIngestBackupWithEvidence(d Data, expired map[string]retention.ExpiredProjection) error {
	return validateIngestBackupWithAllEvidence(d, expired, nil)
}

func validateIngestBackupWithAllEvidence(d Data, expired, drafts map[string]retention.ExpiredProjection, reportEvidence ...map[string]retention.ExpiredProjection) error {
	var reports map[string]retention.ExpiredProjection
	if len(reportEvidence) > 0 {
		reports = reportEvidence[0]
	}
	tables := map[string]map[string]map[string]string{}
	for _, s := range tableCatalog {
		if !slices.Contains([]string{"projects", "branches", "users", "source_imports"}, s.name) {
			continue
		}
		entries := map[string]map[string]string{}
		for _, r := range d[s.name] {
			v := map[string]string{}
			for _, c := range s.cols() {
				v[c] = s.value(r, c)
			}
			entries[v["id"]] = v
		}
		tables[s.name] = entries
	}
	validOwner := func(project, branch, actor string) bool {
		return tables["projects"][project]["workspace_id"] != "" && tables["branches"][branch]["project_id"] == project && tables["users"][actor]["workspace_id"] == tables["projects"][project]["workspace_id"]
	}
	for _, s := range tableCatalog {
		if !slices.Contains(ingestTables, s.name) {
			continue
		}
		seen := map[string]bool{}
		for _, r := range d[s.name] {
			project, actor := s.value(r, "project_id"), s.value(r, "actor_id")
			revision, err := strconv.ParseInt(s.value(r, "revision"), 10, 64)
			if err != nil || revision < 1 {
				return errors.New("invalid ingestion revision")
			}
			if s.name == "import_drafts" {
				source := tables["source_imports"][s.value(r, "import_id")]
				key := s.value(r, "import_id") + ":" + actor
				raw := []byte(s.value(r, "payload"))
				var content domain.ImportDraftContent
				if seen[key] || source["actor_id"] != actor || source["project_id"] != project || !validOwner(project, source["branch_id"], actor) {
					return errors.New("invalid private import draft")
				}
				seen[key] = true
				owner, err := retention.RowOwnerKey("import_drafts", map[string]string{"import_id": source["id"], "actor_id": actor})
				if err != nil {
					return err
				}
				if proof, ok := drafts[owner]; ok {
					p, err := retention.ParseSourceProjection(raw, "import_drafts.payload")
					if err != nil || p.OriginalPayloadHash != s.value(r, "content_hash") || !recipeJSONEqual(marshalRecipeProof(p), marshalRecipeProof(proof)) {
						return errors.New("invalid expired import draft evidence")
					}
				} else if digest(raw) != s.value(r, "content_hash") || json.Unmarshal(raw, &content) != nil || content.Validate() != nil {
					return errors.New("invalid private import draft")
				}
				continue
			}
			id, branch := s.value(r, "id"), s.value(r, "branch_id")
			size, e := strconv.ParseInt(s.value(r, "source_size"), 10, 64)
			raw := []byte(s.value(r, "progress"))
			var progress struct {
				Plan  *retrieval.ImportPlan  `json:"plan"`
				Units []retrieval.ImportUnit `json:"units"`
			}
			proof, isExpired := expired[id]
			if seen[id] || !validOwner(project, branch, actor) || e != nil || size < 1 || size > 32*1024*1024 || len(raw) > 2*1024*1024 || s.value(r, "paused") != "1" || s.value(r, "cancel_requested") != "1" || !slices.Contains([]string{"COMPLETED", "FAILED", "CANCELLED"}, s.value(r, "state")) || !slices.Contains([]string{"enabled", "disabled"}, s.value(r, "ocr_mode")) {
				return errors.New("invalid restored ingestion state")
			}
			seen[id] = true
			if isExpired {
				p, err := retention.ParseSourceProjection(raw, "ingest_jobs.progress")
				if err != nil || p.OriginalPayloadHash != s.value(r, "progress_hash") || !recipeJSONEqual(marshalRecipeProof(p), marshalRecipeProof(proof)) {
					return errors.New("invalid expired ingestion evidence")
				}
				if s.value(r, "state") == "COMPLETED" {
					source := tables["source_imports"][id]
					reportHash, err := importReportHash(source, reports)
					if err != nil {
						return errors.New("invalid completed ingestion receipt")
					}
					if source["project_id"] != project || source["branch_id"] != branch || source["actor_id"] != actor || source["source_hash"] != s.value(r, "source_hash") || reportHash != proof.Summary.ReportHash {
						return errors.New("expired ingestion receipt mismatch")
					}
				}
				continue
			}
			if digest(raw) != s.value(r, "progress_hash") || json.Unmarshal(raw, &progress) != nil {
				return errors.New("invalid ingestion progress")
			}
			if progress.Plan == nil {
				if len(progress.Units) > 0 || s.value(r, "state") == "COMPLETED" {
					return errors.New("ingestion progress has no plan")
				}
				continue
			}
			if progress.Plan.Native != nil || progress.Plan.Count < 1 || progress.Plan.Count > 10000 || progress.Plan.Parser != s.value(r, "parser_revision") || len(progress.Units) > progress.Plan.Count {
				return errors.New("ingestion plan mismatch")
			}
			if len(progress.Units) > 0 {
				if _, err := retrieval.CompleteImportReport(progress.Plan.Parser, progress.Units); err != nil {
					return errors.New("invalid ingestion units")
				}
			}
			if s.value(r, "state") == "COMPLETED" {
				source := tables["source_imports"][id]
				report, e := retrieval.CompleteImportReport(progress.Plan.Parser, progress.Units)
				encoded, _ := json.Marshal(report)
				reportHash, decodeErr := importReportHash(source, reports)
				if e != nil || decodeErr != nil || len(progress.Units) != progress.Plan.Count || source["project_id"] != project || source["branch_id"] != branch || source["actor_id"] != actor || source["source_hash"] != s.value(r, "source_hash") || reportHash != digest(encoded) {
					return errors.New("completed ingestion receipt missing or mismatched")
				}
			}
		}
	}
	return nil
}

// Reports are never reconstructed from an expiry marker. The caller supplies
// only independently verified journal evidence; retained checkpoints still
// compare their own derived report with this exact canonical digest.
func importReportHash(source map[string]string, reports map[string]retention.ExpiredProjection) (string, error) {
	raw := []byte(source["report"])
	if proof, expired := reports[source["id"]]; expired {
		actual, err := retention.ParseSourceProjection(raw, "source_imports.report")
		if err != nil || !recipeJSONEqual(marshalRecipeProof(actual), marshalRecipeProof(proof)) || !hex64.MatchString(proof.Summary.ReportHash) {
			return "", errors.New("invalid expired import report")
		}
		return proof.Summary.ReportHash, nil
	}
	var report retrieval.ImportReport
	if err := strictJSON(raw, &report); err != nil {
		return "", err
	}
	encoded, err := json.Marshal(report)
	return digest(encoded), err
}
