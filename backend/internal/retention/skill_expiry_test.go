package retention

import (
	"encoding/json"
	"novelstudio/internal/domain"
	"strings"
	"testing"
	"time"
)

func TestSkillExpiryKeepsOnlyBoundPackageIdentities(t *testing.T) {
	for _, field := range []string{"definition", "report", "source"} {
		t.Run(field, func(t *testing.T) {
			rows := authorContentFixture()
			row := rows[len(rows)-2]
			if field == "definition" {
				row = rows[len(rows)-1]
			}
			summary, err := TransientEvidence(row, field)
			if err != nil {
				t.Fatal("typed skill handler", err)
			}
			at := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
			raw := []byte(row.Values[field])
			proof, err := ProjectExpired(Expiry{ReferenceID: strings.Repeat("a", 64), Rule: row.Table + "." + field, Revision: 1, PayloadHash: financialDigest(raw), Storage: Inline, Deadline: at}, raw, summary, at)
			if err != nil || validateTransientOwner(row, field, proof) != nil {
				t.Fatal("valid skill proof", err)
			}
			encoded, _ := json.Marshal(proof)
			if strings.Contains(string(encoded), "先规划") || strings.Contains(string(encoded), "story-skill") {
				t.Fatal("skill text leaked")
			}
			for _, col := range TransientColumns(row.Table + "." + field) {
				old := row.Values[col]
				row.Values[col] = old + "x"
				if validateTransientOwner(row, field, proof) == nil {
					t.Fatal("changed scope accepted", col)
				}
				row.Values[col] = old
			}
		})
	}
}

func TestRetiredRecipeLayoutRetainsRelationsWithoutInstructions(t *testing.T) {
	agent := strings.Repeat("a", 32)
	definition := domain.CapabilityDefinition{Kind: "recipe", Instructions: "PRIVATE_PROMPT", Steps: []domain.RecipeStep{
		{Key: "private-draft", AgentID: agent, WaitForConfirmation: true},
		{Key: "private-review", AgentID: agent, After: []string{"private-draft"}},
	}}
	summary := capabilityContentSummary(definition)
	encoded, _ := json.Marshal(summary)
	if !validCapabilityContentExpiry(summary) || strings.Contains(string(encoded), "PRIVATE_PROMPT") || strings.Contains(string(encoded), "private-draft") || strings.Contains(string(encoded), "private-review") {
		t.Fatal("invalid or text-bearing retired layout")
	}
	if !summary.RecipeSteps[0].WaitForConfirmation || summary.RecipeSteps[1].AfterHashes[0] != summary.RecipeSteps[0].KeyHash {
		t.Fatal("confirmation or dependency lost")
	}
	summary.RecipeSteps[0].AfterHashes = []string{summary.RecipeSteps[1].KeyHash}
	if validCapabilityContentExpiry(summary) {
		t.Fatal("cyclic retired layout accepted")
	}
	summary = capabilityContentSummary(definition)
	summary.RecipeSteps[1].AfterHashes = append(summary.RecipeSteps[1].AfterHashes, summary.RecipeSteps[1].AfterHashes[0])
	if validCapabilityContentExpiry(summary) {
		t.Fatal("duplicate dependency accepted")
	}
	summary = capabilityContentSummary(definition)
	summary.AgentRole = "writer"
	if validCapabilityContentExpiry(summary) {
		t.Fatal("mixed agent and recipe summary accepted")
	}
	if validCapabilityContentExpiry(&CapabilityContentExpirySummary{AgentRole: "arbitrary-private-prompt"}) {
		t.Fatal("unbounded role text accepted")
	}
}
