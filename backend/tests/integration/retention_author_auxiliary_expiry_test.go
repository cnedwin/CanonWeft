package integration

import (
	"archive/zip"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"novelstudio/internal/authority"
	"novelstudio/internal/backup"
	"novelstudio/internal/domain"
	"novelstudio/internal/retention"
	"novelstudio/internal/service"
	"novelstudio/internal/storage"
)

func TestExpiredAuthorAuxiliaryHistoryDoesNotBreakActiveProject(t *testing.T) {
	for _, table := range []string{"change_plans", "ip_branch_deltas"} {
		t.Run(table, func(t *testing.T) {
			b, db := setup(t)
			ctx := context.Background()
			project := createProject(b, "历史资料清理", "aux-history")
			branchID := branch(b, project, "正史")
			candidate := propose(b, project, branchID, "", "history-candidate", originalSnapshot())
			actor := domain.Actor{ID: b.request("GET", "/api/me", nil, 200)["id"].(string)}
			ip := propagationService(t, db)
			plan, err := ip.CreateChangePlan(ctx, actor, project, service.ChangePlanInput{BranchID: branchID, CandidateID: candidate, OperationID: "history-plan"})
			if err != nil {
				t.Fatal(err)
			}
			id := candidate
			if table == "change_plans" {
				id = plan.ID
			}
			want := 1
			if table == "ip_branch_deltas" {
				want = 2
			} // The plan pins this candidate's exact delta.
			expireTransientRecord(t, db, actor.ID, project, table, "payload", id, want)
			if table == "change_plans" {
				history, err := ip.ChangePlans(ctx, actor, project, branchID)
				if err != nil || len(history) != 1 || history[0].Retention == nil {
					t.Fatal("expired plan broke history", err)
				}
				if _, err := ip.ReadChangePlan(ctx, actor, project, plan.ID); !errors.Is(err, domain.ErrMaterialExpired) {
					t.Fatal("expired plan reused", err)
				}
			} else {
				history, err := ip.Candidates(ctx, actor, project, branchID)
				if err != nil || len(history) != 1 || history[0].Retention == nil || history[0].Retention.State != "EXPIRED" {
					t.Fatal("expired auxiliary broke candidate history", err)
				}
				if _, err := ip.Adopt(ctx, actor, project, branchID, candidate); !errors.Is(err, domain.ErrMaterialExpired) {
					t.Fatal("expired candidate adopted", err)
				}
			}
			if _, err := db.Exec("DELETE FROM retention_evidence WHERE project_id=?", project); err != nil {
				t.Fatal(err)
			}
			if table == "change_plans" {
				if _, err := ip.ChangePlans(ctx, actor, project, branchID); err == nil {
					t.Fatal("plan history trusted marker without proof")
				}
			} else if _, err := ip.Candidates(ctx, actor, project, branchID); err == nil {
				t.Fatal("candidate history trusted marker without proof")
			}
		})
	}
}

func TestExpiredUnadoptedImportRefusesReuseAndAllowsFreshUpload(t *testing.T) {
	b, db := setup(t)
	ctx := context.Background()
	project := createProject(b, "过期导入续用", "expired-import-runtime")
	branchID := branch(b, project, "正史")
	actor := domain.Actor{ID: b.request("GET", "/api/me", nil, 200)["id"].(string)}
	ip := propagationService(t, db)
	data := []byte("PRIVATE_CANARY 旧导入文本")
	source, err := ip.ImportSource(ctx, actor, project, branchID, "资料.txt", "import-old", data)
	if err != nil {
		t.Fatal(err)
	}
	expireTransientRecord(t, db, actor.ID, project, "source_imports", "report", source.ID)
	if _, err := ip.ReadImport(ctx, actor, project, branchID, source.ID); !errors.Is(err, domain.ErrMaterialExpired) {
		t.Fatal("expired report read", err)
	}
	if _, err := ip.ImportSource(ctx, actor, project, branchID, "资料.txt", "import-old", data); !errors.Is(err, domain.ErrMaterialExpired) {
		t.Fatal("old request reused erased report", err)
	}
	fresh, err := ip.ImportSource(ctx, actor, project, branchID, "资料.txt", "import-new", data)
	if err != nil || fresh.ID == source.ID || fresh.Report.Text != string(data) {
		t.Fatal("new upload failed", err)
	}
}

func TestDeletedImportPreservesLiveConsumersAndCompletedReceipt(t *testing.T) {
	b, db := setup(t)
	ctx := context.Background()
	project := createProject(b, "完整导入资料清理", "import-material-expiry")
	branchID := branch(b, project, "正史")
	actor := domain.Actor{ID: b.request("GET", "/api/me", nil, 200)["id"].(string)}
	ip := propagationService(t, db)
	jobs := &service.IngestJobs{IP: ip}
	job, err := jobs.Create(ctx, actor, project, branchID, "资料.md", "import-expiry", []byte("PRIVATE_CANARY 作者上传的专业资料🌙"))
	if err != nil {
		t.Fatal(err)
	}
	if state, err := jobs.Advance(ctx, job.ID); err != nil || state != "COMPLETED" {
		t.Fatal(state, err)
	}
	if _, err := ip.SaveImportDraft(ctx, actor, project, branchID, job.ID, service.ImportDraft{Content: domain.ImportDraftContent{OperationID: "keep-source", Text: "PRIVATE_CANARY 未确认的编辑"}}); err != nil {
		t.Fatal(err)
	}
	installCompleteRetentionFixture(t, db)
	ledger := authority.Ledger{Schema: os.Getenv("STUDIO_AUTHORITY_SCHEMA")}
	deleteAuthorFixture(t, db, ledger, project, actor.ID)
	store, err := storage.NewLocalRetentionStore(os.Getenv("STUDIO_BLOB_ROOT"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	scanner := &service.RetentionScanner{Workspace: ip.Workspace, Blobs: store}
	source := retention.SourceKey{Table: "source_imports", Primary: map[string]string{"id": job.ID}}
	ingest := retention.SourceKey{Table: "ingest_jobs", Primary: map[string]string{"id": job.ID}}
	for _, field := range []string{"report", "source_hash"} {
		if err := expireAuthorFixture(t, db, scanner, ledger, actor.ID, project, source, field); !errors.Is(err, retention.ErrLiveExpiryConsumers) {
			t.Fatal("live draft did not protect source", field, err)
		}
	}
	draft := retention.SourceKey{Table: "import_drafts", Primary: map[string]string{"import_id": job.ID, "actor_id": actor.ID}}
	if err := expireAuthorFixture(t, db, scanner, ledger, actor.ID, project, draft, "payload"); err != nil {
		t.Fatal(err)
	}
	if err := expireAuthorFixture(t, db, scanner, ledger, actor.ID, project, source, "report"); err != nil {
		t.Fatal(err)
	}
	if _, err := backup.Create(ctx, db, store, filepath.Join(t.TempDir(), "retained-checkpoint.zip")); err != nil {
		t.Fatal("retained checkpoint versus expired report", err)
	}
	if err := expireAuthorFixture(t, db, scanner, ledger, actor.ID, project, ingest, "source_hash"); !errors.Is(err, retention.ErrLiveExpiryConsumers) {
		t.Fatal("live checkpoint did not protect original", err)
	}
	if err := expireAuthorFixture(t, db, scanner, ledger, actor.ID, project, ingest, "progress"); err != nil {
		t.Fatal(err)
	}
	if _, err := backup.Create(ctx, db, store, filepath.Join(t.TempDir(), "expired-checkpoint.zip")); err != nil {
		t.Fatal("expired checkpoint and report digest binding", err)
	}
	for _, key := range []retention.SourceKey{source, ingest} {
		if err := expireAuthorFixture(t, db, scanner, ledger, actor.ID, project, key, "source_hash"); err != nil {
			t.Fatal(key.Table, err)
		}
	}
	archive := filepath.Join(t.TempDir(), "completed-import.zip")
	if _, err := backup.Create(ctx, db, store, archive); err != nil {
		t.Fatal("actual archive", err)
	}
	z, err := zip.OpenReader(archive)
	if err != nil {
		t.Fatal(err)
	}
	defer z.Close()
	for _, f := range z.File {
		if strings.HasSuffix(f.Name, "/"+job.Hash) {
			t.Fatal("expired upload copied into archive", f.Name)
		}
	}
	if _, err := db.Exec("DELETE FROM retention_evidence WHERE project_id=? AND reference_id=(SELECT id FROM retention_references WHERE project_id=? AND owner_table='source_imports' AND field_key='report')", project, project); err != nil {
		t.Fatal(err)
	}
	if _, err := backup.Create(ctx, db, store, filepath.Join(t.TempDir(), "missing-report-proof.zip")); err == nil {
		t.Fatal("archive accepted missing report evidence")
	}
}

// All source content is created through the real author workflows before the
// isolated pending migration and historical project-deletion fixture.
func TestDeletedAuthorAuxiliariesPreservePublishedIPAndArchive(t *testing.T) {
	b, db := setup(t)
	ctx := context.Background()
	project := createProject(b, "作者变更记录清理", "author-aux-expiry")
	parent := branch(b, project, "正史")
	first := propose(b, project, parent, "", "aux-base", originalSnapshot())
	publish(b, project, parent, first)
	child := b.request("POST", "/api/projects/"+project+"/branches", map[string]any{"name": "改编", "operation_id": "aux-fork", "parent_id": parent, "base_revision": first}, 201)["id"].(string)
	changed := originalSnapshot()
	changed.Entities = append(changed.Entities, domain.Entity{ID: "herb", Name: "返照草", Kind: "item"})
	source := propose(b, project, child, first, "aux-child", changed)
	publish(b, project, child, source)
	path := "/api/projects/" + project + "/branches/" + parent
	input := map[string]any{"source_branch": child, "source_head": source, "base_head": first, "operation_id": "aux-promote", "reason": "PRIVATE_CANARY 纳入共享药草设定", "selection": map[string]any{"entities": []string{"herb"}}}
	input["change_reasons"] = deltaReasons(b.request("POST", path+"/promotion-delta-preview", input, 200))
	candidate := b.request("POST", path+"/promotions", input, 201)["id"].(string)
	actor := domain.Actor{ID: b.request("GET", "/api/me", nil, 200)["id"].(string)}
	ip := propagationService(t, db)
	plan, err := ip.CreateChangePlan(ctx, actor, project, service.ChangePlanInput{BranchID: parent, CandidateID: candidate, OperationID: "aux-plan"})
	if err != nil {
		t.Fatal(err)
	}
	installCompleteRetentionFixture(t, db)
	ledger := authority.Ledger{Schema: os.Getenv("STUDIO_AUTHORITY_SCHEMA")}
	deleteAuthorFixture(t, db, ledger, project, actor.ID)
	store, err := storage.NewLocalRetentionStore(os.Getenv("STUDIO_BLOB_ROOT"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	scanner := &service.RetentionScanner{Workspace: ip.Workspace, Blobs: store}
	for _, key := range []struct{ table, primary, id string }{
		{"change_plans", "id", plan.ID},
		{"ip_promotions", "candidate_id", candidate},
		{"ip_branch_deltas", "candidate_id", candidate},
		{"ip_branch_deltas", "candidate_id", source},
		{"ip_branch_deltas", "candidate_id", first},
	} {
		err := expireAuthorFixture(t, db, scanner, ledger, actor.ID, project, retention.SourceKey{Table: key.table, Primary: map[string]string{key.primary: key.id}}, "payload")
		if err != nil {
			t.Fatal(key.table, key.id, err)
		}
	}
	if _, err := scanner.Scan(ctx, actor.ID, project); err != nil {
		t.Fatal("source rescan", err)
	}
	var state, payload string
	if err := db.QueryRow("SELECT status,payload FROM ip_revisions WHERE id=?", source).Scan(&state, &payload); err != nil {
		t.Fatal(err)
	}
	if state != "PUBLISHED" || !json.Valid([]byte(payload)) {
		t.Fatal("cleanup rewrote published IP")
	}
	if _, err := backup.Create(ctx, db, store, filepath.Join(t.TempDir(), "author-auxiliary.zip")); err != nil {
		t.Fatal("actual archive", err)
	}
	if _, err := db.Exec("DELETE FROM retention_evidence WHERE project_id=? LIMIT 1", project); err != nil {
		t.Fatal(err)
	}
	if _, err := backup.Create(ctx, db, store, filepath.Join(t.TempDir(), "missing-proof.zip")); err == nil {
		t.Fatal("archive accepted erased author content without proof")
	}
}

func deleteAuthorFixture(t *testing.T, db *sql.DB, ledger authority.Ledger, project, actor string) {
	t.Helper()
	at := time.Now().UTC().Truncate(time.Microsecond).Add(-180 * 24 * time.Hour)
	var ledgerID string
	if err := db.QueryRow("SELECT ledger_id FROM `" + ledger.Schema + "`.sources WHERE source_database=DATABASE()").Scan(&ledgerID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO `"+ledger.Schema+"`.events(ledger_id,kind,project_id,resource_id,actor_id,reason,created_at,clock_basis) VALUES(?,'project_deleted',?,?,?,'isolated author expiry fixture',?,'UTC_V1')", ledgerID, project, project, actor, at); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE projects SET deleted_at=?,revision=revision+1 WHERE id=?", at, project); err != nil {
		t.Fatal(err)
	}
}

func expireAuthorFixture(t *testing.T, db *sql.DB, scanner *service.RetentionScanner, ledger authority.Ledger, actor, project string, key retention.SourceKey, field string) error {
	t.Helper()
	ctx := context.Background()
	scan, err := scanner.Scan(ctx, actor, project)
	if err != nil {
		return err
	}
	owner, err := retention.RowOwnerKey(key.Table, key.Primary)
	if err != nil {
		return err
	}
	var raw string
	if err := db.QueryRow("SELECT v.snapshot FROM retention_references r JOIN retention_reference_versions v ON v.reference_id=r.id AND v.revision=r.revision WHERE r.project_id=? AND r.owner_table=? AND r.owner_key=? AND r.field_key=?", project, key.Table, owner, field).Scan(&raw); err != nil {
		return err
	}
	var v retention.ReferenceVersion
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return err
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	if v.Deadline.IsZero() || v.Deadline.After(now) {
		return errors.New("fixture material is still protected")
	}
	e := retention.Expiry{ReferenceID: v.Reference.ID, Rule: v.Reference.Rule, PayloadHash: v.Reference.PayloadHash, Revision: v.Reference.Revision, Deadline: v.Deadline, Storage: v.Storage}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	batch, err := retention.LoadExpiryOwnerBatchTx(ctx, tx, project, e, []retention.Expiry{e}, now)
	if err != nil {
		return err
	}
	row, err := batch.Index.Record(key.Table, key.Primary)
	if err != nil {
		return err
	}
	var summary retention.EvidenceSummary
	value := row.Values[field]
	if key.Table == "skill_packages" && field == "source.archive_hash" {
		value, err = retention.SkillArchiveSourceValue(row)
		if err != nil {
			return err
		}
	}
	if key.Table == "artifact_releases" && strings.HasPrefix(field, "manifest.artifacts.") {
		value, err = retention.PublicationArtifactSourceValue(row, field)
		if err != nil {
			return err
		}
	}
	if v.Storage == retention.Blob {
		summary.ValueHash = value
	} else if retention.ClearScalarAllowed(e.Rule) {
		summary = retention.EvidenceSummary{}
	} else if e.Rule == "context_packages.payload" {
		summary, err = retention.ContextEvidence([]byte(row.Values[field]), row.Values["content_hash"])
	} else if e.Rule == "recipe_runs.frozen" || e.Rule == "recipe_steps.frozen" {
		summary, err = retention.RecipeEvidence(row.Table, row.Values["id"], row.Values["run_id"], []byte(row.Values[field]), row.Values["frozen_hash"])
	} else {
		summary, err = retention.TransientEvidence(row, field)
	}
	if err != nil {
		return err
	}
	proof, err := retention.ProjectExpired(e, []byte(value), summary, now)
	if retention.AnonymizedLabelAllowed(e.Rule) {
		proof, err = retention.ProjectAnonymizedLabel(e, []byte(value), summary, now)
	}
	if retention.ClearScalarAllowed(e.Rule) {
		proof, err = retention.ProjectClearedScalar(e, []byte(row.Values[field]), summary, now)
	}
	if err != nil {
		return err
	}
	if err := retention.ApplySourceExpiryTx(ctx, tx, ledger, project, batch.Source, scan.InventoryRevision, batch.Items, []retention.ExpiredProjection{proof}); err != nil {
		return err
	}
	if proof.Materialization == retention.AnonymizedLabel {
		if batch.Source.Table == "capability_heads" {
			batch.Source.Primary["capability_key"] = "~erased:" + proof.OriginalPayloadHash
		}
		if err := retention.ApplySourceExpiryTx(ctx, tx, ledger, project, batch.Source, scan.InventoryRevision, batch.Items, []retention.ExpiredProjection{proof}); err != nil {
			return err
		}
	}
	return tx.Commit()
}
