package retention

import (
	"strings"
	"testing"
)

func inventoryRow(table string, values map[string]string) SourceRow {
	spec, ok := OwnerSpec(table)
	if !ok {
		panic(table)
	}
	row := SourceRow{Table: table, Values: map[string]string{}}
	for _, column := range spec.Columns {
		row.Values[column.Column] = ""
	}
	for key, value := range values {
		row.Values[key] = value
	}
	return row
}

func sourceOwnershipRows() []SourceRow {
	w, a, b, u, ba, bb := strings.Repeat("1", 32), strings.Repeat("2", 32), strings.Repeat("3", 32), strings.Repeat("4", 32), strings.Repeat("5", 32), strings.Repeat("6", 32)
	return []SourceRow{
		inventoryRow("workspaces", map[string]string{"id": w}),
		inventoryRow("users", map[string]string{"id": u, "workspace_id": w}),
		inventoryRow("projects", map[string]string{"id": a, "workspace_id": w}),
		inventoryRow("projects", map[string]string{"id": b, "workspace_id": w}),
		inventoryRow("branches", map[string]string{"id": ba, "project_id": a}),
		inventoryRow("branches", map[string]string{"id": bb, "project_id": b}),
		inventoryRow("operations", map[string]string{"actor_id": u, "operation_id": "first", "kind": "branch.create", "result_id": ba}),
		inventoryRow("operations", map[string]string{"actor_id": u, "operation_id": "second", "kind": "branch.create", "result_id": bb}),
	}
}

func TestSourceInventoryOwnershipUsesTypedTargetNotActor(t *testing.T) {
	rows := sourceOwnershipRows()
	index, err := NewSourceIndex(rows)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct{ operation, project string }{{"first", strings.Repeat("2", 32)}, {"second", strings.Repeat("3", 32)}} {
		owner, err := index.Owner("operations", map[string]string{"actor_id": strings.Repeat("4", 32), "operation_id": item.operation})
		if err != nil || owner.ProjectID != item.project || owner.Shared || owner.WorkspaceID != strings.Repeat("1", 32) {
			t.Fatal(owner, err)
		}
	}
	shared, err := index.Owner("users", map[string]string{"id": strings.Repeat("4", 32)})
	if err != nil || !shared.Shared || shared.ProjectID != "" {
		t.Fatal("shared identity assigned to a project", shared, err)
	}
	rows[4].Values["project_id"] = strings.Repeat("3", 32)
	owner, err := index.Owner("branches", map[string]string{"id": strings.Repeat("5", 32)})
	if err != nil || owner.ProjectID != strings.Repeat("2", 32) {
		t.Fatal("caller mutated frozen source index", err)
	}
}

func TestRecoveredProjectReceiptsBelongToCopy(t *testing.T) {
	rows := sourceOwnershipRows()
	actor, target := strings.Repeat("4", 32), strings.Repeat("3", 32)
	rows = append(rows,
		inventoryRow("operations", map[string]string{"actor_id": actor, "operation_id": "recover", "kind": "project.recover", "result_id": target}),
		inventoryRow("audit_events", map[string]string{"id": "12", "workspace_id": strings.Repeat("1", 32), "actor_id": actor, "action": "project.recover", "resource_id": target}),
	)
	index, err := NewSourceIndex(rows)
	if err != nil {
		t.Fatal(err)
	}
	for table, key := range map[string]map[string]string{
		"operations":   {"actor_id": actor, "operation_id": "recover"},
		"audit_events": {"id": "12"},
	} {
		owner, err := index.Owner(table, key)
		if err != nil || owner.ProjectID != target || owner.Shared {
			t.Fatalf("recovery %s must follow its new project: %+v %v", table, owner, err)
		}
	}
}

func TestJointPublicationRecordsFollowReleaseOwnership(t *testing.T) {
	rows := sourceOwnershipRows()
	actor, project, release := strings.Repeat("4", 32), strings.Repeat("2", 32), strings.Repeat("7", 32)
	rows = append(rows, inventoryRow("artifact_releases", map[string]string{"id": release, "project_id": project, "branch_id": strings.Repeat("5", 32)}))
	for _, kind := range []string{"release.joint-publish", "release.joint-member", "release.joint-restored"} {
		rows = append(rows, inventoryRow("operations", map[string]string{"actor_id": actor, "operation_id": kind, "kind": kind, "result_id": release}))
	}
	rows = append(rows, inventoryRow("audit_events", map[string]string{"id": "13", "workspace_id": strings.Repeat("1", 32), "actor_id": actor, "action": "release.joint-publish", "resource_id": release}))
	index, err := NewSourceIndex(rows)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"release.joint-publish", "release.joint-member", "release.joint-restored"} {
		owner, err := index.Owner("operations", map[string]string{"actor_id": actor, "operation_id": kind})
		if err != nil || owner.ProjectID != project || owner.Shared {
			t.Fatal("joint record escaped project ownership", owner, err)
		}
	}
	owner, err := index.Owner("audit_events", map[string]string{"id": "13"})
	if err != nil || owner.ProjectID != project || owner.Shared {
		t.Fatal("joint audit escaped project ownership", owner, err)
	}
}

func TestSourceInventoryRejectsAmbiguousOrCrossProjectOwnership(t *testing.T) {
	for _, kind := range []string{"cross-user", "cross-branch", "missing-branch", "unknown-operation", "missing-owner", "column-drift", "duplicate-key", "cycle"} {
		t.Run(kind, func(t *testing.T) {
			rows := sourceOwnershipRows()
			table := "branches"
			key := map[string]string{"id": strings.Repeat("5", 32)}
			switch kind {
			case "cross-user":
				otherWorkspace, otherUser := strings.Repeat("a", 32), strings.Repeat("b", 32)
				rows = append(rows, inventoryRow("workspaces", map[string]string{"id": otherWorkspace}), inventoryRow("users", map[string]string{"id": otherUser, "workspace_id": otherWorkspace}))
				table = "project_members"
				key = map[string]string{"project_id": strings.Repeat("2", 32), "user_id": otherUser}
				rows = append(rows, inventoryRow(table, key))
			case "cross-branch", "missing-branch":
				table = "recipe_runs"
				key = map[string]string{"id": strings.Repeat("7", 32)}
				rows = append(rows, inventoryRow(table, map[string]string{"id": key["id"], "workspace_id": strings.Repeat("1", 32), "project_id": strings.Repeat("2", 32), "branch_id": strings.Repeat("6", 32), "actor_id": strings.Repeat("4", 32)}))
				if kind == "missing-branch" {
					rows[len(rows)-1].Values["branch_id"] = ""
				}
			case "unknown-operation":
				table = "operations"
				key = map[string]string{"actor_id": strings.Repeat("4", 32), "operation_id": "first"}
				rows[6].Values["kind"] = "unrecognized"
			case "missing-owner":
				rows[4].Values["project_id"] = strings.Repeat("8", 32)
			case "column-drift":
				rows[4].Values["future_secret"] = "private"
			case "duplicate-key":
				rows = append(rows, rows[4])
			case "cycle":
				rows[4].Values["parent_id"] = rows[4].Values["id"]
			}
			index, err := NewSourceIndex(rows)
			if err == nil {
				_, err = index.Owner(table, key)
			}
			if err == nil {
				t.Fatal("unsafe source ownership accepted")
			}
		})
	}
}
