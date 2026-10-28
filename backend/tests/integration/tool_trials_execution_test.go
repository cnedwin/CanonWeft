package integration

import (
	"context"
	"database/sql"
	"novelstudio/internal/service"
	"novelstudio/internal/storage"
	platformworker "novelstudio/internal/worker"
	"os"
	"strings"
	"testing"
	"time"
)

func toolTrialRuntime(t *testing.T, db *sql.DB) (*service.ToolTrials, *platformworker.Runtime) {
	t.Helper()
	root := os.Getenv("STUDIO_ISOLATION_ROOT")
	if root == "" {
		t.Skip("isolated runtime required; full verification enables it")
	}
	ip := &service.IP{Workspace: &service.Workspace{DB: db}, Blobs: storage.LocalBlobs{Root: os.Getenv("STUDIO_BLOB_ROOT")}}
	runtime, err := platformworker.StartRuntime(context.Background(), &service.Tasks{IP: ip}, os.Getenv("STUDIO_TEMPORAL"), "studio-tool-integration")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runtime.Close)
	return &service.ToolTrials{IP: ip, RuntimeRoot: root}, runtime
}

func waitToolTrial(t *testing.T, b *browser, root, id string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(100 * time.Second)
	for time.Now().Before(deadline) {
		job := b.request("GET", root+"/tool-trials/"+id, nil, 200)
		if job["state"] != "QUEUED" && job["state"] != "RUNNING" {
			return job
		}
		time.Sleep(300 * time.Millisecond)
	}
	t.Fatal("tool trial did not finish")
	return nil
}

func TestToolTrialRealOutboxTemporalGuestAndRevokedResult(t *testing.T) {
	if os.Getenv("STUDIO_ISOLATION_ROOT") == "" {
		t.Skip("isolated runtime required")
	}
	b, db := setup(t)
	pid := createProject(b, "完整包工具执行", "tool-full")
	tool := createTrialTool(b, pid)
	root := "/api/projects/" + pid
	job := b.request("POST", root+"/tool-trials", map[string]any{"operation_id": "real-tool", "version_id": tool, "input_text": `{"source":"中文输入"}`}, 202)
	toolTrialRuntime(t, db)
	result := waitToolTrial(t, b, root, job["id"].(string))
	if result["state"] != "SUCCEEDED" {
		t.Fatalf("real execution failed: %#v", result)
	}
	output := result["output"].(map[string]any)
	if output["valid"] != true || !strings.Contains(output["stdout"].(string), "包内辅助方法:中文输入") {
		t.Fatal("package/input not honored", output)
	}
	// A successful trial is evidence only; it never changes capability qualification.
	manifest := b.request("GET", root+"/capabilities/"+tool+"/manifest", nil, 200)
	if manifest["qualified"] == true {
		t.Fatal("trial self-qualified")
	}
	b.request("POST", root+"/capabilities/"+tool+"/revoke", map[string]any{"reason": "方法已撤回"}, 200)
	stale := b.request("GET", root+"/tool-trials/"+job["id"].(string), nil, 200)
	if stale["stale"] != true || stale["output"] != nil {
		t.Fatal("revoked output remained reusable", stale)
	}
}

func TestToolTrialInvalidGuestOutputCannotQualify(t *testing.T) {
	if os.Getenv("STUDIO_ISOLATION_ROOT") == "" {
		t.Skip("isolated runtime required")
	}
	b, db := setup(t)
	pid := createProject(b, "不合格工具输出", "invalid-tool")
	tool := createTrialToolScript(b, pid, "print('{\"summary\":42,\"qualified\":true}')\n", 10)
	root := "/api/projects/" + pid
	job := b.request("POST", root+"/tool-trials", map[string]any{"operation_id": "invalid-output", "version_id": tool, "input_text": `{"source":"批准输入"}`}, 202)
	toolTrialRuntime(t, db)
	result := waitToolTrial(t, b, root, job["id"].(string))
	if result["state"] != "FAILED" || result["output"].(map[string]any)["valid"] != false {
		t.Fatal("unvalidated output accepted", result)
	}
}

func TestToolTrialActiveCancellationAndRevocationDiscardResults(t *testing.T) {
	if os.Getenv("STUDIO_ISOLATION_ROOT") == "" {
		t.Skip("isolated runtime required")
	}
	for _, action := range []string{"cancel", "revoke"} {
		t.Run(action, func(t *testing.T) {
			b, db := setup(t)
			pid := createProject(b, "执行中撤回工具", "active-tool")
			tool := createTrialToolScript(b, pid, "import time\ntime.sleep(25)\nprint('{\"summary\":\"late\"}')\n", 30)
			root := "/api/projects/" + pid
			job := b.request("POST", root+"/tool-trials", map[string]any{"operation_id": "active-tool-trial", "version_id": tool, "input_text": `{"source":"批准输入"}`}, 202)
			ip := &service.IP{Workspace: &service.Workspace{DB: db}, Blobs: storage.LocalBlobs{Root: os.Getenv("STUDIO_BLOB_ROOT")}}
			svc := &service.ToolTrials{IP: ip, RuntimeRoot: os.Getenv("STUDIO_ISOLATION_ROOT")}
			ctx, cancel := context.WithTimeout(context.Background(), 80*time.Second)
			defer cancel()
			type outcome struct {
				state string
				err   error
			}
			done := make(chan outcome, 1)
			go func() { state, err := svc.Execute(ctx, job["id"].(string)); done <- outcome{state, err} }()
			for attempts := 0; attempts < 100; attempts++ {
				var state string
				if err := db.QueryRow("SELECT state FROM tool_trials WHERE id=?", job["id"]).Scan(&state); err != nil {
					t.Fatal(err)
				}
				if state == "RUNNING" {
					break
				}
				if attempts == 99 {
					t.Fatal("trial never started")
				}
				time.Sleep(100 * time.Millisecond)
			}
			// Allow boot and begin a long-running guest; cancellation must not wait for its result.
			time.Sleep(9 * time.Second)
			started := time.Now()
			expected := "CANCELLED"
			if action == "cancel" {
				b.request("POST", root+"/tool-trials/"+job["id"].(string)+"/cancel", map[string]any{}, 200)
			} else {
				expected = "BLOCKED"
				b.request("POST", root+"/capabilities/"+tool+"/revoke", map[string]any{"reason": "停止正在执行的工具"}, 200)
			}
			select {
			case final := <-done:
				if final.err != nil || final.state != expected {
					t.Fatal(final)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("revocation/cancel failed to stop running guest")
			}
			if time.Since(started) > 10*time.Second {
				t.Fatal("watchdog cancellation exceeded limit")
			}
			final := b.request("GET", root+"/tool-trials/"+job["id"].(string), nil, 200)
			if final["output"] != nil {
				t.Fatal("late result retained", final)
			}
		})
	}
}
