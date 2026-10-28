package memory

// These identities contain no recovered text. The SQL layer supplies them only
// after verifying independent expiry proofs and writes envelopes, never Items.
type RecoveryHeadIdentity struct {
	ID, Project, Branch, Revision, Kind, State, Actor, Session, ContentHash string
}

type RecoveryRevisionIdentity struct {
	ID, Project, ItemID, Parent, ContentHash string
}

func recoveryHeadIdentity(item Item) RecoveryHeadIdentity {
	return RecoveryHeadIdentity{ID: item.ID, Project: item.Project, Branch: item.Branch, Revision: item.Revision, Kind: item.Kind, State: item.State, Actor: item.Actor, Session: item.Session, ContentHash: digest(item)}
}

func recoveryRevisionIdentity(version RecoveryRevision) RecoveryRevisionIdentity {
	return RecoveryRevisionIdentity{ID: version.Item.Revision, Project: version.Item.Project, ItemID: version.Item.ID, Parent: version.Parent, ContentHash: digest(version.Item)}
}

func recoveryStructure(source RecoveryGraph, project string) (map[string]RecoveryHeadIdentity, map[string]RecoveryRevisionIdentity, error) {
	heads := map[string]RecoveryHeadIdentity{}
	versions := map[string]RecoveryRevisionIdentity{}
	allHeads := append([]RecoveryHeadIdentity(nil), source.ExpiredHeads...)
	for _, item := range source.Heads {
		allHeads = append(allHeads, recoveryHeadIdentity(item))
	}
	for _, head := range allHeads {
		if _, duplicate := heads[head.ID]; duplicate || head.Project != project || !nonempty(head.ID) || !nonempty(head.Revision) || !nonempty(head.Branch) || !hashPattern.MatchString(head.ContentHash) {
			return nil, nil, ErrRecoveryGraph
		}
		heads[head.ID] = head
	}
	allVersions := append([]RecoveryRevisionIdentity(nil), source.ExpiredRevisions...)
	for _, v := range source.Revisions {
		head, ok := heads[v.Item.ID]
		if !ok || v.Item.Branch != head.Branch {
			return nil, nil, ErrRecoveryGraph
		}
		allVersions = append(allVersions, recoveryRevisionIdentity(v))
	}
	for _, version := range allVersions {
		if _, duplicate := versions[version.ID]; duplicate || version.Project != project || !nonempty(version.ID) || !nonempty(version.ItemID) || !hashPattern.MatchString(version.ContentHash) {
			return nil, nil, ErrRecoveryGraph
		}
		versions[version.ID] = version
	}
	reachable := map[string]bool{}
	for _, head := range heads {
		version, exists := versions[head.Revision]
		if !exists || version.ItemID != head.ID || version.ContentHash != head.ContentHash {
			return nil, nil, ErrRecoveryGraph
		}
		chain := map[string]bool{}
		for cursor := head.Revision; cursor != ""; {
			version, ok := versions[cursor]
			if !ok || chain[cursor] || version.ItemID != head.ID {
				return nil, nil, ErrRecoveryGraph
			}
			chain[cursor], reachable[cursor] = true, true
			cursor = version.Parent
		}
	}
	if len(reachable) != len(versions) {
		return nil, nil, ErrRecoveryGraph
	}
	return heads, versions, nil
}
