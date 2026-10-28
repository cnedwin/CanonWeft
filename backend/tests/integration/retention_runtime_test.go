package integration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"novelstudio/internal/domain"
	"novelstudio/internal/models"
	"novelstudio/internal/retention"
	"novelstudio/internal/service"
)

func TestRetentionRuntimeExpiredGenerationCannotStartOrDispatch(t *testing.T) {
	for _, reserved := range []bool{false, true} {
		name := "prepared"
		if reserved {
			name = "reserved"
		}
		t.Run(name, func(t *testing.T) {
			transport := &generationTransport{response: generationSuccess}
			b, svc, actor, project, task := generationFixture(t, transport)
			ctx := context.Background()
			var err error
			if reserved {
				task, err = svc.Start(ctx, actor, project, task.ID)
				if err != nil {
					t.Fatal(err)
				}
			}
			budgets := &service.Budgets{Workspace: svc.IP.Workspace}
			before, err := budgets.Read(ctx, actor.ID)
			if err != nil {
				t.Fatal(err)
			}
			var raw []byte
			if err := svc.IP.Workspace.DB.QueryRow("SELECT frozen FROM generation_tasks WHERE id=?", task.ID).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256(raw)
			key, _ := retention.RowOwnerKey("generation_tasks", map[string]string{"id": task.ID})
			ref, _ := retention.ReferenceID(project, "generation_tasks", key, "frozen")
			at := time.Now().UTC().Truncate(time.Microsecond)
			proof, err := retention.ProjectExpired(retention.Expiry{ReferenceID: ref, Rule: "generation_tasks.frozen", PayloadHash: hex.EncodeToString(sum[:]), Revision: 1, Deadline: at, Storage: retention.Inline}, raw, retention.EvidenceSummary{InputHash: task.Frozen.InputHash, CallID: task.CallID}, at)
			if err != nil {
				t.Fatal(err)
			}
			encoded, _ := json.Marshal(proof)
			// Deliberately no metadata registration: this test proves refusal,
			// not historical evidence acceptance or live cleanup authorization.
			if _, err := svc.IP.Workspace.DB.Exec("UPDATE generation_tasks SET frozen=? WHERE id=?", encoded, task.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := svc.Start(ctx, actor, project, task.ID); !errors.Is(err, domain.ErrMaterialExpired) {
				t.Fatal("expired generation accepted start", err)
			}
			if _, err := svc.Execute(ctx, task.ID); !errors.Is(err, domain.ErrMaterialExpired) {
				t.Fatal("expired generation accepted worker dispatch", err)
			}
			b.request("GET", "/api/projects/"+project+"/generations/"+task.ID, nil, 410)
			after, err := budgets.Read(ctx, actor.ID)
			if err != nil || after.Reserved != before.Reserved || after.Spent != before.Spent || transport.calls.Load() != 0 {
				t.Fatal("expiry refusal changed fees or called provider", err)
			}
		})
	}
}

func TestRetentionRuntimeGenerationListKeepsVerifiedExpiredHistory(t *testing.T) {
	transport := &generationTransport{response: generationSuccess}
	b, svc, actor, project, task := generationFixture(t, transport)
	db := svc.IP.Workspace.DB
	ctx := context.Background()
	live, err := svc.Prepare(ctx, actor, project, service.GenerationInput{BranchID: task.BranchID, Role: "writer", PriceID: task.Frozen.Price.ID, OperationID: "live-after-expiry", Request: models.Request{Prompt: "新任务", MaxOutputTokens: 100}})
	if err != nil {
		t.Fatal(err)
	}
	ddl, err := os.ReadFile("../../migrations/retention.sql.pending")
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range strings.Split(string(ddl), ";") {
		if strings.TrimSpace(statement) != "" {
			if _, err := db.Exec(statement); err != nil {
				t.Fatal(err)
			}
		}
	}
	var raw []byte
	if err = db.QueryRow("SELECT frozen FROM generation_tasks WHERE id=?", task.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	hash := func(raw []byte) string {
		sum := sha256.Sum256(raw)
		return hex.EncodeToString(sum[:])
	}
	key, _ := retention.RowOwnerKey("generation_tasks", map[string]string{"id": task.ID})
	ref, _ := retention.ReferenceID(project, "generation_tasks", key, "frozen")
	at := time.Now().UTC().Truncate(time.Microsecond)
	projection, err := retention.ProjectExpired(retention.Expiry{ReferenceID: ref, Rule: "generation_tasks.frozen", PayloadHash: hash(raw), Revision: 1, Deadline: at, Storage: retention.Inline}, raw, retention.EvidenceSummary{InputHash: task.Frozen.InputHash}, at)
	if err != nil {
		t.Fatal(err)
	}
	proof, _ := json.Marshal(projection)
	if _, err = db.Exec("INSERT INTO retention_references(id,project_id,owner_table,owner_key,field_key,class,storage_kind,payload_hash,source_created_at,time_basis,first_verified_at,deadline,state,expired_at) VALUES(?,?,'generation_tasks',?,'frozen','RAW_RUN','INLINE',?,?,'SOURCE_UTC',?,?,'EXPIRED',?)", ref, project, key, hash(raw), at.Add(-30*24*time.Hour), at, at, at); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("INSERT INTO retention_evidence(id,project_id,reference_id,reference_revision,original_payload_hash,projection_hash,projection,expired_at) VALUES(?,?,?,1,?,?,?,?)", hash([]byte(ref)), project, ref, hash(raw), hash(proof), proof, at); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("UPDATE generation_tasks SET frozen=? WHERE id=?", proof, task.ID); err != nil {
		t.Fatal(err)
	}
	path := "/api/projects/" + project + "/generations?branch=" + task.BranchID
	response := b.request("GET", path, nil, 200)
	items := response["items"].([]any)
	if len(items) != 2 {
		t.Fatal("expired row hid another live task", response)
	}
	seen := false
	for _, item := range items {
		value := item.(map[string]any)
		if value["id"] == task.ID {
			marker, ok := value["retention"].(map[string]any)
			if !ok || marker["state"] != "EXPIRED" || marker["expired_at"] == "" {
				t.Fatal("expired history lacks verified marker", value)
			}
			encoded, _ := json.Marshal(value)
			if strings.Contains(string(encoded), "deepseek-flash") || strings.Contains(string(encoded), task.Frozen.InputHash) {
				t.Fatal("expired list exposed frozen private material")
			}
		} else if value["id"] == live.ID {
			seen = value["retention"] == nil
		}
	}
	if !seen || transport.calls.Load() != 0 {
		t.Fatal("live history or no-dispatch boundary lost")
	}
	if _, err = db.Exec("UPDATE retention_evidence SET projection_hash=? WHERE reference_id=?", strings.Repeat("0", 64), ref); err != nil {
		t.Fatal(err)
	}
	b.request("GET", path, nil, 503)
}
