package integration

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"novelstudio/internal/domain"
	"novelstudio/internal/retrieval"
	"novelstudio/internal/service"
	platformworker "novelstudio/internal/worker"
)

func TestIngestJobEmptyAndOversizedWordReportsTerminate(t *testing.T) {
	b, db := setup(t)
	project := createProject(b, "Word 边界", "ingest-word-boundaries")
	branchID := branch(b, project, "正史")
	var actor domain.Actor
	if err := db.QueryRow("SELECT id FROM users WHERE role='owner' LIMIT 1").Scan(&actor.ID); err != nil {
		t.Fatal(err)
	}
	jobs := &service.IngestJobs{IP: propagationService(t, db)}
	for _, oversized := range []bool{false, true} {
		var data bytes.Buffer
		writer := zip.NewWriter(&data)
		file, err := writer.Create("word/document.xml")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = file.Write([]byte("<document><body><p/></body></document>")); err != nil {
			t.Fatal(err)
		}
		op := "empty-word"
		if oversized {
			op = "oversized-word"
			file, err = writer.Create("word/header" + strings.Repeat("a", 440) + ".xml")
			if err != nil {
				t.Fatal(err)
			}
			if _, err = file.Write([]byte("<document>" + strings.Repeat("<ins/>", 4000) + "</document>")); err != nil {
				t.Fatal(err)
			}
		}
		if err = writer.Close(); err != nil {
			t.Fatal(err)
		}
		ctx := context.Background()
		job, err := jobs.Create(ctx, actor, project, branchID, "资料.docx", op, data.Bytes())
		if err != nil {
			t.Fatal(err)
		}
		want := "COMPLETED"
		if oversized {
			want = "FAILED"
		}
		for attempt := 0; attempt < 2; attempt++ {
			if state, err := jobs.Advance(ctx, job.ID); err != nil || state != want {
				t.Fatal(op, state, err)
			}
		}
		if !oversized {
			source, err := jobs.IP.ReadImport(ctx, actor, project, branchID, job.ID)
			if err != nil || source.Report.Complete || source.Report.Pending != 1 || source.Report.Text != "" {
				t.Fatal(source, err)
			}
		}
		original, err := jobs.IP.Blobs.ReadObject(ctx, project, job.Hash)
		if err != nil || !bytes.Equal(original, data.Bytes()) {
			t.Fatal("original changed", err)
		}
	}
}

func TestIngestJobPersistsUploadPauseResumeAndOriginalReport(t *testing.T) {
	b, db := setup(t)
	p := createProject(b, "后台摄取", "ingest-project")
	branch := branch(b, p, "正史")
	var actor domain.Actor
	if err := db.QueryRow("SELECT id FROM users WHERE role='owner' LIMIT 1").Scan(&actor.ID); err != nil {
		t.Fatal(err)
	}
	jobs := &service.IngestJobs{IP: propagationService(t, db)}
	ctx := context.Background()
	j, err := jobs.Create(ctx, actor, p, branch, "素材.md", "ingest-once", []byte("星河🌙的原始记录"))
	if err != nil || j.State != "QUEUED" {
		t.Fatal(j, err)
	}
	again, err := jobs.Create(ctx, actor, p, branch, "素材.md", "ingest-once", []byte("星河🌙的原始记录"))
	if err != nil || again.ID != j.ID {
		t.Fatal("upload duplicated", err)
	}
	if _, err := jobs.Create(ctx, actor, p, branch, "素材.md", "ingest-once", []byte("不同上传")); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("idempotency collision", err)
	}
	paused, err := jobs.Control(ctx, actor, p, branch, j.ID, "pause", j.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if state, err := jobs.Advance(ctx, j.ID); err != nil || state != "PAUSED" {
		t.Fatal(state, err)
	}
	if _, err = jobs.Control(ctx, actor, p, branch, j.ID, "resume", paused.Revision); err != nil {
		t.Fatal(err)
	}
	if state, err := jobs.Advance(ctx, j.ID); err != nil || state != "COMPLETED" {
		t.Fatal(state, err)
	}
	if state, err := jobs.Advance(ctx, j.ID); err != nil || state != "COMPLETED" {
		t.Fatal("completed replay changed", state, err)
	}
	source, err := jobs.IP.ReadImport(ctx, actor, p, branch, j.ID)
	if err != nil || source.Report.Text != "星河🌙的原始记录" || !source.Report.Complete {
		t.Fatal(source, err)
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM source_imports WHERE id=?", j.ID).Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM artifact_releases WHERE project_id=?", p).Scan(&count); err != nil || count != 0 {
		t.Fatal("upload adopted itself", err)
	}
	draftPath := "/api/projects/" + p + "/branches/" + branch + "/imports/" + j.ID + "/draft"
	draft := b.request("GET", draftPath, nil, 200)
	content := draft["content"].(map[string]any)
	content["text"] = "用户还没确认的改稿🌙"
	saved := b.request("PUT", draftPath, draft, 200)
	if saved["revision"] != float64(1) {
		t.Fatal(saved)
	}
	if retry := b.request("PUT", draftPath, draft, 200); retry["revision"] != float64(1) {
		t.Fatal(retry)
	}
	content["text"] = "另一页的过时输入"
	b.request("PUT", draftPath, draft, 409)
	if restored := b.request("GET", draftPath, nil, 200); restored["content"].(map[string]any)["text"] != "用户还没确认的改稿🌙" {
		t.Fatal(restored)
	}
	other := createProject(b, "其他书", "ingest-other")
	ob := branchForIngest(b, other)
	if _, err := jobs.Read(ctx, actor, other, ob, j.ID); !errors.Is(err, domain.ErrForbidden) {
		t.Fatal("cross project task read", err)
	}
}

func TestIngestJobTemporalRestartContinuesAfterCompletedPage(t *testing.T) {
	b, db := setup(t)
	p := createProject(b, "分页面恢复", "ingest-restart")
	branchID := branch(b, p, "正史")
	var actor domain.Actor
	if err := db.QueryRow("SELECT id FROM users WHERE role='owner' LIMIT 1").Scan(&actor.ID); err != nil {
		t.Fatal(err)
	}
	t.Setenv("STUDIO_INGEST_OCR", "disabled")
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "tools", "ingest", "tests", "fixtures", "mixed.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	ip := propagationService(t, db)
	jobs := &service.IngestJobs{IP: ip}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	j, err := jobs.Create(ctx, actor, p, branchID, "mixed.pdf", "restart-pages", data)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if state, err := jobs.Advance(ctx, j.ID); err != nil || state != "RUNNING" {
			t.Fatal(state, err)
		}
	}
	j, err = jobs.Read(ctx, actor, p, branchID, j.ID)
	if err != nil || len(j.Progress.Units) != 1 || j.Revision != 3 {
		t.Fatal(j, err)
	}
	first := j.Progress.Units[0]
	j, err = jobs.Control(ctx, actor, p, branchID, j.ID, "pause", j.Revision)
	if err != nil {
		t.Fatal(err)
	}
	namespace := "ingest-" + service.NewID()
	address := os.Getenv("STUDIO_TEMPORAL")
	runtime, err := platformworker.StartRuntime(ctx, &service.Tasks{IP: ip}, address, namespace)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		var delivered int
		if err := db.QueryRow("SELECT COUNT(*) FROM outbox WHERE kind='ingest.start' AND resource_id=? AND processed_at IS NOT NULL", j.ID).Scan(&delivered); err != nil {
			t.Fatal(err)
		}
		if delivered == 1 {
			break
		}
		if time.Now().After(deadline) {
			runtime.Close()
			t.Fatal("outbox not delivered")
		}
		time.Sleep(50 * time.Millisecond)
	}
	runtime.Close()
	runtime, err = platformworker.StartRuntime(ctx, &service.Tasks{IP: ip}, address, namespace)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	if _, err = jobs.Control(ctx, actor, p, branchID, j.ID, "resume", j.Revision); err != nil {
		t.Fatal(err)
	}
	for {
		j, err = jobs.Read(ctx, actor, p, branchID, j.ID)
		if err != nil {
			t.Fatal(err)
		}
		if j.State == "COMPLETED" {
			break
		}
		if ctx.Err() != nil {
			t.Fatal("restart never completed", j.State)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if j.Revision != 6 || len(j.Progress.Units) != 2 || j.Progress.Units[0] != first || j.Progress.Units[1].Status != "pending" {
		t.Fatal("completed page lost or repeated", j)
	}
	source, err := ip.ReadImport(ctx, actor, p, branchID, j.ID)
	if err != nil || source.Report.Complete || source.Report.Extracted != 1 || source.Report.Pending != 1 {
		t.Fatal(source, err)
	}
}

type heldIngestParser struct {
	entered chan struct{}
	release chan struct{}
}

func (p heldIngestParser) Plan(c context.Context, n string, d []byte, o string) (retrieval.ImportPlan, error) {
	return retrieval.PlanImport(c, n, d, o)
}
func (p heldIngestParser) Unit(c context.Context, n string, d []byte, plan retrieval.ImportPlan, i int, o string) (retrieval.ImportUnit, error) {
	unit, err := retrieval.ExtractImportUnit(c, n, d, plan, i, o)
	close(p.entered)
	select {
	case <-p.release:
	case <-c.Done():
		return unit, c.Err()
	}
	return unit, err
}
func TestIngestJobLatePageCannotCrossPauseResumeRevision(t *testing.T) {
	b, db := setup(t)
	project := createProject(b, "晚到页面", "ingest-late")
	branchID := branch(b, project, "正史")
	var actor domain.Actor
	if err := db.QueryRow("SELECT id FROM users WHERE role='owner' LIMIT 1").Scan(&actor.ID); err != nil {
		t.Fatal(err)
	}
	t.Setenv("STUDIO_INGEST_OCR", "disabled")
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "tools", "ingest", "tests", "fixtures", "mixed.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	held := heldIngestParser{make(chan struct{}), make(chan struct{})}
	jobs := &service.IngestJobs{IP: propagationService(t, db), Parser: held}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	j, err := jobs.Create(ctx, actor, project, branchID, "mixed.pdf", "late-page", data)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = jobs.Advance(ctx, j.ID); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, e := jobs.Advance(ctx, j.ID); done <- e }()
	select {
	case <-held.entered:
	case <-ctx.Done():
		t.Fatal("page did not reach save boundary")
	}
	j, err = jobs.Read(ctx, actor, project, branchID, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	j, err = jobs.Control(ctx, actor, project, branchID, j.ID, "pause", j.Revision)
	if err != nil {
		t.Fatal(err)
	}
	j, err = jobs.Control(ctx, actor, project, branchID, j.ID, "resume", j.Revision)
	if err != nil {
		t.Fatal(err)
	}
	close(held.release)
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	j, err = jobs.Read(ctx, actor, project, branchID, j.ID)
	if err != nil || len(j.Progress.Units) != 0 || j.Revision != 4 {
		t.Fatal("late page crossed revision fence", j, err)
	}
}

func branchForIngest(b *browser, p string) string { return branch(b, p, "正史") }

func TestIngestJobCancellationAndDeletedProjectDoNotParse(t *testing.T) {
	b, db := setup(t)
	p := createProject(b, "撤销导入", "ingest-cancel")
	branch := branch(b, p, "正史")
	var actor domain.Actor
	if err := db.QueryRow("SELECT id FROM users WHERE role='owner' LIMIT 1").Scan(&actor.ID); err != nil {
		t.Fatal(err)
	}
	jobs := &service.IngestJobs{IP: propagationService(t, db)}
	ctx := context.Background()
	j, err := jobs.Create(ctx, actor, p, branch, "素材.pdf", "cancel-before-parse", []byte("invalid PDF should never parse"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = jobs.Control(ctx, actor, p, branch, j.ID, "cancel", j.Revision); err != nil {
		t.Fatal(err)
	}
	if state, err := jobs.Advance(ctx, j.ID); err != nil || state != "CANCELLED" {
		t.Fatal(state, err)
	}
	second, err := jobs.Create(ctx, actor, p, branch, "素材.pdf", "delete-before-parse", []byte("invalid PDF"))
	if err != nil {
		t.Fatal(err)
	}
	b.request("DELETE", "/api/projects/"+p, map[string]any{"confirm_name": "撤销导入", "revision": 1, "operation_id": "delete-ingest"}, 200)
	if state, err := jobs.Advance(ctx, second.ID); err != nil || state != "CANCELLED" {
		t.Fatal(state, err)
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM source_imports WHERE project_id=?", p).Scan(&count); err != nil || count != 0 {
		t.Fatal("cancelled parse committed", err)
	}
}
