package integration

import (
	"context"
	"errors"
	"novelstudio/internal/domain"
	"novelstudio/internal/service"
	"novelstudio/internal/storage"
	"os"
	"testing"
)

func TestSkillVersionsAreImmutableAndRevokedAncestorsBlockReferences(t *testing.T) {
	b, db := setup(t)
	pid := createProject(b, "方法库", "one")
	me := b.request("GET", "/api/me", nil, 200)
	actor := domain.Actor{ID: me["id"].(string)}
	svc := &service.Skills{IP: &service.IP{Workspace: &service.Workspace{DB: db}, Blobs: storage.LocalBlobs{Root: os.Getenv("STUDIO_BLOB_ROOT")}}}
	ctx := context.Background()
	body := "---\nname: source-review\ndescription: Use when checking source evidence.\nlicense: LicenseRef-Private\n---\nRead the source, preserve evidence, propose a candidate."
	first, err := svc.Create(ctx, actor, pid, "first", body)
	if err != nil {
		t.Fatal(err)
	}
	same, err := svc.Create(ctx, actor, pid, "first", body)
	if err != nil || same.ID != first.ID {
		t.Fatal("retry duplicated version", err)
	}
	if _, err = svc.Create(ctx, actor, pid, "first", body+" changed"); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("idempotency conflict missing", err)
	}
	next, err := svc.Edit(ctx, actor, pid, first.ID, service.SkillEdit{OperationID: "edit", ExpectedHash: first.Report.Hash, Changes: []service.SkillFileChange{{Path: "SKILL.md", Text: body + " Keep exact anchors."}}})
	if err != nil {
		t.Fatal(err)
	}
	original, err := svc.File(ctx, actor, pid, first.ID, "SKILL.md")
	if err != nil || original.Text != body || next.ParentID != first.ID {
		t.Fatal("old method overwritten", err)
	}
	if _, err = svc.Reference(ctx, actor, pid, next.ID); err != nil {
		t.Fatal(err)
	}
	referenced, err := svc.Read(ctx, actor, pid, next.ID)
	if err != nil || referenced.State != "REFERENCE" || !referenced.Available || referenced.Report.Qualification != "STATIC_ONLY" {
		t.Fatal("reference falsely qualifies execution", referenced, err)
	}
	if err = svc.Revoke(ctx, actor, pid, first.ID, "该上游方法撤销"); err != nil {
		t.Fatal(err)
	}
	referenced, err = svc.Read(ctx, actor, pid, next.ID)
	if err != nil || referenced.Available {
		t.Fatal("derived reference escaped revocation", err)
	}
	if _, err = svc.Reference(ctx, actor, pid, next.ID); !errors.Is(err, domain.ErrNotReady) {
		t.Fatal("revoked ancestor activated", err)
	}
	other := createProject(b, "其他项目", "other")
	if _, err = svc.File(ctx, actor, other, next.ID, "SKILL.md"); !errors.Is(err, domain.ErrForbidden) {
		t.Fatal("cross project source exposed", err)
	}
	if _, err = svc.Edit(ctx, actor, pid, next.ID, service.SkillEdit{OperationID: "escape", ExpectedHash: next.Report.Hash, Changes: []service.SkillFileChange{{Path: "../bad", Text: "x"}}}); err == nil {
		t.Fatal("unsafe edit accepted")
	}
}

func TestPresetCopyRetainsContractsAndCannotEscapeCatalog(t *testing.T) {
	b, _ := setup(t)
	pid := createProject(b, "预置方法", "preset-project")
	root := "/api/projects/" + pid + "/skills"
	list := b.request("GET", root+"/presets", nil, 200)
	if len(list["items"].([]any)) < 1 {
		t.Fatal("missing bundled methods")
	}
	input := map[string]any{"operation_id": "copy-preset"}
	first := b.request("POST", root+"/presets/source-ledger-extract", input, 201)
	second := b.request("POST", root+"/presets/source-ledger-extract", input, 201)
	if first["id"] != second["id"] || first["state"] != "CANDIDATE" {
		t.Fatal("preset retry duplicated or silently qualified", first, second)
	}
	file := b.request("GET", root+"/"+first["id"].(string)+"/files?path=output.schema.json", nil, 200)
	if file["text"] == "" || file["hash"] == "" {
		t.Fatal("schema missing")
	}
	b.request("POST", root+"/presets/not-bundled", map[string]any{"operation_id": "bad"}, 400)
}
