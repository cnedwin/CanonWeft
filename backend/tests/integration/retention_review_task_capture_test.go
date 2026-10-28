package integration

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"

	"novelstudio/internal/authority"
	"novelstudio/internal/domain"
	"novelstudio/internal/retention"
	"novelstudio/internal/service"
	"novelstudio/internal/storage"
)

func TestRetentionReviewTaskPinsInputAndCancelsWithoutBody(t *testing.T) {
	b, db := setup(t)
	project := createProject(b, "审查任务来源", "review-source-project")
	branchID := branch(b, project, "正史")
	candidate := propose(b, project, branchID, "", "review-source-candidate", originalSnapshot())
	actor := domain.Actor{ID: b.request("GET", "/api/me", nil, 200)["id"].(string)}
	svc := &service.Tasks{IP: &service.IP{Workspace: &service.Workspace{DB: db}, Blobs: storage.LocalBlobs{Root: os.Getenv("STUDIO_BLOB_ROOT")}}}
	ctx := context.Background()
	installSourceRegistrationFixture(t, db)
	task, err := svc.StartReview(ctx, actor, project, branchID, candidate, "review-source-first")
	if err != nil {
		t.Fatal(err)
	}
	input := capturedVersion(t, db, project, "platform_tasks", task.ID, "input_hash")
	candidateProof := capturedVersion(t, db, project, "ip_revisions", candidate, "payload")
	pinned := false
	for _, source := range input.Reference.Sources {
		if source.ReferenceID == candidateProof.Reference.ID && source.Revision == candidateProof.Reference.Revision {
			pinned = true
		}
	}
	if !pinned || input.Reference.ContentRoot {
		t.Fatal("review did not pin its original candidate", input)
	}
	if state, err := svc.Inspect(ctx, task.ID); err != nil || state != "WAITING_ADOPTION" {
		t.Fatal("review inspect", state, err)
	}
	current, err := svc.Read(ctx, actor, project, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	reason := capturedVersion(t, db, project, "platform_tasks", task.ID, "reason")
	if _, err := db.Exec("CREATE TRIGGER reject_review_reason BEFORE INSERT ON retention_reference_versions FOR EACH ROW BEGIN IF JSON_UNQUOTE(JSON_EXTRACT(NEW.snapshot,'$.reference.rule'))='platform_tasks.reason' THEN SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='test rejected review reason'; END IF; END"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.Exec("DROP TRIGGER IF EXISTS reject_review_reason") })
	if _, err := svc.Control(ctx, actor, project, task.ID, "pause", current.Revision); err == nil {
		t.Fatal("unrecorded pause committed")
	}
	after, err := svc.Read(ctx, actor, project, task.ID)
	if err != nil || after.State != current.State || after.Revision != current.Revision {
		t.Fatal("partial review control", after, err)
	}
	if _, err := db.Exec("DROP TRIGGER reject_review_reason"); err != nil {
		t.Fatal(err)
	}
	second, err := svc.StartReview(ctx, actor, project, branchID, candidate, "review-source-second")
	if err != nil {
		t.Fatal(err)
	}
	// Point only this test service to an empty store: SQL source records remain,
	// but the actual input file is unavailable to a running inspection.
	svc.IP.Blobs = storage.LocalBlobs{Root: t.TempDir()}
	if state, err := svc.Inspect(ctx, task.ID); err != nil || state != "BLOCKED" {
		t.Fatal("missing original body diagnostic", state, err)
	}
	current, err = svc.Read(ctx, actor, project, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Cancellation must not invoke any blob reader, even for provenance.
	svc.IP.Blobs = nil
	cancelled, err := svc.Control(ctx, actor, project, task.ID, "cancel", current.Revision)
	if err != nil || cancelled.State != "CANCELLED" {
		t.Fatal("cannot cancel without original body", cancelled, err)
	}
	final := capturedVersion(t, db, project, "platform_tasks", task.ID, "reason")
	if final.Reference.Revision <= reason.Reference.Revision {
		t.Fatal("missing subsequent diagnostic history")
	}
	retained := capturedVersion(t, db, project, "platform_tasks", task.ID, "input_hash")
	if retained.Reference.Revision != input.Reference.Revision || !slices.Equal(retained.Reference.Sources, input.Reference.Sources) || !retained.Reference.FirstVerifiedAt.Equal(input.Reference.FirstVerifiedAt) {
		t.Fatal("control rewrote original input proof")
	}
	if _, err := db.Exec("UPDATE users SET enabled=FALSE WHERE id=?", actor.ID); err != nil {
		t.Fatal(err)
	}
	if state, err := svc.Inspect(ctx, task.ID); err != nil || state != "CANCELLED" {
		t.Fatal("revocation replaced terminal cancellation", state, err)
	}
	if state, err := svc.Inspect(ctx, second.ID); err != nil || state != "BLOCKED" {
		t.Fatal("revoked actor kept running", state, err)
	}
	capturedVersion(t, db, project, "platform_tasks", second.ID, "reason")
}

func TestRetentionDiagnosticCaptureCannotSuppressFullInputValidation(t *testing.T) {
	b, db := setup(t)
	project := createProject(b, "说明与输入隔离", "diagnostic-boundary-project")
	branchID := branch(b, project, "正史")
	candidate := propose(b, project, branchID, "", "diagnostic-boundary-candidate", originalSnapshot())
	actor := domain.Actor{ID: b.request("GET", "/api/me", nil, 200)["id"].(string)}
	svc := &service.Tasks{IP: &service.IP{Workspace: &service.Workspace{DB: db}, Blobs: storage.LocalBlobs{Root: os.Getenv("STUDIO_BLOB_ROOT")}}}
	ctx := context.Background()
	installSourceRegistrationFixture(t, db)
	task, err := svc.StartReview(ctx, actor, project, branchID, candidate, "diagnostic-boundary-review")
	if err != nil {
		t.Fatal(err)
	}
	before := capturedVersion(t, db, project, "platform_tasks", task.ID, "input_hash")
	if _, err := db.Exec("UPDATE platform_tasks SET input_hash=?,reason='已发现原输入缺失' WHERE id=?", strings.Repeat("0", 64), task.ID); err != nil {
		t.Fatal(err)
	}
	capture := func(keys ...retention.SourceKey) error {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if err := retention.CaptureSourceValues(ctx, tx, authority.Ledger{Schema: os.Getenv("STUDIO_AUTHORITY_SCHEMA")}, project, keys...); err != nil {
			return err
		}
		return tx.Commit()
	}
	full := retention.SourceKey{Table: "platform_tasks", Primary: map[string]string{"id": task.ID}}
	diagnostic := full
	diagnostic.DiagnosticsOnly = true
	if err := capture(diagnostic); err != nil {
		t.Fatal("scalar diagnostic depends on corrupt input", err)
	}
	for _, keys := range [][]retention.SourceKey{{diagnostic, full}, {full, diagnostic}} {
		if err := capture(keys...); err == nil {
			t.Fatal("diagnostic selection bypassed full source validation")
		}
	}
	if err := capture(retention.SourceKey{Table: "projects", Primary: map[string]string{"id": project}, DiagnosticsOnly: true}); err == nil {
		t.Fatal("non-diagnostic content accepted as debug text")
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM retention_source_values v JOIN retention_source_slots s ON s.id=v.source_id WHERE s.project_id=? AND s.owner_table='platform_tasks' AND s.field_key='input_hash'", project).Scan(&count); err != nil || count != 1 {
		t.Fatal("diagnostic capture observed changed input", count, err)
	}
	after := capturedVersion(t, db, project, "platform_tasks", task.ID, "input_hash")
	if after.Reference.Revision != before.Reference.Revision || after.Reference.PayloadHash != before.Reference.PayloadHash {
		t.Fatal("diagnostic capture certified corrupt input")
	}
}
