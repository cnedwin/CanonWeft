package qualification_test

import (
	"context"
	"encoding/json"
	"testing"

	"novelstudio/internal/retention"
	"novelstudio/internal/service"
)

// Reuse the already executed real VM evaluation. Faulting only the approval
// transaction must not run any of its eighteen children again.
func approveWithRetainedSources(t *testing.T, f *fixture, result service.QualificationRun) service.QualificationRun {
	t.Helper()
	ctx := context.Background()
	installQualificationSourceFixture(t, f)
	semanticSources(t, f)
	var toolsBefore int
	if err := f.db.QueryRow("SELECT COUNT(*) FROM tool_trials WHERE project_id=?", f.project).Scan(&toolsBefore); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec("CREATE TRIGGER reject_approval_source BEFORE INSERT ON retention_reference_versions FOR EACH ROW BEGIN IF JSON_UNQUOTE(JSON_EXTRACT(NEW.snapshot,'$.reference.rule'))='qualification_approvals.payload' THEN SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='test rejected executable approval proof'; END IF; END"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = f.db.Exec("DROP TRIGGER IF EXISTS reject_approval_source") })
	note := "Reviewed fixed runtime, dependency hashes and actual normal/negative isolation receipts"
	if _, err := f.svc.ApproveExecutable(ctx, f.actor, f.project, result.ID, result.ReportHash, note); err == nil {
		t.Fatal("unproven approval was committed")
	}
	for _, table := range []string{"qualification_approvals", "capability_qualifications"} {
		var count int
		if err := f.db.QueryRow("SELECT COUNT(*) FROM "+table+" WHERE run_id=?", result.ID).Scan(&count); err != nil || count != 0 {
			t.Fatal("approval failure left executable authority", table, count, err)
		}
	}
	var state, qualificationID string
	if err := f.db.QueryRow("SELECT state,qualification_id FROM qualification_runs WHERE id=?", result.ID).Scan(&state, &qualificationID); err != nil || state != "REVIEW_REQUIRED" || qualificationID != "" {
		t.Fatal("approval failure changed run", state, qualificationID, err)
	}
	if _, err := f.db.Exec("DROP TRIGGER reject_approval_source"); err != nil {
		t.Fatal(err)
	}
	approved, err := f.svc.ApproveExecutable(ctx, f.actor, f.project, result.ID, result.ReportHash, note)
	if err != nil {
		t.Fatal("approval retry", err)
	}
	owner, err := retention.RowOwnerKey("qualification_approvals", map[string]string{"run_id": result.ID})
	if err != nil {
		t.Fatal(err)
	}
	var raw string
	if err := f.db.QueryRow(`SELECT v.snapshot FROM retention_references r JOIN retention_reference_versions v ON v.reference_id=r.id AND v.revision=r.revision WHERE r.project_id=? AND r.owner_table='qualification_approvals' AND r.owner_key=? AND r.field_key='payload'`, f.project, owner).Scan(&raw); err != nil {
		t.Fatal("approval not captured at save", err)
	}
	var version retention.ReferenceVersion
	if json.Unmarshal([]byte(raw), &version) != nil || len(version.Reference.Sources) != 2 || version.Reference.ContentRoot {
		t.Fatal("approval source pins incomplete", version)
	}
	for _, source := range version.Reference.Sources {
		if source.Revision < 1 {
			t.Fatal("unresolved approval source", source)
		}
	}
	var toolsAfter int
	if err := f.db.QueryRow("SELECT COUNT(*) FROM tool_trials WHERE project_id=?", f.project).Scan(&toolsAfter); err != nil || toolsAfter != toolsBefore {
		t.Fatal("approval retry created extra execution", toolsBefore, toolsAfter, err)
	}
	return approved
}
