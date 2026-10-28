package integration

import (
	"context"
	"novelstudio/internal/domain"
	"novelstudio/internal/service"
	"novelstudio/internal/storage"
	"novelstudio/presets"
	"os"
	"testing"
)

func TestExternalAndCreativePresetsPersistCompleteEditableCandidates(t *testing.T) {
	b, db := setup(t)
	pid := createProject(b, "外部技能与创作素材", "creative-catalog")
	root := "/api/projects/" + pid + "/skills"
	listed := b.request("GET", root+"/presets", nil, 200)["items"].([]any)
	entries, err := presets.ExternalCatalog()
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) < len(entries)+8 {
		t.Fatal("missing complete catalog")
	}
	var original map[string]any
	for _, entry := range entries {
		input := map[string]any{"operation_id": "import-" + entry.ID}
		first := b.request("POST", root+"/presets/"+entry.ID, input, 201)
		retry := b.request("POST", root+"/presets/"+entry.ID, input, 201)
		source := first["source"].(map[string]any)
		if first["id"] != retry["id"] || first["state"] != "CANDIDATE" || source["commit"] != entry.Source.Commit || source["repository"] != entry.Source.Repository {
			t.Fatal("source or idempotency lost", first)
		}
		version := root + "/" + first["id"].(string)
		b.request("GET", version+"/files?path=NOTICE", nil, 200)
		b.request("GET", version+"/files?path=LICENSE", nil, 200)
		if entry.Name == "novel-outline" {
			original = first
		}
	}
	if original == nil {
		t.Fatal("outline missing")
	}
	me := b.request("GET", "/api/me", nil, 200)
	actor := domain.Actor{ID: me["id"].(string)}
	svc := &service.Skills{IP: &service.IP{Workspace: &service.Workspace{DB: db}, Blobs: storage.LocalBlobs{Root: os.Getenv("STUDIO_BLOB_ROOT")}}}
	ctx := context.Background()
	originalID := original["id"].(string)
	file, err := svc.File(ctx, actor, pid, originalID, "SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	edited, err := svc.Edit(ctx, actor, pid, originalID, service.SkillEdit{OperationID: "local-edit", ExpectedHash: original["report"].(map[string]any)["hash"].(string), Changes: []service.SkillFileChange{{Path: "SKILL.md", Text: file.Text + "\n本项目补充：保留长篇大纲，不自动替换。"}}})
	if err != nil || edited.ParentID != originalID {
		t.Fatal("editing external candidate", err)
	}
	preserved, err := svc.File(ctx, actor, pid, originalID, "SKILL.md")
	if err != nil || preserved.Text != file.Text {
		t.Fatal("upstream overwritten", err)
	}
	names := b.request("POST", root+"/presets/character-naming", map[string]any{"operation_id": "names"}, 201)
	b.request("GET", root+"/"+names["id"].(string)+"/files?path=references/surname-seeds.json", nil, 200)
	research := b.request("POST", root+"/presets/genre-research", map[string]any{"operation_id": "research"}, 201)
	b.request("GET", root+"/"+research["id"].(string)+"/files?path=output.schema.json", nil, 200)
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM skill_packages WHERE project_id=?", pid).Scan(&count); err != nil || count != len(entries)+3 {
		t.Fatal("database candidates missing", count, err)
	}
	other := createProject(b, "其他项目", "other-creative")
	b.request("GET", "/api/projects/"+other+"/skills/"+originalID, nil, 403)
}
