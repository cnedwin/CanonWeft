package qualification_test

import (
	"context"
	"testing"
)

func TestRetentionEvaluationCallWriterKeepsQualificationBalance(t *testing.T) {
	f, provider, input := modelFixture(t)
	installQualificationSourceFixture(t, f)
	ctx := context.Background()
	run, err := f.svc.Create(ctx, f.actor, f.project, input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Advance(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	run, err = f.svc.Read(ctx, f.actor, f.project, run.ID)
	if err != nil || run.Steps[0].ChildID == "" {
		t.Fatal("evaluation child absent", err)
	}
	if _, err := f.svc.Generations.Execute(ctx, run.Steps[0].ChildID); err != nil {
		t.Fatal(err)
	}
	var count int
	for _, state := range []string{"RESERVED", "DISPATCHED", "SUCCEEDED"} {
		if err := f.db.QueryRow("SELECT COUNT(*) FROM retention_reference_versions v JOIN retention_references r ON r.id=v.reference_id WHERE r.project_id=? AND r.owner_table='qualification_calls' AND r.field_key='accounting' AND JSON_UNQUOTE(JSON_EXTRACT(v.snapshot,'$.accounting.summary.state'))=?", f.project, state).Scan(&count); err != nil || count != 1 {
			t.Fatal("qualification call history missing or duplicated", state, count, err)
		}
	}
	if provider.calls.Load() != 1 {
		t.Fatal("capturing source evidence dispatched another provider call")
	}
	var charged, held int64
	if err := f.db.QueryRow("SELECT charged_micros,held_micros FROM qualification_calls WHERE step_id=?", run.Steps[0].ID).Scan(&charged, &held); err != nil || charged != 30 || held != 0 {
		t.Fatal("qualification accounting changed", charged, held, err)
	}
	if _, err := f.db.Exec("CREATE TRIGGER reject_metrics_proof BEFORE INSERT ON retention_reference_versions FOR EACH ROW BEGIN IF NEW.revision>1 AND EXISTS(SELECT 1 FROM retention_references WHERE id=NEW.reference_id AND owner_table='qualification_steps' AND field_key='metrics') THEN SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='test rejected metrics proof'; END IF; END"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = f.db.Exec("DROP TRIGGER IF EXISTS reject_metrics_proof") })
	if _, err := f.svc.Advance(ctx, run.ID); err == nil {
		t.Fatal("metrics committed without source proof")
	}
	var state, output string
	if err := f.db.QueryRow("SELECT state,COALESCE(JSON_UNQUOTE(JSON_EXTRACT(metrics,'$.output_hash')),'') FROM qualification_steps WHERE id=?", run.Steps[0].ID).Scan(&state, &output); err != nil || state != "RUNNING" || output != "" {
		t.Fatal("failed source write partially committed metrics", state, output, err)
	}
	if _, err := f.db.Exec("DROP TRIGGER reject_metrics_proof"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Advance(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	assertSemanticCaptured(t, f, "qualification_steps", run.Steps[0].ID, "metrics", 3)
	if provider.calls.Load() != 1 {
		t.Fatal("metrics retry resent an already settled evaluation")
	}
}
