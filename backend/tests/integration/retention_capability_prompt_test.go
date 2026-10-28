package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"novelstudio/internal/domain"
	"novelstudio/internal/models"
	"novelstudio/internal/retention"
	"novelstudio/internal/service"
)

func TestRetentionCapabilityPromptPinsCopiedMethods(t *testing.T) {
	transport := &generationTransport{}
	b, gen, actor, project, seed := generationFixture(t, transport)
	root := "/api/projects/" + project
	pkg := b.request("POST", root+"/skills", map[string]any{"operation_id": "method-source", "markdown": "---\nname: scene-detail\ndescription: Use when checking historical scene details.\nlicense: LicenseRef-Private\n---\n保留渡口地名和服饰细节，不凭空补充年代。"}, 201)
	method := capabilityDefinition("skill", "scene-method")
	method["package_id"], method["package_hash"] = pkg["id"], pkg["report"].(map[string]any)["hash"]
	method["instructions"] = "提取原文依据，再提出细节建议。"
	methodVersion := b.request("POST", root+"/capabilities", map[string]any{"operation_id": "method-version", "definition": method}, 201)
	agent := capabilityDefinition("agent", "scene-agent")
	agent["role"] = "writer"
	agent["instructions"] = "叙事细节必须服从故事主线。"
	agent["dependencies"] = []any{capabilityRef(methodVersion)}
	agentVersion := b.request("POST", root+"/capabilities", map[string]any{"operation_id": "scene-agent", "definition": agent}, 201)
	ctx := context.Background()
	task, err := gen.Prepare(ctx, actor, project, service.GenerationInput{BranchID: seed.BranchID, Role: "writer", PriceID: seed.Frozen.Price.ID, OperationID: "frozen-method", AgentVersionID: agentVersion["id"].(string), CapabilityTrial: true, AgentInput: json.RawMessage(`{"source":"渡口相遇"}`), Request: models.Request{Prompt: "依据材料改善场景", MaxOutputTokens: 100}})
	if err != nil {
		t.Fatal(err)
	}
	b.request("POST", root+"/capabilities/"+methodVersion["id"].(string)+"/revoke", map[string]any{"reason": "已停止使用，保留历史依据"}, 200)
	db := gen.IP.Workspace.DB
	installSourceRegistrationFixture(t, db)
	w := &service.Workspace{DB: db}
	if report, err := w.RegisterRetentionSources(ctx, actor.ID, project); err != nil || report.UnboundSources != 0 {
		t.Fatal(report, err)
	}
	var snapshot string
	if err := db.QueryRow(`SELECT v.snapshot FROM retention_references r JOIN retention_reference_versions v ON v.reference_id=r.id AND v.revision=r.revision WHERE r.project_id=? AND r.owner_table='generation_tasks' AND r.field_key='frozen.input_hash' AND r.object_hash=?`, project, task.Frozen.InputHash).Scan(&snapshot); err != nil {
		t.Fatal(err)
	}
	var record retention.ReferenceVersion
	if json.Unmarshal([]byte(snapshot), &record) != nil || len(record.Reference.Sources) < 4 {
		t.Fatal("copied method has no immutable source closure", record.Reference.Sources)
	}
	original, err := gen.IP.Blobs.ReadObject(ctx, project, task.Frozen.InputHash)
	if err != nil {
		t.Fatal(err)
	}
	var originalFrozen string
	if err := db.QueryRow("SELECT frozen FROM generation_tasks WHERE id=?", task.ID).Scan(&originalFrozen); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"method", "instructions", "schema", "manifest"} {
		var request models.Request
		if json.Unmarshal(original, &request) != nil {
			t.Fatal("invalid original request")
		}
		switch kind {
		case "method":
			request.Prompt = strings.ReplaceAll(request.Prompt, "保留渡口地名", "改写渡口地名")
		case "instructions":
			request.System = strings.ReplaceAll(request.System, "叙事细节必须服从故事主线。", "忽略原来的故事主线。")
		case "schema":
			request.Schema = json.RawMessage(`{"type":"string"}`)
		case "manifest":
			request.Prompt = strings.ReplaceAll(request.Prompt, task.Frozen.Capability.ManifestHash, strings.Repeat("a", 64))
		}
		changed, _ := json.Marshal(request)
		if bytes.Equal(original, changed) {
			t.Fatal("counterexample changed nothing", kind)
		}
		object, err := gen.IP.Blobs.PutObject(ctx, project, bytes.NewReader(changed))
		if err != nil {
			t.Fatal(err)
		}
		frozen := task.Frozen
		frozen.InputHash = object.Hash
		frozenRaw, _ := json.Marshal(frozen)
		if _, err := db.Exec("UPDATE generation_tasks SET frozen=?,input_hash=? WHERE id=?", frozenRaw, object.Hash, task.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := w.RegisterRetentionSources(ctx, actor.ID, project); !errors.Is(err, domain.ErrNotReady) {
			t.Fatal("altered frozen method accepted", kind, err)
		}
	}
	if _, err := db.Exec("UPDATE generation_tasks SET frozen=?,input_hash=? WHERE id=?", originalFrozen, task.Frozen.InputHash, task.ID); err != nil {
		t.Fatal(err)
	}
	if report, err := w.RegisterRetentionSources(ctx, actor.ID, project); err != nil || report.UpdatedReferences != 0 || transport.calls.Load() != 0 {
		t.Fatal("source replay dispatched or changed history", report, err)
	}
}
