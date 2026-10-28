package retention

import "novelstudio/internal/memory"

// MemoryDocumentMetadataOwner checks durable scope and parent pointers without
// reconstructing erased text. Document authority is checked separately.
func (index *SourceIndex) MemoryDocumentMetadataOwner(row SourceRow) (SourceOwner, error) {
	if row.Table == "memory_sessions" {
		return index.MemorySessionMetadataOwner(row)
	}
	v := row.Values
	if (row.Table != "memory_items" && row.Table != "memory_revisions") || !id32.MatchString(v["id"]) {
		return SourceOwner{}, ErrSourceOwnership
	}
	current := row
	if row.Table == "memory_revisions" {
		var err error
		current, err = index.memoryRow("memory_items", v["item_id"], v["project_id"])
		if err != nil || !id32.MatchString(v["actor_id"]) {
			return SourceOwner{}, ErrSourceOwnership
		}
		seen := map[string]bool{v["id"]: true}
		parent := v["parent_id"]
		for parent != "" {
			if seen[parent] || len(seen) > 10000 {
				return SourceOwner{}, ErrSourceOwnership
			}
			seen[parent] = true
			prior, err := index.memoryRow("memory_revisions", parent, v["project_id"])
			if err != nil || prior.Values["item_id"] != v["item_id"] {
				return SourceOwner{}, ErrSourceOwnership
			}
			parent = prior.Values["parent_id"]
		}
	}
	branch, err := index.Owner("branches", map[string]string{"id": current.Values["branch_id"]})
	if err != nil || branch.Shared || branch.ProjectID != v["project_id"] {
		return SourceOwner{}, ErrSourceOwnership
	}
	if actor := v["actor_id"]; actor != "" {
		user, err := index.Owner("users", map[string]string{"id": actor})
		if err != nil || user.WorkspaceID != branch.WorkspaceID {
			return SourceOwner{}, ErrSourceOwnership
		}
	}
	if row.Table == "memory_items" {
		if v["actor_id"] == "" && v["session_id"] != "" {
			return SourceOwner{}, ErrSourceOwnership
		}
		if v["actor_id"] != "" && index.memorySession(v["session_id"], v["project_id"], v["branch_id"], v["actor_id"]) != nil {
			return SourceOwner{}, ErrSourceOwnership
		}
	}
	head, err := index.memoryRow("memory_revisions", current.Values["head_revision"], v["project_id"])
	if err != nil || head.Values["item_id"] != current.Values["id"] {
		return SourceOwner{}, ErrSourceOwnership
	}
	return branch, nil
}

func ValidateMemoryDocumentExpiry(row SourceRow, proof ExpiredProjection) error {
	if row.Table == "memory_sessions" {
		return ValidateMemorySessionExpiry(row, proof)
	}
	if (row.Table != "memory_items" && row.Table != "memory_revisions") || row.Nulls["document"] {
		return ErrSourceOwnership
	}
	key, err := RowOwnerKey(row.Table, row.Values)
	if err != nil {
		return err
	}
	id, err := ReferenceID(row.Values["project_id"], row.Table, key, "document")
	actual, parseErr := ParseSourceProjection([]byte(row.Values["document"]), row.Table+".document")
	if err != nil || parseErr != nil || proof.ReferenceID != id || stableHash(actual) != stableHash(proof) || validateTransientOwner(row, "document", proof) != nil {
		return ErrSourceOwnership
	}
	return nil
}

// The caller must first verify the independent expiry journal. Bind every
// supplied memory proof before resolving cross-row heads, then call Owner.
func (index *SourceIndex) BindVerifiedMemoryExpiry(row SourceRow, proof ExpiredProjection) error {
	if ValidateMemoryDocumentExpiry(row, proof) != nil {
		return ErrSourceOwnership
	}
	if _, err := index.MemoryDocumentMetadataOwner(row); err != nil {
		return err
	}
	if index.expiryProofs == nil {
		index.expiryProofs = map[string]ExpiredProjection{}
	}
	index.expiryProofs[proof.ReferenceID] = proof
	return nil
}

func (index *SourceIndex) memoryOriginalDigest(row SourceRow) (string, error) {
	key, err := RowOwnerKey(row.Table, row.Values)
	if err != nil {
		return "", err
	}
	id, err := ReferenceID(row.Values["project_id"], row.Table, key, "document")
	if err != nil {
		return "", err
	}
	if proof, ok := index.expiryProofs[id]; ok {
		if ValidateMemoryDocumentExpiry(row, proof) != nil {
			return "", ErrSourceOwnership
		}
		return proof.Summary.InputHash, nil
	}
	var item memory.Item
	if memoryJSON(row.Values["document"], &item) != nil || memory.ValidateItem(item) != nil {
		return "", ErrSourceOwnership
	}
	return stableHash(item), nil
}

func (index *SourceIndex) validateExpiredMemoryLinks(row SourceRow, proof ExpiredProjection) error {
	if ValidateMemoryDocumentExpiry(row, proof) != nil {
		return ErrSourceOwnership
	}
	if _, err := index.MemoryDocumentMetadataOwner(row); err != nil {
		return err
	}
	var other SourceRow
	var err error
	if row.Table == "memory_items" {
		other, err = index.memoryRow("memory_revisions", row.Values["head_revision"], row.Values["project_id"])
	} else {
		other, err = index.memoryRow("memory_items", row.Values["item_id"], row.Values["project_id"])
		if err == nil && other.Values["head_revision"] != row.Values["id"] {
			return nil
		}
	}
	if err != nil {
		return err
	}
	hash, err := index.memoryOriginalDigest(other)
	if err != nil || hash != proof.Summary.InputHash {
		return ErrSourceOwnership
	}
	return nil
}
