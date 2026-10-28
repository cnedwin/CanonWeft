package retention

import (
	"encoding/json"
	"slices"
	"strings"
)

// RecoveryOwner identifies a row, not a content hash. Historical-only source
// rows need not become executable rows in the recovered project.
type RecoveryOwner struct{ Table, Key string }

type RecoveryExpiryMap struct {
	SourceProject, TargetProject   string
	Owners                         map[RecoveryOwner]string
	Rows                           map[RecoveryOwner]SourceRow
	Resources, Revisions, Releases map[string]string
	historicalOwners               map[string]string
}

type RecoveryExpiryGraph struct {
	Versions []ReferenceVersion
	Proofs   map[RecoveryReferenceKey]ExpiredProjection
}

type RecoveryReferenceKey struct {
	ID       string
	Revision int64
}

// RebindRecoveryExpiryGraph transforms verified, body-free historical evidence.
// The caller must supply the exact pinned ancestors and each expired version's
// persisted parent/evidence pair. It proves no source-row ownership or restore
// authorization; those remain checks of the owning recovery SQL transaction.
func RebindRecoveryExpiryGraph(versions []ReferenceVersion, journals []ExpiryJournalRecord, mapping RecoveryExpiryMap) (RecoveryExpiryGraph, error) {
	fail := func() (RecoveryExpiryGraph, error) { return RecoveryExpiryGraph{}, ErrInvalidInventory }
	if !id32.MatchString(mapping.SourceProject) || !id32.MatchString(mapping.TargetProject) || mapping.SourceProject == mapping.TargetProject || len(versions) > 65536 || len(journals) > 65536 {
		return fail()
	}
	if err := ValidateArchivedPins(versions); err != nil {
		return fail()
	}
	original := map[referenceVersionKey]ReferenceVersion{}
	proofs := map[referenceVersionKey]ExpiredProjection{}
	refIDs := map[string]string{}
	fields := map[string]string{}
	owners := map[string]string{}
	targetIDs := map[string]string{}
	for _, v := range versions {
		if v.ProjectID != mapping.SourceProject {
			return fail()
		}
		key := referenceKey(v.Reference)
		if _, duplicate := original[key]; duplicate {
			return fail()
		}
		original[key] = v
		owner := recoveryExpiryOwner(v, mapping)
		field, err := recoveryExpiryField(v.Table, v.Field, mapping.Resources)
		if err != nil {
			return fail()
		}
		id, err := ReferenceID(mapping.TargetProject, v.Table, owner, field)
		if err != nil {
			return fail()
		}
		if prior, exists := targetIDs[id]; exists && prior != v.Reference.ID {
			return fail()
		}
		targetIDs[id] = v.Reference.ID
		refIDs[v.Reference.ID], fields[v.Reference.ID], owners[v.Reference.ID] = id, field, owner
	}
	memoryTargets := map[string]SourceRow{}
	for _, row := range mapping.Rows {
		if row.Table == "memory_items" || row.Table == "memory_revisions" {
			key := row.Table + ":" + row.Values["id"]
			if _, duplicate := memoryTargets[key]; duplicate {
				return fail()
			}
			memoryTargets[key] = row
		}
	}
	for _, journal := range journals {
		v, proof, err := ValidateExpiryJournalRecord(journal)
		key := referenceKey(v.Reference)
		want, exists := original[key]
		if err != nil || !exists || stableHash(want) != stableHash(v) {
			return fail()
		}
		if _, duplicate := proofs[key]; duplicate {
			return fail()
		}
		parent, parentErr := decodeReferenceVersion(journal.Parent, journal.ParentHash)
		stored, exists := original[referenceKey(parent.Reference)]
		if parentErr != nil || !exists || stableHash(stored) != journal.ParentHash {
			return fail()
		}
		proofs[key] = proof
	}
	result := RecoveryExpiryGraph{Versions: []ReferenceVersion{}, Proofs: map[RecoveryReferenceKey]ExpiredProjection{}}
	mapped := map[referenceVersionKey]ReferenceVersion{}
	ordered := slices.Clone(versions)
	slices.SortFunc(ordered, func(a, b ReferenceVersion) int {
		if a.Reference.ID != b.Reference.ID {
			return strings.Compare(a.Reference.ID, b.Reference.ID)
		}
		if a.Reference.Revision < b.Reference.Revision {
			return -1
		}
		if a.Reference.Revision > b.Reference.Revision {
			return 1
		}
		return 0
	})
	for _, v := range ordered {
		key := referenceKey(v.Reference)
		var next ReferenceVersion
		if v.Schema == 2 {
			proof, exists := proofs[key]
			parent, hasParent := mapped[referenceVersionKey{v.Reference.ID, v.Expiration.PriorRevision}]
			if !exists || !hasParent {
				return fail()
			}
			proof.ReferenceID = refIDs[v.Reference.ID]
			_, historical := mapping.historicalOwners[v.Reference.ID]
			if row, exists := mapping.Rows[RecoveryOwner{v.Table, v.OwnerKey}]; exists && !historical {
				rowKey, err := RowOwnerKey(row.Table, row.Values)
				if err != nil || row.Table != v.Table || rowKey != owners[v.Reference.ID] {
					return fail()
				}
				if len(TransientColumns(v.Reference.Rule)) > 0 {
					proof.Summary.ContentScopeHash, err = TransientIdentity(parent.Reference.Rule, row.Values)
					if err != nil {
						return fail()
					}
				}
				if v.Field == "document" {
					proof.Summary.InputHash, err = recoveryMemoryCounterpartHash(row, memoryTargets, proof.Summary.InputHash)
					if err != nil {
						return fail()
					}
				}
				if v.Table == "artifact_releases" && v.Field == "content_hash" {
					if _, erased := ParseSourceProjection([]byte(row.Values["manifest"]), "artifact_releases.manifest"); erased != nil {
						if _, err := releaseExpirySummary(row); err != nil {
							return fail()
						}
						proof.Summary.RecoveryManifestHash = row.Values["content_hash"]
					}
				}
			}
			if proof.Summary.Release != nil {
				// Clone before changing IDs: caller's evidence must remain intact.
				copySummary := *proof.Summary.Release
				copySummary.Artifacts = slices.Clone(copySummary.Artifacts)
				for i := range copySummary.Artifacts {
					id := copySummary.Artifacts[i].ResourceID
					if to, exists := mapping.Resources[id]; exists {
						copySummary.Artifacts[i].ResourceID = to
					}
				}
				proof.Summary.Release = &copySummary
			}
			var err error
			next, err = expiredReferenceVersion(parent, v.Deadline, proof)
			if err != nil {
				return fail()
			}
			result.Proofs[RecoveryReferenceKey{next.Reference.ID, next.Reference.Revision}] = proof
		} else {
			raw, _ := json.Marshal(v)
			if json.Unmarshal(raw, &next) != nil {
				return fail()
			}
			next.ProjectID = mapping.TargetProject
			_, historical := mapping.historicalOwners[v.Reference.ID]
			next.ProvenanceOnly = v.ProvenanceOnly || historical
			next.OwnerKey, next.Field = owners[v.Reference.ID], fields[v.Reference.ID]
			next.Reference.ID = refIDs[v.Reference.ID]
			next.Reference.Rule = next.Table + "." + next.Field
			next.SourceID = observationID(next.ProjectID, next.Table, next.OwnerKey, strings.SplitN(next.Field, ".", 2)[0])
			next.Lifecycle = Lifecycle{}
			class := next.Class
			if next.Reference.ContentRoot {
				class = Content
			}
			var err error
			next.Deadline, err = Deadline(class, next.Reference.FirstVerifiedAt, next.Lifecycle)
			if err != nil {
				return fail()
			}
			for i := range next.Reference.Sources {
				id, exists := refIDs[next.Reference.Sources[i].ReferenceID]
				if !exists {
					return fail()
				}
				next.Reference.Sources[i].ReferenceID = id
			}
			slices.SortFunc(next.Reference.Sources, func(a, b ReferenceSource) int { return strings.Compare(a.ReferenceID, b.ReferenceID) })
			if next.DraftVersion != nil {
				next.DraftVersion.BaseHead = recoveryExpiryBase(next.Table, next.DraftVersion.BaseHead, mapping)
			}
			if p := next.WorkingProjection; p != nil {
				p.BaseHead = recoveryExpiryBase(next.Table, p.BaseHead, mapping)
				for i := range p.Dependencies {
					id, exists := refIDs[p.Dependencies[i].ReferenceID]
					if !exists {
						return fail()
					}
					p.Dependencies[i].ReferenceID = id
				}
				slices.SortFunc(p.Dependencies, func(a, b ProjectionDependency) int { return strings.Compare(a.ReferenceID, b.ReferenceID) })
			}
			if !validReferenceVersion(next) {
				return fail()
			}
		}
		mapped[key] = next
		result.Versions = append(result.Versions, next)
	}
	if err := ValidateArchivedPins(result.Versions); err != nil {
		return fail()
	}
	return result, nil
}

func recoveryExpiryOwner(v ReferenceVersion, mapping RecoveryExpiryMap) string {
	if owner, exists := mapping.historicalOwners[v.Reference.ID]; exists {
		return owner
	}
	if owner, exists := mapping.Owners[RecoveryOwner{v.Table, v.OwnerKey}]; exists {
		return owner
	}
	return v.OwnerKey
}

func (p *RecoveryExpiryPlan) bindHistoricalOwners(mapping RecoveryExpiryMap) RecoveryExpiryMap {
	mapping.historicalOwners = map[string]string{}
	for _, v := range p.versions {
		if _, current := p.current[v.Reference.ID]; current {
			continue
		}
		// The source slot keeps nested fields together without occupying the
		// recovered author's current business identity or needing an execution row.
		owner, _ := OwnerKey(DigestKeyPart("recovery-provenance:" + p.project + ":" + v.SourceID))
		mapping.historicalOwners[v.Reference.ID] = owner
	}
	return mapping
}

func recoveryExpiryBase(table, id string, mapping RecoveryExpiryMap) string {
	ids := mapping.Revisions
	if table == "resource_drafts" || table == "artifact_releases" {
		ids = mapping.Releases
	}
	if target, exists := ids[id]; exists {
		return target
	}
	return id
}

func recoveryExpiryField(table, field string, resources map[string]string) (string, error) {
	if table != "artifact_releases" || !strings.HasPrefix(field, "manifest.artifacts.") {
		return field, nil
	}
	parts := strings.Split(field, ".")
	if len(parts) != 4 {
		return "", ErrInvalidInventory
	}
	if target, exists := resources[parts[2]]; exists {
		parts[2] = target
	}
	return strings.Join(parts, "."), nil
}
