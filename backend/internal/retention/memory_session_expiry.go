package retention

import "strconv"

// MemorySessionMetadataOwner resolves only the non-content account/project/
// branch scope. Archive evidence loading uses it before validating the erased
// document against its independent journal. It does not certify that document.
func (index *SourceIndex) MemorySessionMetadataOwner(row SourceRow) (SourceOwner, error) {
	v := row.Values
	revision, err := strconv.ParseInt(v["revision"], 10, 64)
	if row.Table != "memory_sessions" || !id32.MatchString(v["id"]) || err != nil || revision < 1 || strconv.FormatInt(revision, 10) != v["revision"] {
		return SourceOwner{}, ErrSourceOwnership
	}
	branch, err := index.Owner("branches", map[string]string{"id": v["branch_id"]})
	if err != nil || branch.Shared || branch.ProjectID != v["project_id"] {
		return SourceOwner{}, ErrSourceOwnership
	}
	actor, err := index.Owner("users", map[string]string{"id": v["actor_id"]})
	if err != nil || actor.WorkspaceID != branch.WorkspaceID {
		return SourceOwner{}, ErrSourceOwnership
	}
	return branch, nil
}

// The caller supplies an independently verified journal projection, not one
// copied out of document. Exact envelope and scope equality prevent substitution.
func ValidateMemorySessionExpiry(row SourceRow, proof ExpiredProjection) error {
	if row.Table != "memory_sessions" || row.Nulls["document"] {
		return ErrSourceOwnership
	}
	key, err := RowOwnerKey(row.Table, row.Values)
	if err != nil {
		return err
	}
	id, err := ReferenceID(row.Values["project_id"], row.Table, key, "document")
	actual, parseErr := ParseSourceProjection([]byte(row.Values["document"]), "memory_sessions.document")
	if err != nil || parseErr != nil || proof.ReferenceID != id || stableHash(actual) != stableHash(proof) || validateTransientOwner(row, "document", proof) != nil {
		return ErrSourceOwnership
	}
	return nil
}
