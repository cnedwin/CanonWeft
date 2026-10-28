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

func TestRecoveryKeepsExpiredSkillAndDiscoveryHistory(t *testing.T) {
	for _, field := range []string{"report", "source", "source_snapshot", "inventory"} {
		t.Run(field, func(t *testing.T) {
			b, db := setup(t)
			ctx := context.Background()
			name := "保留过期方法历史"
			project := createProject(b, name, "recover-expired-method")
			branch(b, project, "正史")
			me := b.request("GET", "/api/me", nil, 200)
			actor := domain.Actor{ID: me["id"].(string), WorkspaceID: me["workspace_id"].(string), Role: me["role"].(string)}
			ip := propagationService(t, db)
			skills := &service.Skills{IP: ip}
			discovery := &service.SkillDiscovery{IP: ip, Remote: emptyRetentionDiscovery{}}
			if field == "report" || field == "source" {
				pkg, err := skills.Create(ctx, actor, project, "old-skill", "---\nname: source-review\ndescription: Use when checking source evidence.\nlicense: LicenseRef-Private\n---\nPreserve evidence and propose a candidate.")
				if err != nil {
					t.Fatal(err)
				}
				expireTransientRecord(t, db, actor.ID, project, "skill_packages", field, pkg.ID)
			} else {
				source, err := discovery.SaveSource(ctx, actor, project, "", service.DiscoverySourceInput{OperationID: "source", Repository: "fixture/repository", Ref: "main", Path: "skills", Limit: 1, Enabled: true})
				if err != nil {
					t.Fatal(err)
				}
				run, err := discovery.Start(ctx, actor, project, source.ID, "old-run")
				if err != nil {
					t.Fatal(err)
				}
				if state, err := discovery.Execute(ctx, run.ID); err != nil || state != "COMPLETED" {
					t.Fatal(state, err)
				}
				expireTransientRecord(t, db, actor.ID, project, "skill_discovery_runs", field, run.ID)
			}
			store, err := storage.NewLocalRetentionStore(os.Getenv("STUDIO_BLOB_ROOT"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			for i := 0; i < 2; i++ {
				if err := ip.Workspace.DeleteProject(ctx, actor.ID, project, name, 1, "request-v2:"+service.NewID()); err != nil {
					t.Fatal(err)
				}
				copy, err := ip.RecoverProject(ctx, actor.ID, project, service.ProjectRecoveryInput{Name: name, Confirmation: name, Revision: 2, OperationID: "request-v2:" + service.NewID()})
				if err != nil {
					t.Fatal("expired method blocked recovery", err)
				}
				if field == "report" || field == "source" {
					list, err := skills.List(ctx, actor, copy.ID)
					if err != nil || len(list) != 1 || list[0].Retention == nil || list[0].Available {
						t.Fatal("skill history lost or activated", err)
					}
					if _, err := skills.Reference(ctx, actor, copy.ID, list[0].ID); !errors.Is(err, domain.ErrMaterialExpired) {
						t.Fatal("expired skill reused", err)
					}
				} else {
					overview, err := discovery.Overview(ctx, actor, copy.ID)
					if err != nil || len(overview.Runs) != 1 || overview.Runs[0].Retention == nil || len(overview.Sources) != 1 || overview.Sources[0].Enabled {
						t.Fatal("discovery history lost or scheduled", err)
					}
					if _, err := discovery.Execute(ctx, overview.Runs[0].ID); !errors.Is(err, domain.ErrMaterialExpired) {
						t.Fatal("expired discovery executed", err)
					}
				}
				if _, err := backup.Create(ctx, db, store, filepath.Join(t.TempDir(), "expired-methods.zip")); err != nil {
					t.Fatal("method archive rejected", err)
				}
				project = copy.ID
			}
		})
	}
}
