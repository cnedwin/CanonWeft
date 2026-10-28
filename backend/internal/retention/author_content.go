package retention

import (
	"bytes"
	"encoding/json"
	"sort"

	"novelstudio/internal/domain"
	"novelstudio/internal/skills"
)

// AuthorContentReferences preserves saved work independently of publication or
// execution qualification. It does not preserve raw executions or turn an
// automatically discovered, unreferenced skill candidate into retained content.
// Like adopted roots, this is one input to expiry planning, not GC permission.
func (index *SourceIndex) AuthorContentReferences(project string) ([]ContentReference, error) {
	if index == nil || !id32.MatchString(project) {
		return nil, ErrSourceOwnership
	}
	g := authorContentGraph{
		adoptedContentGraph: adoptedContentGraph{index: index, project: project, refs: map[string]ContentReference{}},
		capabilities:        map[string]domain.CapabilityDefinition{}, packages: map[string]bool{}, visiting: map[string]bool{},
	}
	if _, err := g.row("projects", project); err != nil {
		return nil, err
	}
	for table, field := range map[string]string{
		"novel_setup_drafts": "payload", "novel_settings_versions": "payload", "novel_settings_drafts": "payload",
		"ip_drafts": "payload", "patch_drafts": "payload", "import_drafts": "payload",
		"resource_drafts": "body", "resource_policy_drafts": "payload",
	} {
		for _, row := range index.rows[table] {
			owner, err := index.Owner(table, row.Values)
			if err != nil {
				return nil, err
			}
			if owner.ProjectID != project {
				continue
			}
			// Incomplete/stale private drafts remain the author's work; requiring
			// publishable JSON here would let failed validation erase that work.
			if !index.replacedColumn(project, row, field) {
				if err := g.add(row, field, ""); err != nil {
					return nil, err
				}
			}
			if table == "import_drafts" {
				source, err := g.row("source_imports", row.Values["import_id"])
				if err != nil || source.Values["actor_id"] != row.Values["actor_id"] {
					return nil, ErrSourceOwnership
				}
				if err := g.add(source, "source_hash", source.Values["source_hash"]); err != nil {
					return nil, err
				}
				if err := g.add(source, "report", ""); err != nil {
					return nil, err
				}
			}
			if table == "resource_drafts" && !index.replacedColumn(project, row, field) {
				if err := g.nameSourceOriginals(row); err != nil {
					return nil, err
				}
			}
		}
	}
	for _, row := range index.rows["capability_versions"] {
		if row.Values["project_id"] == project {
			if index.replacedColumn(project, row, "definition") {
				proof, err := g.expiredAuthorField(row, "definition")
				if err != nil || !validCapabilityContentExpiry(proof.Summary.CapabilityContent) {
					return nil, ErrSourceOwnership
				}
				packageID := proof.Summary.CapabilityContent.PackageID
				if packageID != "" {
					pkg, err := g.row("skill_packages", packageID)
					if err != nil || pkg.Values["bundle_hash"] != proof.Summary.CapabilityContent.PackageHash {
						return nil, ErrSourceOwnership
					}
					if err := g.skillPackage(packageID, 0); err != nil {
						return nil, err
					}
				}
				continue
			}
			if _, err := g.capability(row.Values["id"], 0); err != nil {
				return nil, err
			}
		}
	}
	refs := make([]ContentReference, 0, len(g.refs))
	for _, ref := range g.refs {
		refs = append(refs, ref)
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].ID < refs[j].ID })
	return refs, nil
}

type authorContentGraph struct {
	adoptedContentGraph
	capabilities map[string]domain.CapabilityDefinition
	packages     map[string]bool
	visiting     map[string]bool
}

func (g *authorContentGraph) expiredAuthorField(row SourceRow, field string) (ExpiredProjection, error) {
	key, err := RowOwnerKey(row.Table, row.Values)
	if err != nil {
		return ExpiredProjection{}, err
	}
	id, err := ReferenceID(g.project, row.Table, key, field)
	if err != nil {
		return ExpiredProjection{}, err
	}
	proof, ok := g.index.expiryProofs[id]
	if !ok || validateTransientOwner(row, field, proof) != nil {
		return ExpiredProjection{}, ErrSourceOwnership
	}
	return proof, nil
}

// SQL may reorder JSON keys, but unknown keys, aliases and duplicate keys may
// not hide a blob or dependency. Preserve exact JSON numbers in schema hashes.
func authorDocument(raw string, target any) error {
	if memoryJSON(raw, target) != nil {
		return ErrSourceOwnership
	}
	canonical, err := json.Marshal(target)
	if err != nil {
		return ErrSourceOwnership
	}
	return contentJSON([]byte(raw), target, financialDigest(canonical))
}

func authorCapabilityHash(parent string, definition domain.CapabilityDefinition) string {
	return capabilityProjectionHash([]any{parent, definition})
}

func capabilityProjectionHash(value any) string {
	raw, _ := json.Marshal(value)
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var normalized any
	if decoder.Decode(&normalized) != nil {
		return ""
	}
	canonical, _ := json.Marshal(normalized)
	return financialDigest(canonical)
}

func (g *authorContentGraph) capability(id string, depth int) (domain.CapabilityDefinition, error) {
	var definition domain.CapabilityDefinition
	if depth > 256 || g.visiting["capability:"+id] {
		return definition, ErrSourceOwnership
	}
	if cached, ok := g.capabilities[id]; ok {
		return cached, nil
	}
	row, err := g.row("capability_versions", id)
	if err != nil {
		return definition, err
	}
	if authorDocument(row.Values["definition"], &definition) != nil || definition.Validate() != nil || definition.Kind != row.Values["kind"] || definition.Key != row.Values["capability_key"] || authorCapabilityHash(row.Values["parent_id"], definition) != row.Values["content_hash"] {
		return definition, ErrSourceOwnership
	}
	g.visiting["capability:"+id] = true
	defer delete(g.visiting, "capability:"+id)
	if parent := row.Values["parent_id"]; parent != "" {
		previous, err := g.capability(parent, depth+1)
		if err != nil || previous.Kind != definition.Kind || previous.Key != definition.Key {
			return definition, ErrSourceOwnership
		}
	}
	for _, dependency := range definition.Dependencies {
		dep, err := g.capability(dependency.ID, depth+1)
		if err != nil || dep.Kind != dependency.Kind {
			return definition, ErrSourceOwnership
		}
		related, err := g.row("capability_versions", dependency.ID)
		if err != nil || related.Values["content_hash"] != dependency.Hash {
			return definition, ErrSourceOwnership
		}
	}
	if definition.PackageID != "" {
		pkg, err := g.row("skill_packages", definition.PackageID)
		if err != nil || pkg.Values["bundle_hash"] != definition.PackageHash {
			return definition, ErrSourceOwnership
		}
		if err := g.skillPackage(definition.PackageID, 0); err != nil {
			return definition, err
		}
	}
	if err := g.add(row, "definition", ""); err != nil {
		return definition, err
	}
	g.capabilities[id] = definition
	return definition, nil
}

type authorSkillSource struct {
	Kind        string `json:"kind"`
	Repository  string `json:"repository,omitempty"`
	Commit      string `json:"commit,omitempty"`
	Path        string `json:"path,omitempty"`
	ArchiveHash string `json:"archive_hash,omitempty"`
	URL         string `json:"url,omitempty"`
}

func (g *authorContentGraph) skillPackage(id string, depth int) error {
	if depth > 256 || g.visiting["package:"+id] {
		return ErrSourceOwnership
	}
	if g.packages[id] {
		return nil
	}
	row, err := g.row("skill_packages", id)
	if err != nil {
		return err
	}
	var report skills.Report
	var source authorSkillSource
	if g.index.replacedColumn(g.project, row, "report") {
		proof, err := g.expiredAuthorField(row, "report")
		if err != nil || proof.Summary.InputHash != row.Values["bundle_hash"] {
			return ErrSourceOwnership
		}
	} else if authorDocument(row.Values["report"], &report) != nil || report.Hash != row.Values["bundle_hash"] {
		return ErrSourceOwnership
	}
	if g.index.replacedColumn(g.project, row, "source") {
		proof, err := g.expiredAuthorField(row, "source")
		if err != nil {
			return err
		}
		source.ArchiveHash = proof.Summary.InputHash
	} else if authorDocument(row.Values["source"], &source) != nil || source.Kind == "" {
		return ErrSourceOwnership
	}
	g.visiting["package:"+id] = true
	defer delete(g.visiting, "package:"+id)
	for _, parent := range []string{row.Values["parent_id"], row.Values["upstream_id"]} {
		if parent != "" {
			if err := g.skillPackage(parent, depth+1); err != nil {
				return err
			}
		}
	}
	if err := g.add(row, "bundle_hash", row.Values["bundle_hash"]); err != nil {
		return err
	}
	for _, field := range []string{"report", "source"} {
		if err := g.add(row, field, ""); err != nil {
			return err
		}
	}
	if source.ArchiveHash != "" {
		if err := g.add(row, "source.archive_hash", source.ArchiveHash); err != nil {
			return err
		}
	}
	g.packages[id] = true
	return nil
}
