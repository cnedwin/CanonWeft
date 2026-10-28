package retention

import (
	"fmt"
	"sort"
)

// ArchivedMaterials binds the current journal to actual archived business rows.
// Call on a fresh index, once per project; discard the index after any error.
// Journal/source-clock/dependency validation must precede this step. No SQL or
// blob reads occur, and the result grants no live authority or cleanup rights.
func (index *SourceIndex) ArchivedMaterials(project string, versions map[string]ReferenceVersion, journals map[string]ExpiryJournalRecord, aliases ...RequestAliasRecord) ([]MaterialReference, error) {
	return index.archivedMaterials(project, versions, journals, true, aliases...)
}

type ArchiveMaterialProject struct {
	Project  string
	Versions map[string]ReferenceVersion
	Journals map[string]ExpiryJournalRecord
}

// Bind every project's proof before resolving cross-table ownership. An audit
// or operation row can refer to an erased session in another archived project;
// ownership must not depend on Go map iteration order.
func (index *SourceIndex) ArchivedMaterialsBatch(projects []ArchiveMaterialProject, aliases ...RequestAliasRecord) (map[string][]MaterialReference, error) {
	seen := map[string]bool{}
	for _, p := range projects {
		if seen[p.Project] {
			return nil, ErrSourceOwnership
		}
		seen[p.Project] = true
		if _, err := index.archivedMaterials(p.Project, p.Versions, p.Journals, false, aliases...); err != nil {
			return nil, err
		}
	}
	result := map[string][]MaterialReference{}
	for _, p := range projects {
		materials, err := index.collectArchivedMaterials(p.Project, p.Versions)
		if err != nil {
			return nil, err
		}
		result[p.Project] = materials
	}
	return result, nil
}

func (index *SourceIndex) archivedMaterials(project string, versions map[string]ReferenceVersion, journals map[string]ExpiryJournalRecord, collect bool, aliases ...RequestAliasRecord) ([]MaterialReference, error) {
	if index == nil || !id32.MatchString(project) || len(versions) > 250000 || len(aliases) > 250000 {
		return nil, ErrSourceOwnership
	}
	aliasByKey := map[string]RequestAliasRecord{}
	for _, alias := range aliases {
		key := requestAliasKey(alias.Table, alias.Identity.ActorID, alias.OriginalKeyHash)
		if _, duplicate := aliasByKey[key]; duplicate {
			return nil, ErrSourceOwnership
		}
		aliasByKey[key] = alias
	}
	for id, v := range versions {
		if id != v.Reference.ID || v.ProjectID != project || !validReferenceVersion(v) {
			return nil, ErrSourceOwnership
		}
		if _, alreadyBound := index.expired[id]; alreadyBound {
			return nil, ErrSourceOwnership
		}
	}
	err := index.bindVerifiedExpiryVersions(project, versions,
		func(v ReferenceVersion) (ExpiredProjection, error) {
			record, exists := journals[v.Reference.ID]
			if !exists {
				return ExpiredProjection{}, ErrInvalidInventory
			}
			verified, proof, err := ValidateExpiryJournalRecord(record)
			if err != nil || stableHash(verified) != stableHash(v) {
				return ExpiredProjection{}, ErrInvalidInventory
			}
			if identity := proof.Summary.RequestIdentity; identity != nil {
				alias, exists := aliasByKey[requestAliasKey(v.Table, identity.ActorID, v.Reference.PayloadHash)]
				if !exists || validateRequestAlias(v, proof, alias) != nil {
					return ExpiredProjection{}, ErrSourceOwnership
				}
			}
			return proof, nil
		},
		func(row SourceRow, v ReferenceVersion) (string, error) {
			return nestedSourceValue(project, row, v, versions, nil)
		})
	if err != nil {
		return nil, fmt.Errorf("archive expiry binding: %w", err)
	}
	if err := index.validateRetiredRequestCoverage(project); err != nil {
		return nil, err
	}
	if !collect {
		return nil, nil
	}
	return index.collectArchivedMaterials(project, versions)
}

func (index *SourceIndex) collectArchivedMaterials(project string, versions map[string]ReferenceVersion) ([]MaterialReference, error) {
	materials, err := index.MaterialReferences(project)
	if err != nil {
		return nil, fmt.Errorf("archive material enumeration: %w", err)
	}
	for _, m := range index.preservedAfterExpiry {
		if _, selected := versions[m.ID]; selected {
			materials = append(materials, m.MaterialReference)
		}
	}
	actual := map[string]MaterialReference{}
	for _, material := range materials {
		if _, duplicate := actual[material.ID]; duplicate {
			return nil, ErrSourceOwnership
		}
		actual[material.ID] = material
	}
	for id, v := range versions {
		if v.Reference.State == Expired {
			if _, verified := index.expired[id]; !verified {
				return nil, ErrSourceOwnership
			}
			continue
		}
		m, exists := actual[id]
		if !exists || m.Table != v.Table || m.OwnerKey != v.OwnerKey || m.Field != v.Field || m.Storage != v.Storage || m.Class != v.Class || m.PayloadHash != v.Reference.PayloadHash || m.ObjectHash != v.Reference.ObjectHash || m.SourceID != v.SourceID || m.SourcePayloadHash != v.SourcePayloadHash {
			return nil, fmt.Errorf("archive material mismatch %s.%s ref=%s exists=%t payload=%t object=%t source=%t sourcePayload=%t: %w", v.Table, v.Field, id, exists, m.PayloadHash == v.Reference.PayloadHash, m.ObjectHash == v.Reference.ObjectHash, m.SourceID == v.SourceID, m.SourcePayloadHash == v.SourcePayloadHash, ErrSourceOwnership)
		}
	}
	sort.Slice(materials, func(i, j int) bool { return materials[i].ID < materials[j].ID })
	return materials, nil
}
