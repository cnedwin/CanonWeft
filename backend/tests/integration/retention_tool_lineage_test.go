package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"novelstudio/internal/domain"
	"novelstudio/internal/isolation"
	"novelstudio/internal/retention"
	"novelstudio/internal/service"
	"novelstudio/internal/storage"
)

func TestRetentionToolOutputPinsActualReceipt(t *testing.T) {
	runtime := os.Getenv("STUDIO_ISOLATION_ROOT")
	if runtime == "" {
		t.Skip("real isolated runtime required")
	}
	for _, tc := range []struct{ name, script, state string }{
		{"success", "print('{\"summary\":\"渡口记录\"}')\n", "SUCCEEDED"},
		{"invalid_contract", "print('{\"summary\":42}')\n", "FAILED"},
		{"invalid_utf8", "import sys\nsys.stdout.buffer.write(bytes([255]))\n", "FAILED"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, db := setup(t)
			project := createProject(b, "工具回执来源", "tool-output-lineage")
			tool := createTrialToolScript(b, project, tc.script, 10)
			root := "/api/projects/" + project
			job := b.request("POST", root+"/tool-trials", map[string]any{"operation_id": "receipt-lineage", "version_id": tool, "input_text": `{"source":"原始材料"}`}, 202)
			actor := b.request("GET", "/api/me", nil, 200)["id"].(string)
			ctx := context.Background()
			blobs := storage.LocalBlobs{Root: os.Getenv("STUDIO_BLOB_ROOT")}
			w := &service.Workspace{DB: db}
			svc := &service.ToolTrials{IP: &service.IP{Workspace: w, Blobs: blobs}, RuntimeRoot: runtime}
			installSourceRegistrationFixture(t, db)
			if state, err := svc.Execute(ctx, job["id"].(string)); err != nil || state != tc.state {
				t.Fatal("real guest did not produce expected receipt", state, err)
			}
			var captured int
			if err := db.QueryRow(`SELECT COUNT(*) FROM retention_references r JOIN retention_reference_versions v ON v.reference_id=r.id AND v.revision=r.revision WHERE r.project_id=? AND r.owner_table='tool_trials' AND r.field_key='output_hash' AND JSON_LENGTH(JSON_EXTRACT(v.snapshot,'$.reference.sources'))>=2`, project).Scan(&captured); err != nil || captured != 1 {
				t.Fatal("tool result did not capture original sources in its transaction", captured, err)
			}
			if err := (&service.Capabilities{IP: svc.IP}).Revoke(ctx, domain.Actor{ID: actor}, project, tool, "保留历史但停止使用"); err != nil {
				t.Fatal(err)
			}
			if report, err := w.RegisterRetentionSources(ctx, actor, project); err != nil || report.UnboundSources != 0 {
				t.Fatal("actual receipt source registration failed", report, err)
			}
			var snapshot, originalHash string
			if err := db.QueryRow(`SELECT v.snapshot FROM retention_references r JOIN retention_reference_versions v ON v.reference_id=r.id AND v.revision=r.revision WHERE r.project_id=? AND r.owner_table='tool_trials' AND r.field_key='output_hash'`, project).Scan(&snapshot); err != nil {
				t.Fatal(err)
			}
			var proof retention.ReferenceVersion
			if json.Unmarshal([]byte(snapshot), &proof) != nil || len(proof.Reference.Sources) < 2 {
				t.Fatal("receipt lacks original input and frozen sources")
			}
			if err := db.QueryRow("SELECT output_hash FROM tool_trials WHERE id=?", job["id"]).Scan(&originalHash); err != nil {
				t.Fatal(err)
			}
			original, err := blobs.ReadObject(ctx, project, originalHash)
			if err != nil {
				t.Fatal(err)
			}
			for _, field := range []string{"input", "image", "stdout", "stderr", "valid", "reason"} {
				var output service.ToolTrialOutput
				if err := json.Unmarshal(original, &output); err != nil {
					t.Fatal(err)
				}
				switch field {
				case "input":
					output.Result.InputHash = strings.Repeat("a", 64)
				case "image":
					output.Result.ImageHash = strings.Repeat("b", 64)
				case "stdout":
					output.Stdout = "被替换的展示文字"
				case "stderr":
					output.Stderr = "被替换的诊断文字"
				case "valid":
					output.Valid = !output.Valid
				case "reason":
					output.Result.Reason = "invented"
				}
				changed, _ := json.Marshal(output)
				object, err := blobs.PutObject(ctx, project, bytes.NewReader(changed))
				if err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec("UPDATE tool_trials SET output_hash=? WHERE id=?", object.Hash, job["id"]); err != nil {
					t.Fatal(err)
				}
				if _, err := w.RegisterRetentionSources(ctx, actor, project); !errors.Is(err, domain.ErrNotReady) {
					t.Fatal("substituted receipt accepted", field, err)
				}
			}
			if _, err := db.Exec("UPDATE tool_trials SET output_hash=? WHERE id=?", originalHash, job["id"]); err != nil {
				t.Fatal(err)
			}
			if report, err := w.RegisterRetentionSources(ctx, actor, project); err != nil || report.UnboundSources != 0 || report.UpdatedReferences != 0 {
				t.Fatal("receipt replay changed pinned history", report, err)
			}
		})
	}
}

func TestRetentionToolPinsFrozenPackageAndRequest(t *testing.T) {
	b, db := setup(t)
	project := createProject(b, "工具历史来源", "tool-lineage")
	tool := createTrialTool(b, project)
	root := "/api/projects/" + project
	job := b.request("POST", root+"/tool-trials", map[string]any{"operation_id": "tool-history", "version_id": tool, "input_text": `{"source":"渡口记录"}`}, 202)
	actor := b.request("GET", "/api/me", nil, 200)["id"].(string)
	// Revocation stops execution; it must not erase or rewrite the old source.
	b.request("POST", root+"/capabilities/"+tool+"/revoke", map[string]any{"reason": "停止使用，保留来源"}, 200)
	installSourceRegistrationFixture(t, db)
	w, ctx := &service.Workspace{DB: db}, context.Background()
	if report, err := w.RegisterRetentionSources(ctx, actor, project); err != nil || report.UnboundSources != 0 {
		t.Fatal(report, err)
	}
	var raw string
	if err := db.QueryRow(`SELECT v.snapshot FROM retention_references r JOIN retention_reference_versions v ON v.reference_id=r.id AND v.revision=r.revision WHERE r.project_id=? AND r.owner_table='tool_trials' AND r.field_key='frozen.input_hash'`, project).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var version retention.ReferenceVersion
	if json.Unmarshal([]byte(raw), &version) != nil || len(version.Reference.Sources) < 2 {
		t.Fatal("tool request lacks immutable capability/package sources", version.Reference.Sources)
	}
	var original string
	if err := db.QueryRow("SELECT frozen FROM tool_trials WHERE id=?", job["id"]).Scan(&original); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"version_hash", "manifest_hash", "package_hash", "image_hash"} {
		var frozen map[string]any
		if err := json.Unmarshal([]byte(original), &frozen); err != nil {
			t.Fatal(err)
		}
		frozen[field] = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		changed, _ := json.Marshal(frozen)
		if _, err := db.Exec("UPDATE tool_trials SET frozen=? WHERE id=?", changed, job["id"]); err != nil {
			t.Fatal(err)
		}
		if _, err := w.RegisterRetentionSources(ctx, actor, project); !errors.Is(err, domain.ErrNotReady) {
			t.Fatal("forged tool pin accepted", field, err)
		}
	}
	var frozen service.ToolTrialFrozen
	if err := json.Unmarshal([]byte(original), &frozen); err != nil {
		t.Fatal(err)
	}
	blobs := storage.LocalBlobs{Root: os.Getenv("STUDIO_BLOB_ROOT")}
	requestBytes, err := blobs.ReadObject(ctx, project, frozen.InputHash)
	var request isolation.Request
	if err != nil || json.Unmarshal(requestBytes, &request) != nil || len(request.Files) == 0 {
		t.Fatal("missing actual immutable request", err)
	}
	request.Files[0].Data = []byte("silently substituted file")
	changedRequest, _ := json.Marshal(request)
	object, err := blobs.PutObject(ctx, project, bytes.NewReader(changedRequest))
	if err != nil {
		t.Fatal(err)
	}
	frozen.InputHash = object.Hash
	changedFrozen, _ := json.Marshal(frozen)
	if _, err := db.Exec("UPDATE tool_trials SET frozen=? WHERE id=?", changedFrozen, job["id"]); err != nil {
		t.Fatal(err)
	}
	if _, err := w.RegisterRetentionSources(ctx, actor, project); !errors.Is(err, domain.ErrNotReady) {
		t.Fatal("substituted package file accepted", err)
	}
	if _, err := db.Exec("UPDATE tool_trials SET frozen=? WHERE id=?", original, job["id"]); err != nil {
		t.Fatal(err)
	}
	if report, err := w.RegisterRetentionSources(ctx, actor, project); err != nil || report.UpdatedReferences != 0 || report.UnboundSources != 0 {
		t.Fatal("replay changed original pins", report, err)
	}
	var state string
	if err := db.QueryRow("SELECT state FROM tool_trials WHERE id=?", job["id"]).Scan(&state); err != nil || state != "QUEUED" {
		t.Fatal("registration executed or changed tool task", state, err)
	}
}
