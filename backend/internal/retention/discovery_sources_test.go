package retention

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDiscoverySnapshotPinsExactConfigurationOrRequiresOriginalJournal(t *testing.T) {
	id := func(s string) string { return strings.Repeat(s, 32) }
	for _, mode := range []string{"valid", "disabled-now", "disabled-snapshot", "wrong-keyword", "wrong-revision", "future-revision", "old-configuration", "unknown-field"} {
		t.Run(mode, func(t *testing.T) {
			source := inventoryRow("skill_discovery_sources", map[string]string{"id": id("a"), "project_id": id("2"), "actor_id": id("4"), "repository": "fixture/repository", "source_ref": "main", "source_path": "skills", "keyword": "method", "enabled": "1", "candidate_limit": "1", "revision": "2"})
			snapshot := map[string]any{"id": id("a"), "repository": "fixture/repository", "ref": "main", "path": "skills", "keyword": "method", "enabled": true, "limit": 1, "revision": 2}
			runRevision := "2"
			switch mode {
			case "disabled-now":
				source.Values["enabled"] = "0"
			case "disabled-snapshot":
				snapshot["enabled"] = false
			case "wrong-keyword":
				snapshot["keyword"] = "substituted"
			case "wrong-revision":
				snapshot["revision"] = 1
			case "future-revision":
				snapshot["revision"], runRevision = 3, "3"
			case "old-configuration":
				source.Values["revision"], source.Values["keyword"] = "3", "changed"
			case "unknown-field":
				snapshot["extra_prompt"] = "unproved copied text"
			}
			raw, _ := json.Marshal(snapshot)
			run := inventoryRow("skill_discovery_runs", map[string]string{"id": id("b"), "project_id": id("2"), "actor_id": id("4"), "source_id": id("a"), "source_revision": runRevision, "source_snapshot": string(raw)})
			index, err := NewSourceIndex(append(sourceOwnershipRows(), source, run))
			if err != nil {
				t.Fatal(err)
			}
			key, _ := RowOwnerKey(run.Table, run.Values)
			refs := []MaterialReference{{Table: run.Table, OwnerKey: key, Field: "source_snapshot"}}
			err = index.attachDiscoverySources(id("2"), refs)
			valid := mode == "valid" || mode == "disabled-now" || mode == "old-configuration"
			if (err == nil) != valid {
				t.Fatal(mode, err)
			}
			if !valid {
				return
			}
			if len(refs[0].Sources) != 4 {
				t.Fatal("incomplete fields", refs)
			}
			for _, source := range refs[0].Sources {
				if source.Unresolved != (mode == "old-configuration") {
					t.Fatal("current fields substituted for historical configuration")
				}
			}
		})
	}
}
