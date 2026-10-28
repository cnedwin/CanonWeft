package integration

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"novelstudio/internal/domain"
	"novelstudio/internal/retention"
	"novelstudio/internal/service"
)

func TestRetentionIdentityAtomicCutoverAndRollback(t *testing.T) {
	b, db := setup(t)
	project := createProject(b, "事务中断与重试", "atomic-retention-project")
	me := b.request("GET", "/api/me", nil, 200)
	actor := domain.Actor{ID: me["id"].(string), WorkspaceID: me["workspace_id"].(string)}
	ctx := context.Background()
	budgets := &service.Budgets{Workspace: &service.Workspace{DB: db}}
	if _, err := budgets.Configure(ctx, actor.ID, service.Budget{Currency: "USD", Limit: 1000, PerCall: 100, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	key, input := "private-original-key", strings.Repeat("a", 64)
	call, err := budgets.Reserve(ctx, actor, project, key, 80, "USD", input)
	if err != nil {
		t.Fatal(err)
	}
	installIdentityFixture(t, db)
	var fingerprint string
	if err = db.QueryRow("SELECT input_hash FROM model_calls WHERE id=?", call.ID).Scan(&fingerprint); err != nil {
		t.Fatal(err)
	}
	owner, _ := retention.RowOwnerKey("model_calls", map[string]string{"id": call.ID})
	target := retention.RequestKeyRetirement{WorkspaceID: actor.WorkspaceID, ActorID: actor.ID, Table: "model_calls", OriginalKey: key, SourceID: call.ID, RequestHash: fingerprint, OriginalOwnerKey: owner}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	first, err := retention.RetireRequestKeyTx(ctx, tx, target)
	if err != nil {
		t.Fatal(err)
	}
	if first.OriginalOwnerKey != owner || first.CurrentOwnerKey != owner || !strings.HasPrefix(first.AnonymousKey, "retired-v1:") {
		t.Fatal("invalid key conversion", first)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	var original string
	var aliases, policies int
	if err = db.QueryRow("SELECT operation_id FROM model_calls WHERE id=?", call.ID).Scan(&original); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow("SELECT COUNT(*) FROM retention_operation_keys").Scan(&aliases); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow("SELECT COUNT(*) FROM retention_identity_policies WHERE legacy_writes_closed=TRUE").Scan(&policies); err != nil {
		t.Fatal(err)
	}
	if original != key || aliases != 0 || policies != 0 {
		t.Fatal("rollback left a half-retired identity")
	}
	// Admission and retirement take the same actor/policy locks. A concurrent
	// replay cannot observe a half-written alias or reserve a second call.
	tx, err = db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = retention.RetireRequestKeyTx(ctx, tx, target); err != nil {
		t.Fatal(err)
	}
	type result struct {
		id  string
		err error
	}
	started, done := make(chan struct{}), make(chan result, 1)
	go func() {
		close(started)
		waiting, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		got, e := budgets.Reserve(waiting, actor, project, key, 80, "USD", input)
		done <- result{got.ID, e}
	}()
	<-started
	select {
	case got := <-done:
		t.Fatalf("replay crossed uncommitted retirement: %+v", got)
	case <-time.After(100 * time.Millisecond):
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	got := <-done
	if got.err != nil || got.id != call.ID {
		t.Fatal("concurrent replay did not resolve original result", got.err)
	}
	tx, err = db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	again, err := retention.RetireRequestKeyTx(ctx, tx, target)
	if err != nil || again != first {
		t.Fatal("retirement retry is not idempotent", err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err = budgets.Reserve(ctx, actor, project, "unknown-old-request", 20, "USD", input); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("cutover failed to close legacy admission", err)
	}
	budget, err := budgets.Read(ctx, actor.ID)
	if err != nil || budget.Reserved != 80 || budget.Spent != 0 {
		t.Fatal("retirement changed fees", err)
	}
}
