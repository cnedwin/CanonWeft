package retention

import (
	"encoding/json"
	"slices"
	"strconv"

	"novelstudio/internal/skills"
)

// Discovery is a public-data input, never permission to execute a downloaded
// package. Configuration edits cannot rebind the immutable request snapshot.
func (index *SourceIndex) attachDiscoverySources(project string, refs []MaterialReference) error {
	for i := range refs {
		m := &refs[i]
		if m.Table != "skill_discovery_runs" && m.Table != "skill_discovery_candidates" {
			continue
		}
		row := index.rows[m.Table][m.OwnerKey]
		b := contextSourceBuilder{index: index, project: project, m: m}
		switch {
		case m.Table == "skill_discovery_runs" && m.Field == "source_snapshot":
			var snapshot struct {
				ID         string `json:"id"`
				Repository string `json:"repository"`
				Ref        string `json:"ref"`
				Path       string `json:"path"`
				Keyword    string `json:"keyword"`
				Enabled    bool   `json:"enabled"`
				Limit      int    `json:"limit"`
				Revision   int64  `json:"revision"`
			}
			revision, err := strconv.ParseInt(row.Values["source_revision"], 10, 64)
			if err != nil || authorDocument(row.Values["source_snapshot"], &snapshot) != nil || snapshot.ID != row.Values["source_id"] || snapshot.Revision != revision || revision < 1 || !snapshot.Enabled || snapshot.Limit < 1 || snapshot.Limit > 5 || len(snapshot.Keyword) > 120 || !skills.ValidRemoteSource(skills.RemoteRequest{Repository: snapshot.Repository, Ref: snapshot.Ref, Path: snapshot.Path}) {
				return ErrSourceOwnership
			}
			source, err := index.sourceRow("skill_discovery_sources", map[string]string{"id": snapshot.ID})
			if err != nil {
				return err
			}
			current, err := strconv.ParseInt(source.Values["revision"], 10, 64)
			if err != nil || current < revision {
				return ErrSourceOwnership
			}
			// Restore disables current execution without changing historical
			// configuration. The snapshot must prove it was enabled then;
			// today's switch is authorization, not provenance of that result.
			if current == revision && source.Values["candidate_limit"] != strconv.Itoa(snapshot.Limit) {
				return ErrSourceOwnership
			}
			for _, field := range []struct{ name, value string }{{"repository", snapshot.Repository}, {"source_ref", snapshot.Ref}, {"source_path", snapshot.Path}, {"keyword", snapshot.Keyword}} {
				if current == revision {
					if source.Values[field.name] != field.value {
						return ErrSourceOwnership
					}
					if err := b.link(source, field.name, ""); err != nil {
						return err
					}
					continue
				}
				// Only an already journaled snapshot proves the old configuration
				// as a whole. Matching individual historical strings is insufficient.
				key, err := RowOwnerKey(source.Table, source.Values)
				if err != nil {
					return err
				}
				id, err := ReferenceID(project, source.Table, key, field.name)
				if err != nil {
					return err
				}
				if err := appendSourceRequest(m, MaterialSource{ReferenceID: id, Unresolved: true}); err != nil {
					return err
				}
			}
		case m.Table == "skill_discovery_runs" && m.Field == "inventory":
			if err := b.link(row, "source_snapshot", ""); err != nil {
				return err
			}
		case m.Table == "skill_discovery_candidates" && m.Field == "source_path":
			run, err := index.sourceRow("skill_discovery_runs", map[string]string{"id": row.Values["run_id"]})
			var inventory skills.DiscoveryInventory
			if err != nil || run.Values["source_id"] != row.Values["source_id"] || json.Unmarshal([]byte(run.Values["inventory"]), &inventory) != nil || inventory.Commit != row.Values["source_commit"] || !slices.Contains(inventory.Paths, row.Values["source_path"]) {
				return ErrSourceOwnership
			}
			if err := b.link(run, "inventory", ""); err != nil {
				return err
			}
		}
	}
	return nil
}
