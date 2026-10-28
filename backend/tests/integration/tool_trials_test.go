package integration

import (
	"context"
	"database/sql"
	"github.com/go-sql-driver/mysql"
	"novelstudio/internal/service"
	"novelstudio/internal/storage"
	"os"
	"testing"
	"time"
)

func createTrialTool(b *browser, pid string) string {
	return createTrialToolScript(b, pid, "import json,sys\nfrom helper import result\nprint(json.dumps(result(json.load(sys.stdin)),ensure_ascii=False))\n", 10)
}

func TestToolTrialTemporaryDatabaseFailureDoesNotBecomeRevocation(t *testing.T) {
	b, db := setup(t)
	pid := createProject(b, "临时锁等待", "tool-transient")
	tool := createTrialTool(b, pid)
	job := b.request("POST", "/api/projects/"+pid+"/tool-trials", map[string]any{"operation_id": "transient-trial", "version_id": tool, "input_text": `{"source":"待检查"}`}, 202)
	actor := b.request("GET", "/api/me", nil, 200)["id"]
	blocker, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback()
	var held string
	if err = blocker.QueryRow("SELECT id FROM users WHERE id=? FOR UPDATE", actor).Scan(&held); err != nil {
		t.Fatal(err)
	}
	config, err := mysql.ParseDSN(os.Getenv("STUDIO_TEST_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	if config.Params == nil {
		config.Params = map[string]string{}
	}
	config.Params["innodb_lock_wait_timeout"] = "1"
	limited, err := sql.Open("mysql", config.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	defer limited.Close()
	svc := &service.ToolTrials{IP: &service.IP{Workspace: &service.Workspace{DB: limited}, Blobs: storage.LocalBlobs{Root: os.Getenv("STUDIO_BLOB_ROOT")}}, RuntimeRoot: "must-not-run"}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	state, err := svc.Execute(ctx, job["id"].(string))
	if err == nil || state == "BLOCKED" {
		t.Fatal("transient database failure classified as revocation", state, err)
	}
	var actual string
	if err = db.QueryRow("SELECT state FROM tool_trials WHERE id=?", job["id"]).Scan(&actual); err != nil || actual != "QUEUED" {
		t.Fatal("transient failure altered queued job", actual, err)
	}
}
func createTrialToolScript(b *browser, pid, script string, timeout int) string {
	root := "/api/projects/" + pid
	pkg := b.request("POST", root+"/skills", map[string]any{"operation_id": "tool-source", "markdown": "---\nname: source-tool\ndescription: Use when checking a source using approved inputs.\nlicense: LicenseRef-Private\n---\nOutput a JSON candidate only."}, 201)
	pkg = b.request("POST", root+"/skills/"+pkg["id"].(string)+"/edit", map[string]any{"operation_id": "tool-script", "expected_hash": pkg["report"].(map[string]any)["hash"], "changes": []any{map[string]any{"path": "scripts/main.py", "text": script}, map[string]any{"path": "helper.py", "text": "def result(value):\n return {'summary':'包内辅助方法:'+value['source']}\n"}}}, 201)
	d := capabilityDefinition("tool", "source-check-tool")
	d["package_id"] = pkg["id"]
	d["package_hash"] = pkg["report"].(map[string]any)["hash"]
	d["tool"] = map[string]any{"runtime": "linux-python", "entrypoint": "scripts/main.py", "timeout_seconds": timeout, "memory_mib": 256, "network": false}
	return b.request("POST", root+"/capabilities", map[string]any{"operation_id": "tool-capability", "definition": d}, 201)["id"].(string)
}

func TestToolTrialQueuesImmutableInputAndCancelsBeforeExecution(t *testing.T) {
	b, db := setup(t)
	pid := createProject(b, "真实工具试评", "tool-trial")
	tool := createTrialTool(b, pid)
	root := "/api/projects/" + pid + "/tool-trials"
	input := map[string]any{"operation_id": "evaluate-tool", "version_id": tool, "input_text": `{"source":"只读批准材料"}`}
	job := b.request("POST", root, input, 202)
	again := b.request("POST", root, input, 202)
	if job["id"] != again["id"] || job["state"] != "QUEUED" {
		t.Fatal("tool trial duplicate or implicit execution")
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM outbox WHERE kind='tool.start' AND resource_id=?", job["id"]).Scan(&count); err != nil || count != 1 {
		t.Fatal("missing transactional scheduling", err, count)
	}
	input["input_text"] = `{"source":"改动输入"}`
	b.request("POST", root, input, 409)
	input["operation_id"] = "bad-input"
	input["input_text"] = `{"source":4}`
	b.request("POST", root, input, 400)
	cancelled := b.request("POST", root+"/"+job["id"].(string)+"/cancel", map[string]any{}, 200)
	if cancelled["state"] != "CANCELLED" {
		t.Fatal("cancel did not persist")
	}
	svc := &service.ToolTrials{IP: &service.IP{Workspace: &service.Workspace{DB: db}, Blobs: storage.LocalBlobs{Root: os.Getenv("STUDIO_BLOB_ROOT")}}, RuntimeRoot: "missing-runtime-must-not-be-used"}
	state, err := svc.Execute(context.Background(), job["id"].(string))
	if err != nil || state != "CANCELLED" {
		t.Fatal("cancelled trial reached runner", state, err)
	}
	other := createProject(b, "另一项目", "other-tool-project")
	b.request("GET", "/api/projects/"+other+"/tool-trials/"+job["id"].(string), nil, 403)
}

func TestToolTrialRechecksRevocationBeforeGuestLaunch(t *testing.T) {
	b, db := setup(t)
	pid := createProject(b, "撤销工具", "revoke-tool-trial")
	tool := createTrialTool(b, pid)
	root := "/api/projects/" + pid
	job := b.request("POST", root+"/tool-trials", map[string]any{"operation_id": "queued-tool", "version_id": tool, "input_text": `{"source":"待检查"}`}, 202)
	b.request("POST", root+"/capabilities/"+tool+"/revoke", map[string]any{"reason": "停止此工具版本"}, 200)
	svc := &service.ToolTrials{IP: &service.IP{Workspace: &service.Workspace{DB: db}, Blobs: storage.LocalBlobs{Root: os.Getenv("STUDIO_BLOB_ROOT")}}, RuntimeRoot: "missing-runtime-must-not-be-used"}
	state, err := svc.Execute(context.Background(), job["id"].(string))
	if err != nil || state != "BLOCKED" {
		t.Fatal("revoked trial started", state, err)
	}
	result := b.request("GET", root+"/tool-trials/"+job["id"].(string), nil, 200)
	if result["reason"] != "能力版本或来源已变更、撤销，未执行工具" || result["output"] != nil {
		t.Fatal("revocation did not precede runtime lookup", result)
	}
}
