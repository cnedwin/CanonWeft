package service

import (
	"encoding/json"
	"novelstudio/internal/domain"
	"novelstudio/internal/retention"
	"sort"
	"strings"
	"time"
)

// retentionOwnerProjection is pure preparation, not deletion authority. The
// cleanup source coordinator loads it under project/source locks; the atomic
// eraser independently verifies original values, revisions and proofs. Legacy
// ledger conversions and unimplemented owner types remain blocked.
type retentionOwnerProjection struct {
	OwnerKey        string
	OriginalRowHash string
	Updates         map[string]string
	Evidence        []retention.ExpiredProjection
}

// prepareRetentionOwner extracts every value from the same ORIGINAL SQL row
// before producing updates. This prevents whole-frozen replacement from hiding
// its nested blob reference or making result order depend on the plan order.
// Only these four typed owner handlers are classified here; all others refuse.
func prepareRetentionOwner(project, table string, row map[string]string, expiries []retention.Expiry, at time.Time) (retentionOwnerProjection, error) {
	fail := func() (retentionOwnerProjection, error) {
		return retentionOwnerProjection{}, retention.ErrInvalidInventory
	}
	if row["project_id"] != project || len(expiries) == 0 {
		return fail()
	}
	key, err := retention.RowOwnerKey(table, row)
	if err != nil {
		return fail()
	}
	raw, ok := row["frozen"]
	if !ok || len(raw) == 0 || len(raw) > 16*1024*1024 || !strings.HasPrefix(strings.TrimSpace(raw), "{") {
		return fail()
	}
	var header struct {
		State string `json:"state"`
	}
	if json.Unmarshal([]byte(raw), &header) != nil || header.State == "EXPIRED" {
		return fail()
	}
	summary, err := retentionFrozenSummary(table, row, []byte(raw))
	if err != nil {
		return fail()
	}
	// Keep a separate raw-value dictionary; do not read any returned update while
	// validating subsequent items, even when the whole frozen field comes first.
	values := map[string]string{"frozen": raw}
	if table != "qualification_runs" {
		values["frozen.input_hash"] = summary.InputHash
	}
	prepared := retentionOwnerProjection{OwnerKey: key, Updates: map[string]string{}, Evidence: []retention.ExpiredProjection{}}
	encodedRow, err := json.Marshal(row)
	if err != nil {
		return fail()
	}
	prepared.OriginalRowHash = digest(string(encodedRow))
	items := append([]retention.Expiry(nil), expiries...)
	sort.Slice(items, func(i, j int) bool { return items[i].ReferenceID < items[j].ReferenceID })
	for i, item := range items {
		if i > 0 && items[i-1].ReferenceID == item.ReferenceID {
			return fail()
		}
		prefix := table + "."
		if !strings.HasPrefix(item.Rule, prefix) {
			return fail()
		}
		field := strings.TrimPrefix(item.Rule, prefix)
		value, found := values[field]
		if !found {
			return fail()
		}
		id, e := retention.ReferenceID(project, table, key, field)
		if e != nil || id != item.ReferenceID {
			return fail()
		}
		itemSummary := summary
		if field != "frozen" {
			itemSummary = retention.EvidenceSummary{ValueHash: value}
		}
		proof, e := retention.ProjectExpired(item, []byte(value), itemSummary, at)
		if e != nil {
			return fail()
		}
		prepared.Evidence = append(prepared.Evidence, proof)
		if field == "frozen" {
			serialized, e := json.Marshal(proof)
			if e != nil {
				return fail()
			}
			prepared.Updates["frozen"] = string(serialized)
		}
	}
	return prepared, nil
}

func retentionFrozenSummary(table string, row map[string]string, raw []byte) (retention.EvidenceSummary, error) {
	summary := retention.EvidenceSummary{State: row["state"]}
	switch table {
	case "generation_tasks":
		var f GenerationFrozen
		if json.Unmarshal(raw, &f) != nil {
			return summary, retention.ErrInvalidInventory
		}
		summary.InputHash, summary.EstimateMicros = f.InputHash, f.Estimate
		summary.CallID = row["call_id"]
		summary.ConfigID, summary.ConfigRevision = f.Config.ID, f.Config.Revision
		summary.ConfigHash = capabilityDigest(f.Config)
		summary.Price = &retention.PriceSummary{Currency: f.Price.Currency}
		summary.Generation = &retention.GenerationBindingSummary{
			NovelDetail:  f.NovelDetail,
			NovelChapter: f.NovelChapter,
			Schema:       1, RecipeRunID: f.RecipeRunID, RecipeStepID: f.RecipeStepID, RecipeInputHash: f.RecipeInputHash,
			SemanticReviewID: f.SemanticReviewID, ChangePlanID: f.ChangePlanID, BaseHead: f.BaseHead,
			ContextID: f.ContextID, Role: f.Role, PriceHash: capabilityDigest(f.Price),
		}
		if f.Capability != nil {
			summary.Capability = &retention.CapabilitySummary{Mode: f.Capability.Mode, EvaluationID: f.Capability.EvaluationID, EvaluationStepID: f.Capability.EvaluationStepID, SuiteHash: f.Capability.SuiteHash}
			summary.Generation.AgentVersionID, summary.Generation.ManifestHash = f.Capability.AgentVersionID, f.Capability.ManifestHash
		}
		if input, exists := row["input_hash"]; exists && input != f.InputHash {
			return summary, retention.ErrInvalidInventory
		}
	case "qualification_runs":
		var f QualificationFrozen
		if json.Unmarshal(raw, &f) != nil {
			return summary, retention.ErrInvalidInventory
		}
		summary.ConfigID, summary.ConfigRevision = f.Config.ID, f.Config.Revision
		summary.Suite = &retention.SuiteSummary{Policy: f.Suite.Policy, Version: f.Suite.Version, Hash: f.Suite.Hash}
		summary.ConfigHash = capabilityDigest(f.Config)
		summary.Price = &retention.PriceSummary{Currency: f.Price.Currency}
		summary.EstimateMicros = f.Estimate
		summary.ReportHash = row["report_hash"]
		if f.Weekly != nil {
			summary.WeeklyGrant = &retention.WeeklyGrantSummary{ID: f.Weekly.ID, ScheduleID: f.Weekly.ScheduleID, Revision: f.Weekly.Revision, Week: f.Weekly.Week, CapMicros: f.Weekly.Cap}
		}
	case "semantic_review_runs":
		var f domain.SemanticReviewFrozen
		if json.Unmarshal(raw, &f) != nil {
			return summary, retention.ErrInvalidInventory
		}
		summary.InputHash, summary.ContentScopeHash = f.InputHash, f.ContentScopeHash
		summary.ReportHash, summary.Verdict = row["report_hash"], row["verdict"]
	case "tool_trials":
		var f ToolTrialFrozen
		if json.Unmarshal(raw, &f) != nil {
			return summary, retention.ErrInvalidInventory
		}
		summary.InputHash, summary.OutputHash = f.InputHash, row["output_hash"]
	default:
		return summary, retention.ErrInvalidInventory
	}
	if table != "qualification_runs" && summary.InputHash == "" {
		return summary, retention.ErrInvalidInventory
	}
	return summary, retention.ValidateSummary(summary, retention.Inline)
}
