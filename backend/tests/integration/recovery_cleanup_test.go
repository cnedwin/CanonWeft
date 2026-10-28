package integration

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"novelstudio/internal/authority"
	"novelstudio/internal/retention"
	"novelstudio/internal/service"
	"novelstudio/internal/storage"
)

type recoveryDeleteFailure struct{ *storage.LocalRetentionStore }

func (s recoveryDeleteFailure) DeleteObject(context.Context, string, string) (storage.GCResult, error) {
	return "", errors.New("test deletion unavailable")
}

func TestRecoveryCleanupResumesFromDurableJournalAndPreservesAuthority(t *testing.T) {
	b, db := setup(t)
	ctx := context.Background()
	source := createProject(b, "恢复残留核对", "recovery-cleanup-source")
	actor := b.request("GET", "/api/me", nil, 200)["id"].(string)
	installCompleteRetentionFixture(t, db)
	blobs, err := storage.NewLocalRetentionStore(os.Getenv("STUDIO_BLOB_ROOT"))
	if err != nil {
		t.Fatal(err)
	}
	defer blobs.Close()
	scanner := &service.RetentionScanner{Workspace: &service.Workspace{DB: db}, Blobs: blobs}
	original, err := blobs.PutObject(ctx, source, strings.NewReader("原作正文不属于恢复垃圾"))
	if err != nil {
		t.Fatal(err)
	}
	makeAttempt := func(namespace string) (string, string) {
		t.Helper()
		target := service.NewID()
		body := "未提交副本的正文"
		hash := fmt.Sprintf("%x", sha256.Sum256([]byte(body)))
		if enabled, err := retention.BeginRecoveryAttempt(ctx, db, source, target, actor, namespace); err != nil || !enabled {
			t.Fatal("durable attempt", enabled, err)
		}
		if err := retention.RecordRecoveryObject(ctx, db, target, retention.PhysicalObject{Hash: hash, Size: int64(len(body))}); err != nil {
			t.Fatal(err)
		}
		if _, err := blobs.PutObject(ctx, target, strings.NewReader(body)); err != nil {
			t.Fatal(err)
		}
		return target, hash
	}
	age := func(target string) {
		t.Helper()
		if _, err := db.Exec("UPDATE retention_recovery_attempts SET created_at=UTC_TIMESTAMP(6)-INTERVAL 11 MINUTE,updated_at=UTC_TIMESTAMP(6)-INTERVAL 11 MINUTE WHERE target_project_id=?", target); err != nil {
			t.Fatal(err)
		}
	}
	state := func(target, want string) {
		t.Helper()
		var value string
		if err := db.QueryRow("SELECT state FROM retention_recovery_attempts WHERE target_project_id=?", target).Scan(&value); err != nil || value != want {
			t.Fatal("unexpected journal state", value, want, err)
		}
	}
	target, hash := makeAttempt(blobs.RetentionNamespace())
	prepared := filepath.Join(os.Getenv("STUDIO_BLOB_ROOT"), target, "prepared-recovery-"+hash)
	if err := os.WriteFile(prepared, []byte("未提交副本的正文"), 0600); err != nil {
		t.Fatal(err)
	}
	if changed, err := scanner.ReconcileRecovery(ctx); err != nil || changed {
		t.Fatal("fresh writer grace was ignored", changed, err)
	}
	age(target)
	// A still-running recovery owns the actor row. Maintenance must wait, even
	// when a stale clock would otherwise make the attempt eligible.
	active, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var locked string
	if err := active.QueryRowContext(ctx, "SELECT id FROM users WHERE id=? FOR UPDATE", actor).Scan(&locked); err != nil {
		active.Rollback()
		t.Fatal(err)
	}
	bounded, cancel := context.WithTimeout(ctx, 150*time.Millisecond)
	_, blocked := scanner.ReconcileRecovery(bounded)
	cancel()
	if blocked == nil {
		active.Rollback()
		t.Fatal("maintenance raced a live recovery")
	}
	if _, err := blobs.ReadObject(ctx, target, hash); err != nil {
		active.Rollback()
		t.Fatal("live recovery lost its file", err)
	}
	if _, err := os.Stat(prepared); err != nil {
		active.Rollback()
		t.Fatal("live recovery lost its prepared file", err)
	}
	if err := active.Rollback(); err != nil {
		t.Fatal(err)
	}
	failing := &service.RetentionScanner{Workspace: scanner.Workspace, Blobs: recoveryDeleteFailure{blobs}}
	if _, err := failing.ReconcileRecovery(ctx); err == nil {
		t.Fatal("physical failure was hidden")
	}
	state(target, "WRITING")
	// Recreate the service: no in-memory write list or failed HTTP request is
	// available to this new instance.
	restarted := &service.RetentionScanner{Workspace: &service.Workspace{DB: db}, Blobs: blobs}
	if changed, err := restarted.ReconcileRecovery(ctx); err != nil || !changed {
		t.Fatal("restart did not reconcile abandoned copy", changed, err)
	}
	state(target, "CLEANED")
	if _, err := blobs.ReadObject(ctx, target, hash); err == nil {
		t.Fatal("abandoned text remains")
	}
	if _, err := os.Stat(prepared); !os.IsNotExist(err) {
		t.Fatal("prepared link remains", err)
	}
	for _, phase := range []string{"empty", "partial"} {
		t.Run(phase, func(t *testing.T) {
			target, hash := makeAttempt(blobs.RetentionNamespace())
			if _, err := blobs.DeleteObject(ctx, target, hash); err != nil {
				t.Fatal(err)
			}
			data := []byte("未提交")
			if phase == "empty" {
				data = nil
			}
			path := filepath.Join(os.Getenv("STUDIO_BLOB_ROOT"), target, "prepared-recovery-"+hash)
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			age(target)
			if changed, err := restarted.ReconcileRecovery(ctx); err != nil || !changed {
				t.Fatal(changed, err)
			}
			state(target, "CLEANED")
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatal("partial author data remains", err)
			}
		})
	}
	for _, scenario := range []string{"unknown-file", "temporary-file", "wrong-store", "protected-project", "malformed", "restored", "committed"} {
		t.Run(scenario, func(t *testing.T) {
			namespace := blobs.RetentionNamespace()
			if scenario == "wrong-store" {
				namespace = strings.Repeat("f", 64)
			}
			target, hash := makeAttempt(namespace)
			prepared := filepath.Join(os.Getenv("STUDIO_BLOB_ROOT"), target, "prepared-recovery-"+hash)
			if err := os.WriteFile(prepared, []byte("未提交"), 0600); err != nil {
				t.Fatal(err)
			}
			age(target)
			switch scenario {
			case "temporary-file":
				if err := os.WriteFile(filepath.Join(os.Getenv("STUDIO_BLOB_ROOT"), target, "prepared-"+service.NewID()), []byte("partial temporary write"), 0600); err != nil {
					t.Fatal(err)
				}
			case "malformed":
				if _, err := db.Exec("UPDATE retention_recovery_attempts SET objects='[{\"unexpected\":true}]' WHERE target_project_id=?", target); err != nil {
					t.Fatal(err)
				}
			case "unknown-file":
				if _, err := blobs.PutObject(ctx, target, strings.NewReader("未登记文件不得删除")); err != nil {
					t.Fatal(err)
				}
			case "protected-project":
				tx, err := db.BeginTx(ctx, nil)
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback()
				ledger := authority.Ledger{Schema: os.Getenv("STUDIO_AUTHORITY_SCHEMA")}
				if err := ledger.AppendTx(ctx, tx, authority.Event{Kind: authority.ProjectCreated, ProjectID: target, ResourceID: target, ActorID: actor}); err != nil {
					t.Fatal(err)
				}
				if err := tx.Commit(); err != nil {
					t.Fatal(err)
				}
			case "restored":
				if _, err := db.Exec("UPDATE retention_recovery_attempts SET restored_disabled=1 WHERE target_project_id=?", target); err != nil {
					t.Fatal(err)
				}
			case "committed":
				if _, err := db.Exec("UPDATE retention_recovery_attempts SET state='COMMITTED' WHERE target_project_id=?", target); err != nil {
					t.Fatal(err)
				}
			}
			changed, err := restarted.ReconcileRecovery(ctx)
			if err != nil || changed != (scenario != "restored" && scenario != "committed") {
				t.Fatal("unsafe attempt result", changed, err)
			}
			want := "BLOCKED"
			if scenario == "restored" {
				want = "WRITING"
			}
			if scenario == "committed" {
				want = "COMMITTED"
			}
			state(target, want)
			if _, err := blobs.ReadObject(ctx, target, hash); err != nil {
				t.Fatal("unsafe attempt was deleted", err)
			}
			if _, err := os.Stat(prepared); err != nil {
				t.Fatal("unsafe attempt lost prepared material", err)
			}
		})
	}
	if _, err := blobs.ReadObject(ctx, source, original.Hash); err != nil {
		t.Fatal("source text changed", err)
	}
}
