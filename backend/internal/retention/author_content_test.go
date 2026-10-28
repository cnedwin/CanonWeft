package retention

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"novelstudio/internal/domain"
	"novelstudio/internal/skills"
)

func authorTestJSON(value any) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}

func authorTestHash(value any) string {
	d := json.NewDecoder(bytes.NewBufferString(authorTestJSON(value)))
	d.UseNumber()
	var decoded any
	_ = d.Decode(&decoded)
	return financialDigest([]byte(authorTestJSON(decoded)))
}

func authorContentFixture() []SourceRow {
	id := func(s string) string { return strings.Repeat(s, 32) }
	rows := sourceOwnershipRows()
	report := skills.Report{Hash: strings.Repeat("a", 64)}
	rows = append(rows,
		inventoryRow("ip_drafts", map[string]string{"branch_id": id("5"), "actor_id": id("4"), "revision": "1", "payload": `{"unfinished":"作者草稿"}`}),
		inventoryRow("source_imports", map[string]string{"id": id("7"), "project_id": id("2"), "branch_id": id("5"), "actor_id": id("4"), "source_hash": strings.Repeat("b", 64), "report": `{"text":"导入原文"}`}),
		inventoryRow("import_drafts", map[string]string{"import_id": id("7"), "project_id": id("2"), "actor_id": id("4"), "revision": "1", "payload": `{"text":"尚未采用的修改"}`}),
		inventoryRow("skill_packages", map[string]string{"id": id("8"), "project_id": id("2"), "created_by": id("4"), "bundle_hash": report.Hash, "report": authorTestJSON(report), "source": `{"kind":"archive","archive_hash":"` + strings.Repeat("c", 64) + `"}`}),
	)
	d := domain.CapabilityDefinition{Kind: "skill", Key: "story-skill", Name: "写作方法", Description: "作者保留的方法", Instructions: "先规划后写作", InputSchema: json.RawMessage(`{"type":"object"}`), OutputSchema: json.RawMessage(`{"type":"object"}`), Dependencies: []domain.CapabilityRef{}, PackageID: id("8"), PackageHash: report.Hash}
	rows = append(rows, inventoryRow("capability_versions", map[string]string{"id": id("9"), "project_id": id("2"), "kind": d.Kind, "capability_key": d.Key, "created_by": id("4"), "definition": authorTestJSON(d), "content_hash": authorTestHash([]any{"", d})}))
	return rows
}

func TestAuthorContentKeepsDraftsAndRequiredSkillFiles(t *testing.T) {
	rows := authorContentFixture()
	rows = append(rows, inventoryRow("generation_tasks", map[string]string{"id": strings.Repeat("d", 32), "project_id": strings.Repeat("2", 32), "branch_id": strings.Repeat("5", 32), "input_hash": strings.Repeat("a", 64)}))
	index, err := NewSourceIndex(rows)
	if err != nil {
		t.Fatal(err)
	}
	refs, err := index.AuthorContentReferences(strings.Repeat("2", 32))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		"ip_drafts.payload": false, "import_drafts.payload": false,
		"source_imports.source_hash": false, "source_imports.report": false,
		"capability_versions.definition": false, "skill_packages.bundle_hash": false,
		"skill_packages.report": false, "skill_packages.source": false, "skill_packages.source.archive_hash": false,
	}
	for _, ref := range refs {
		key := ref.Table + "." + ref.Field
		if _, exists := want[key]; !exists {
			t.Fatalf("unexpected preserved runtime material: %s", key)
		}
		want[key] = true
	}
	for key, found := range want {
		if !found {
			t.Error("missing author content", key)
		}
	}
	other, err := index.AuthorContentReferences(strings.Repeat("3", 32))
	if err != nil || len(other) != 0 {
		t.Fatal("cross project content", err)
	}
}

func TestAuthorContentRejectsBrokenCapabilitySources(t *testing.T) {
	for _, mode := range []string{"foreign-package", "wrong-hash", "foreign-parent", "package-cycle", "missing-upstream", "hidden-archive", "invalid-definition"} {
		t.Run(mode, func(t *testing.T) {
			rows := authorContentFixture()
			pkg, cap := &rows[11], &rows[12]
			switch mode {
			case "foreign-package":
				pkg.Values["project_id"] = strings.Repeat("3", 32)
			case "wrong-hash":
				pkg.Values["bundle_hash"] = strings.Repeat("d", 64)
			case "foreign-parent":
				parent := inventoryRow("skill_packages", map[string]string{"id": strings.Repeat("f", 32), "project_id": strings.Repeat("3", 32)})
				pkg.Values["parent_id"] = parent.Values["id"]
				rows = append(rows, parent)
			case "package-cycle":
				pkg.Values["parent_id"] = pkg.Values["id"]
			case "missing-upstream":
				pkg.Values["upstream_id"] = strings.Repeat("f", 32)
			case "hidden-archive":
				pkg.Values["source"] = `{"kind":"manual","Archive_hash":"` + strings.Repeat("a", 64) + `"}`
			case "invalid-definition":
				cap.Values["definition"] = `{}`
			}
			index, err := NewSourceIndex(rows)
			if err != nil {
				t.Fatal(err)
			}
			refs, err := index.AuthorContentReferences(strings.Repeat("2", 32))
			if err == nil || len(refs) != 0 {
				t.Fatal("broken graph returned preservation roots", mode, err)
			}
		})
	}
}

func TestAuthorContentPreservesHistoricalDependenciesAndPackageLineage(t *testing.T) {
	rows := authorContentFixture()
	id := func(s string) string { return strings.Repeat(s, 32) }
	var method domain.CapabilityDefinition
	if err := json.Unmarshal([]byte(rows[12].Values["definition"]), &method); err != nil {
		t.Fatal(err)
	}
	oldHash := rows[12].Values["content_hash"]
	for _, key := range []string{"b", "c"} {
		pkg := inventoryRow("skill_packages", map[string]string{"id": id(key), "project_id": id("2"), "created_by": id("4"), "bundle_hash": strings.Repeat(key, 64), "report": authorTestJSON(skills.Report{Hash: strings.Repeat(key, 64)}), "source": `{"kind":"manual"}`})
		if key == "c" {
			pkg.Values["parent_id"], pkg.Values["upstream_id"] = id("8"), id("b")
		}
		rows = append(rows, pkg)
	}
	method.PackageID, method.PackageHash = id("c"), strings.Repeat("c", 64)
	rows = append(rows, inventoryRow("capability_versions", map[string]string{"id": id("e"), "parent_id": id("9"), "project_id": id("2"), "kind": method.Kind, "capability_key": method.Key, "definition": authorTestJSON(method), "content_hash": authorTestHash([]any{id("9"), method})}))
	agent := domain.CapabilityDefinition{Kind: "agent", Key: "story-agent", Name: "写作角色", Description: "固定旧版方法", Instructions: "按采用版本生成候选", InputSchema: json.RawMessage(`{"type":"object"}`), OutputSchema: json.RawMessage(`{"type":"object"}`), Dependencies: []domain.CapabilityRef{{ID: id("9"), Hash: oldHash, Kind: "skill"}}, Role: "writer"}
	rows = append(rows, inventoryRow("capability_versions", map[string]string{"id": id("d"), "project_id": id("2"), "kind": agent.Kind, "capability_key": agent.Key, "definition": authorTestJSON(agent), "content_hash": authorTestHash([]any{"", agent})}))
	// An unreferenced discovered candidate does not acquire permanent roots.
	rows = append(rows, inventoryRow("skill_packages", map[string]string{"id": id("f"), "project_id": id("2"), "bundle_hash": strings.Repeat("f", 64), "report": `{}`, "source": `{}`}))
	index, err := NewSourceIndex(rows)
	if err != nil {
		t.Fatal(err)
	}
	refs, err := index.AuthorContentReferences(id("2"))
	if err != nil {
		t.Fatal(err)
	}
	packages, definitions := 0, 0
	for _, ref := range refs {
		if ref.Table == "capability_versions" {
			definitions++
		}
		if ref.Field == "bundle_hash" {
			packages++
			if ref.ObjectHash == strings.Repeat("f", 64) {
				t.Fatal("unreferenced package promoted")
			}
		}
	}
	if packages != 3 || definitions != 3 {
		t.Fatal("history or package lineage lost", packages, definitions)
	}
	// A definition's own hash is valid; its historical dependency pin is not.
	agent.Dependencies[0].Hash = strings.Repeat("f", 64)
	rows[len(rows)-2].Values["definition"] = authorTestJSON(agent)
	rows[len(rows)-2].Values["content_hash"] = authorTestHash([]any{"", agent})
	index, err = NewSourceIndex(rows)
	if err != nil {
		t.Fatal(err)
	}
	if refs, err := index.AuthorContentReferences(id("2")); err == nil || len(refs) != 0 {
		t.Fatal("wrong dependency pin accepted", err)
	}
}

func TestAuthorContentKeepsAllPrivateDraftTypesWithoutRequiringPublication(t *testing.T) {
	id := func(s string) string { return strings.Repeat(s, 32) }
	rows := sourceOwnershipRows()
	rows = append(rows,
		inventoryRow("resources", map[string]string{"id": id("7"), "project_id": id("2"), "branch_id": id("5")}),
		inventoryRow("ip_drafts", map[string]string{"branch_id": id("5"), "actor_id": id("4"), "payload": `{"unfinished":true}`}),
		inventoryRow("patch_drafts", map[string]string{"project_id": id("2"), "branch_id": id("5"), "actor_id": id("4"), "payload": `{"unfinished":true}`}),
		inventoryRow("resource_drafts", map[string]string{"resource_id": id("7"), "actor_id": id("4"), "body": "还未完成的人物对白"}),
		inventoryRow("resource_policy_drafts", map[string]string{"resource_id": id("7"), "actor_id": id("4"), "payload": `{"unfinished":true}`}),
	)
	index, err := NewSourceIndex(rows)
	if err != nil {
		t.Fatal(err)
	}
	refs, err := index.AuthorContentReferences(id("2"))
	if err != nil || len(refs) != 4 {
		t.Fatal("private draft body dropped", len(refs), err)
	}
	for _, ref := range refs {
		rule, ok := ConcreteRule(ref.Table + "." + ref.Field)
		if !ok || !rule.Promotable || rule.Storage != Inline {
			t.Fatal("unexpected draft rule", ref)
		}
	}
}
