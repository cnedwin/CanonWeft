package integration

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"novelstudio/internal/authority"
	"novelstudio/internal/backup"
	"novelstudio/internal/domain"
	"novelstudio/internal/retention"
	"novelstudio/internal/service"
	"novelstudio/internal/storage"
)

func TestExpiredPublicationHistoryRefusesReuseAndAllowsFreshCandidates(t *testing.T) {
	for _, kind := range []string{"ip", "release"} {
		t.Run(kind, func(t *testing.T) {
			b, db := setup(t)
			ctx := context.Background()
			project := createProject(b, "候选到期续写", "expired-publication-history")
			branchID := branch(b, project, "正史")
			me := b.request("GET", "/api/me", nil, 200)
			actor := domain.Actor{ID: me["id"].(string), WorkspaceID: me["workspace_id"].(string), Role: me["role"].(string)}
			ip := propagationService(t, db)
			var table, field, id, rid string
			want := 2
			if kind == "ip" {
				table, field = "ip_revisions", "payload"
				id = propose(b, project, branchID, "", "old-ip", originalSnapshot())
			} else {
				table, field, want = "artifact_releases", "manifest", 2
				rid = resource(b, project, branchID, "正文")
				id = prepareRelease(b, project, branchID, "", "", "old-release", []map[string]any{{"resource_id": rid, "text": "候选正文"}})
			}
			objects := 0
			if kind == "release" {
				objects = 1
			}
			expireTransientRecord(t, db, actor.ID, project, table, field, id, want, objects)
			if kind == "ip" {
				history, err := ip.Candidates(ctx, actor, project, branchID)
				if err != nil || len(history) != 1 || history[0].Retention == nil {
					t.Fatal("IP history", err)
				}
				if _, err := ip.Adopt(ctx, actor, project, branchID, id); !errors.Is(err, domain.ErrMaterialExpired) {
					t.Fatal("expired IP adopted", err)
				}
				fresh, err := ip.Propose(ctx, actor, project, branchID, "", "fresh-ip", originalSnapshot())
				if err != nil || fresh.ID == id || fresh.Retention != nil {
					t.Fatal("fresh IP", err)
				}
			} else {
				history, err := ip.Releases(ctx, actor, project, branchID)
				if err != nil || len(history) != 1 || history[0].Retention == nil || len(history[0].Manifest.Artifacts) != 0 {
					t.Fatal("release history", err)
				}
				if _, err := ip.AdoptRelease(ctx, actor, project, id); !errors.Is(err, domain.ErrMaterialExpired) {
					t.Fatal("expired release adopted", err)
				}
				fresh, err := ip.PrepareRelease(ctx, actor, project, domain.ReleaseInput{BranchID: branchID, OperationID: "fresh-release", Updates: []domain.ResourceUpdate{{ResourceID: rid, Text: "候选正文"}}})
				if err != nil || fresh.ID == id || fresh.Retention != nil {
					t.Fatal("fresh release", err)
				}
				store, err := storage.NewLocalRetentionStore(os.Getenv("STUDIO_BLOB_ROOT"))
				if err != nil {
					t.Fatal(err)
				}
				defer store.Close()
				if _, err := backup.Create(ctx, db, store, filepath.Join(t.TempDir(), "same-content-new-release.zip")); err != nil {
					t.Fatal("old deletion and new release archive", err)
				}
				history, err = ip.Releases(ctx, actor, project, branchID)
				if err != nil || len(history) != 2 {
					t.Fatal("old and fresh history", err)
				}
				for _, entry := range history {
					if (entry.ID == id) != (entry.Retention != nil) {
						t.Fatal("fresh write revived expired release")
					}
				}
			}
			if _, err := db.Exec("DELETE FROM retention_evidence WHERE project_id=?", project); err != nil {
				t.Fatal(err)
			}
			if kind == "ip" {
				if _, err := ip.Candidates(ctx, actor, project, branchID); err == nil {
					t.Fatal("unproved IP marker")
				}
			} else if _, err := ip.Releases(ctx, actor, project, branchID); err == nil {
				t.Fatal("unproved release marker")
			}
		})
	}
}

func TestDeletedPublicationExpiryPreservesConsumersAndArchive(t *testing.T) {
	b, db := setup(t)
	ctx := context.Background()
	project := createProject(b, "作品发布清理", "publication-expiry")
	branchID := branch(b, project, "正史")
	candidate := propose(b, project, branchID, "", "publication-ip", originalSnapshot())
	b.request("POST", "/api/projects/"+project+"/branches/"+branchID+"/candidates/"+candidate+"/adopt", map[string]any{}, 200)
	b.request("POST", "/api/projects/"+project+"/branches/"+branchID+"/candidates/"+candidate+"/publish", map[string]any{}, 200)
	rid := resource(b, project, branchID, "第一章")
	release := prepareRelease(b, project, branchID, candidate, "", "publication-release", []map[string]any{{"resource_id": rid, "text": "PRIVATE_CANARY 药师寻找母亲。"}})
	b.request("GET", "/api/projects/"+project+"/releases/"+release, nil, 200)
	releaseAdopt(b, project, release)
	releasePublish(b, project, release)
	actor := b.request("GET", "/api/me", nil, 200)["id"].(string)
	installCompleteRetentionFixture(t, db)
	ledger := authority.Ledger{Schema: os.Getenv("STUDIO_AUTHORITY_SCHEMA")}
	deleteAuthorFixture(t, db, ledger, project, actor)
	store, err := storage.NewLocalRetentionStore(os.Getenv("STUDIO_BLOB_ROOT"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	scanner := &service.RetentionScanner{Workspace: propagationService(t, db).Workspace, Blobs: store}
	ipKey := retention.SourceKey{Table: "ip_revisions", Primary: map[string]string{"id": candidate}}
	releaseKey := retention.SourceKey{Table: "artifact_releases", Primary: map[string]string{"id": release}}
	if err := expireAuthorFixture(t, db, scanner, ledger, actor, project, ipKey, "payload"); !errors.Is(err, retention.ErrLiveExpiryConsumers) {
		t.Fatal("release did not protect IP", err)
	}
	if err := expireAuthorFixture(t, db, scanner, ledger, actor, project, releaseKey, "manifest"); !errors.Is(err, retention.ErrLiveExpiryConsumers) {
		t.Fatal("artifact did not protect manifest", err)
	}
	field, _ := retention.ArtifactField(rid, false)
	if err := expireAuthorFixture(t, db, scanner, ledger, actor, project, releaseKey, field); !errors.Is(err, retention.ErrLiveExpiryConsumers) {
		t.Fatal("published index did not protect artifact", err)
	}
	rows, err := db.Query("SELECT id FROM knowledge_indexes WHERE project_id=?", project)
	if err != nil {
		t.Fatal(err)
	}
	var indexes []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		indexes = append(indexes, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	for _, indexID := range indexes {
		chunks, err := db.Query("SELECT id FROM knowledge_chunks WHERE index_id=?", indexID)
		if err != nil {
			t.Fatal(err)
		}
		var ids []string
		for chunks.Next() {
			var id string
			if err := chunks.Scan(&id); err != nil {
				t.Fatal(err)
			}
			ids = append(ids, id)
		}
		if err := chunks.Err(); err != nil {
			t.Fatal(err)
		}
		chunks.Close()
		for _, id := range ids {
			for _, column := range []string{"content", "vector", "source_hash", "resource_name"} {
				if err := expireAuthorFixture(t, db, scanner, ledger, actor, project, retention.SourceKey{Table: "knowledge_chunks", Primary: map[string]string{"id": id}}, column); err != nil {
					t.Fatal("published chunk", column, err)
				}
			}
		}
		if err := expireAuthorFixture(t, db, scanner, ledger, actor, project, retention.SourceKey{Table: "knowledge_indexes", Primary: map[string]string{"id": indexID}}, "manifest"); err != nil {
			t.Fatal("published index", err)
		}
	}
	if err := expireAuthorFixture(t, db, scanner, ledger, actor, project, releaseKey, "manifest"); !errors.Is(err, retention.ErrLiveExpiryConsumers) {
		t.Fatal("current resource did not protect manifest", err)
	}
	if err := expireAuthorFixture(t, db, scanner, ledger, actor, project, retention.SourceKey{Table: "resources", Primary: map[string]string{"id": rid}}, "content_hash"); err != nil {
		t.Fatal("current resource", err)
	}
	if err := expireAuthorFixture(t, db, scanner, ledger, actor, project, releaseKey, field); err != nil {
		t.Fatal("artifact", err)
	}
	if _, err := backup.Create(ctx, db, store, filepath.Join(t.TempDir(), "partial-publication.zip")); err != nil {
		t.Fatal("partial archive", err)
	}
	if err := expireAuthorFixture(t, db, scanner, ledger, actor, project, releaseKey, "manifest"); err != nil {
		t.Fatal("manifest", err)
	}
	if err := expireAuthorFixture(t, db, scanner, ledger, actor, project, releaseKey, "content_hash"); err != nil {
		t.Fatal("manifest object", err)
	}
	// Candidate auxiliary records are independent consumers and expire first.
	var delta int
	if err := db.QueryRow("SELECT COUNT(*) FROM ip_branch_deltas WHERE candidate_id=?", candidate).Scan(&delta); err != nil {
		t.Fatal(err)
	}
	if delta > 0 {
		if err := expireAuthorFixture(t, db, scanner, ledger, actor, project, retention.SourceKey{Table: "ip_branch_deltas", Primary: map[string]string{"candidate_id": candidate}}, "payload"); err != nil {
			t.Fatal("delta", err)
		}
	}
	if err := expireAuthorFixture(t, db, scanner, ledger, actor, project, ipKey, "payload"); err != nil {
		t.Fatal("IP", err)
	}
	if err := expireAuthorFixture(t, db, scanner, ledger, actor, project, ipKey, "content_hash"); err != nil {
		t.Fatal("IP object", err)
	}
	if _, err := backup.Create(ctx, db, store, filepath.Join(t.TempDir(), "expired-publication.zip")); err != nil {
		t.Fatal("expired archive", err)
	}
	if _, err := db.Exec("DELETE FROM retention_evidence WHERE project_id=? AND reference_id=(SELECT id FROM retention_references WHERE project_id=? AND owner_table='artifact_releases' AND field_key='manifest')", project, project); err != nil {
		t.Fatal(err)
	}
	if _, err := backup.Create(ctx, db, store, filepath.Join(t.TempDir(), "missing-proof.zip")); err == nil {
		t.Fatal("archive trusted erased publication without proof")
	}
}
