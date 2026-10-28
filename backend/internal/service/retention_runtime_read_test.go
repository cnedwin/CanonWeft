package service

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"novelstudio/internal/domain"
	"novelstudio/internal/retention"
)

type retainedRuntimeTestRow struct{ raw []byte }

func (r retainedRuntimeTestRow) Scan(values ...any) error {
	for _, value := range values {
		if raw, ok := value.(*[]byte); ok {
			*raw = r.raw
		}
	}
	return nil
}

func TestExpiredRuntimePayloadCannotDecodeIntoEmptyExecutableConfig(t *testing.T) {
	for _, table := range []string{"generation_tasks", "qualification_runs", "tool_trials", "semantic_review_runs", "recipe_runs"} {
		t.Run(table, func(t *testing.T) {
			at := time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)
			raw := []byte(`{"input_hash":"` + strings.Repeat("a", 64) + `"}`)
			projection, err := retention.ProjectExpired(retention.Expiry{ReferenceID: strings.Repeat("b", 64), Rule: table + ".frozen", PayloadHash: digest(string(raw)), Revision: 1, Deadline: at, Storage: retention.Inline}, raw, retention.EvidenceSummary{InputHash: strings.Repeat("a", 64)}, at)
			if table == "recipe_runs" {
				projection = retention.ExpiredProjection{Schema: 1, State: retention.Expired, Materialization: retention.InlineEnvelope, ReferenceID: strings.Repeat("b", 64), OriginalPayloadHash: digest(string(raw)), ExpiredAt: at,
					Summary: retention.EvidenceSummary{RecipeRun: &retention.RecipeRunSummary{ID: strings.Repeat("c", 32), DefinitionID: strings.Repeat("d", 32), DefinitionHash: strings.Repeat("e", 64), ManifestHash: strings.Repeat("f", 64), InputHash: strings.Repeat("a", 64)}}}
				err = retention.ValidateProjectionForRule(projection, "recipe_runs.frozen")
			}
			if err != nil {
				t.Fatal(err)
			}
			saved, _ := json.Marshal(projection)
			row := retainedRuntimeTestRow{raw: saved}
			switch table {
			case "generation_tasks":
				_, err = scanGeneration(row)
			case "qualification_runs":
				_, err = scanQualificationRun(row)
			case "tool_trials":
				_, err = scanToolTrial(row)
			case "semantic_review_runs":
				_, err = scanSemanticReview(row)
			case "recipe_runs":
				_, err = scanRecipe(row)
			}
			if !errors.Is(err, domain.ErrMaterialExpired) {
				t.Fatal("expired proof decoded into a runnable zero-value frozen configuration")
			}
			alias := []byte(strings.Replace(string(saved), `"state":"EXPIRED"`, `"STATE":"EXPIRED"`, 1))
			if err := rejectExpiredRuntime(alias, table+".frozen"); !errors.Is(err, domain.ErrNotReady) {
				t.Fatal("noncanonical envelope accepted", err)
			}
		})
	}
}
