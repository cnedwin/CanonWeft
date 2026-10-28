package qualification_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"novelstudio/internal/backup"
	"novelstudio/internal/domain"
	"novelstudio/internal/retention"
	"novelstudio/internal/service"
	"novelstudio/internal/storage"
)

func completeQualificationRetentionFixture(t *testing.T, f *fixture) {
	t.Helper()
	installQualificationSourceFixture(t, f)
	var text strings.Builder
	for _, name := range []string{"retention.sql.pending", "retention_identity.sql.pending", "retention_sources.sql.pending"} {
		raw, err := os.ReadFile(filepath.Join("../../migrations", name))
		if err != nil {
			t.Fatal(err)
		}
		text.WriteString(strings.TrimRight(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n"))
		text.WriteString("\n")
	}
	hash := sha256.Sum256([]byte(text.String()))
	checksum := hex.EncodeToString(hash[:])
	if _, err := f.db.Exec("UPDATE schema_migrations SET checksum=? WHERE version=36", checksum); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec("UPDATE schema_version SET version=36,checksum=?,history_initialized=TRUE WHERE id=1", checksum); err != nil {
		t.Fatal(err)
	}
}

func ageQualificationMaterial(t *testing.T, f *fixture, table, field, id string) {
	t.Helper()
	if len(retention.TransientColumns(table+"."+field)) == 0 {
		t.Fatal("unknown field")
	}
	primary := "id"
	if table == "qualification_approvals" {
		primary = "run_id"
	}
	var raw string
	if err := f.db.QueryRow("SELECT "+field+" FROM "+table+" WHERE "+primary+"=?", id).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	key, _ := retention.RowOwnerKey(table, map[string]string{primary: id})
	ref, _ := retention.ReferenceID(f.project, table, key, field)
	hash := sha256.Sum256([]byte(raw))
	if _, err := f.db.Exec("INSERT INTO retention_source_slots(id,project_id,owner_table,owner_key,field_key,time_basis,first_verified_at) VALUES(?,?,?,?,?,'LEGACY_REGISTERED','2020-01-02 03:04:05.000006')", ref, f.project, table, key, field); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec("INSERT INTO retention_source_values(source_id,payload_hash,is_null,first_verified_at) VALUES(?,?,FALSE,'2020-01-02 03:04:05.000006')", ref, hex.EncodeToString(hash[:])); err != nil {
		t.Fatal(err)
	}
}

func TestRetentionExecutableApprovalExpiresWithoutRepeatingTools(t *testing.T) {
	runtime := os.Getenv("STUDIO_ISOLATION_ROOT")
	if runtime == "" {
		t.Skip("actual isolated tool runtime required")
	}
	f := isolated(t)
	f.svc.RuntimeRoot = runtime
	f.svc.ToolTrials = &service.ToolTrials{IP: f.svc.IP, RuntimeRoot: runtime}
	ctx := context.Background()
	presets, err := f.svc.InstallBaselines(ctx, f.actor, f.project, service.QualificationPresetInput{SuiteID: "shared-contract-v1", Kind: "tool"})
	if err != nil {
		t.Fatal(err)
	}
	run, err := f.svc.Create(ctx, f.actor, f.project, service.QualificationInput{OperationID: service.NewID(), VersionID: presets.BaselineID, BaselineID: presets.BaselineID, BranchID: f.branch, SuiteID: "shared-contract-v1", MaxOutputTokens: 512})
	if err != nil {
		t.Fatal(err)
	}
	run = drive(t, f, run.ID)
	if run.State != "REVIEW_REQUIRED" {
		t.Fatal(run.State, run.Reason)
	}
	if _, err = f.svc.ApproveExecutable(ctx, f.actor, f.project, run.ID, run.ReportHash, "PRIVATE_APPROVAL_NOTE"); err != nil {
		t.Fatal(err)
	}
	completeQualificationRetentionFixture(t, f)
	ageQualificationMaterial(t, f, "qualification_approvals", "payload", run.ID)
	var toolsBefore int
	if err = f.db.QueryRow("SELECT COUNT(*) FROM tool_trials WHERE project_id=?", f.project).Scan(&toolsBefore); err != nil {
		t.Fatal(err)
	}
	store, err := storage.NewLocalRetentionStore(os.Getenv("STUDIO_BLOB_ROOT"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	scanner := &service.RetentionScanner{Workspace: f.svc.IP.Workspace, Blobs: store}
	job, err := scanner.PlanJob(ctx, f.actor.ID, f.project, "request-v2:"+service.NewID())
	if err != nil || job.Expiring != 1 || job.RemovableObjects != 0 {
		t.Fatal(job, err)
	}
	if _, err = scanner.StartJob(ctx, f.actor.ID, f.project, job.ID, job.PlanHash); err != nil {
		t.Fatal(err)
	}
	state := ""
	for i := 0; i < 8 && state != "COMPLETE"; i++ {
		state, err = scanner.AdvanceJob(ctx, job.ID)
		if err != nil || state == "BLOCKED" {
			t.Fatal(state, err)
		}
	}
	if state != "COMPLETE" {
		t.Fatal("approval cleanup incomplete", state)
	}
	var raw string
	var toolsAfter int
	if err = f.db.QueryRow("SELECT payload FROM qualification_approvals WHERE run_id=?", run.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, "PRIVATE_APPROVAL_NOTE") {
		t.Fatal("approval note retained")
	}
	if _, err = retention.ParseSourceProjection([]byte(raw), "qualification_approvals.payload"); err != nil {
		t.Fatal(err)
	}
	if err = f.db.QueryRow("SELECT COUNT(*) FROM tool_trials WHERE project_id=?", f.project).Scan(&toolsAfter); err != nil || toolsAfter != toolsBefore {
		t.Fatal("tools repeated", err)
	}
	if _, err = backup.Create(ctx, f.db, store, filepath.Join(t.TempDir(), "expired-approval.zip")); err != nil {
		t.Fatal(err)
	}
}

func TestRetentionEvaluationMaterialsExpireWithoutReexecutingOrLosingCharges(t *testing.T) {
	for _, field := range []string{"payload", "metrics"} {
		t.Run(field, func(t *testing.T) {
			f, provider, input := modelFixture(t)
			ctx := context.Background()
			run, err := f.svc.Create(ctx, f.actor, f.project, input)
			if err != nil {
				t.Fatal(err)
			}
			run = drive(t, f, run.ID)
			if run.State != "PASSED" {
				t.Fatal(run.State)
			}
			if _, err := f.svc.Activate(ctx, f.actor, f.project, service.BindingInput{OperationID: "before-expiry", QualificationID: run.QualificationID, TaskKind: "shared.contract"}); err != nil {
				t.Fatal(err)
			}
			callsBefore := provider.calls.Load()
			var reservedBefore, spentBefore int64
			if err := f.db.QueryRow("SELECT reserved_micros,spent_micros FROM model_budgets WHERE workspace_id=?", f.actor.WorkspaceID).Scan(&reservedBefore, &spentBefore); err != nil {
				t.Fatal(err)
			}
			completeQualificationRetentionFixture(t, f)
			table, id, want := "capability_qualifications", run.QualificationID, 1
			if field == "metrics" {
				table, id, want = "qualification_steps", run.Steps[0].ID, 4
			}
			ageQualificationMaterial(t, f, table, field, id)
			store, err := storage.NewLocalRetentionStore(os.Getenv("STUDIO_BLOB_ROOT"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			scanner := &service.RetentionScanner{Workspace: f.svc.IP.Workspace, Blobs: store}
			job, err := scanner.PlanJob(ctx, f.actor.ID, f.project, "request-v2:"+service.NewID())
			if err != nil || job.Expiring != want {
				t.Fatal("unexpected dependent expiry closure", job, err)
			}
			if _, err := scanner.StartJob(ctx, f.actor.ID, f.project, job.ID, job.PlanHash); err != nil {
				t.Fatal(err)
			}
			state := ""
			for i := 0; i < 20 && state != "COMPLETE"; i++ {
				state, err = scanner.AdvanceJob(ctx, job.ID)
				if err != nil || state == "BLOCKED" {
					t.Fatal("cleanup", state, err)
				}
			}
			if state != "COMPLETE" {
				t.Fatal("cleanup did not finish", state)
			}
			if _, err := f.svc.Activate(ctx, f.actor, f.project, service.BindingInput{OperationID: "expired", QualificationID: run.QualificationID, TaskKind: "shared.contract"}); err != domain.ErrMaterialExpired {
				t.Fatal("expired qualification activated", err)
			}
			bindings, err := f.svc.Bindings(ctx, f.actor, f.project)
			if err != nil || len(bindings) != 1 || bindings[0].Valid || bindings[0].Qualification == nil || bindings[0].Qualification.Retention == nil {
				t.Fatal("expired binding history unavailable or usable", bindings, err)
			}
			if field == "metrics" {
				if _, err := f.svc.Read(ctx, f.actor, f.project, run.ID); err != domain.ErrMaterialExpired {
					t.Fatal("erased report reused", err)
				}
			}
			var reservedAfter, spentAfter int64
			if err := f.db.QueryRow("SELECT reserved_micros,spent_micros FROM model_budgets WHERE workspace_id=?", f.actor.WorkspaceID).Scan(&reservedAfter, &spentAfter); err != nil {
				t.Fatal(err)
			}
			if reservedBefore != reservedAfter || spentBefore != spentAfter || callsBefore != provider.calls.Load() {
				t.Fatal("cleanup changed money or reissued calls")
			}
			if _, err := backup.Create(ctx, f.db, store, filepath.Join(t.TempDir(), "expired-evaluation.zip")); err != nil {
				t.Fatal("actual archive", err)
			}
		})
	}
}

func TestRetentionKeepsLiveScheduleConfigurationDespiteOldAge(t *testing.T) {
	f, provider, input := modelFixture(t)
	ctx := context.Background()
	schedule, err := f.svc.SaveSchedule(ctx, f.actor, f.project, service.QualificationScheduleInput{Input: input, Enabled: false, BudgetLimit: 101})
	if err != nil {
		t.Fatal(err)
	}
	completeQualificationRetentionFixture(t, f)
	ageQualificationMaterial(t, f, "qualification_schedules", "config", schedule.ID)
	store, err := storage.NewLocalRetentionStore(os.Getenv("STUDIO_BLOB_ROOT"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	scanner := &service.RetentionScanner{Workspace: f.svc.IP.Workspace, Blobs: store}
	job, err := scanner.PlanJob(ctx, f.actor.ID, f.project, "request-v2:"+service.NewID())
	if err != nil || job.Expiring != 0 {
		t.Fatal("active project configuration treated as temporary", job, err)
	}
	read, err := f.svc.ReadSchedule(ctx, f.actor, f.project)
	if err != nil || read.BudgetLimit != 101 || provider.calls.Load() != 0 {
		t.Fatal("schedule changed or dispatched", err)
	}
}
