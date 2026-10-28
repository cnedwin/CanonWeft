package integration

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"novelstudio/internal/domain"
	"novelstudio/internal/service"
)

func verifyPlanSourceLineage(t *testing.T, b *browser, db *sql.DB, project, id string) {
	t.Helper()
	actor := b.request("GET", "/api/me", nil, 200)["id"].(string)
	installSourceRegistrationFixture(t, db)
	w, ctx := &service.Workspace{DB: db}, context.Background()
	if report, err := w.RegisterRetentionSources(ctx, actor, project); err != nil || report.UnboundSources != 0 {
		t.Fatal("plan history lacks source proof", report, err)
	}
	var original, originalHash string
	if err := db.QueryRow("SELECT payload,content_hash FROM change_plans WHERE id=?", id).Scan(&original, &originalHash); err != nil {
		t.Fatal(err)
	}
	save := func(plan domain.ChangePlan) {
		t.Helper()
		raw, _ := json.Marshal(plan)
		hash := sha256.Sum256(raw)
		if _, err := db.Exec("UPDATE change_plans SET payload=?,content_hash=? WHERE id=?", raw, fmt.Sprintf("%x", hash), id); err != nil {
			t.Fatal(err)
		}
	}
	for _, field := range []string{"difference", "graph_link", "graph_frozen", "impact"} {
		var plan domain.ChangePlan
		if json.Unmarshal([]byte(original), &plan) != nil || plan.GraphSnapshot == nil || len(plan.Differences) == 0 || len(plan.Items) == 0 || len(plan.GraphSnapshot.Links) == 0 {
			t.Fatal("fixture lacks actual frozen impact graph")
		}
		switch field {
		case "difference":
			plan.Differences[0].Fields[0].Before = "invented original text"
		case "graph_link":
			plan.GraphSnapshot.Links[0].Reason = "invented dependency reason"
		case "graph_frozen":
			for key, value := range plan.GraphSnapshot.Frozen {
				plan.GraphSnapshot.Frozen[key] = !value
				break
			}
		case "impact":
			plan.Items[0].Action = "IGNORE"
		}
		// Rehash the altered public graph too: its content must still agree
		// with original resource policies and exact source versions.
		graphRaw, _ := json.Marshal(plan.GraphSnapshot)
		plan.GraphHash = fmt.Sprintf("%x", sha256.Sum256(graphRaw))
		save(plan)
		if _, err := w.RegisterRetentionSources(ctx, actor, project); !errors.Is(err, domain.ErrNotReady) {
			t.Fatal("self-consistent but substituted plan accepted", field, err)
		}
	}
	var legacy domain.ChangePlan
	if err := json.Unmarshal([]byte(original), &legacy); err != nil {
		t.Fatal(err)
	}
	legacy.GraphSnapshot = nil
	save(legacy)
	if report, err := w.RegisterRetentionSources(ctx, actor, project); err != nil || report.UnboundSources != 1 {
		t.Fatal("legacy digest-only graph was invented or lost", report, err)
	}
	if _, err := db.Exec("UPDATE change_plans SET payload=?,content_hash=? WHERE id=?", original, originalHash, id); err != nil {
		t.Fatal(err)
	}
	if report, err := w.RegisterRetentionSources(ctx, actor, project); err != nil || report.UnboundSources != 0 {
		t.Fatal("restored plan cannot recover original proofs", report, err)
	}
	if report, err := w.RegisterRetentionSources(ctx, actor, project); err != nil || report.UpdatedReferences != 0 || report.UnboundSources != 0 {
		t.Fatal("plan source replay changed history", report, err)
	}
}
