package integration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"

	"novelstudio/internal/domain"
)

func TestManualReleaseRecordsServerOriginAndLegacyRemainsReadOnly(t *testing.T) {
	b, db := setup(t)
	pid := createProject(b, "来源证据", "release-origin")
	bid := branch(b, pid, "正史")
	rid := resource(b, pid, bid, "人工笔记")
	root := "/api/projects/" + pid
	id := prepareRelease(b, pid, bid, "", "", "manual-origin", []map[string]any{{"resource_id": rid, "text": "人工整理的参考资料"}})
	release := b.request("GET", root+"/releases/"+id, nil, 200)
	manifest := release["manifest"].(map[string]any)
	origin := manifest["artifacts"].([]any)[0].(map[string]any)["origin"].(map[string]any)
	if manifest["schema"] != float64(2) || origin["kind"] != "manual" || origin["review_required"] != false || origin["resource_id"] != rid || origin["actor_id"] == "" {
		t.Fatal("server provenance missing", release)
	}
	// An actual old schema record is still readable, but cannot silently acquire v2 authority.
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	var legacy domain.Manifest
	if err = json.Unmarshal(raw, &legacy); err != nil {
		t.Fatal(err)
	}
	legacy.Schema = 1
	legacy.Artifacts[0].Origin = nil
	raw, err = json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(raw)
	if _, err = db.Exec("UPDATE artifact_releases SET manifest=?,content_hash=? WHERE id=?", raw, hex.EncodeToString(hash[:]), id); err != nil {
		t.Fatal(err)
	}
	b.request("GET", root+"/releases/"+id, nil, 200)
	b.request("POST", root+"/releases/"+id+"/adopt", map[string]any{}, 409)
	if b.request("GET", root+"/resources/"+rid, nil, 200)["head"] != "" {
		t.Fatal("legacy write advanced target")
	}
}

func TestUnqualifiedGenerationOriginCannotPublishOrBecomeManualAfterReload(t *testing.T) {
	b, gen, actor, pid, job := generationFixture(t, &generationTransport{response: generationSuccess})
	ctx := context.Background()
	if _, err := gen.Start(ctx, actor, pid, job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := gen.Execute(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	rid := resource(b, pid, job.BranchID, "来源候选")
	root := "/api/projects/" + pid
	source := prepareRelease(b, pid, job.BranchID, "", "", "source-receipt", []map[string]any{{"resource_id": rid, "text": "测试候选", "origin": map[string]any{"generation_id": job.ID}}})
	read := b.request("GET", root+"/releases/"+source, nil, 200)
	origin := read["manifest"].(map[string]any)["artifacts"].([]any)[0].(map[string]any)["origin"].(map[string]any)
	if origin["kind"] != "generation" || origin["review_required"] != true || origin["generation"].(map[string]any)["generation_id"] != job.ID {
		t.Fatal("unqualified output laundered", origin)
	}
	b.request("POST", root+"/releases/"+source+"/adopt", map[string]any{}, 503)
	// A restored private text draft need not resend the selection. The server retains its lineage.
	derived := prepareRelease(b, pid, job.BranchID, "", "", "edited-after-reload", []map[string]any{{"resource_id": rid, "text": "手工修改了测试候选"}})
	read = b.request("GET", root+"/releases/"+derived, nil, 200)
	origin = read["manifest"].(map[string]any)["artifacts"].([]any)[0].(map[string]any)["origin"].(map[string]any)
	if origin["kind"] != "derived" || origin["review_required"] != true || origin["generation"].(map[string]any)["generation_id"] != job.ID {
		t.Fatal("known AI ancestor lost on manual edit", origin)
	}
	targets := read["review_targets"].([]any)
	target := targets[0].(map[string]any)
	if len(targets) != 1 || target["release_id"] != derived || target["resource_id"] != rid || target["content_hash"] != origin["content_hash"] || target["source_generation_id"] != job.ID || target["manifest_hash"] != read["hash"] {
		t.Fatal("independent review handoff did not fix exact content and target", targets)
	}
	b.request("POST", root+"/releases/"+derived+"/adopt", map[string]any{}, 503)
	// Client-supplied origin kind/qualification are not writable DTO fields.
	b.request("POST", root+"/releases", map[string]any{"branch_id": job.BranchID, "operation_id": "forged-origin", "updates": []any{map[string]any{"resource_id": rid, "text": "手改", "origin": map[string]any{"kind": "manual", "qualified": true}}}}, 400)
}

func TestGenerationOriginRejectsWrongProjectBranchAndNonFinalResult(t *testing.T) {
	b, gen, actor, pid, job := generationFixture(t, &generationTransport{response: generationSuccess})
	rid := resource(b, pid, job.BranchID, "等待结果")
	input := map[string]any{"operation_id": "unfinished", "branch_id": job.BranchID, "updates": []any{map[string]any{"resource_id": rid, "text": "测试候选", "origin": map[string]any{"generation_id": job.ID}}}}
	b.request("POST", "/api/projects/"+pid+"/releases", input, 409)
	if _, err := gen.Start(context.Background(), actor, pid, job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := gen.Execute(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	otherBranch := branch(b, pid, "其他分支")
	otherResource := resource(b, pid, otherBranch, "其他目标")
	input["operation_id"] = "wrong-branch"
	input["branch_id"] = otherBranch
	input["updates"] = []any{map[string]any{"resource_id": otherResource, "text": "测试候选", "origin": map[string]any{"generation_id": job.ID}}}
	b.request("POST", "/api/projects/"+pid+"/releases", input, 403)
	otherProject := createProject(b, "其他项目", "other-origin")
	branchID := branch(b, otherProject, "正史")
	resourceID := resource(b, otherProject, branchID, "目标")
	input["branch_id"] = branchID
	input["updates"] = []any{map[string]any{"resource_id": resourceID, "text": "测试候选", "origin": map[string]any{"generation_id": job.ID}}}
	b.request("POST", "/api/projects/"+otherProject+"/releases", input, 403)
}
