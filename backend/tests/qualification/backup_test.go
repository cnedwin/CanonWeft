package qualification_test

import (
	"context"
	"database/sql"
	"novelstudio/internal/backup"
	"novelstudio/internal/service"
	"novelstudio/internal/storage"
	"path/filepath"
	"testing"
	"time"
)

func TestQualificationReportRestoresAsEvidenceWithAllAuthorityDisabled(t *testing.T) {
	f, transport, input := modelFixture(t)
	ctx := context.Background()
	quote, err := f.svc.Quote(ctx, f.actor, f.project, input)
	if err != nil {
		t.Fatal(err)
	}
	schedule, r := weeklyRun(t, f, input, quote.Frozen.Estimate+10000)
	r = drive(t, f, r.ID)
	binding, err := f.svc.Activate(ctx, f.actor, f.project, service.BindingInput{OperationID: service.NewID(), QualificationID: r.QualificationID, TaskKind: "shared.contract"})
	if err != nil {
		t.Fatal(err)
	}
	// Keep a genuine issued-but-unknown second run alongside the immutable report.
	input.ExpectedBinding = binding.ID
	transport.lost = true
	schedule, err = f.svc.SaveSchedule(ctx, f.actor, f.project, service.QualificationScheduleInput{Input: input, Enabled: true, BudgetLimit: quote.Frozen.Estimate + 10000, Revision: schedule.Revision})
	if err != nil {
		t.Fatal(err)
	}
	if err = f.svc.Schedule(ctx, schedule.ID, service.QualificationWeek(time.Now())); err != nil {
		t.Fatal(err)
	}
	runs, err := f.svc.List(ctx, f.actor, f.project)
	if err != nil {
		t.Fatal(err)
	}
	var unknown service.QualificationRun
	for _, run := range runs {
		if run.ID != r.ID {
			unknown = drive(t, f, run.ID)
		}
	}
	if unknown.State != "BLOCKED" || unknown.Steps[0].State != "UNKNOWN" {
		t.Fatal("unknown receipt fixture not created", unknown.State)
	}
	// The shared week row now has a newer cap/revision than both historical runs.
	// Lowering the cap never erases the old issued UNKNOWN hold or spend.
	if _, err = f.svc.SaveSchedule(ctx, f.actor, f.project, service.QualificationScheduleInput{Input: input, Enabled: false, BudgetLimit: 0, Revision: schedule.Revision}); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	archive := filepath.Join(root, "qualification.zip")
	if _, err = backup.Create(ctx, f.db, f.svc.IP.Blobs.(storage.LocalBlobs), archive); err != nil {
		t.Fatal(err)
	}
	if err = f.svc.RevokeVersion(ctx, f.actor, f.project, r.QualificationID, "revoked after snapshot"); err != nil {
		t.Fatal(err)
	}
	config := f.config.Clone()
	config.DBName = "studio_restore_" + service.NewID()
	t.Cleanup(func() {
		if _, err := f.admin.Exec("DROP DATABASE IF EXISTS `" + config.DBName + "`"); err != nil {
			t.Error(err)
		}
	})
	opt := backup.RestoreOptions{ArchivePath: archive, TargetDSN: config.FormatDSN(), BlobRoot: filepath.Join(root, "restored-blobs"), Authority: backup.ProtectedAuthority{DB: f.admin, Ledger: f.ledger}}
	if _, err = backup.Restore(ctx, opt); err != nil {
		t.Fatal(err)
	}
	restored, err := sql.Open("mysql", config.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	var enabled bool
	var state string
	if err = restored.QueryRow("SELECT enabled FROM capability_bindings WHERE id=?", binding.ID).Scan(&enabled); err != nil || enabled {
		t.Fatal("restored binding active", err)
	}
	if err = restored.QueryRow("SELECT enabled FROM capability_qualifications WHERE id=?", r.QualificationID).Scan(&enabled); err != nil || enabled {
		t.Fatal("restored qualification active", err)
	}
	if err = restored.QueryRow("SELECT state FROM qualification_runs WHERE id=?", r.ID).Scan(&state); err != nil || state != "CANCELLED" {
		t.Fatal("restored evaluation resumable", state, err)
	}
	var revoked int
	if err = restored.QueryRow("SELECT COUNT(*) FROM capability_revocations WHERE version_id=?", input.VersionID).Scan(&revoked); err != nil || revoked != 1 {
		t.Fatal("current protected revocation not replayed", revoked, err)
	}
	if _, err = (storage.LocalBlobs{Root: opt.BlobRoot}).ReadObject(ctx, f.project, r.ReportHash); err != nil {
		t.Fatal("qualification evidence report lost", err)
	}
	var held, spent int64
	if err = restored.QueryRow("SELECT enabled,reserved_micros,spent_micros FROM qualification_week_grants WHERE project_id=?", f.project).Scan(&enabled, &held, &spent); err != nil || enabled || held <= 0 || spent != 540 {
		t.Fatal("restored weekly authority active or financial history lost", enabled, held, spent, err)
	}
	var childState string
	var childHeld int64
	if err = restored.QueryRow("SELECT state,held_micros FROM qualification_calls WHERE run_id=?", unknown.ID).Scan(&childState, &childHeld); err != nil || childState != "UNKNOWN" || childHeld != held {
		t.Fatal("restored unknown weekly reservation lost", childState, childHeld, held, err)
	}
}
