package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"novelstudio/internal/domain"
	"novelstudio/internal/storage"
)

func TestOperationsDiagnosticsAreScopedRedactedAndOwnerOnly(t *testing.T) {
	s, owner, _, _ := authorityWorkspace(t, true)
	ctx := context.Background()
	project, err := s.CreateProject(ctx, owner.ID, "PRIVATE_PROJECT_CANARY", NewID())
	if err != nil {
		t.Fatal(err)
	}
	other, otherProject, otherUser := NewID(), NewID(), NewID()
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{"INSERT INTO workspaces(id,name) VALUES(?,'OTHER_WORKSPACE_CANARY')", []any{other}},
		{"INSERT INTO users(id,workspace_id,username,password_hash,role) VALUES(?,?,'other-owner','PASSWORD_CANARY','owner')", []any{otherUser, other}},
		{"INSERT INTO projects(id,workspace_id,name) VALUES(?,?,'OTHER_PROJECT_CANARY')", []any{otherProject, other}},
		{"INSERT INTO model_calls(id,workspace_id,project_id,actor_id,operation_id,input_hash,reserved_micros,state,provider_receipt) VALUES(?,?,?,?,?,?,1,'UNKNOWN','PRIVATE_RECEIPT_CANARY')", []any{NewID(), owner.WorkspaceID, project.ID, owner.ID, NewID(), strings.Repeat("a", 64)}},
		{"INSERT INTO model_calls(id,workspace_id,project_id,actor_id,operation_id,input_hash,reserved_micros,state) VALUES(?,?,?,?,?,?,1,'UNKNOWN')", []any{NewID(), other, otherProject, otherUser, NewID(), strings.Repeat("b", 64)}},
	} {
		if _, err = s.DB.ExecContext(ctx, q.sql, q.args...); err != nil {
			t.Fatal(err)
		}
	}
	report, err := s.OperationsDiagnostics(ctx, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	var exports int
	if err = s.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM audit_events WHERE actor_id=? AND action='diagnostics.export'", owner.ID).Scan(&exports); err != nil || exports != 1 {
		t.Fatal("diagnostic access was not audited", err)
	}
	if report.SchemaVersion != storage.SchemaVersion || report.ActiveProjects != 1 || report.States["model_calls"]["UNKNOWN"] != 1 {
		t.Fatal("diagnostics mixed workspace or lost states", report)
	}
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"CANARY", owner.ID, owner.Username, project.ID, other, strings.Repeat("a", 64)} {
		if strings.Contains(string(raw), secret) {
			t.Fatal("diagnostics leaked content or identity")
		}
	}
	editor, err := s.CreateMember(ctx, owner.ID, "diag-editor", "test-password-123456", "editor")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.OperationsDiagnostics(ctx, editor.ID); !errors.Is(err, domain.ErrForbidden) {
		t.Fatal("non-owner can export diagnostics", err)
	}
	if _, err = s.DB.ExecContext(ctx, "UPDATE users SET enabled=FALSE WHERE id=?", owner.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.OperationsDiagnostics(ctx, owner.ID); !errors.Is(err, domain.ErrForbidden) {
		t.Fatal("disabled owner can export diagnostics", err)
	}
}
