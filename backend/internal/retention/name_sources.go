package retention

import "encoding/json"

// A saved name catalog keeps its cited originals even when no generic import
// editor was opened. Names are author-editable; this preserves provenance, not
// the truth or publication status of any name.
func (g *authorContentGraph) nameSourceOriginals(row SourceRow) error {
	var catalog struct {
		Schema    string `json:"schema"`
		Actor     string `json:"actor_id"`
		Materials struct {
			Records []struct {
				ImportID string `json:"source_import_id"`
			} `json:"records"`
		} `json:"materials"`
	}
	if json.Unmarshal([]byte(row.Values["body"]), &catalog) != nil || catalog.Schema != "name-material-catalog-v1" {
		return nil
	}
	if catalog.Actor != row.Values["actor_id"] || len(catalog.Materials.Records) > 600 {
		return ErrSourceOwnership
	}
	resource, err := g.row("resources", row.Values["resource_id"])
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, record := range catalog.Materials.Records {
		if record.ImportID == "" || seen[record.ImportID] {
			continue
		}
		seen[record.ImportID] = true
		source, err := g.row("source_imports", record.ImportID)
		if err != nil || source.Values["actor_id"] != catalog.Actor || source.Values["branch_id"] != resource.Values["branch_id"] {
			return ErrSourceOwnership
		}
		if err = g.add(source, "source_hash", source.Values["source_hash"]); err != nil {
			return err
		}
		if err = g.add(source, "report", ""); err != nil {
			return err
		}
	}
	return nil
}
