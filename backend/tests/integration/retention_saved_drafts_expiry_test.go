package integration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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

func TestSavedDraftExpiryRequiresDeletedProjectAndPreservesArchive(t *testing.T) {
	for _, state := range []string{"active", "recoverable", "expired"} {
		t.Run(state, func(t *testing.T) {
			b, db := setup(t)
			ctx := context.Background()
			project := createProject(b, "草稿生命周期", "draft-expiry")
			branchID := branch(b, project, "正史")
			resourceID := resource(b, project, branchID, "草稿正文")
			actor := domain.Actor{ID: b.request("GET", "/api/me", nil, 200)["id"].(string)}
			ip := propagationService(t, db)
			if _, err := ip.SaveResourceDraft(ctx, actor, project, resourceID, service.ResourceDraft{Text: "PRIVATE_CANARY 原始正文🌙"}); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec("INSERT INTO ip_drafts(branch_id,actor_id,revision,base_head,payload) VALUES(?,?,1,'',?)", branchID, actor.ID, `{"unfinished":"PRIVATE_CANARY 世界设定"}`); err != nil {
				t.Fatal(err)
			}
			patch := service.PatchDraft{Content: &domain.PatchDraftContent{OperationID: "draft-patch", Group: domain.TextPatchGroup{ID: strings.Repeat("a", 32)}, Snippet: "PRIVATE_CANARY 待润色片段"}}
			if _, err := ip.SavePatchDraft(ctx, actor, project, branchID, patch); err != nil {
				t.Fatal(err)
			}
			source, err := ip.ImportSource(ctx, actor, project, branchID, "资料.txt", "draft-source", []byte("保留的作者上传原文"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ip.SaveImportDraft(ctx, actor, project, branchID, source.ID, service.ImportDraft{Content: domain.ImportDraftContent{OperationID: "draft-import", Text: "PRIVATE_CANARY 导入编辑"}}); err != nil {
				t.Fatal(err)
			}
			installCompleteRetentionFixture(t, db)
			store, err := storage.NewLocalRetentionStore(os.Getenv("STUDIO_BLOB_ROOT"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			scanner := &service.RetentionScanner{Workspace: ip.Workspace, Blobs: store}
			ledger := authority.Ledger{Schema: os.Getenv("STUDIO_AUTHORITY_SCHEMA")}
			keys := []struct {
				table, field string
				primary      map[string]string
			}{
				{"ip_drafts", "payload", map[string]string{"branch_id": branchID, "actor_id": actor.ID}},
				{"resource_drafts", "body", map[string]string{"resource_id": resourceID, "actor_id": actor.ID}},
				{"resource_policy_drafts", "payload", map[string]string{"resource_id": resourceID, "actor_id": actor.ID}},
				{"patch_drafts", "payload", map[string]string{"branch_id": branchID, "actor_id": actor.ID}},
				{"import_drafts", "payload", map[string]string{"import_id": source.ID, "actor_id": actor.ID}},
				{"source_imports", "report", map[string]string{"id": source.ID}},
				{"source_imports", "source_hash", map[string]string{"id": source.ID}},
			}
			// Seed only the isolated fixture's original observation time. Drafts
			// and their upload/report are old even while active or recoverable.
			for _, key := range keys {
				var raw string
				if err := db.QueryRow("SELECT " + key.field + " FROM " + key.table).Scan(&raw); err != nil {
					t.Fatal(err)
				}
				owner, _ := retention.RowOwnerKey(key.table, key.primary)
				id, _ := retention.ReferenceID(project, key.table, owner, key.field)
				hash := sha256.Sum256([]byte(raw))
				if _, err := db.Exec("INSERT INTO retention_source_slots(id,project_id,owner_table,owner_key,field_key,time_basis,first_verified_at) VALUES(?,?,?,?,?,'LEGACY_REGISTERED','2020-01-02 03:04:05.000006')", id, project, key.table, owner, key.field); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec("INSERT INTO retention_source_values(source_id,payload_hash,is_null,first_verified_at) VALUES(?,?,FALSE,'2020-01-02 03:04:05.000006')", id, hex.EncodeToString(hash[:])); err != nil {
					t.Fatal(err)
				}
			}
			if state != "active" {
				// Authoritative deletion fixture; do not change the real clock or
				// shorten production recovery windows to exercise old deletion.
				at := time.Now().UTC().Truncate(time.Microsecond)
				if state == "expired" {
					at = at.Add(-180 * 24 * time.Hour)
				}
				var ledgerID string
				if err := db.QueryRow("SELECT ledger_id FROM `" + ledger.Schema + "`.sources WHERE source_database=DATABASE()").Scan(&ledgerID); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec("INSERT INTO `"+ledger.Schema+"`.events(ledger_id,kind,project_id,resource_id,actor_id,reason,created_at,clock_basis) VALUES(?,'project_deleted',?,?,?,'isolated expiry fixture',?,'UTC_V1')", ledgerID, project, project, actor.ID, at); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec("UPDATE projects SET deleted_at=?,revision=revision+1 WHERE id=?", at, project); err != nil {
					t.Fatal(err)
				}
			}
			for _, key := range keys {
				scan, err := scanner.Scan(ctx, actor.ID, project)
				if err != nil {
					t.Fatal("scan", key.table, err)
				}
				var raw string
				if err := db.QueryRow("SELECT v.snapshot FROM retention_references r JOIN retention_reference_versions v ON v.reference_id=r.id AND v.revision=r.revision WHERE r.project_id=? AND r.owner_table=? AND r.field_key=?", project, key.table, key.field).Scan(&raw); err != nil {
					t.Fatal(err)
				}
				var v retention.ReferenceVersion
				if err := json.Unmarshal([]byte(raw), &v); err != nil || !v.Reference.ContentRoot {
					t.Fatal("saved author root lost", key.table, key.field, err)
				}
				now := time.Now().UTC().Truncate(time.Microsecond)
				if state != "expired" {
					if !v.Deadline.IsZero() && !v.Deadline.After(now) {
						t.Fatal("live or recoverable draft eligible for erasure", key.table)
					}
					continue
				}
				expiry := retention.Expiry{ReferenceID: v.Reference.ID, Rule: v.Reference.Rule, PayloadHash: v.Reference.PayloadHash, Revision: v.Reference.Revision, Deadline: v.Deadline, Storage: v.Storage}
				tx, err := db.BeginTx(ctx, nil)
				if err != nil {
					t.Fatal(err)
				}
				batch, err := retention.LoadExpiryOwnerBatchTx(ctx, tx, project, expiry, []retention.Expiry{expiry}, now)
				if err != nil {
					tx.Rollback()
					t.Fatal(err)
				}
				row, err := batch.Index.Record(key.table, key.primary)
				if err != nil {
					tx.Rollback()
					t.Fatal(err)
				}
				var summary retention.EvidenceSummary
				if v.Storage == retention.Blob {
					summary.ValueHash = row.Values[key.field]
				} else {
					summary, err = retention.TransientEvidence(row, key.field)
				}
				if err != nil {
					tx.Rollback()
					t.Fatal(err)
				}
				proof, err := retention.ProjectExpired(expiry, []byte(row.Values[key.field]), summary, now)
				if err != nil {
					tx.Rollback()
					t.Fatal(err)
				}
				if err := retention.ApplySourceExpiryTx(ctx, tx, ledger, project, batch.Source, scan.InventoryRevision, batch.Items, []retention.ExpiredProjection{proof}); err != nil {
					tx.Rollback()
					t.Fatal(key.table, err)
				}
				if err := tx.Commit(); err != nil {
					t.Fatal(err)
				}
			}
			if state == "expired" {
				if _, err := ip.ReadResourceDraft(ctx, actor, project, resourceID); err == nil {
					t.Fatal("deleted project still exposes private draft")
				}
				if _, err := scanner.Scan(ctx, actor.ID, project); err != nil {
					t.Fatal("post-erasure scan", err)
				}
			}
			if _, err := backup.Create(ctx, db, store, filepath.Join(t.TempDir(), "saved-drafts.zip")); err != nil {
				t.Fatal("actual archive", err)
			}
			if state == "expired" {
				if _, err := db.Exec("DELETE FROM retention_evidence WHERE project_id=? LIMIT 1", project); err != nil {
					t.Fatal(err)
				}
				if _, err := backup.Create(ctx, db, store, filepath.Join(t.TempDir(), "missing-proof.zip")); err == nil {
					t.Fatal("archive accepted an erased draft without independent evidence")
				}
			}
		})
	}
}
