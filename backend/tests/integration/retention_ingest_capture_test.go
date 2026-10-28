package integration

import (
	"context"
	"testing"

	"novelstudio/internal/domain"
	"novelstudio/internal/service"
)

func TestRetentionImportAndIngestCaptureRollbackAndResume(t *testing.T) {
	b, db := setup(t)
	project := createProject(b, "上传来源记录", "ingest-capture")
	branchID := branch(b, project, "正史")
	actor := domain.Actor{ID: b.request("GET", "/api/me", nil, 200)["id"].(string)}
	jobs := &service.IngestJobs{IP: propagationService(t, db)}
	ctx := context.Background()
	installSourceRegistrationFixture(t, db)
	source, err := jobs.IP.ImportSource(ctx, actor, project, branchID, "原始知识.md", "import", []byte("未采用的原始资料，不应成为正史。"))
	if err != nil {
		t.Fatal(err)
	}
	capturedVersion(t, db, project, "source_imports", source.ID, "source_hash")
	report := capturedVersion(t, db, project, "source_imports", source.ID, "report")
	if len(report.Reference.Sources) != 2 || report.Reference.ContentRoot {
		t.Fatal("import report source pins or authority", report)
	}
	job, err := jobs.Create(ctx, actor, project, branchID, "章节资料.txt", "upload", []byte("待整理的专业知识与人物背景。"))
	if err != nil {
		t.Fatal(err)
	}
	before := capturedVersion(t, db, project, "ingest_jobs", job.ID, "progress")
	file := capturedVersion(t, db, project, "ingest_jobs", job.ID, "source_hash")
	if _, err := db.Exec("CREATE TRIGGER reject_ingest_report BEFORE INSERT ON retention_reference_versions FOR EACH ROW BEGIN IF JSON_UNQUOTE(JSON_EXTRACT(NEW.snapshot,'$.reference.rule'))='source_imports.report' THEN SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='test rejected import report'; END IF; END"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.Exec("DROP TRIGGER IF EXISTS reject_ingest_report") })
	if _, err := jobs.Advance(ctx, job.ID); err == nil {
		t.Fatal("import completed without report provenance")
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM source_imports WHERE id=?", job.ID).Scan(&count); err != nil || count != 0 {
		t.Fatal("partial import saved", count, err)
	}
	current, err := jobs.Read(ctx, actor, project, branchID, job.ID)
	if err != nil || current.State != "QUEUED" || current.Revision != job.Revision {
		t.Fatal("progress partially advanced", current, err)
	}
	if _, err := db.Exec("DROP TRIGGER reject_ingest_report"); err != nil {
		t.Fatal(err)
	}
	if state, err := jobs.Advance(ctx, job.ID); err != nil || state != "COMPLETED" {
		t.Fatal(state, err)
	}
	after := capturedVersion(t, db, project, "ingest_jobs", job.ID, "progress")
	if after.Reference.Revision != before.Reference.Revision+1 || len(after.Reference.Sources) != 2 {
		t.Fatal("progress history incomplete", after)
	}
	capturedVersion(t, db, project, "source_imports", job.ID, "report")
	retained := capturedVersion(t, db, project, "ingest_jobs", job.ID, "source_hash")
	if retained.Reference.Revision != file.Reference.Revision || !retained.Reference.FirstVerifiedAt.Equal(file.Reference.FirstVerifiedAt) {
		t.Fatal("parsing renewed uploaded source")
	}
	if state, err := jobs.Advance(ctx, job.ID); err != nil || state != "COMPLETED" {
		t.Fatal("completed replay", state, err)
	}
	if report, err := jobs.IP.Workspace.RegisterRetentionSources(ctx, actor.ID, project); err != nil || report.UnboundSources != 0 {
		t.Fatal("import inventory", report, err)
	}
	cancel, err := jobs.Create(ctx, actor, project, branchID, "取消.txt", "cancel-upload", []byte("应保留取消操作"))
	if err != nil {
		t.Fatal(err)
	}
	jobs.IP.Blobs = nil
	if _, err := jobs.Control(ctx, actor, project, branchID, cancel.ID, "cancel", cancel.Revision); err != nil {
		t.Fatal("cancellation read missing body", err)
	}
}
