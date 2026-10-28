package integration

import (
	"context"
	"encoding/json"
	"testing"

	"novelstudio/internal/domain"
	"novelstudio/internal/service"
)

func TestRetentionRevocationsRecordReasonsWithoutReadingPackages(t *testing.T) {
	b, db := setup(t)
	project := createProject(b, "撤销说明", "revoke-capture")
	actor := domain.Actor{ID: b.request("GET", "/api/me", nil, 200)["id"].(string)}
	ip, ctx := propagationService(t, db), context.Background()
	skills, caps := &service.Skills{IP: ip}, &service.Capabilities{IP: ip}
	pkg, err := skills.Create(ctx, actor, project, "revocable", "---\nname: revocable\ndescription: 测试方法\n---\n保留来源后提出候选。")
	if err != nil {
		t.Fatal(err)
	}
	definition := capabilityDefinition("skill", "revocable")
	definition["package_id"], definition["package_hash"] = pkg.ID, pkg.Report.Hash
	raw, _ := json.Marshal(definition)
	var method domain.CapabilityDefinition
	if err := json.Unmarshal(raw, &method); err != nil {
		t.Fatal(err)
	}
	capability, err := caps.Save(ctx, actor, project, service.CapabilitySave{OperationID: "method", Definition: method})
	if err != nil {
		t.Fatal(err)
	}
	installSourceRegistrationFixture(t, db)
	ip.Blobs = nil
	if err := skills.Revoke(ctx, actor, project, pkg.ID, "上游内容不再适用"); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM retention_references WHERE project_id=? AND owner_table='skill_revocations' AND field_key='reason'", project).Scan(&count); err != nil || count != 1 {
		t.Fatal("missing revocation reason", count, err)
	}
	if err := skills.Revoke(ctx, actor, project, pkg.ID, "重复请求不能重置原说明"); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM retention_source_values v JOIN retention_source_slots s ON s.id=v.source_id WHERE s.project_id=? AND s.owner_table='skill_revocations'", project).Scan(&count); err != nil || count != 1 {
		t.Fatal("revoke replay replaced original explanation", count, err)
	}
	if _, err := db.Exec("CREATE TRIGGER reject_revoke_reason BEFORE INSERT ON retention_reference_versions FOR EACH ROW BEGIN IF JSON_UNQUOTE(JSON_EXTRACT(NEW.snapshot,'$.reference.rule'))='capability_revocations.reason' THEN SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='test rejected revoke reason'; END IF; END"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.Exec("DROP TRIGGER IF EXISTS reject_revoke_reason") })
	if err := caps.Revoke(ctx, actor, project, capability.ID, "维护者停止使用此方法"); err == nil {
		t.Fatal("revoke committed without explanation history")
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM capability_revocations WHERE version_id=?", capability.ID).Scan(&count); err != nil || count != 0 {
		t.Fatal("partial revoke", count, err)
	}
	if _, err := db.Exec("DROP TRIGGER reject_revoke_reason"); err != nil {
		t.Fatal(err)
	}
	if err := caps.Revoke(ctx, actor, project, capability.ID, "维护者停止使用此方法"); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM retention_references WHERE project_id=? AND owner_table='capability_revocations' AND field_key='reason'", project).Scan(&count); err != nil || count != 1 {
		t.Fatal("missing capability reason", count, err)
	}
}
