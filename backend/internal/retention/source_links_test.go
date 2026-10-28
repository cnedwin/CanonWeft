package retention

import (
	"strings"
	"testing"
)

func TestSourceOwnershipRejectsForeignCapabilityAndDiscoveryLinks(t *testing.T) {
	id := func(v string) string { return strings.Repeat(v, 32) }
	for _, target := range []string{"capability_heads", "capability_revocations", "skill_revocations", "skill_discovery_runs", "skill_discovery_candidates", "tool_trials", "capability_bindings", "capability_binding_heads", "qualification_week_grants"} {
		t.Run(target, func(t *testing.T) {
			rows := sourceOwnershipRows()
			base := len(rows)
			rows = append(rows,
				inventoryRow("capability_versions", map[string]string{"id": id("a"), "project_id": id("3"), "capability_key": "foreign"}),
				inventoryRow("skill_packages", map[string]string{"id": id("b"), "project_id": id("3")}),
				inventoryRow("skill_discovery_sources", map[string]string{"id": id("c"), "project_id": id("3")}),
				inventoryRow("qualification_runs", map[string]string{"id": id("d"), "project_id": id("3")}),
				inventoryRow("capability_qualifications", map[string]string{"id": id("e"), "project_id": id("3"), "run_id": id("d")}),
				inventoryRow("capability_bindings", map[string]string{"id": id("f"), "project_id": id("3"), "qualification_id": id("e"), "task_kind": "writer"}),
				inventoryRow("qualification_schedules", map[string]string{"id": id("7"), "project_id": id("3")}),
				inventoryRow("skill_discovery_runs", map[string]string{"id": id("9"), "project_id": id("3"), "source_id": id("c")}),
			)
			values := map[string]string{"id": id("8"), "project_id": id("2")}
			switch target {
			case "capability_heads":
				values = map[string]string{"project_id": id("2"), "capability_key": "foreign", "version_id": id("a")}
			case "capability_revocations":
				values = map[string]string{"project_id": id("2"), "version_id": id("a")}
			case "skill_revocations":
				values = map[string]string{"project_id": id("2"), "package_id": id("b")}
			case "skill_discovery_runs":
				values["source_id"] = id("c")
			case "skill_discovery_candidates":
				values["source_id"] = id("c")
				values["run_id"] = id("9")
				values["package_id"] = id("b")
				values["previous_id"] = id("b")
			case "tool_trials":
				values["capability_id"] = id("a")
			case "capability_bindings":
				values["qualification_id"] = id("e")
			case "capability_binding_heads":
				values = map[string]string{"project_id": id("2"), "task_kind": "writer", "binding_id": id("f")}
			case "qualification_week_grants":
				values["schedule_id"] = id("7")
			}
			rows = append(rows, inventoryRow(target, values))
			index, err := NewSourceIndex(rows)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := index.Owner(target, values); err == nil {
				t.Fatal("foreign linked source was attributed to local project")
			}
			for i := base; i < len(rows); i++ {
				rows[i].Values["project_id"] = id("2")
			}
			index, err = NewSourceIndex(rows)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := index.Owner(target, values); err != nil {
				t.Fatal("valid linked source rejected", err)
			}
			if target == "capability_heads" || target == "capability_binding_heads" {
				column := "capability_key"
				if target == "capability_binding_heads" {
					column = "task_kind"
				}
				rows[len(rows)-1].Values[column] = "wrong-target-key"
				index, _ = NewSourceIndex(rows)
				if _, err := index.Owner(target, rows[len(rows)-1].Values); err == nil {
					t.Fatal("head points to a different logical key in the same project")
				}
			}
		})
	}
}

func TestSourceOwnershipBindsExactModelConnectionVersion(t *testing.T) {
	id := func(v string) string { return strings.Repeat(v, 32) }
	for _, mode := range []string{"valid", "foreign", "missing-version"} {
		t.Run(mode, func(t *testing.T) {
			workspace := id("1")
			if mode == "foreign" {
				workspace = id("a")
			}
			rows := sourceOwnershipRows()
			rows = append(rows, inventoryRow("workspaces", map[string]string{"id": id("a")}), inventoryRow("model_connections", map[string]string{"id": id("b"), "workspace_id": workspace}), inventoryRow("model_connection_versions", map[string]string{"connection_id": id("b"), "revision": "1"}), inventoryRow("model_bindings", map[string]string{"workspace_id": id("1"), "role": "writer", "connection_id": id("b"), "connection_revision": "1"}))
			if mode == "missing-version" {
				rows[len(rows)-1].Values["connection_revision"] = "2"
			}
			index, err := NewSourceIndex(rows)
			if err != nil {
				t.Fatal(err)
			}
			_, err = index.Owner("model_bindings", rows[len(rows)-1].Values)
			if (mode == "valid") != (err == nil) {
				t.Fatalf("%s: %v", mode, err)
			}
		})
	}
}
