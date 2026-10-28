package backup

import (
	"context"
	"errors"
	"strings"
	"testing"

	"novelstudio/internal/domain"
	"novelstudio/internal/service"
)

// Exercise the real dispatch boundary with an enabled operator and budget. A
// restored RESERVED row must remain non-dispatchable even if higher-level
// identity/budget restrictions are subsequently lifted by an administrator.
func TestRetentionSanitizedCallCannotDispatchWithEnabledOperator(t *testing.T) {
	db, _, admin := testDatabase(t)
	ctx := context.Background()
	w, u, p := randomID(t), randomID(t), randomID(t)
	execTest(t, db, "INSERT INTO workspaces(id,name) VALUES(?,'sanitizer test')", w)
	execTest(t, db, "INSERT INTO users(id,workspace_id,username,password_hash,role) VALUES(?,?,'owner','test','owner')", u, w)
	execTest(t, db, "INSERT INTO projects(id,workspace_id,name) VALUES(?,?,'sanitizer test')", p, w)
	execTest(t, db, "INSERT INTO project_members VALUES(?,?,'owner')", p, u)
	provisionTestAuthority(t, db, admin)
	execTest(t, db, "INSERT INTO model_budgets(workspace_id,currency,limit_micros,per_call_micros,reserved_micros,enabled,revision) VALUES(?,'USD',1000,500,100,1,1)", w)
	want := map[string]string{}
	for _, state := range []string{"RESERVED", "DISPATCHED"} {
		id := randomID(t)
		want[id] = state
		execTest(t, db, "INSERT INTO model_calls(id,workspace_id,project_id,actor_id,operation_id,input_hash,reserved_micros,state) VALUES(?,?,?,?,?,?,50,?)", id, w, p, u, state, strings.Repeat("a", 64), state)
	}
	var spec tableSpec
	for _, candidate := range tableCatalog {
		if candidate.name == "model_calls" {
			spec = candidate
		}
	}
	rows, err := readRows(ctx, db, spec)
	if err != nil {
		t.Fatal(err)
	}
	data := Data{"model_calls": rows}
	sanitizeArchiveData(data, retentionSchemaVersion)
	for _, row := range rows {
		execTest(t, db, "UPDATE model_calls SET state=?,cancel_requested=? WHERE id=?", spec.value(row, "state"), spec.value(row, "cancel_requested"), spec.value(row, "id"))
	}
	budgets := service.Budgets{Workspace: &service.Workspace{DB: db}}
	actor := domain.Actor{ID: u, WorkspaceID: w, Role: "owner"}
	for id, state := range want {
		if err := budgets.Dispatch(ctx, actor, p, id); !errors.Is(err, domain.ErrConflict) {
			t.Fatal("sanitized call passed dispatch or failed at an unrelated gate", state, err)
		}
		var actual string
		var reserved, charged int64
		var cancelled bool
		if err := db.QueryRow("SELECT state,reserved_micros,charged_micros,cancel_requested FROM model_calls WHERE id=?", id).Scan(&actual, &reserved, &charged, &cancelled); err != nil || actual != state || reserved != 50 || charged != 0 || !cancelled {
			t.Fatal("dispatch changed historical bill", actual, reserved, charged, cancelled, err)
		}
	}
	var reserved, spent int64
	if err := db.QueryRow("SELECT reserved_micros,spent_micros FROM model_budgets WHERE workspace_id=?", w).Scan(&reserved, &spent); err != nil || reserved != 100 || spent != 0 {
		t.Fatal("disabling runtime settled or released funds", reserved, spent, err)
	}
}
