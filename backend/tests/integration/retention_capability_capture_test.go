package integration

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"novelstudio/internal/domain"
	"novelstudio/internal/service"
	"novelstudio/internal/storage"
)

func TestRetentionSavedCapabilityCapturesPinnedSkillAndHistory(t *testing.T) {
	b, db := setup(t)
	project := createProject(b, "能力保存来源", "capability-source-writer")
	actor := domain.Actor{ID: b.request("GET", "/api/me", nil, 200)["id"].(string)}
	ip := &service.IP{Workspace: &service.Workspace{DB: db}, Blobs: storage.LocalBlobs{Root: os.Getenv("STUDIO_BLOB_ROOT")}}
	ctx := context.Background()
	installSourceRegistrationFixture(t, db)
	skills := &service.Skills{IP: ip}
	pkg, err := skills.Create(ctx, actor, project, "original-method", "---\nname: original-method\ndescription: 保留来源的写作方法\n---\n核对原始材料，再提出候选；保留不确定性。")
	if err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM retention_references WHERE project_id=? AND owner_table='skill_packages' AND field_key='bundle_hash'", project).Scan(&count); err != nil || count != 1 {
		t.Fatal("skill saved without original package proof", count, err)
	}
	method := capabilityDefinition("skill", "original-method")
	method["package_id"], method["package_hash"] = pkg.ID, pkg.Report.Hash
	decode := func(value map[string]any) domain.CapabilityDefinition {
		t.Helper()
		raw, _ := json.Marshal(value)
		var definition domain.CapabilityDefinition
		if err := json.Unmarshal(raw, &definition); err != nil {
			t.Fatal(err)
		}
		return definition
	}
	caps := &service.Capabilities{IP: ip}
	m, err := caps.Save(ctx, actor, project, service.CapabilitySave{OperationID: "method-version", Definition: decode(method)})
	if err != nil {
		t.Fatal(err)
	}
	agent := capabilityDefinition("agent", "writer-agent")
	agent["role"] = "writer"
	agent["dependencies"] = []domain.CapabilityRef{{ID: m.ID, Hash: m.Hash, Kind: "skill"}}
	a, err := caps.Save(ctx, actor, project, service.CapabilitySave{OperationID: "agent-version", Definition: decode(agent)})
	if err != nil {
		t.Fatal(err)
	}
	var packageAge string
	if err := db.QueryRow("SELECT CAST(first_verified_at AS CHAR) FROM retention_references WHERE project_id=? AND owner_table='skill_packages' AND field_key='bundle_hash'", project).Scan(&packageAge); err != nil {
		t.Fatal(err)
	}
	edited, err := skills.Edit(ctx, actor, project, pkg.ID, service.SkillEdit{OperationID: "edit-package", ExpectedHash: pkg.Report.Hash, Changes: []service.SkillFileChange{{Path: "SKILL.md", Text: "---\nname: original-method\ndescription: 修改后的写作方法\n---\n先标注不确定性，再按作者意见修改候选。"}}})
	if err != nil || edited.ParentID != pkg.ID {
		t.Fatal("package edit lost original lineage", err)
	}
	method["package_id"], method["package_hash"] = edited.ID, edited.Report.Hash
	method["instructions"] = "新方法需要重新审查；不要覆盖旧方法。"
	m2, err := caps.Save(ctx, actor, project, service.CapabilitySave{OperationID: "method-edit", ExpectedHead: m.ID, Definition: decode(method)})
	if err != nil || m2.ParentID != m.ID {
		t.Fatal("method history lost", err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM retention_references WHERE project_id=? AND owner_table='capability_versions' AND field_key='definition' AND content_root=TRUE", project).Scan(&count); err != nil || count != 3 {
		t.Fatal("saved capability history was not retained", count, err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM retention_references WHERE project_id=? AND owner_table='skill_packages' AND field_key='bundle_hash' AND content_root=TRUE", project).Scan(&count); err != nil || count != 2 {
		t.Fatal("pinned method package was not retained", count, err)
	}
	manifest, err := caps.Manifest(ctx, actor, project, a.ID)
	if err != nil || manifest.Qualified || !manifest.TrialReady || len(manifest.Versions) != 2 {
		t.Fatal("source capture granted qualification or changed manifest", err)
	}
	ip.Blobs = missingCapturedBody{BlobStore: ip.Blobs, hash: pkg.Report.Hash}
	method["instructions"] = "不能在原始方法文件缺失时提交这个新版本。"
	if _, err := caps.Save(ctx, actor, project, service.CapabilitySave{OperationID: "missing-original", ExpectedHead: m2.ID, Definition: decode(method)}); err == nil {
		t.Fatal("capability save ignored missing original source bytes")
	}
	ip.Blobs = storage.LocalBlobs{Root: os.Getenv("STUDIO_BLOB_ROOT")}
	var current, age string
	if err := db.QueryRow("SELECT version_id FROM capability_heads WHERE project_id=? AND capability_key='original-method'", project).Scan(&current); err != nil || current != m2.ID {
		t.Fatal("failed proof partially updated capability head", current, err)
	}
	if err := db.QueryRow("SELECT CAST(first_verified_at AS CHAR) FROM retention_references WHERE project_id=? AND owner_table='skill_packages' AND field_key='bundle_hash' AND object_hash=?", project, pkg.Report.Hash).Scan(&age); err != nil || age != packageAge {
		t.Fatal("editing or failed save renewed original source age", age, packageAge, err)
	}
	if report, err := ip.Workspace.RegisterRetentionSources(ctx, actor.ID, project); err != nil || report.UnboundSources != 0 {
		t.Fatal("writer capture disagrees with full source registration", report, err)
	}
}
