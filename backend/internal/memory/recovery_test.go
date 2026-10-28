package memory

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func memoryRecoveryFixture() (RecoveryGraph, RecoveryMap) {
	hash := strings.Repeat("a", 64)
	scene := Item{ID: "scene", Revision: "scene-v2", Project: "original", Branch: "canon", Kind: "summary", State: "published", Visibility: "restricted", KnownBy: []string{"scene"}, Text: "scene / canon / original 都是正文，不应被身份替换", Sources: []Source{{Resource: "body", Revision: "release", Hash: hash, Start: 0, End: 8}}, Summary: &Summary{Level: "scene", UnitID: "scene", Title: "场景"}}
	old := scene
	old.Revision = "scene-v1"
	old.Text = "旧场景原文"
	chapter := Item{ID: "chapter", Revision: "chapter-v1", Project: "original", Branch: "canon", Kind: "summary", State: "published", Visibility: "restricted", KnownBy: []string{"scene"}, Text: "章节摘要", Summary: &Summary{Level: "chapter", UnitID: "chapter-unit", Title: "章节", Inputs: []SummaryPin{{ID: old.ID, Revision: old.Revision, Hash: SummaryTextHash(old.Text)}}}, Dependencies: []string{scene.ID}}
	working := Item{ID: "draft", Revision: "draft-v1", Project: "original", Branch: "adaptation", Actor: "writer", Session: "session", Kind: "short_term", State: "working", Visibility: "author", Text: "未采用意见", Sources: []Source{{Resource: "body", Revision: "release", Hash: hash, Start: 2, End: 5}}}
	graph := RecoveryGraph{Sessions: []RecoverySession{{ID: "session", Project: "original", Branch: "adaptation", Actor: "writer", Revision: 3, Title: "改编草稿", Notes: "作者没有采用", FocusIDs: []string{"draft"}}}, Heads: []Item{scene, chapter, working}, Revisions: []RecoveryRevision{{Item: old}, {Item: scene, Parent: old.Revision}, {Item: chapter}, {Item: working}}}
	ids := RecoveryMap{SourceProject: "original", TargetProject: "copy", Branches: map[string]string{"canon": "new-canon", "adaptation": "new-adaptation"}, Sessions: map[string]string{"session": "new-session"}, Items: map[string]string{"scene": "new-scene", "chapter": "new-chapter", "draft": "new-draft"}, Revisions: map[string]string{"scene-v1": "new-scene-v1", "scene-v2": "new-scene-v2", "chapter-v1": "new-chapter-v1", "draft-v1": "new-draft-v1"}, Resources: map[RecoverySource]RecoverySource{{Resource: "body", Revision: "release", Hash: hash}: {Resource: "new-body", Revision: "new-release", Hash: hash}}}
	return graph, ids
}

func TestRecoveryRetainsHistoricalSummaryPinsAndPrivateWork(t *testing.T) {
	source, ids := memoryRecoveryFixture()
	before, _ := json.Marshal(source)
	copy, err := RemapRecovery(source, ids)
	if err != nil {
		t.Fatal(err)
	}
	if len(copy.Heads) != 3 || len(copy.Revisions) != 4 || copy.Revisions[1].Parent != "new-scene-v1" {
		t.Fatal("history flattened or lost")
	}
	if copy.Heads[0].ID != "new-scene" || copy.Heads[0].Branch != "new-canon" || copy.Heads[0].Project != "copy" || copy.Heads[0].Sources[0].Resource != "new-body" || copy.Heads[0].Sources[0].Revision != "new-release" {
		t.Fatal("source graph identity retained")
	}
	pin := copy.Heads[1].Summary.Inputs[0]
	if pin.ID != "new-scene" || pin.Revision != "new-scene-v1" || pin.Hash != SummaryTextHash(source.Revisions[0].Item.Text) || ValidateSummarySource(copy.Heads[1], copy.Revisions[0].Item, pin) != nil {
		t.Fatal("historical summary pin silently rebound to latest")
	}
	if copy.Heads[1].Dependencies[0] != "new-scene" {
		t.Fatal("dependency not remapped")
	}
	for i, item := range copy.Heads {
		if item.Text != source.Heads[i].Text || item.State != source.Heads[i].State || item.Actor != source.Heads[i].Actor || !reflect.DeepEqual(item.KnownBy, source.Heads[i].KnownBy) {
			t.Fatal("copy changed text, authorship, narrative identity or adoption state")
		}
	}
	if copy.Heads[2].Session != "new-session" || copy.Heads[2].Branch != "new-adaptation" || copy.Heads[0].Summary.UnitID != "scene" {
		t.Fatal("private session or narrative unit changed incorrectly")
	}
	if len(copy.Sessions) != 1 || copy.Sessions[0].ID != "new-session" || copy.Sessions[0].Revision != 3 || copy.Sessions[0].FocusIDs[0] != "new-draft" || copy.Sessions[0].Actor != "writer" || copy.Sessions[0].Notes != source.Sessions[0].Notes {
		t.Fatal("short-term checkpoint or privacy lost")
	}
	copy.Heads[0].KnownBy[0] = "changed"
	copy.Heads[1].Summary.Inputs[0].ID = "changed"
	copy.Sessions[0].FocusIDs[0] = "changed"
	after, _ := json.Marshal(source)
	if string(before) != string(after) {
		t.Fatal("copy mutated original graph")
	}
}

func TestRecoveryRejectsIncompleteOrConflictingGraphWithoutPartialCopy(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*RecoveryGraph, *RecoveryMap)
	}{
		{"same project", func(_ *RecoveryGraph, ids *RecoveryMap) { ids.TargetProject = ids.SourceProject }},
		{"missing branch", func(_ *RecoveryGraph, ids *RecoveryMap) { delete(ids.Branches, "canon") }},
		{"missing session", func(_ *RecoveryGraph, ids *RecoveryMap) { delete(ids.Sessions, "session") }},
		{"missing checkpoint", func(g *RecoveryGraph, _ *RecoveryMap) { g.Sessions = nil }},
		{"private owner changed", func(g *RecoveryGraph, _ *RecoveryMap) { g.Sessions[0].Actor = "other-writer" }},
		{"foreign focus", func(g *RecoveryGraph, _ *RecoveryMap) { g.Sessions[0].FocusIDs = []string{"scene"} }},
		{"missing revision", func(_ *RecoveryGraph, ids *RecoveryMap) { delete(ids.Revisions, "scene-v1") }},
		{"collision", func(_ *RecoveryGraph, ids *RecoveryMap) { ids.Items["draft"] = ids.Items["scene"] }},
		{"old identity", func(_ *RecoveryGraph, ids *RecoveryMap) { ids.Items["draft"] = "draft" }},
		{"source alias", func(_ *RecoveryGraph, ids *RecoveryMap) { ids.Items["draft"] = "scene" }},
		{"missing body", func(_ *RecoveryGraph, ids *RecoveryMap) { ids.Resources = nil }},
		{"changed body", func(_ *RecoveryGraph, ids *RecoveryMap) {
			for from, to := range ids.Resources {
				to.Hash = strings.Repeat("b", 64)
				ids.Resources[from] = to
			}
		}},
		{"missing historical parent", func(g *RecoveryGraph, _ *RecoveryMap) { g.Revisions = g.Revisions[1:] }},
		{"cycle", func(g *RecoveryGraph, _ *RecoveryMap) { g.Revisions[0].Parent = "scene-v2" }},
		{"disconnected history", func(g *RecoveryGraph, _ *RecoveryMap) { g.Revisions[1].Parent = "" }},
		{"head mismatch", func(g *RecoveryGraph, _ *RecoveryMap) { g.Heads[0].Text = "different" }},
		{"duplicate version", func(g *RecoveryGraph, _ *RecoveryMap) { g.Revisions = append(g.Revisions, g.Revisions[0]) }},
		{"foreign project", func(g *RecoveryGraph, _ *RecoveryMap) { g.Revisions[0].Item.Project = "foreign" }},
		{"foreign history", func(g *RecoveryGraph, _ *RecoveryMap) { g.Revisions[0].Item.Branch = "adaptation" }},
		{"wrong summary proof", func(g *RecoveryGraph, _ *RecoveryMap) { g.Heads[1].Summary.Inputs[0].Hash = strings.Repeat("b", 64) }},
		{"missing dependency", func(g *RecoveryGraph, _ *RecoveryMap) { g.Heads[1].Dependencies[0] = "missing" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			graph, ids := memoryRecoveryFixture()
			test.change(&graph, &ids)
			copy, err := RemapRecovery(graph, ids)
			if err == nil || len(copy.Heads) != 0 || len(copy.Revisions) != 0 {
				t.Fatal("invalid graph produced a usable partial copy")
			}
		})
	}
}
