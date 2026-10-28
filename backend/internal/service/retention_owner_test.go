package service

import (
	"encoding/json"
	"novelstudio/internal/retention"
	"reflect"
	"strings"
	"testing"
	"time"
)

func retentionOwnerFixture(t *testing.T) (map[string]string, []retention.Expiry, time.Time) {
	t.Helper()
	project, id, input := strings.Repeat("a", 32), strings.Repeat("b", 32), strings.Repeat("c", 64)
	raw := `{"config":{"id":"` + strings.Repeat("d", 32) + `","revision":2,"name":"PRIVATE_CANARY","base_url":"PRIVATE_CANARY"},"price":{"currency":"USD","source":"PRIVATE_CANARY"},"input_hash":"` + input + `","estimate_micros":17,"capability":{"mode":"EVALUATION","evaluation_id":"` + strings.Repeat("e", 32) + `","evaluation_step_id":"` + strings.Repeat("f", 32) + `"},"future_body":{"prompt":"PRIVATE_CANARY"}}`
	row := map[string]string{"id": id, "project_id": project, "frozen": raw, "call_id": strings.Repeat("1", 32), "state": "UNKNOWN", "input_hash": input}
	key, err := retention.RowOwnerKey("generation_tasks", row)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)
	var expiries []retention.Expiry
	for _, field := range []string{"frozen", "frozen.input_hash"} {
		ref, err := retention.ReferenceID(project, "generation_tasks", key, field)
		if err != nil {
			t.Fatal(err)
		}
		storage, payload := retention.Inline, raw
		if field != "frozen" {
			storage, payload = retention.Blob, input
		}
		expiries = append(expiries, retention.Expiry{ReferenceID: ref, Rule: "generation_tasks." + field, PayloadHash: digest(payload), Revision: 1, Deadline: now, Storage: storage})
	}
	return row, expiries, now
}

func TestRetentionOwnerProjectsWholeAndNestedFromOneOriginalSnapshot(t *testing.T) {
	row, expiries, now := retentionOwnerFixture(t)
	before := row["frozen"]
	got, err := prepareRetentionOwner(row["project_id"], "generation_tasks", row, expiries, now)
	if err != nil {
		t.Fatal(err)
	}
	if row["frozen"] != before || len(got.Updates) != 1 || len(got.Evidence) != 2 || got.OriginalRowHash == "" {
		t.Fatal("source row changed or nested evidence missing", got)
	}
	var p retention.ExpiredProjection
	if json.Unmarshal([]byte(got.Updates["frozen"]), &p) != nil {
		t.Fatal("invalid update")
	}
	if strings.Contains(got.Updates["frozen"], "PRIVATE_CANARY") || p.Summary.InputHash != row["input_hash"] || p.Summary.EstimateMicros != 17 || p.Summary.Price.Currency != "USD" || p.Summary.Capability.Mode != "EVALUATION" || p.Summary.State != "UNKNOWN" || p.Summary.CallID != row["call_id"] {
		t.Fatal("summary lost financial scope or leaked text", p)
	}
	expiries[0], expiries[1] = expiries[1], expiries[0]
	again, err := prepareRetentionOwner(row["project_id"], "generation_tasks", row, expiries, now)
	if err != nil || !reflect.DeepEqual(got, again) {
		t.Fatal("plan order changes owner projection", err)
	}
}

func TestRetentionGenerationExpiryKeepsRecipeIdentityWithoutPrompt(t *testing.T) {
	row, _, now := retentionOwnerFixture(t)
	var frozen GenerationFrozen
	if err := json.Unmarshal([]byte(row["frozen"]), &frozen); err != nil {
		t.Fatal(err)
	}
	frozen.RecipeRunID = strings.Repeat("2", 32)
	frozen.RecipeStepID = strings.Repeat("3", 32)
	frozen.RecipeInputHash = strings.Repeat("4", 64)
	frozen.BaseHead = strings.Repeat("5", 32)
	frozen.ContextID = strings.Repeat("6", 32)
	frozen.Role = "writer"
	frozen.Capability.Mode = "TRIAL"
	frozen.Capability.EvaluationID = ""
	frozen.Capability.EvaluationStepID = ""
	frozen.Capability.AgentVersionID = strings.Repeat("7", 32)
	frozen.Capability.ManifestHash = strings.Repeat("8", 64)
	raw, _ := json.Marshal(frozen)
	row["frozen"] = string(raw)
	key, _ := retention.RowOwnerKey("generation_tasks", row)
	id, _ := retention.ReferenceID(row["project_id"], "generation_tasks", key, "frozen")
	result, err := prepareRetentionOwner(row["project_id"], "generation_tasks", row, []retention.Expiry{{ReferenceID: id, Rule: "generation_tasks.frozen", PayloadHash: digest(string(raw)), Revision: 1, Deadline: now, Storage: retention.Inline}}, now)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Summary struct {
			Generation struct {
				RecipeRunID     string `json:"recipe_run_id"`
				RecipeStepID    string `json:"recipe_step_id"`
				RecipeInputHash string `json:"recipe_input_hash"`
				BaseHead        string `json:"base_head"`
				ContextID       string `json:"context_id"`
				Role            string `json:"role"`
				AgentVersionID  string `json:"agent_version_id"`
				ManifestHash    string `json:"manifest_hash"`
				PriceHash       string `json:"price_hash"`
			} `json:"generation"`
		} `json:"summary"`
	}
	if err := json.Unmarshal([]byte(result.Updates["frozen"]), &got); err != nil {
		t.Fatal(err)
	}
	b := got.Summary.Generation
	if b.RecipeRunID != frozen.RecipeRunID || b.RecipeStepID != frozen.RecipeStepID || b.RecipeInputHash != frozen.RecipeInputHash || b.BaseHead != frozen.BaseHead || b.ContextID != frozen.ContextID || b.Role != frozen.Role || b.AgentVersionID != frozen.Capability.AgentVersionID || b.ManifestHash != frozen.Capability.ManifestHash || b.PriceHash != capabilityDigest(frozen.Price) {
		t.Fatal("recipe routing was erased with private input", b)
	}
	if strings.Contains(result.Updates["frozen"], "PRIVATE_CANARY") {
		t.Fatal("proof copied private model/price text")
	}
}

func TestRetentionOwnerRejectsAnyStaleNestedItemWithoutPartialProjection(t *testing.T) {
	for _, kind := range []string{"stale nested", "wrong project", "wrong id", "duplicate", "unknown field", "changed source", "already expired"} {
		t.Run(kind, func(t *testing.T) {
			row, expiries, now := retentionOwnerFixture(t)
			project := row["project_id"]
			switch kind {
			case "stale nested":
				expiries[1].PayloadHash = strings.Repeat("9", 64)
			case "wrong project":
				project = strings.Repeat("9", 32)
			case "wrong id":
				expiries[1].ReferenceID = strings.Repeat("9", 64)
			case "duplicate":
				expiries = append(expiries, expiries[0])
			case "unknown field":
				expiries[1].Rule = "generation_tasks.future_private"
			case "changed source":
				row["frozen"] = `{"input_hash":"` + strings.Repeat("2", 64) + `"}`
			case "already expired":
				row["frozen"] = `{"schema":1,"state":"EXPIRED","summary":{}}`
				expiries[0].PayloadHash = digest(row["frozen"])
			}
			original := row["frozen"]
			got, err := prepareRetentionOwner(project, "generation_tasks", row, expiries, now)
			if err == nil || len(got.Updates) > 0 || len(got.Evidence) > 0 || row["frozen"] != original {
				t.Fatal("unsafe/partial projection accepted", got, err)
			}
		})
	}
}

func TestRetentionOwnerPreservesWeeklyGrantAndSemanticScopeWithoutPrivateBodies(t *testing.T) {
	for _, table := range []string{"qualification_runs", "semantic_review_runs", "tool_trials"} {
		t.Run(table, func(t *testing.T) {
			project, id := strings.Repeat("a", 32), strings.Repeat("b", 32)
			now := time.Now().UTC()
			raw := `{"input_hash":"` + strings.Repeat("c", 64) + `","content_scope_hash":"` + strings.Repeat("d", 64) + `","price":{"currency":"CNY"},"estimate_micros":18,"suite":{"policy":"repository-deterministic-v3","version":1,"hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"weekly_grant":{"id":"` + strings.Repeat("e", 32) + `","schedule_id":"` + strings.Repeat("f", 32) + `","revision":3,"week":"2026-W43","cap_micros":42},"input":{"prompt":"PRIVATE_CANARY"}}`
			row := map[string]string{"id": id, "project_id": project, "frozen": raw, "state": "SUCCEEDED"}
			key, _ := retention.RowOwnerKey(table, row)
			ref, _ := retention.ReferenceID(project, table, key, "frozen")
			expiry := retention.Expiry{ReferenceID: ref, Rule: table + ".frozen", PayloadHash: digest(raw), Revision: 1, Deadline: now, Storage: retention.Inline}
			got, err := prepareRetentionOwner(project, table, row, []retention.Expiry{expiry}, now)
			if err != nil {
				t.Fatal(err)
			}
			summary := got.Evidence[0].Summary
			if strings.Contains(got.Updates["frozen"], "PRIVATE_CANARY") {
				t.Fatal("raw input leaked")
			}
			if table == "qualification_runs" && (summary.WeeklyGrant == nil || summary.WeeklyGrant.CapMicros != 42 || summary.Price.Currency != "CNY" || summary.EstimateMicros != 18) {
				t.Fatal("weekly financial scope lost")
			}
			if table == "semantic_review_runs" && summary.ContentScopeHash != strings.Repeat("d", 64) {
				t.Fatal("semantic scope hash lost")
			}
		})
	}
}
