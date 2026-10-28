package retention

import (
	"encoding/json"
	"strings"
	"testing"

	"novelstudio/internal/domain"
)

func contentRootFixture() ([]SourceRow, domain.Manifest) {
	id := func(s string) string { return strings.Repeat(s, 32) }
	rows := sourceOwnershipRows()
	snapshot, _ := json.Marshal(domain.Snapshot{Entities: []domain.Entity{}, Assertions: []domain.Assertion{}})
	rows = append(rows,
		inventoryRow("ip_revisions", map[string]string{"id": id("7"), "branch_id": id("5"), "status": "PUBLISHED", "content_hash": financialDigest(snapshot), "payload": string(snapshot)}),
		inventoryRow("adoptions", map[string]string{"candidate_id": id("7"), "actor_id": id("4"), "content_hash": financialDigest(snapshot)}),
		inventoryRow("resources", map[string]string{"id": id("8"), "project_id": id("2"), "branch_id": id("5"), "head_revision": id("9"), "content_hash": strings.Repeat("a", 64)}),
	)
	manifest := domain.Manifest{Schema: 2, ReleaseID: id("9"), BranchID: id("5"), ExpectedIPHead: id("7"), Artifacts: []domain.ArtifactRef{{ResourceID: id("8"), Hash: strings.Repeat("a", 64), Size: 4}}}
	manifest.Artifacts[0].Origin = &domain.ArtifactOrigin{Kind: "manual", ActorID: id("4"), ResourceID: id("8"), BranchID: id("5"), IPHead: id("7"), ContentHash: strings.Repeat("a", 64)}
	raw, _ := json.Marshal(manifest)
	rows = append(rows, inventoryRow("artifact_releases", map[string]string{"id": id("9"), "project_id": id("2"), "branch_id": id("5"), "status": "PUBLISHED", "manifest": string(raw), "content_hash": financialDigest(raw), "adopted_hash": financialDigest(raw), "adopted_by": id("4")}))
	return rows, manifest
}

func TestAdoptedContentReferencesAreSourceBoundAndDoNotPromoteRawRuns(t *testing.T) {
	rows, _ := contentRootFixture()
	rows = append(rows, inventoryRow("generation_tasks", map[string]string{"id": strings.Repeat("b", 32), "project_id": strings.Repeat("2", 32), "branch_id": strings.Repeat("5", 32), "input_hash": strings.Repeat("a", 64)}))
	index, err := NewSourceIndex(rows)
	if err != nil {
		t.Fatal(err)
	}
	refs, err := index.AdoptedContentReferences(strings.Repeat("2", 32))
	if err != nil || len(refs) != 6 {
		t.Fatal("missing adopted content references", len(refs), err)
	}
	for _, ref := range refs {
		rule, ok := ConcreteRule(ref.Table + "." + ref.Field)
		if !ok || !rule.Promotable || ref.Table == "generation_tasks" {
			t.Fatal("raw run promoted to content", ref)
		}
	}
	again, err := index.AdoptedContentReferences(strings.Repeat("3", 32))
	if err != nil || len(again) != 0 {
		t.Fatal("content escaped its project", err)
	}
}

func TestCompositionKeepsUnadoptedParentsAndRejectsMissingHistory(t *testing.T) {
	rows, m := contentRootFixture()
	for _, letter := range []string{"b", "c"} {
		parent := m
		parent.ReleaseID = strings.Repeat(letter, 32)
		raw, _ := json.Marshal(parent)
		rows = append(rows, inventoryRow("artifact_releases", map[string]string{"id": parent.ReleaseID, "project_id": strings.Repeat("2", 32), "branch_id": parent.BranchID, "status": "PREPARED", "manifest": string(raw), "content_hash": financialDigest(raw)}))
		m.CompositionParents = append(m.CompositionParents, parent.ReleaseID)
	}
	raw, _ := json.Marshal(m)
	rows[11].Values["manifest"], rows[11].Values["content_hash"], rows[11].Values["adopted_hash"] = string(raw), financialDigest(raw), financialDigest(raw)
	index, err := NewSourceIndex(rows)
	if err != nil {
		t.Fatal(err)
	}
	refs, err := index.AdoptedContentReferences(strings.Repeat("2", 32))
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range m.CompositionParents {
		found := false
		for _, ref := range refs {
			if ref.Table == "artifact_releases" && ref.Field == "manifest" && strings.Contains(ref.OwnerKey, id) {
				found = true
			}
		}
		if !found {
			t.Fatal("unadopted composition parent lost", id)
		}
	}
	index, err = NewSourceIndex(rows[:len(rows)-1])
	if err != nil {
		t.Fatal(err)
	}
	if _, err = index.AdoptedContentReferences(strings.Repeat("2", 32)); err == nil {
		t.Fatal("missing composition parent accepted")
	}
}

func TestAdoptedContentReferencesRejectBrokenPublicationGraphs(t *testing.T) {
	for _, mode := range []string{"foreign-resource", "duplicate-resource", "wrong-body-hash", "wrong-adoption", "missing-ip-adoption", "wrong-resource-head", "self-history", "unknown-manifest-field", "case-alias", "unpublished-ip-base", "missing-import"} {
		t.Run(mode, func(t *testing.T) {
			rows, m := contentRootFixture()
			switch mode {
			case "foreign-resource":
				rows[10].Values["project_id"] = strings.Repeat("3", 32)
				rows[10].Values["branch_id"] = strings.Repeat("6", 32)
			case "duplicate-resource":
				m.Artifacts = append(m.Artifacts, m.Artifacts[0])
			case "wrong-body-hash":
				rows[10].Values["content_hash"] = strings.Repeat("b", 64)
			case "wrong-adoption":
				rows[11].Values["adopted_hash"] = strings.Repeat("b", 64)
			case "missing-ip-adoption":
				rows[9].Values["content_hash"] = strings.Repeat("b", 64)
			case "wrong-resource-head":
				rows[10].Values["head_revision"] = strings.Repeat("e", 32)
			case "self-history":
				m.Artifacts[0].ExpectedHead = m.ReleaseID
			case "unpublished-ip-base":
				rows[8].Values["status"] = "PREPARED"
			case "missing-import":
				m.Artifacts[0].SourceImportID = strings.Repeat("c", 32)
				m.Artifacts[0].OriginalHash = strings.Repeat("d", 64)
			}
			if mode == "duplicate-resource" || mode == "self-history" || mode == "missing-import" {
				raw, _ := json.Marshal(m)
				rows[11].Values["manifest"] = string(raw)
				rows[11].Values["content_hash"] = financialDigest(raw)
				rows[11].Values["adopted_hash"] = financialDigest(raw)
			}
			if mode == "unknown-manifest-field" {
				rows[11].Values["manifest"] = strings.Replace(rows[11].Values["manifest"], `"schema":2`, `"future_blob":"secret","schema":2`, 1)
			}
			if mode == "case-alias" {
				rows[11].Values["manifest"] = strings.Replace(rows[11].Values["manifest"], `"schema":2`, `"Schema":2`, 1)
			}
			index, err := NewSourceIndex(rows)
			if err != nil {
				t.Fatal(err)
			}
			if refs, err := index.AdoptedContentReferences(strings.Repeat("2", 32)); err == nil || len(refs) != 0 {
				t.Fatal("broken publication graph accepted or partial roots leaked", mode, err)
			}
		})
	}
}

func TestAdoptedContentReferencesPreserveImportedOriginalAndLargeIP(t *testing.T) {
	rows, m := contentRootFixture()
	id := func(s string) string { return strings.Repeat(s, 32) }
	original := strings.Repeat("c", 64)
	m.Artifacts[0].SourceImportID = id("d")
	m.Artifacts[0].OriginalHash = original
	m.Artifacts[0].Origin.Kind = "import"
	m.Artifacts[0].Origin.ImportID = id("d")
	m.Artifacts[0].Origin.ImportHash = original
	raw, _ := json.Marshal(m)
	rows[11].Values["manifest"], rows[11].Values["content_hash"], rows[11].Values["adopted_hash"] = string(raw), financialDigest(raw), financialDigest(raw)
	rows = append(rows, inventoryRow("source_imports", map[string]string{"id": id("d"), "project_id": id("2"), "branch_id": id("5"), "actor_id": id("4"), "source_hash": original, "report": `{"text":"已采用的原文"}`}))
	snapshot := domain.Snapshot{Entities: []domain.Entity{{ID: "hero", Kind: "character", Name: "主角", Description: strings.Repeat("长篇设定", 20000)}}, Assertions: []domain.Assertion{}}
	raw, _ = json.Marshal(snapshot)
	rows[8].Values["payload"], rows[8].Values["content_hash"] = string(raw), financialDigest(raw)
	rows[9].Values["content_hash"] = financialDigest(raw)
	index, err := NewSourceIndex(rows)
	if err != nil {
		t.Fatal(err)
	}
	refs, err := index.AdoptedContentReferences(id("2"))
	if err != nil || len(refs) != 9 {
		t.Fatal("large author content or original source was lost", len(refs), err)
	}
	found := false
	for _, ref := range refs {
		if ref.Table == "source_imports" && ref.Field == "source_hash" && ref.ObjectHash == original {
			found = true
		}
	}
	if !found {
		t.Fatal("imported original not preserved")
	}
}
