package backup

import "novelstudio/internal/retention"

func archiveRetentionMaterials(business Data, history *archiveReferenceHistory, aliases ...retention.RequestAliasRecord) (map[string][]retention.MaterialReference, error) {
	owners, err := newRetentionArchiveOwners(business)
	if err != nil || history == nil {
		return nil, errRetentionOwner
	}
	byProject := map[string]map[string]retention.ReferenceVersion{}
	// A project can predate the first retention scan. Its actual material still
	// belongs in a backup even when it has no registered current references yet.
	for _, row := range owners.rows["projects"] {
		byProject[row.Values["id"]] = map[string]retention.ReferenceVersion{}
	}
	for id, v := range history.Current {
		if byProject[v.ProjectID] == nil {
			byProject[v.ProjectID] = map[string]retention.ReferenceVersion{}
		}
		byProject[v.ProjectID][id] = v
	}
	projects := []retention.ArchiveMaterialProject{}
	for project, current := range byProject {
		proofs := map[string]retention.ExpiryJournalRecord{}
		for id, v := range current {
			if proof, ok := history.Expiry[archiveVersionKey(id, v.Reference.Revision)]; ok {
				proofs[id] = proof
			}
		}
		projects = append(projects, retention.ArchiveMaterialProject{Project: project, Versions: current, Journals: proofs})
	}
	return owners.index.ArchivedMaterialsBatch(projects, aliases...)
}
