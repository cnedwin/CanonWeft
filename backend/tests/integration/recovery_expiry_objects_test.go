package integration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"novelstudio/internal/backup"
	"novelstudio/internal/domain"
	"novelstudio/internal/retention"
	"novelstudio/internal/service"
	"novelstudio/internal/storage"
)

func TestRecoveryKeepsIndependentlyExpiredOriginalObjects(t *testing.T) {
	for _, kind := range []string{"release_object", "release_all", "import_object", "skill_object"} {
		t.Run(kind, func(t *testing.T) {
			b, db := setup(t)
			ctx := context.Background()
			name := "原文件过期恢复"
			project := createProject(b, name, "expired-original")
			branchID := branch(b, project, "正史")
			me := b.request("GET", "/api/me", nil, 200)
			actor := domain.Actor{ID: me["id"].(string), WorkspaceID: me["workspace_id"].(string), Role: me["role"].(string)}
			ip := propagationService(t, db)
			rid := resource(b, project, branchID, "正式第一章")
			live := prepareRelease(b, project, branchID, "", "", "live", []map[string]any{{"resource_id": rid, "text": "正式正文保持完整。"}})
			releaseAdopt(b, project, live)
			releasePublish(b, project, live)
			var table, id, hash string
			fields := []string{}
			switch kind {
			case "release_object", "release_all":
				rid := resource(b, project, branchID, "旧章节草案")
				id = prepareRelease(b, project, branchID, "", "", "old-release", []map[string]any{{"resource_id": rid, "text": "独立候选正文。"}})
				table, fields = "artifact_releases", []string{"content_hash"}
				if kind == "release_all" {
					fields = append(fields, "manifest")
				}
			case "import_object":
				source, err := ip.ImportSource(ctx, actor, project, branchID, "待选资料.txt", "import", []byte("未采用的原始资料。"))
				if err != nil {
					t.Fatal(err)
				}
				table, id, fields = "source_imports", source.ID, []string{"source_hash"}
			case "skill_object":
				pkg, err := (&service.Skills{IP: ip}).Create(ctx, actor, project, "method", "---\nname: source-review\ndescription: Use when checking evidence.\nlicense: LicenseRef-Private\n---\nReview source evidence.")
				if err != nil {
					t.Fatal(err)
				}
				table, id, fields = "skill_packages", pkg.ID, []string{"bundle_hash"}
			}
			if err := db.QueryRow("SELECT "+fields[0]+" FROM "+table+" WHERE id=?", id).Scan(&hash); err != nil {
				t.Fatal(err)
			}
			installCompleteRetentionFixture(t, db)
			owner, _ := retention.RowOwnerKey(table, map[string]string{"id": id})
			for _, field := range fields {
				var raw string
				if err := db.QueryRow("SELECT "+field+" FROM "+table+" WHERE id=?", id).Scan(&raw); err != nil {
					t.Fatal(err)
				}
				ref, _ := retention.ReferenceID(project, table, owner, field)
				valueHash := sha256.Sum256([]byte(raw))
				if _, err := db.Exec("INSERT INTO retention_source_slots(id,project_id,owner_table,owner_key,field_key,time_basis,first_verified_at) VALUES(?,?,?,?,?,'LEGACY_REGISTERED','2020-01-02 03:04:05.000006')", ref, project, table, owner, field); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec("INSERT INTO retention_source_values(source_id,payload_hash,is_null,first_verified_at) VALUES(?,?,FALSE,'2020-01-02 03:04:05.000006')", ref, hex.EncodeToString(valueHash[:])); err != nil {
					t.Fatal(err)
				}
			}
			store, err := storage.NewLocalRetentionStore(os.Getenv("STUDIO_BLOB_ROOT"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			scanner := &service.RetentionScanner{Workspace: ip.Workspace, Blobs: store}
			job, err := scanner.PlanJob(ctx, actor.ID, project, "request-v2:"+service.NewID())
			if err != nil || job.Expiring == 0 || job.RemovableObjects == 0 {
				t.Fatal("no actual object expiry", job, err)
			}
			t.Log("actual cleanup plan", job.Expiring, job.RemovableObjects)
			if _, err = scanner.StartJob(ctx, actor.ID, project, job.ID, job.PlanHash); err != nil {
				t.Fatal(err)
			}
			state := ""
			for i := 0; i < 32 && state != "COMPLETE"; i++ {
				state, err = scanner.AdvanceJob(ctx, job.ID)
				if err != nil || state == "BLOCKED" {
					t.Fatal(state, err)
				}
			}
			if state != "COMPLETE" {
				t.Fatal(state)
			}
			if _, err = ip.Blobs.ReadObject(ctx, project, hash); err == nil {
				t.Fatal("fixture did not remove original object")
			}
			for i := 0; i < 2; i++ {
				if err = ip.Workspace.DeleteProject(ctx, actor.ID, project, name, 1, "request-v2:"+service.NewID()); err != nil {
					t.Fatal(err)
				}
				copy, err := ip.RecoverProject(ctx, actor.ID, project, service.ProjectRecoveryInput{Name: name, Confirmation: name, Revision: 2, OperationID: "request-v2:" + service.NewID()})
				if err != nil {
					t.Fatal("expired object blocked recovery", err)
				}
				branches, err := ip.Branches(ctx, actor, copy.ID)
				if err != nil || len(branches) != 1 {
					t.Fatal(err)
				}
				history, err := ip.Releases(ctx, actor, copy.ID, branches[0].ID)
				if err != nil {
					t.Fatal(err)
				}
				foundLive, foundExpired := false, false
				for _, entry := range history {
					if entry.Retention != nil {
						foundExpired = true
						if kind == "release_object" {
							if entry.Hash == hash {
								t.Fatal("retained manifest was not remapped")
							}
							if _, err = ip.Blobs.ReadObject(ctx, copy.ID, entry.Hash); err == nil {
								t.Fatal("recovery recreated an unavailable manifest file")
							}
							var erasedHash string
							owner, _ := retention.RowOwnerKey("artifact_releases", map[string]string{"id": entry.ID})
							ref, _ := retention.ReferenceID(copy.ID, "artifact_releases", owner, "content_hash")
							if err = db.QueryRow("SELECT object_hash FROM retention_references WHERE id=? AND state='EXPIRED'", ref).Scan(&erasedHash); err != nil || erasedHash != hash {
								t.Fatal("original erased hash changed", err)
							}
						}
						if _, err = ip.AdoptRelease(ctx, actor, copy.ID, entry.ID); !errors.Is(err, domain.ErrMaterialExpired) {
							t.Fatal("expired release adopted", err)
						}
					} else if entry.Status == "PUBLISHED" {
						foundLive = true
						body, err := ip.Blobs.ReadObject(ctx, copy.ID, entry.Manifest.Artifacts[0].Hash)
						if err != nil || string(body) != "正式正文保持完整。" {
							t.Fatal("live body lost", err)
						}
					}
				}
				if !foundLive || ((kind == "release_object" || kind == "release_all") && !foundExpired) {
					t.Fatal("history lost")
				}
				if kind == "import_object" {
					var importID string
					if err = db.QueryRow("SELECT id FROM source_imports WHERE project_id=?", copy.ID).Scan(&importID); err != nil {
						t.Fatal(err)
					}
					if _, err = ip.ReadImport(ctx, actor, copy.ID, branches[0].ID, importID); !errors.Is(err, domain.ErrMaterialExpired) {
						t.Fatal("expired import readable", err)
					}
				}
				if kind == "skill_object" {
					svc := &service.Skills{IP: ip}
					list, err := svc.List(ctx, actor, copy.ID)
					if err != nil || len(list) != 1 || list[0].Retention == nil || list[0].Available {
						t.Fatal("expired skill revived", err)
					}
				}
				if _, err = ip.Blobs.ReadObject(ctx, copy.ID, hash); err == nil {
					t.Fatal("erased original recreated")
				}
				if _, err = backup.Create(ctx, db, store, filepath.Join(t.TempDir(), "originals.zip")); err != nil {
					t.Fatal("recovered archive rejected", err)
				}
				if _, err = scanner.PlanJob(ctx, actor.ID, copy.ID, "request-v2:"+service.NewID()); err != nil {
					t.Fatal("recovered project cannot plan cleanup", err)
				}
				project = copy.ID
			}
		})
	}
}
