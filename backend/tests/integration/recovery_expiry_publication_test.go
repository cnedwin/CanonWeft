package integration

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"novelstudio/internal/backup"
	"novelstudio/internal/domain"
	"novelstudio/internal/service"
	"novelstudio/internal/storage"
)

func TestRecoveryKeepsExpiredResourceAndImportHistory(t *testing.T) {
	for _, kind := range []string{"release", "import"} {
		t.Run(kind, func(t *testing.T) {
			b, db := setup(t)
			ctx := context.Background()
			name := "过期资料不阻断写作"
			project := createProject(b, name, "recover-expired-material")
			branchID := branch(b, project, "正史")
			me := b.request("GET", "/api/me", nil, 200)
			actor := domain.Actor{ID: me["id"].(string), WorkspaceID: me["workspace_id"].(string), Role: me["role"].(string)}
			ip := propagationService(t, db)
			rid := resource(b, project, branchID, "正式第一章")
			live := prepareRelease(b, project, branchID, "", "", "live-resource", []map[string]any{{"resource_id": rid, "text": "正式正文必须保留"}})
			releaseAdopt(b, project, live)
			releasePublish(b, project, live)
			var oldID, oldHash string
			if kind == "release" {
				rid := resource(b, project, branchID, "弃用章节候选")
				oldID = prepareRelease(b, project, branchID, "", "", "expired-resource", []map[string]any{{"resource_id": rid, "text": "待清理的旧候选正文"}})
				expireTransientRecord(t, db, actor.ID, project, "artifact_releases", "manifest", oldID, 2, 1)
			} else {
				source, err := ip.ImportSource(ctx, actor, project, branchID, "旧资料.txt", "request-v2:"+service.NewID(), []byte("未使用的研究资料"))
				if err != nil {
					t.Fatal(err)
				}
				oldID, oldHash = source.ID, source.Hash
				expireTransientRecord(t, db, actor.ID, project, "source_imports", "report", oldID, 1, 0)
			}
			store, err := storage.NewLocalRetentionStore(os.Getenv("STUDIO_BLOB_ROOT"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			for copyNumber := 0; copyNumber < 2; copyNumber++ {
				if err = ip.Workspace.DeleteProject(ctx, actor.ID, project, name, 1, "request-v2:"+service.NewID()); err != nil {
					t.Fatal(err)
				}
				input := service.ProjectRecoveryInput{Name: name, Confirmation: name, Revision: 2, OperationID: "request-v2:" + service.NewID()}
				copy, err := ip.RecoverProject(ctx, actor.ID, project, input)
				if err != nil {
					t.Fatal("expired material blocked recovery", err)
				}
				branches, err := ip.Branches(ctx, actor, copy.ID)
				if err != nil || len(branches) != 1 {
					t.Fatal("branch lost", err)
				}
				history, err := ip.Releases(ctx, actor, copy.ID, branches[0].ID)
				if err != nil {
					t.Fatal("history unavailable", err)
				}
				foundExpired, foundLive := false, false
				for _, entry := range history {
					if entry.Retention != nil {
						foundExpired = true
						if entry.ID == oldID || len(entry.Manifest.Artifacts) != 0 {
							t.Fatal("expired identity/body reused")
						}
						if _, err = ip.AdoptRelease(ctx, actor, copy.ID, entry.ID); !errors.Is(err, domain.ErrMaterialExpired) {
							t.Fatal("expired release became executable", err)
						}
					} else if entry.Status == "PUBLISHED" {
						foundLive = true
						body, err := ip.Blobs.ReadObject(ctx, copy.ID, entry.Manifest.Artifacts[0].Hash)
						if err != nil || string(body) != "正式正文必须保留" {
							t.Fatal("published content lost", err)
						}
					}
				}
				if !foundLive || (kind == "release" && !foundExpired) {
					t.Fatal("history dropped")
				}
				if kind == "import" {
					var copiedImport string
					if err = db.QueryRow("SELECT id FROM source_imports WHERE project_id=?", copy.ID).Scan(&copiedImport); err != nil || copiedImport == oldID {
						t.Fatal("import identity lost", err)
					}
					if _, err = ip.ReadImport(ctx, actor, copy.ID, branches[0].ID, copiedImport); !errors.Is(err, domain.ErrMaterialExpired) {
						t.Fatal("expired import became usable", err)
					}
					if body, err := ip.Blobs.ReadObject(ctx, copy.ID, oldHash); err != nil || string(body) != "未使用的研究资料" {
						t.Fatal("retained original file lost", err)
					}
				}
				if _, err = backup.Create(ctx, db, store, filepath.Join(t.TempDir(), "expired-material-copy.zip")); err != nil {
					t.Fatal("backup rejected recovered history", err)
				}
				project = copy.ID
			}
		})
	}
}
