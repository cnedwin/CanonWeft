package integration

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"

	"novelstudio/internal/domain"
	"novelstudio/internal/models"
	"novelstudio/internal/retention"
	"novelstudio/internal/service"
)

func TestRetentionIdentityRetiredRequestCannotBecomeAnotherPaidCall(t *testing.T) {
	b, db := setup(t)
	project := createProject(b, "请求编号兼容", "identity-project")
	me := b.request("GET", "/api/me", nil, 200)
	actor := domain.Actor{ID: me["id"].(string)}
	workspace := me["workspace_id"].(string)
	budgets := &service.Budgets{Workspace: &service.Workspace{DB: db}}
	ctx := context.Background()
	if _, err := budgets.Configure(ctx, actor.ID, service.Budget{Currency: "USD", Limit: 1000, PerCall: 100, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	op := "Résumé-private-request"
	input := strings.Repeat("a", 64)
	call, err := budgets.Reserve(ctx, actor, project, op, 80, "USD", input)
	if err != nil {
		t.Fatal(err)
	}
	// model_calls uses utf8mb4_bin (PAD SPACE), unlike the ai_ci task tables.
	equivalent, err := budgets.Reserve(ctx, actor, project, op+" ", 80, "USD", input)
	if err != nil || equivalent.ID != call.ID {
		t.Fatal("fixture does not reproduce legacy MySQL equality", err)
	}
	ddl, err := os.ReadFile("../../migrations/retention_identity.sql.pending")
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
	var storedHash string
	if err = db.QueryRow("SELECT input_hash FROM model_calls WHERE id=?", call.ID).Scan(&storedHash); err != nil {
		t.Fatal(err)
	}
	ownerKey, _ := retention.RowOwnerKey("model_calls", map[string]string{"id": call.ID})
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	retired, err := retention.RetireRequestKeyTx(ctx, tx, retention.RequestKeyRetirement{WorkspaceID: workspace, ActorID: actor.ID, Table: "model_calls", OriginalKey: op, SourceID: call.ID, RequestHash: storedHash, OriginalOwnerKey: ownerKey})
	if err != nil {
		t.Fatal(err)
	}
	anonymous := retired.AnonymousKey
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	replay, err := budgets.Reserve(ctx, actor, project, op, 80, "USD", input)
	if err != nil || replay.ID != call.ID {
		t.Fatal("expired original ID was treated as a fresh request", err)
	}
	if _, err := budgets.Reserve(ctx, actor, project, op+" ", 80, "USD", input); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("unknown legacy equality variant was admitted as a new call", err)
	}
	if _, err := budgets.Reserve(ctx, actor, project, anonymous, 80, "USD", input); !errors.Is(err, domain.ErrValidation) {
		t.Fatal("client impersonated a reserved anonymous key", err)
	}
	fresh, err := budgets.Reserve(ctx, actor, project, "request-v2:"+service.NewID(), 20, "USD", strings.Repeat("b", 64))
	if err != nil || fresh.ID == call.ID {
		t.Fatal("canonical new request was blocked", err)
	}
	budget, err := budgets.Read(ctx, actor.ID)
	if err != nil || budget.Reserved != 100 || budget.Spent != 0 {
		t.Fatal("identity replay changed the monetary ledger", err)
	}
	if _, err = db.Exec("UPDATE retention_operation_keys SET source_id=? WHERE actor_id=?", fresh.ID, actor.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := budgets.Reserve(ctx, actor, project, op, 80, "USD", input); !errors.Is(err, domain.ErrNotReady) {
		t.Fatal("corrupt alias silently resolved or admitted a new call", err)
	}
}

func installIdentityFixture(t *testing.T, db *sql.DB) {
	t.Helper()
	ddl, err := os.ReadFile("../../migrations/retention_identity.sql.pending")
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
}

// Installs metadata only in the disposable test DB and invokes the real atomic
// key conversion. It does not exercise expiry eligibility or the full executor.
func retireIdentityFixture(t *testing.T, db *sql.DB, workspace, actor, table, key, id string) string {
	t.Helper()
	query := ""
	switch table {
	case "operations":
		query = "SELECT request_hash,kind FROM operations WHERE actor_id=? AND operation_id=?"
	case "generation_tasks":
		query = "SELECT request_hash,'' FROM generation_tasks WHERE actor_id=? AND operation_id=?"
	default:
		t.Fatal("unsupported fixture table")
	}
	var fingerprint, kind string
	if err := db.QueryRow(query, actor, key).Scan(&fingerprint, &kind); err != nil {
		t.Fatal(err)
	}
	primary := map[string]string{"id": id}
	if table == "operations" {
		primary = map[string]string{"actor_id": actor, "operation_id": key}
	}
	owner, err := retention.RowOwnerKey(table, primary)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	retired, err := retention.RetireRequestKeyTx(context.Background(), tx, retention.RequestKeyRetirement{WorkspaceID: workspace, ActorID: actor, Table: table, OriginalKey: key, SourceID: id, RequestHash: fingerprint, Kind: kind, OriginalOwnerKey: owner})
	if err != nil {
		t.Fatal(err)
	}
	if table == "operations" && (retired.OriginalOwnerKey == retired.CurrentOwnerKey || strings.Contains(retired.CurrentOwnerKey, key)) {
		t.Fatal("composite primary key conversion lost its original identity or retained private text")
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return retired.AnonymousKey
}

func TestRetentionIdentityProjectReceiptAndCanonicalReplay(t *testing.T) {
	b, db := setup(t)
	me := b.request("GET", "/api/me", nil, 200)
	actor, workspace := me["id"].(string), me["workspace_id"].(string)
	svc := &service.Workspace{DB: db}
	ctx := context.Background()
	key := "request-v2:" + service.NewID()
	p, err := svc.CreateProject(ctx, actor, "被保留的原项目", key)
	if err != nil {
		t.Fatal(err)
	}
	installIdentityFixture(t, db)
	anonymous := retireIdentityFixture(t, db, workspace, actor, "operations", key, p.ID)
	replay, err := svc.CreateProject(ctx, actor, p.Name, key)
	if err != nil || replay.ID != p.ID {
		t.Fatal("canonical retired request created another project", err)
	}
	if _, err = svc.CreateProject(ctx, actor, "changed request", key); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("retirement changed request fingerprint semantics", err)
	}
	for _, denied := range []string{"unknown-legacy", "request-v2:" + strings.Repeat("A", 32), "generation-" + service.NewID()} {
		if _, err = svc.CreateProject(ctx, actor, "must not create", denied); !errors.Is(err, domain.ErrConflict) {
			t.Fatal("noncanonical or public internal request admitted", denied, err)
		}
	}
	if _, err = svc.CreateProject(ctx, actor, p.Name, anonymous); !errors.Is(err, domain.ErrValidation) {
		t.Fatal("anonymous namespace was public", err)
	}
	if _, err = svc.CreateProject(ctx, actor, "new project", "request-v2:"+service.NewID()); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("UPDATE retention_operation_keys SET operation_kind='project.delete' WHERE actor_id=?", actor); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.CreateProject(ctx, actor, p.Name, key); !errors.Is(err, domain.ErrNotReady) {
		t.Fatal("altered operation kind accepted", err)
	}
}

func TestRetentionIdentityGenerationReplayAndInternalReservation(t *testing.T) {
	transport := &generationTransport{response: generationSuccess}
	b, svc, actor, project, original := generationFixture(t, transport)
	db := svc.IP.Workspace.DB
	me := b.request("GET", "/api/me", nil, 200)
	workspace := me["workspace_id"].(string)
	ctx := context.Background()
	input := service.GenerationInput{BranchID: original.BranchID, Role: "writer", PriceID: original.Frozen.Price.ID, OperationID: "Résumé-task", Request: models.Request{Prompt: "固定输入", MaxOutputTokens: 100}}
	task, err := svc.Prepare(ctx, actor, project, input)
	if err != nil {
		t.Fatal(err)
	}
	var equivalentID string
	if err = db.QueryRow("SELECT id FROM generation_tasks WHERE actor_id=? AND operation_id=?", actor.ID, "resume-task").Scan(&equivalentID); err != nil || equivalentID != task.ID {
		t.Fatal("task collation fixture mismatch", err)
	}
	installIdentityFixture(t, db)
	anonymous := retireIdentityFixture(t, db, workspace, actor.ID, "generation_tasks", input.OperationID, task.ID)
	replay, err := svc.Prepare(ctx, actor, project, input)
	if err != nil || replay.ID != task.ID {
		t.Fatal("retired task was recreated", err)
	}
	input.OperationID = "resume-task"
	if _, err = svc.Prepare(ctx, actor, project, input); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("unknown accent variant became new task", err)
	}
	input.OperationID = strings.Replace(anonymous, "retired", "rétired", 1)
	if _, err = svc.Prepare(ctx, actor, project, input); !errors.Is(err, domain.ErrValidation) {
		t.Fatal("accent variant reached anonymous source", err)
	}
	input.OperationID = "request-v2:" + service.NewID()
	fresh, err := svc.Prepare(ctx, actor, project, input)
	if err != nil {
		t.Fatal("canonical generation not admitted", err)
	}
	if _, err = svc.Start(ctx, actor, project, fresh.ID); err != nil {
		t.Fatal("server-generated reservation ID was blocked", err)
	}
	if transport.calls.Load() != 0 {
		t.Fatal("identity checks dispatched a model")
	}
}

func TestRetentionIdentityPartialMetadataFailsClosed(t *testing.T) {
	b, db := setup(t)
	me := b.request("GET", "/api/me", nil, 200)
	installIdentityFixture(t, db)
	if _, err := db.Exec("DROP TABLE retention_operation_keys"); err != nil {
		t.Fatal(err)
	}
	_, err := (&service.Workspace{DB: db}).CreateProject(context.Background(), me["id"].(string), "must not create", "request-v2:"+service.NewID())
	if !errors.Is(err, domain.ErrNotReady) {
		t.Fatal("partial identity schema silently admitted a write", err)
	}
}
