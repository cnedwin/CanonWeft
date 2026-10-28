package integration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"novelstudio/internal/backup"
	"novelstudio/internal/domain"
	"novelstudio/internal/retention"
	"novelstudio/internal/service"
	"novelstudio/internal/storage"
)

func TestRetentionJobExpiresToolResultWithoutErasingInput(t *testing.T) {
	runtime := os.Getenv("STUDIO_ISOLATION_ROOT")
	if runtime == "" {
		t.Skip("real isolated runtime required")
	}
	b, db := setup(t)
	project := createProject(b, "工具结果清理", "tool-result-expiry")
	version := createTrialToolScript(b, project, "print('{\"summary\":\"PRIVATE_TOOL_RESULT\"}')\n", 10)
	response := b.request("POST", "/api/projects/"+project+"/tool-trials", map[string]any{"operation_id": "result-expiry", "version_id": version, "input_text": `{"source":"preserved input"}`}, 202)
	id := response["id"].(string)
	actor := domain.Actor{ID: b.request("GET", "/api/me", nil, 200)["id"].(string)}
	ctx := context.Background()
	blobs := storage.LocalBlobs{Root: os.Getenv("STUDIO_BLOB_ROOT")}
	workspace := &service.Workspace{DB: db}
	svc := &service.ToolTrials{IP: &service.IP{Workspace: workspace, Blobs: blobs}, RuntimeRoot: runtime}
	if state, err := svc.Execute(ctx, id); err != nil || state != "SUCCEEDED" {
		t.Fatal("real tool did not finish", state, err)
	}
	before, err := svc.Read(ctx, actor, project, id)
	if err != nil || before.Output == nil {
		t.Fatal("original tool result unreadable", err)
	}
	var hash string
	if err := db.QueryRow("SELECT output_hash FROM tool_trials WHERE id=?", id).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	installCompleteRetentionFixture(t, db)
	key, _ := retention.RowOwnerKey("tool_trials", map[string]string{"id": id})
	ref, _ := retention.ReferenceID(project, "tool_trials", key, "output_hash")
	payload := sha256.Sum256([]byte(hash))
	if _, err := db.Exec("INSERT INTO retention_source_slots(id,project_id,owner_table,owner_key,field_key,time_basis,first_verified_at) VALUES(?,?,'tool_trials',?,'output_hash','LEGACY_REGISTERED','2020-01-02 03:04:05.000006')", ref, project, key); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO retention_source_values(source_id,payload_hash,is_null,first_verified_at) VALUES(?,?,FALSE,'2020-01-02 03:04:05.000006')", ref, hex.EncodeToString(payload[:])); err != nil {
		t.Fatal(err)
	}
	store, err := storage.NewLocalRetentionStore(os.Getenv("STUDIO_BLOB_ROOT"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	scanner := &service.RetentionScanner{Workspace: workspace, Blobs: store}
	job, err := scanner.PlanJob(ctx, actor.ID, project, "request-v2:"+service.NewID())
	if err != nil || job.Expiring != 1 || job.RemovableObjects != 1 {
		t.Fatal("only old result should expire", job, err)
	}
	if _, err := scanner.StartJob(ctx, actor.ID, project, job.ID, job.PlanHash); err != nil {
		t.Fatal(err)
	}
	if state, err := scanner.AdvanceJob(ctx, job.ID); err != nil || state != "DETACHING" {
		t.Fatal("output reference did not expire", state, err)
	}
	if _, err := store.ReadObject(ctx, project, hash); err != nil {
		t.Fatal("test missed interval before physical deletion", err)
	}
	if _, err := svc.Read(ctx, actor, project, id); err != domain.ErrMaterialExpired {
		t.Fatal("expired output still readable before deletion", err)
	}
	finishPhysicalJob(t, scanner, job.ID)
	if _, err := store.ReadObject(ctx, project, hash); err == nil {
		t.Fatal("expired output file still exists")
	}
	if _, err := store.ReadObject(ctx, project, before.Frozen.InputHash); err != nil {
		t.Fatal("unexpired input was erased", err)
	}
	if _, err := backup.Create(ctx, db, store, filepath.Join(t.TempDir(), "expired-tool.zip")); err != nil {
		t.Fatal("archive still requires erased output", err)
	}
}
