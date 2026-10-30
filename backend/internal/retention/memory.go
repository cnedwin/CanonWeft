package retention

import (
	"bytes"
	"encoding/json"
	"io"
	"slices"
	"strconv"
	"unicode/utf8"

	"novelstudio/internal/domain"
	"novelstudio/internal/memory"
)

type memoryCheckpoint struct {
	Title    string   `json:"title"`
	Goal     string   `json:"goal"`
	Notes    string   `json:"notes"`
	FocusIDs []string `json:"focus_ids"`
}

// Memory JSON is a typed document, never an instruction or an ownership claim.
// Check duplicate object keys before normal decoding, including nested sources.
func memoryJSON(raw string, target any) error {
	return boundedTypedJSON(raw, target, 2<<20)
}

// Only dedicated typed contracts may opt into a larger limit. Ordinary memory,
// execution and author documents keep their established two-MiB boundary.
func boundedTypedJSON(raw string, target any, limit int) error {
	if len(raw) == 0 || len(raw) > limit || !utf8.ValidString(raw) {
		return ErrSourceOwnership
	}
	d := json.NewDecoder(bytes.NewBufferString(raw))
	d.UseNumber()
	var value func(int) error
	value = func(depth int) error {
		if depth > 32 {
			return ErrSourceOwnership
		}
		token, err := d.Token()
		if err != nil {
			return ErrSourceOwnership
		}
		if delim, ok := token.(json.Delim); ok {
			switch delim {
			case '{':
				seen := map[string]bool{}
				for d.More() {
					key, err := d.Token()
					name, ok := key.(string)
					if err != nil || !ok || seen[name] {
						return ErrSourceOwnership
					}
					seen[name] = true
					if err = value(depth + 1); err != nil {
						return err
					}
				}
				end, err := d.Token()
				if err != nil || end != json.Delim('}') {
					return ErrSourceOwnership
				}
			case '[':
				for d.More() {
					if err := value(depth + 1); err != nil {
						return err
					}
				}
				end, err := d.Token()
				if err != nil || end != json.Delim(']') {
					return ErrSourceOwnership
				}
			default:
				return ErrSourceOwnership
			}
		}
		return nil
	}
	if value(0) != nil {
		return ErrSourceOwnership
	}
	if _, err := d.Token(); err != io.EOF {
		return ErrSourceOwnership
	}
	d = json.NewDecoder(bytes.NewBufferString(raw))
	d.DisallowUnknownFields()
	if d.Decode(target) != nil {
		return ErrSourceOwnership
	}
	return nil
}

func (index *SourceIndex) memoryRow(table, id, project string) (SourceRow, error) {
	if !id32.MatchString(id) {
		return SourceRow{}, ErrSourceOwnership
	}
	row, err := index.sourceRow(table, map[string]string{"id": id})
	if err != nil || row.Values["project_id"] != project {
		return SourceRow{}, ErrSourceOwnership
	}
	return row, nil
}

func (index *SourceIndex) memorySession(id, project, branch, actor string) error {
	session, err := index.memoryRow("memory_sessions", id, project)
	if err != nil || session.Values["branch_id"] != branch || session.Values["actor_id"] != actor {
		return ErrSourceOwnership
	}
	return nil
}

func (index *SourceIndex) validateMemoryLinks(row SourceRow, owner SourceOwner) error {
	v := row.Values
	if row.Table == "memory_sessions" {
		key, err := RowOwnerKey(row.Table, v)
		if err != nil {
			return err
		}
		id, err := ReferenceID(owner.ProjectID, row.Table, key, "document")
		if err != nil {
			return err
		}
		if proof, ok := index.expiryProofs[id]; ok {
			if _, err := index.MemorySessionMetadataOwner(row); err != nil {
				return err
			}
			return ValidateMemorySessionExpiry(row, proof)
		}
		revision, err := strconv.ParseInt(v["revision"], 10, 64)
		var checkpoint memoryCheckpoint
		if err != nil || revision < 1 || !id32.MatchString(v["actor_id"]) || memoryJSON(v["document"], &checkpoint) != nil || utf8.RuneCountInString(checkpoint.Title) > 160 || utf8.RuneCountInString(checkpoint.Goal) > 8000 || utf8.RuneCountInString(checkpoint.Notes) > 32768 || len(checkpoint.FocusIDs) > 128 {
			return ErrSourceOwnership
		}
		var fields map[string]json.RawMessage
		if json.Unmarshal([]byte(v["document"]), &fields) != nil || len(fields) != 4 || checkpoint.FocusIDs == nil {
			return ErrSourceOwnership
		}
		for _, key := range []string{"title", "goal", "notes", "focus_ids"} {
			if raw, ok := fields[key]; !ok || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
				return ErrSourceOwnership
			}
		}
		seen := map[string]bool{}
		for _, id := range checkpoint.FocusIDs {
			item, err := index.memoryRow("memory_items", id, owner.ProjectID)
			if err != nil || seen[id] || item.Values["branch_id"] != v["branch_id"] || (item.Values["actor_id"] != "" && item.Values["actor_id"] != v["actor_id"]) {
				return ErrSourceOwnership
			}
			seen[id] = true
		}
		return nil
	}
	key, err := RowOwnerKey(row.Table, v)
	if err != nil {
		return err
	}
	id, err := ReferenceID(owner.ProjectID, row.Table, key, "document")
	if err != nil {
		return err
	}
	if proof, ok := index.expiryProofs[id]; ok {
		return index.validateExpiredMemoryLinks(row, proof)
	}
	var item memory.Item
	if memoryJSON(v["document"], &item) != nil || memory.ValidateItem(item) != nil || !id32.MatchString(item.ID) || !id32.MatchString(item.Revision) || item.Project != owner.ProjectID || !id32.MatchString(item.Branch) {
		return ErrSourceOwnership
	}
	branch, err := index.memoryRow("branches", item.Branch, owner.ProjectID)
	if err != nil || branch.Values["id"] != item.Branch {
		return ErrSourceOwnership
	}
	if item.Actor != "" {
		actor, err := index.sourceRow("users", map[string]string{"id": item.Actor})
		if err != nil || actor.Values["workspace_id"] != owner.WorkspaceID || index.memorySession(item.Session, item.Project, item.Branch, item.Actor) != nil {
			return ErrSourceOwnership
		}
	} else if item.Session != "" {
		return ErrSourceOwnership
	}
	if slices.Contains([]string{"published", "adopted_plan"}, item.State) && (item.Actor != "" || item.Session != "") {
		return ErrSourceOwnership
	}
	if row.Table == "memory_items" {
		if item.ID != v["id"] || item.Revision != v["head_revision"] || item.Branch != v["branch_id"] || item.Kind != v["kind"] || item.State != v["state"] || item.Actor != v["actor_id"] || item.Session != v["session_id"] {
			return ErrSourceOwnership
		}
		head, err := index.memoryRow("memory_revisions", item.Revision, owner.ProjectID)
		var frozen memory.Item
		if err != nil || head.Values["item_id"] != item.ID || memoryJSON(head.Values["document"], &frozen) != nil {
			return ErrSourceOwnership
		}
		left, _ := json.Marshal(item)
		right, _ := json.Marshal(frozen)
		if !bytes.Equal(left, right) {
			return ErrSourceOwnership
		}
	} else {
		if item.Revision != v["id"] || item.ID != v["item_id"] || !id32.MatchString(v["actor_id"]) || (item.Actor != "" && item.Actor != v["actor_id"]) {
			return ErrSourceOwnership
		}
		current, err := index.memoryRow("memory_items", item.ID, item.Project)
		if err != nil || current.Values["branch_id"] != item.Branch {
			return ErrSourceOwnership
		}
		seen := map[string]bool{item.Revision: true}
		parent := v["parent_id"]
		for parent != "" {
			if seen[parent] || len(seen) > 10000 {
				return ErrSourceOwnership
			}
			seen[parent] = true
			prior, err := index.memoryRow("memory_revisions", parent, item.Project)
			if err != nil || prior.Values["item_id"] != item.ID {
				return ErrSourceOwnership
			}
			parent = prior.Values["parent_id"]
		}
	}
	for _, source := range item.Sources {
		resource, err := index.memoryRow("resources", source.Resource, item.Project)
		if err != nil || resource.Values["branch_id"] != item.Branch {
			return ErrSourceOwnership
		}
		release, err := index.memoryRow("artifact_releases", source.Revision, item.Project)
		var manifest domain.Manifest
		if err != nil || release.Values["branch_id"] != item.Branch || release.Values["status"] != "PUBLISHED" || contentJSON([]byte(release.Values["manifest"]), &manifest, release.Values["content_hash"]) != nil || (manifest.Schema != 1 && manifest.Schema != 2) || manifest.BranchID != item.Branch || (manifest.ReleaseID != "" && manifest.ReleaseID != source.Revision) {
			return ErrSourceOwnership
		}
		found := 0
		for _, ref := range manifest.Artifacts {
			if ref.ResourceID == source.Resource && ref.Hash == source.Hash {
				found++
			}
		}
		if found != 1 {
			return ErrSourceOwnership
		}
	}
	if item.Summary != nil {
		for _, pin := range item.Summary.Inputs {
			revision, err := index.memoryRow("memory_revisions", pin.Revision, item.Project)
			var child memory.Item
			if err != nil || revision.Values["item_id"] != pin.ID || memoryJSON(revision.Values["document"], &child) != nil || memory.ValidateItem(child) != nil || memory.ValidateSummarySource(item, child, pin) != nil {
				return ErrSourceOwnership
			}
		}
	}
	seen := map[string]bool{}
	for _, id := range item.Dependencies {
		dependency, err := index.memoryRow("memory_items", id, item.Project)
		if err != nil || seen[id] || id == item.ID || dependency.Values["branch_id"] != item.Branch {
			return ErrSourceOwnership
		}
		seen[id] = true
	}
	return nil
}

func (g *adoptedContentGraph) memory() error {
	for _, table := range []string{"memory_items", "memory_revisions"} {
		for _, row := range g.index.rows[table] {
			if row.Values["project_id"] != g.project {
				continue
			}
			if _, err := g.index.Owner(table, row.Values); err != nil {
				return err
			}
			if g.index.replacedColumn(g.project, row, "document") {
				continue
			}
			var item memory.Item
			if memoryJSON(row.Values["document"], &item) != nil {
				return ErrSourceOwnership
			}
			if !slices.Contains([]string{"published", "adopted_plan"}, item.State) {
				continue
			}
			if err := g.add(row, "document", ""); err != nil {
				return err
			}
			for _, source := range item.Sources {
				if _, err := g.release(source.Revision, 0); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
