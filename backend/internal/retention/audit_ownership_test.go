package retention

import (
	"errors"
	"strings"
	"testing"
)

func TestLocalPasswordResetAuditKeepsVerifiedUserWorkspaceOwnership(t *testing.T) {
	rows := sourceOwnershipRows()
	workspace, actor := strings.Repeat("1", 32), strings.Repeat("4", 32)
	rows = append(rows, inventoryRow("audit_events", map[string]string{
		"id": "14", "workspace_id": workspace, "actor_id": actor,
		"action": "local_password_reset", "resource_id": actor,
	}))
	index, err := NewSourceIndex(rows)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := index.Owner("audit_events", map[string]string{"id": "14"})
	if err != nil || owner != (SourceOwner{WorkspaceID: workspace, Shared: true}) {
		t.Fatalf("local account maintenance must remain workspace-scoped: %+v %v", owner, err)
	}
	// Account maintenance must not become a project's accounting material.
	refs, err := index.AccountingReferences(strings.Repeat("2", 32))
	if err != nil || len(refs) != 0 {
		t.Fatalf("workspace audit leaked into project accounting: %d %v", len(refs), err)
	}
}

func TestTaskRefreshAuditKeepsTypedProjectOwnership(t *testing.T) {
	rows := sourceOwnershipRows()
	workspace, actor, project, task := strings.Repeat("1", 32), strings.Repeat("4", 32), strings.Repeat("2", 32), strings.Repeat("7", 32)
	rows = append(rows,
		inventoryRow("platform_tasks", map[string]string{"id": task, "project_id": project, "actor_id": actor}),
		inventoryRow("audit_events", map[string]string{"id": "15", "workspace_id": workspace, "actor_id": actor, "action": "task.refresh", "resource_id": task, "created_at": "2026-10-28 07:15:00.000001"}),
	)
	index, err := NewSourceIndex(rows)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := index.Owner("audit_events", map[string]string{"id": "15"})
	if err != nil || owner != (SourceOwner{WorkspaceID: workspace, ProjectID: project}) {
		t.Fatalf("task refresh must follow its actual task: %+v %v", owner, err)
	}
	refs, err := index.AccountingReferences(project)
	if err != nil || len(refs) != 1 || refs[0].Accounting == nil || refs[0].Accounting.Audit == nil || refs[0].Accounting.Audit.Action != "task.refresh" {
		t.Fatalf("task refresh lost its closed accounting projection: %+v %v", refs, err)
	}
}

func TestMaintenanceAuditOwnershipStillRejectsInvalidRelationships(t *testing.T) {
	for _, action := range []string{"local_password_reset", "task.refresh"} {
		for _, invalid := range []string{"unknown-action", "empty-target", "missing-target", "wrong-target-type", "missing-actor", "foreign-actor", "foreign-event-workspace", "foreign-target"} {
			t.Run(action+"/"+invalid, func(t *testing.T) {
				rows := sourceOwnershipRows()
				workspace, actor, project, task := strings.Repeat("1", 32), strings.Repeat("4", 32), strings.Repeat("2", 32), strings.Repeat("7", 32)
				foreignWorkspace, foreignUser := strings.Repeat("a", 32), strings.Repeat("b", 32)
				rows = append(rows,
					inventoryRow("workspaces", map[string]string{"id": foreignWorkspace}),
					inventoryRow("users", map[string]string{"id": foreignUser, "workspace_id": foreignWorkspace}),
					inventoryRow("platform_tasks", map[string]string{"id": task, "project_id": project, "actor_id": actor}),
				)
				audit := inventoryRow("audit_events", map[string]string{"id": "16", "workspace_id": workspace, "actor_id": actor, "action": action, "resource_id": actor})
				if action == "task.refresh" {
					audit.Values["resource_id"] = task
				}
				switch invalid {
				case "unknown-action":
					audit.Values["action"] = "unknown." + action
				case "empty-target":
					audit.Values["resource_id"] = ""
				case "missing-target":
					audit.Values["resource_id"] = strings.Repeat("c", 32)
				case "wrong-target-type":
					audit.Values["resource_id"] = project
				case "missing-actor":
					audit.Values["actor_id"] = strings.Repeat("c", 32)
				case "foreign-actor":
					audit.Values["actor_id"] = foreignUser
				case "foreign-event-workspace":
					audit.Values["workspace_id"] = foreignWorkspace
				case "foreign-target":
					if action == "local_password_reset" {
						audit.Values["resource_id"] = foreignUser
					} else {
						foreignProject := strings.Repeat("d", 32)
						rows = append(rows, inventoryRow("projects", map[string]string{"id": foreignProject, "workspace_id": foreignWorkspace}))
						rows[len(rows)-2].Values["project_id"] = foreignProject
						rows[len(rows)-2].Values["actor_id"] = foreignUser
					}
				}
				index, err := NewSourceIndex(append(rows, audit))
				if err != nil {
					t.Fatal(err)
				}
				if _, err = index.Owner("audit_events", map[string]string{"id": "16"}); !errors.Is(err, ErrSourceOwnership) {
					t.Fatalf("invalid audit relationship accepted: %v", err)
				}
			})
		}
	}
}
