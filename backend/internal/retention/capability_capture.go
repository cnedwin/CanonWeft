package retention

import (
	"context"

	"novelstudio/internal/domain"
	"novelstudio/internal/skills"
)

func capabilityCaptureKeys(row SourceRow) ([]SourceKey, error) {
	var keys []SourceKey
	if row.Table == "skill_packages" {
		for _, field := range []string{"parent_id", "upstream_id"} {
			if id := row.Values[field]; id != "" {
				keys = append(keys, SourceKey{Table: "skill_packages", Primary: map[string]string{"id": id}})
			}
		}
		return keys, nil
	}
	var definition domain.CapabilityDefinition
	if authorDocument(row.Values["definition"], &definition) != nil || definition.Validate() != nil || authorCapabilityHash(row.Values["parent_id"], definition) != row.Values["content_hash"] || definition.Key != row.Values["capability_key"] || definition.Kind != row.Values["kind"] {
		return nil, ErrSourceOwnership
	}
	if parent := row.Values["parent_id"]; parent != "" {
		keys = append(keys, SourceKey{Table: "capability_versions", Primary: map[string]string{"id": parent}})
	}
	for _, dependency := range definition.Dependencies {
		keys = append(keys, SourceKey{Table: "capability_versions", Primary: map[string]string{"id": dependency.ID}})
	}
	if definition.PackageID != "" {
		keys = append(keys, SourceKey{Table: "skill_packages", Primary: map[string]string{"id": definition.PackageID}})
	}
	return keys, nil
}

// Verify saved package bytes and lineage without treating an unreferenced
// discovered candidate as authored content or giving it execution authority.
func (index *SourceIndex) verifyCapturedPackages(ctx context.Context, project string, rows map[string]SourceRow, read ReadSourceObject) error {
	g := authorContentGraph{adoptedContentGraph: adoptedContentGraph{index: index, project: project, refs: map[string]ContentReference{}}, capabilities: map[string]domain.CapabilityDefinition{}, packages: map[string]bool{}, visiting: map[string]bool{}}
	for _, row := range rows {
		if row.Table != "skill_packages" {
			continue
		}
		if err := g.skillPackage(row.Values["id"], 0); err != nil {
			return err
		}
	}
	// skillPackage verifies the owner, report/hash, ancestry and any independent
	// expiry proofs. Its references exclude only proven erased fields. Validate
	// the remaining bytes without using an execution-context builder, which
	// rightly rejects historical packages whose report/source has expired.
	for _, ref := range g.refs {
		if ref.ObjectHash == "" {
			continue
		}
		data, err := read(ctx, project, ref.ObjectHash)
		if err != nil || financialDigest(data) != ref.ObjectHash {
			return ErrSourceOwnership
		}
		if ref.Field == "bundle_hash" {
			var bundle skills.Bundle
			if contentJSON(data, &bundle, ref.ObjectHash) != nil || bundle.Validate() != nil {
				return ErrSourceOwnership
			}
		}
	}
	return nil
}
