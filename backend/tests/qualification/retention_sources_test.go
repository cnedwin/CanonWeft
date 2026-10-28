package qualification_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"novelstudio/internal/domain"
	"novelstudio/internal/models"
	"novelstudio/internal/retention"
	"novelstudio/internal/service"
)

// This fixture never enables the production migration/readiness gate.
func installQualificationSourceFixture(t *testing.T, f *fixture) {
	t.Helper()
	for _, name := range []string{"retention.sql.pending", "retention_identity.sql.pending", "retention_sources.sql.pending"} {
		raw, err := os.ReadFile("../../migrations/" + name)
		if err != nil {
			t.Fatal(err)
		}
		for _, statement := range strings.Split(string(raw), ";") {
			if strings.TrimSpace(statement) != "" {
				if _, err := f.db.Exec(statement); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	if _, err := f.db.Exec("INSERT INTO schema_migrations(version,name,checksum,origin) VALUES(36,?,?,'EXECUTED')", retention.MigrationName, strings.Repeat("0", 64)); err != nil {
		t.Fatal(err)
	}
}

func TestRetentionQualificationBindsFrozenCasesAndQualifiedOrigin(t *testing.T) {
	f, provider, input := modelFixture(t)
	ctx := context.Background()
	installQualificationSourceFixture(t, f)
	run, err := f.svc.Create(ctx, f.actor, f.project, input)
	if err != nil {
		t.Fatal(err)
	}
	assertSemanticCaptured(t, f, "qualification_runs", run.ID, "frozen", 2)
	run = drive(t, f, run.ID)
	if run.State != "PASSED" {
		t.Fatal(run.State)
	}
	assertSemanticCaptured(t, f, "qualification_runs", run.ID, "report_hash", len(run.Steps)+1)
	assertSemanticCaptured(t, f, "capability_qualifications", run.QualificationID, "payload", 2)
	binding, err := f.svc.Activate(ctx, f.actor, f.project, service.BindingInput{OperationID: "retention-binding", QualificationID: run.QualificationID, TaskKind: "shared.contract"})
	if err != nil {
		t.Fatal(err)
	}
	job, err := f.svc.Generations.Prepare(ctx, f.actor, f.project, service.GenerationInput{BindingID: binding.ID, TaskKind: "shared.contract", BranchID: f.branch, Role: "researcher", PriceID: input.PriceID, OperationID: "retention-qualified-task", AgentInput: json.RawMessage(`{"source":"保留资格来源"}`), Request: models.Request{Prompt: "生成候选", MaxOutputTokens: 128}})
	if err != nil {
		t.Fatal(err)
	}
	beforeCalls := provider.calls.Load()
	w := &service.Workspace{DB: f.db}
	if report, err := w.RegisterRetentionSources(ctx, f.actor.ID, f.project); err != nil || report.UnboundSources != 0 {
		tx, beginErr := f.db.BeginTx(ctx, nil)
		if beginErr == nil {
			defer tx.Rollback()
			_, detail := retention.RegisterSourceObservations(ctx, tx, f.ledger, f.project, f.svc.IP.Blobs.ReadObject)
			t.Log("internal source diagnostic", detail)
		}
		t.Fatal("qualification case source chain incomplete", report, err)
	}
	var snapshot string
	if err := f.db.QueryRow(`SELECT v.snapshot FROM retention_references r JOIN retention_reference_versions v ON v.reference_id=r.id AND v.revision=r.revision WHERE r.project_id=? AND r.owner_table='qualification_runs' AND r.field_key='frozen'`, f.project).Scan(&snapshot); err != nil {
		t.Fatal(err)
	}
	var proof retention.ReferenceVersion
	if json.Unmarshal([]byte(snapshot), &proof) != nil || len(proof.Reference.Sources) < 2 {
		t.Fatal("frozen evaluation lacks original capability sources")
	}
	var original string
	if err := f.db.QueryRow("SELECT frozen FROM qualification_runs WHERE id=?", run.ID).Scan(&original); err != nil {
		t.Fatal(err)
	}
	changed := run.Frozen
	changed.Suite.Cases[0].Description = "被替换的评测题目"
	raw, _ := json.Marshal(changed)
	if _, err := f.db.Exec("UPDATE qualification_runs SET frozen=? WHERE id=?", raw, run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := w.RegisterRetentionSources(ctx, f.actor.ID, f.project); !errors.Is(err, domain.ErrNotReady) {
		t.Fatal("substituted frozen case accepted", err)
	}
	if _, err := f.db.Exec("UPDATE qualification_runs SET frozen=? WHERE id=?", original, run.ID); err != nil {
		t.Fatal(err)
	}
	first := run.Steps[0]
	var originalMetrics string
	if err := f.db.QueryRow("SELECT metrics FROM qualification_steps WHERE id=?", first.ID).Scan(&originalMetrics); err != nil {
		t.Fatal(err)
	}
	var metrics service.EvaluationMetrics
	if json.Unmarshal([]byte(originalMetrics), &metrics) != nil {
		t.Fatal("invalid metrics fixture")
	}
	metrics.Output = "不来自真实回执的输出"
	raw, _ = json.Marshal(metrics)
	if _, err := f.db.Exec("UPDATE qualification_steps SET metrics=? WHERE id=?", raw, first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := w.RegisterRetentionSources(ctx, f.actor.ID, f.project); !errors.Is(err, domain.ErrNotReady) {
		t.Fatal("changed evaluation output accepted", err)
	}
	if _, err := f.db.Exec("UPDATE qualification_steps SET metrics=? WHERE id=?", originalMetrics, first.ID); err != nil {
		t.Fatal(err)
	}
	var anotherCase string
	if err := f.db.QueryRow("SELECT case_key FROM qualification_steps WHERE run_id=? AND case_key<>? LIMIT 1", run.ID, first.CaseID).Scan(&anotherCase); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec("UPDATE qualification_steps SET case_key=? WHERE id=?", anotherCase, first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := w.RegisterRetentionSources(ctx, f.actor.ID, f.project); !errors.Is(err, domain.ErrNotReady) {
		t.Fatal("request rebound to another heldout case", err)
	}
	if _, err := f.db.Exec("UPDATE qualification_steps SET case_key=? WHERE id=?", first.CaseID, first.ID); err != nil {
		t.Fatal(err)
	}
	var originalJob string
	if err := f.db.QueryRow("SELECT frozen FROM generation_tasks WHERE id=?", job.ID).Scan(&originalJob); err != nil {
		t.Fatal(err)
	}
	job.Frozen.Capability.QualificationID = service.NewID()
	raw, _ = json.Marshal(job.Frozen)
	if _, err := f.db.Exec("UPDATE generation_tasks SET frozen=? WHERE id=?", raw, job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := w.RegisterRetentionSources(ctx, f.actor.ID, f.project); !errors.Is(err, domain.ErrNotReady) {
		t.Fatal("unrelated qualification accepted", err)
	}
	if _, err := f.db.Exec("UPDATE generation_tasks SET frozen=? WHERE id=?", originalJob, job.ID); err != nil {
		t.Fatal(err)
	}
	if report, err := w.RegisterRetentionSources(ctx, f.actor.ID, f.project); err != nil || report.UnboundSources != 0 || report.UpdatedReferences != 0 || provider.calls.Load() != beforeCalls {
		t.Fatal("source replay changed tasks or history", report, err)
	}
}
