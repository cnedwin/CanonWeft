package integration

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"novelstudio/internal/backup"
	"novelstudio/internal/domain"
	"novelstudio/internal/models"
	"novelstudio/internal/retention"
	"novelstudio/internal/service"
	"novelstudio/internal/skills"
	"novelstudio/internal/storage"
)

// A complete empty discovery is useful history, without introducing unrelated
// candidate dependencies. No network or fetched code is involved.
type emptyRetentionDiscovery struct{}

func (emptyRetentionDiscovery) Discover(context.Context, skills.RemoteRequest, string, int) (skills.DiscoveryInventory, error) {
	return skills.DiscoveryInventory{Commit: strings.Repeat("a", 40), Paths: []string{}, Total: 0}, nil
}
func (emptyRetentionDiscovery) Fetch(context.Context, skills.RemoteRequest) (skills.Bundle, skills.RemoteSource, error) {
	return skills.Bundle{}, skills.RemoteSource{}, skills.ErrFetch
}

func expireTransientRecord(t *testing.T, db *sql.DB, actor, project, table, field, id string, expected ...int) {
	t.Helper()
	primary := "id"
	if table == "generation_previews" {
		primary = "generation_id"
	}
	if table == "ip_promotions" || table == "ip_branch_deltas" {
		primary = "candidate_id"
	}
	if table == "resource_drafts" || table == "resource_policy_drafts" {
		primary = "resource_id"
	}
	if len(retention.TransientColumns(table+"."+field)) == 0 {
		t.Fatal("unknown test field")
	}
	var raw string
	filter, args := primary+"=?", []any{id}
	pk := map[string]string{primary: id}
	if primary == "resource_id" {
		filter += " AND actor_id=?"
		args = append(args, actor)
		pk["actor_id"] = actor
	}
	if err := db.QueryRow("SELECT "+field+" FROM "+table+" WHERE "+filter, args...).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	installCompleteRetentionFixture(t, db)
	key, _ := retention.RowOwnerKey(table, pk)
	ref, _ := retention.ReferenceID(project, table, key, field)
	hash := sha256.Sum256([]byte(raw))
	if _, err := db.Exec("INSERT INTO retention_source_slots(id,project_id,owner_table,owner_key,field_key,time_basis,first_verified_at) VALUES(?,?,?,?,?,'LEGACY_REGISTERED','2020-01-02 03:04:05.000006')", ref, project, table, key, field); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO retention_source_values(source_id,payload_hash,is_null,first_verified_at) VALUES(?,?,FALSE,'2020-01-02 03:04:05.000006')", ref, hex.EncodeToString(hash[:])); err != nil {
		t.Fatal(err)
	}
	// An IP candidate includes its generated delta. Age both observations in
	// this fixture; a newly retained delta must otherwise block payload expiry.
	if table == "ip_revisions" {
		var delta string
		err := db.QueryRow("SELECT payload FROM ip_branch_deltas WHERE candidate_id=?", id).Scan(&delta)
		if err != nil && err != sql.ErrNoRows {
			t.Fatal(err)
		}
		if err == nil {
			owner, _ := retention.RowOwnerKey("ip_branch_deltas", map[string]string{"candidate_id": id})
			ref, _ := retention.ReferenceID(project, "ip_branch_deltas", owner, "payload")
			hash := sha256.Sum256([]byte(delta))
			if _, err := db.Exec("INSERT INTO retention_source_slots(id,project_id,owner_table,owner_key,field_key,time_basis,first_verified_at) VALUES(?,?,'ip_branch_deltas',?,'payload','LEGACY_REGISTERED','2020-01-02 03:04:05.000006')", ref, project, owner); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec("INSERT INTO retention_source_values(source_id,payload_hash,is_null,first_verified_at) VALUES(?,?,FALSE,'2020-01-02 03:04:05.000006')", ref, hex.EncodeToString(hash[:])); err != nil {
				t.Fatal(err)
			}
		}
	}
	store, err := storage.NewLocalRetentionStore(os.Getenv("STUDIO_BLOB_ROOT"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	scanner := &service.RetentionScanner{Workspace: &service.Workspace{DB: db}, Blobs: store}
	job, err := scanner.PlanJob(ctx, actor, project, "request-v2:"+service.NewID())
	wantExpiring := 1
	if field == "source_snapshot" {
		wantExpiring = 2
	} // Inventory inherits the older snapshot's deadline.
	if len(expected) > 0 {
		wantExpiring = expected[0]
	}
	wantObjects := 0
	if len(expected) > 1 {
		wantObjects = expected[1]
	}
	if err != nil || job.Expiring != wantExpiring || job.RemovableObjects != wantObjects {
		t.Fatal("only selected transient text should expire", job, err)
	}
	if _, err := scanner.StartJob(ctx, actor, project, job.ID, job.PlanHash); err != nil {
		t.Fatal(err)
	}
	state := ""
	for i := 0; i < 32 && state != "COMPLETE"; i++ {
		state, err = scanner.AdvanceJob(ctx, job.ID)
		if err != nil || state == "BLOCKED" {
			var reason string
			_ = db.QueryRow("SELECT reason_code FROM retention_jobs WHERE id=?", job.ID).Scan(&reason)
			t.Fatal("transient cleanup", state, reason, err)
		}
	}
	if state != "COMPLETE" {
		t.Fatal("transient cleanup incomplete", state)
	}
	if _, err := backup.Create(ctx, db, store, filepath.Join(t.TempDir(), "expired-transient.zip")); err != nil {
		t.Fatal("expired transient archive", err)
	}
}

func TestRetentionJobTransientRecordsPreserveHistoryAndBlockReuse(t *testing.T) {
	t.Run("ingest", func(t *testing.T) {
		b, db := setup(t)
		project := createProject(b, "导入历史", "transient-ingest")
		branchID := branch(b, project, "正史")
		actor := domain.Actor{ID: b.request("GET", "/api/me", nil, 200)["id"].(string)}
		jobs := &service.IngestJobs{IP: propagationService(t, db)}
		ctx := context.Background()
		job, err := jobs.Create(ctx, actor, project, branchID, "素材.md", "transient", []byte("需要保留的小说资料🌙"))
		if err != nil {
			t.Fatal(err)
		}
		if state, err := jobs.Advance(ctx, job.ID); err != nil || state != "COMPLETED" {
			t.Fatal(state, err)
		}
		expireTransientRecord(t, db, actor.ID, project, "ingest_jobs", "progress", job.ID)
		history, err := jobs.Read(ctx, actor, project, branchID, job.ID)
		if err != nil || history.Retention == nil || history.Retention.State != "EXPIRED" {
			t.Fatal("expired import history", history, err)
		}
		list, err := jobs.List(ctx, actor, project, branchID)
		if err != nil || len(list) != 1 || list[0].Retention == nil {
			t.Fatal("expired import listing", err)
		}
		if _, err := jobs.Advance(ctx, job.ID); err != domain.ErrMaterialExpired {
			t.Fatal("erased import checkpoint reused", err)
		}
		if _, err := jobs.Control(ctx, actor, project, branchID, job.ID, "resume", history.Revision); err != domain.ErrMaterialExpired {
			t.Fatal("erased import checkpoint resumed", err)
		}
		source, err := jobs.IP.ReadImport(ctx, actor, project, branchID, job.ID)
		if err != nil || source.Report.Text != "需要保留的小说资料🌙" || !source.Report.Complete {
			t.Fatal("imported author material changed", err)
		}
	})
	for _, field := range []string{"inventory", "source_snapshot"} {
		t.Run("discovery-"+field, func(t *testing.T) {
			b, db := setup(t)
			project := createProject(b, "发现历史", "transient-discovery")
			actor := domain.Actor{ID: b.request("GET", "/api/me", nil, 200)["id"].(string)}
			svc := &service.SkillDiscovery{IP: propagationService(t, db), Remote: emptyRetentionDiscovery{}}
			ctx := context.Background()
			source, err := svc.SaveSource(ctx, actor, project, "", service.DiscoverySourceInput{OperationID: "source", Repository: "fixture/repository", Ref: "main", Path: "skills", Limit: 1, Enabled: true})
			if err != nil {
				t.Fatal(err)
			}
			run, err := svc.Start(ctx, actor, project, source.ID, "transient")
			if err != nil {
				t.Fatal(err)
			}
			if state, err := svc.Execute(ctx, run.ID); err != nil || state != "COMPLETED" {
				t.Fatal(state, err)
			}
			expireTransientRecord(t, db, actor.ID, project, "skill_discovery_runs", field, run.ID)
			history, err := svc.ReadRun(ctx, actor, project, run.ID)
			if err != nil || history.Retention == nil || history.Inventory != nil {
				t.Fatal("expired discovery history", history, err)
			}
			if field == "source_snapshot" && history.Source.ID != "" {
				t.Fatal("expired snapshot disclosed")
			}
			overview, err := svc.Overview(ctx, actor, project)
			if err != nil || len(overview.Runs) != 1 || overview.Runs[0].Retention == nil {
				t.Fatal("expired discovery listing", err)
			}
			if _, err := svc.Execute(ctx, run.ID); err != domain.ErrMaterialExpired {
				t.Fatal("expired discovery executed", err)
			}
			if _, err := svc.Control(ctx, actor, project, run.ID, "resume"); err != domain.ErrMaterialExpired {
				t.Fatal("expired discovery resumed", err)
			}
		})
	}
	t.Run("preview", func(t *testing.T) {
		b, svc, actor, project, original := generationFixture(t, &generationTransport{})
		var attempts atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			attempts.Add(1)
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"id\":\"expiry-preview\",\"choices\":[{\"delta\":{\"content\":\"PRIVATE_PREVIEW\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
		}))
		defer server.Close()
		target, _ := url.Parse(server.URL)
		svc.Adapter = models.New(localStreamTransport{target})
		root := "/api/projects/" + project + "/generations"
		prepared := b.request("POST", root, map[string]any{"branch_id": original.BranchID, "role": "writer", "price_id": original.Frozen.Price.ID, "operation_id": "transient-preview", "stream": true, "request": map[string]any{"prompt": "local test", "max_output_tokens": 100}}, 201)
		id := prepared["id"].(string)
		ctx := context.Background()
		if _, err := svc.Start(ctx, actor, project, id); err != nil {
			t.Fatal(err)
		}
		if state, err := svc.Execute(ctx, id); err != nil || state != "UNKNOWN" {
			t.Fatal("missing usage must remain unknown", state, err)
		}
		before := b.request("GET", root+"/"+id, nil, 200)
		if before["preview"] == nil {
			t.Fatal("no persisted preview to expire")
		}
		expireTransientRecord(t, svc.IP.Workspace.DB, actor.ID, project, "generation_previews", "content", id)
		after := b.request("GET", root+"/"+id, nil, 200)
		if after["preview"] != nil || after["state"] != "UNKNOWN" || after["call"].(map[string]any)["state"] != "UNKNOWN" {
			t.Fatal("preview expiry changed receipt or disclosed text", after)
		}
		if _, err := svc.Execute(ctx, id); err != nil || attempts.Load() != 1 {
			t.Fatal("unknown request resent", attempts.Load(), err)
		}
	})
}
