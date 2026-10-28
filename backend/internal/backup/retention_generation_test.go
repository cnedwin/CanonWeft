package backup

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"novelstudio/internal/domain"
	"novelstudio/internal/retention"
	"novelstudio/internal/service"
)

func TestRecipeArchiveDoesNotTreatUnprovenExpiredInputAsOrdinaryGeneration(t *testing.T) {
	a, _ := detachedCleanupFixture(t)
	if err := validateRecipeBackup(a.Data); err == nil {
		t.Fatal("expired input without independent proof silently became a non-recipe generation")
	}
}

func TestRetentionArchiveGenerationProofRequiresJournalAndActualSource(t *testing.T) {
	for _, change := range []string{"valid", "missing proof", "missing reference", "changed source", "changed call"} {
		t.Run(change, func(t *testing.T) {
			a, _ := detachedCleanupFixture(t)
			_, metadata, err := splitRetentionData(36, a.Data)
			if err != nil {
				t.Fatal(err)
			}
			history, err := readArchiveReferenceHistory(metadata, a.Manifest.SnapshotAt)
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "missing proof":
				history.Expiry = nil
			case "missing reference":
				history.Current = nil
			case "changed source", "changed call":
				for _, spec := range tableCatalog {
					if spec.name == "generation_tasks" {
						if change == "changed call" {
							spec.set(a.Data[spec.name][0], "call_id", strings.Repeat("f", 32))
						} else {
							spec.set(a.Data[spec.name][0], "frozen", "{}")
						}
					}
				}
			}
			proofs, err := readArchiveGenerationEvidence(a.Data, history)
			if change == "valid" {
				if err != nil || len(proofs) != 1 {
					t.Fatal("valid independently bound generation rejected", err)
				}
			} else if err == nil {
				t.Fatal("unbound expiry accepted", change)
			}
		})
	}
}

func expiredRecipeBusinessFixture(t *testing.T) (Data, map[string]retention.ExpiredProjection) {
	t.Helper()
	a := emptyArchive(t)
	id := func(s string) string { return strings.Repeat(s, 32) }
	project, workspace, actor, branch, run, step, child := id("a"), id("b"), id("c"), id("d"), id("e"), id("f"), id("1")
	encode := func(v any) string {
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	canonicalHash := func(v any) string {
		var normalized any
		decoder := json.NewDecoder(bytes.NewBufferString(encode(v)))
		decoder.UseNumber()
		if err := decoder.Decode(&normalized); err != nil {
			t.Fatal(err)
		}
		return digest([]byte(encode(normalized)))
	}
	def := domain.CapabilityDefinition{Kind: "agent", Key: "test-agent", Name: "agent", Description: "test", Instructions: "test", Role: "writer", InputSchema: json.RawMessage(`{"type":"object"}`), OutputSchema: json.RawMessage(`{"type":"object"}`)}
	agent := service.CapabilityVersion{ID: id("2"), Hash: strings.Repeat("3", 64), Definition: def}
	definition := def
	definition.Kind, definition.Key, definition.Role = "recipe", "test-recipe", ""
	definition.Dependencies = []domain.CapabilityRef{{ID: agent.ID, Hash: agent.Hash, Kind: "agent"}}
	definition.Steps = []domain.RecipeStep{{Key: "draft", AgentID: agent.ID, After: []string{}}}
	root := service.RecipeFrozen{Protocol: 1, Definition: service.CapabilityVersion{ID: id("4"), Hash: strings.Repeat("5", 64), Definition: definition}, ManifestHash: strings.Repeat("6", 64), Input: json.RawMessage(`{}`), Trial: true}
	config := domain.ModelConfig{ID: id("7"), Revision: 1, Name: "private model name"}
	price := service.PriceCard{ID: id("8"), Currency: "USD", ConnectionID: config.ID, ConnectionRevision: config.Revision}
	stage := service.RecipeStepFrozen{Step: definition.Steps[0], Input: service.RecipeStepInput{Key: "draft", PriceID: price.ID, MaxOutputTokens: 100, Input: json.RawMessage(`{}`)}, ManifestHash: strings.Repeat("9", 64), Agent: agent, Config: config, Price: price}
	inputHash, outputHash := strings.Repeat("a", 64), strings.Repeat("b", 64)
	binding := &retention.GenerationBindingSummary{Schema: 1, RecipeRunID: run, RecipeStepID: step, RecipeInputHash: inputHash, Role: "writer", AgentVersionID: agent.ID, ManifestHash: stage.ManifestHash, PriceHash: canonicalHash(price)}
	proof := retention.ExpiredProjection{Schema: 1, Materialization: retention.InlineEnvelope, State: retention.Expired, ReferenceID: strings.Repeat("c", 64), OriginalPayloadHash: strings.Repeat("d", 64), ExpiredAt: time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC), Summary: retention.EvidenceSummary{Generation: binding, ConfigHash: canonicalHash(config), Price: &retention.PriceSummary{Currency: "USD"}, Capability: &retention.CapabilitySummary{Mode: "TRIAL"}}}
	for table, rows := range map[string][]map[string]string{
		"workspaces": {{"id": workspace}}, "users": {{"id": actor, "workspace_id": workspace}}, "projects": {{"id": project, "workspace_id": workspace}}, "branches": {{"id": branch, "project_id": project}},
		"capability_versions":       {{"id": agent.ID, "project_id": project, "content_hash": agent.Hash, "definition": encode(agent.Definition)}, {"id": root.Definition.ID, "project_id": project, "content_hash": root.Definition.Hash, "definition": encode(root.Definition.Definition)}},
		"model_connection_versions": {{"connection_id": config.ID, "revision": "1", "config": encode(config)}}, "model_prices": {{"id": price.ID, "workspace_id": workspace, "payload": encode(price)}},
		"recipe_runs":      {{"id": run, "project_id": project, "workspace_id": workspace, "actor_id": actor, "branch_id": branch, "revision": "1", "budget_limit_micros": "1000", "currency": "USD", "restored_disabled": "1", "cancel_requested": "1", "paused": "0", "state": "COMPLETED", "frozen": encode(root), "frozen_hash": digest([]byte(encode(root)))}},
		"recipe_steps":     {{"id": step, "run_id": run, "project_id": project, "ordinal": "0", "attempt": "1", "active": "1", "step_key": "draft", "frozen": encode(stage), "frozen_hash": digest([]byte(encode(stage))), "state": "COMPLETED", "input_hash": inputHash, "output_hash": outputHash, "generation_id": child}},
		"generation_tasks": {{"id": child, "project_id": project, "actor_id": actor, "branch_id": branch, "frozen": encode(proof)}},
	} {
		for _, row := range rows {
			testRow(a, table, row)
		}
	}
	return a.Data, map[string]retention.ExpiredProjection{child: proof}
}

func TestRetentionArchiveRecipeValidatesExpiredGenerationBindings(t *testing.T) {
	data, evidence := expiredRecipeBusinessFixture(t)
	if err := validateRecipeBackupWithEvidence(data, evidence); err != nil {
		t.Fatal("proven expired child rejected by full recipe validator", err)
	}
	for _, change := range []string{"missing binding", "run", "step", "input", "base", "context", "role", "agent", "manifest", "price", "config", "orphan"} {
		t.Run(change, func(t *testing.T) {
			data, evidence := expiredRecipeBusinessFixture(t)
			id := strings.Repeat("1", 32)
			p := evidence[id]
			b := p.Summary.Generation
			switch change {
			case "missing binding":
				p.Summary.Generation = nil
			case "run":
				b.RecipeRunID = strings.Repeat("0", 32)
			case "step":
				b.RecipeStepID = strings.Repeat("0", 32)
			case "input":
				b.RecipeInputHash = strings.Repeat("0", 64)
			case "base":
				b.BaseHead = strings.Repeat("0", 32)
			case "context":
				b.ContextID = strings.Repeat("0", 32)
			case "role":
				b.Role = "critic"
			case "agent":
				b.AgentVersionID = strings.Repeat("0", 32)
			case "manifest":
				b.ManifestHash = strings.Repeat("0", 64)
			case "price":
				b.PriceHash = strings.Repeat("0", 64)
			case "config":
				p.Summary.ConfigHash = strings.Repeat("0", 64)
			case "orphan":
				for _, s := range tableCatalog {
					if s.name == "recipe_steps" {
						s.set(data[s.name][0], "generation_id", "")
					}
				}
			}
			evidence[id] = p
			if err := validateRecipeBackupWithEvidence(data, evidence); err == nil {
				t.Fatal("changed recipe execution binding accepted", change)
			}
		})
	}
}
