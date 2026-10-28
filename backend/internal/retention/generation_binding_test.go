package retention

import (
	"novelstudio/internal/domain"
	"strings"
	"testing"
	"time"
)

func TestGenerationBindingIsClosedTypedAndRuleSpecific(t *testing.T) {
	b := GenerationBindingSummary{Schema: 1, RecipeRunID: strings.Repeat("a", 32), RecipeStepID: strings.Repeat("b", 32), RecipeInputHash: strings.Repeat("c", 64), Role: "writer", AgentVersionID: strings.Repeat("d", 32), ManifestHash: strings.Repeat("e", 64), PriceHash: strings.Repeat("f", 64)}
	p := ExpiredProjection{Schema: 1, State: Expired, Materialization: InlineEnvelope, ReferenceID: strings.Repeat("1", 64), OriginalPayloadHash: strings.Repeat("2", 64), ExpiredAt: time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC), Summary: EvidenceSummary{Generation: &b, ConfigHash: strings.Repeat("3", 64), Price: &PriceSummary{Currency: "USD"}, Capability: &CapabilitySummary{Mode: "TRIAL"}}}
	if err := ValidateProjectionForRule(p, "generation_tasks.frozen"); err != nil {
		t.Fatal(err)
	}
	for _, change := range []string{"schema", "role", "id", "price", "manifest", "partial recipe", "missing agent", "missing price", "missing config", "missing capability", "foreign rule"} {
		t.Run(change, func(t *testing.T) {
			bad, copy := p, b
			bad.Summary.Generation = &copy
			rule := "generation_tasks.frozen"
			switch change {
			case "schema":
				copy.Schema = 2
			case "role":
				copy.Role = "PRIVATE_PROMPT_TEXT"
			case "id":
				copy.ContextID = "PRIVATE_PROMPT_TEXT"
			case "price":
				copy.PriceHash = "PRIVATE_PROMPT_TEXT"
			case "manifest":
				copy.ManifestHash = ""
			case "partial recipe":
				copy.RecipeStepID = ""
			case "missing agent":
				copy.AgentVersionID = ""
				copy.ManifestHash = ""
			case "missing price":
				bad.Summary.Price = nil
			case "missing config":
				bad.Summary.ConfigHash = ""
			case "missing capability":
				bad.Summary.Capability = nil
			case "foreign rule":
				rule = "tool_trials.frozen"
			}
			if err := ValidateProjectionForRule(bad, rule); err == nil {
				t.Fatal("untyped or misbound summary accepted", change)
			}
		})
	}
	legacy := p
	legacy.Summary.Generation = nil
	if err := ValidateProjectionForRule(legacy, "generation_tasks.frozen"); err != nil {
		t.Fatal("old proof unnecessarily invalidated", err)
	}
}

func TestGenerationBindingPreservesOnlyValidNovelDetailIdentity(t *testing.T) {
	b := GenerationBindingSummary{Schema: 1, Role: "writer", PriceHash: strings.Repeat("a", 64), NovelDetail: &domain.DependencyRef{Kind: "resource", ID: strings.Repeat("b", 32), BranchID: strings.Repeat("c", 32), Revision: strings.Repeat("d", 32), Hash: strings.Repeat("e", 64)}}
	if !validGenerationBinding(b) {
		t.Fatal("valid planning pin rejected")
	}
	b.NovelDetail.Hash = "private story content"
	if validGenerationBinding(b) {
		t.Fatal("planning summary accepted non-hash content")
	}
}
