package backup

import (
	"strings"
	"testing"

	"novelstudio/internal/retention"
)

func TestRetentionArchiveOwnerUsesFixedPrimaryKeyAndActualParentProject(t *testing.T) {
	a := emptyArchive(t)
	project, other := strings.Repeat("a", 32), strings.Repeat("b", 32)
	branch, candidate, actor := strings.Repeat("c", 32), strings.Repeat("d", 32), strings.Repeat("e", 32)
	workspace := strings.Repeat("f", 32)
	testRow(a, "workspaces", map[string]string{"id": workspace})
	testRow(a, "users", map[string]string{"id": actor, "workspace_id": workspace})
	testRow(a, "projects", map[string]string{"id": project, "workspace_id": workspace})
	testRow(a, "projects", map[string]string{"id": other, "workspace_id": workspace})
	testRow(a, "branches", map[string]string{"id": branch, "project_id": project})
	testRow(a, "ip_revisions", map[string]string{"id": candidate, "branch_id": branch, "base_head": "", "created_by": actor, "content_hash": digest([]byte("private original")), "payload": "private original"})
	testRow(a, "ip_drafts", map[string]string{"branch_id": branch, "actor_id": actor, "revision": "1", "base_head": "", "payload": "unsaved idea"})
	owners, err := newRetentionArchiveOwners(a.Data)
	if err != nil {
		t.Fatal(err)
	}
	key, _ := retention.RowOwnerKey("ip_revisions", map[string]string{"id": candidate})
	owner, err := owners.field("ip_revisions", key, "payload")
	if err != nil || owner.ProjectID != project || string(owner.Value) != "private original" {
		t.Fatalf("owner lookup lost source scope: %+v %v", owner, err)
	}
	key, _ = retention.RowOwnerKey("ip_drafts", map[string]string{"branch_id": branch, "actor_id": actor})
	owner, err = owners.field("ip_drafts", key, "payload")
	if err != nil || owner.ProjectID != project || string(owner.Value) != "unsaved idea" {
		t.Fatalf("composite source scope lost: %+v %v", owner, err)
	}
	wrong, _ := retention.OwnerKey(actor, branch)
	if _, err := owners.field("ip_drafts", wrong, "payload"); err == nil {
		t.Fatal("reordered primary key accepted")
	}
}

func TestRetentionArchiveOwnerRejectsMissingDuplicateOrUnclassifiedSource(t *testing.T) {
	project, call, receipt := strings.Repeat("a", 32), strings.Repeat("b", 32), strings.Repeat("c", 64)
	for _, change := range []string{"missing-parent", "duplicate", "short-row", "workspace-as-project", "unknown-field"} {
		t.Run(change, func(t *testing.T) {
			a := emptyArchive(t)
			testRow(a, "projects", map[string]string{"id": project})
			testRow(a, "model_calls", map[string]string{"id": call, "project_id": project})
			testRow(a, "model_call_receipts", map[string]string{"id": receipt, "call_id": call, "payload": "original receipt"})
			if change == "missing-parent" {
				a.Data["model_calls"] = nil
			}
			if change == "duplicate" {
				a.Data["model_call_receipts"] = append(a.Data["model_call_receipts"], a.Data["model_call_receipts"][0])
			}
			if change == "short-row" {
				a.Data["model_call_receipts"][0] = Row{[]byte(receipt)}
			}
			if change == "workspace-as-project" {
				testRow(a, "workspaces", map[string]string{"id": project, "name": "private workspace"})
			}
			owners, err := newRetentionArchiveOwners(a.Data)
			if change == "duplicate" || change == "short-row" {
				if err == nil {
					t.Fatal("malformed source accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			table, field, keyColumn, id := "model_call_receipts", "payload", "id", receipt
			if change == "workspace-as-project" {
				table, field, id = "workspaces", "name", project
			}
			if change == "unknown-field" {
				field = "unclassified_private_field"
			}
			key, _ := retention.RowOwnerKey(table, map[string]string{keyColumn: id})
			if _, err := owners.field(table, key, field); err == nil {
				t.Fatal("unproven owner accepted")
			}
		})
	}
}

func TestRetentionArchiveOwnerReceiptDerivesProjectFromCall(t *testing.T) {
	a := emptyArchive(t)
	project, call, receipt := strings.Repeat("a", 32), strings.Repeat("b", 32), strings.Repeat("c", 64)
	workspace := strings.Repeat("f", 32)
	testRow(a, "workspaces", map[string]string{"id": workspace})
	testRow(a, "projects", map[string]string{"id": project, "workspace_id": workspace})
	testRow(a, "model_calls", map[string]string{"id": call, "project_id": project})
	testRow(a, "model_call_receipts", map[string]string{"id": receipt, "call_id": call, "payload": "private receipt"})
	owners, err := newRetentionArchiveOwners(a.Data)
	if err != nil {
		t.Fatal(err)
	}
	key, _ := retention.RowOwnerKey("model_call_receipts", map[string]string{"id": receipt})
	got, err := owners.field("model_call_receipts", key, "payload")
	if err != nil || got.ProjectID != project {
		t.Fatalf("receipt scope: %+v %v", got, err)
	}
}

func TestRetentionArchiveOwnerRejectsCrossProjectSecondaryBranch(t *testing.T) {
	a := emptyArchive(t)
	workspace, project, other, branch, resource := strings.Repeat("a", 32), strings.Repeat("b", 32), strings.Repeat("c", 32), strings.Repeat("d", 32), strings.Repeat("e", 32)
	testRow(a, "workspaces", map[string]string{"id": workspace})
	testRow(a, "projects", map[string]string{"id": project, "workspace_id": workspace})
	testRow(a, "projects", map[string]string{"id": other, "workspace_id": workspace})
	testRow(a, "branches", map[string]string{"id": branch, "project_id": other})
	testRow(a, "resources", map[string]string{"id": resource, "project_id": project, "branch_id": branch, "name": "private"})
	owners, err := newRetentionArchiveOwners(a.Data)
	if err != nil {
		t.Fatal(err)
	}
	key, _ := retention.RowOwnerKey("resources", map[string]string{"id": resource})
	if _, err := owners.field("resources", key, "name"); err == nil {
		t.Fatal("archive accepted an ownership graph rejected by the online scanner")
	}
}
