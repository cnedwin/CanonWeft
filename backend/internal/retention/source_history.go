package retention

// Back-pointers (branch -> head -> branch) are not ownership recursion. Inspect
// their explicit PK/branch columns directly, then check the fork lineage. This
// permits a legitimate inherited head without accepting a sibling's revision.
func (index *SourceIndex) branchLineage(branch, project string) (map[string]bool, error) {
	lineage := map[string]bool{}
	for branch != "" {
		if lineage[branch] || len(lineage) >= 10000 {
			return nil, ErrSourceOwnership
		}
		row, err := index.sourceRow("branches", map[string]string{"id": branch})
		if err != nil || row.Values["project_id"] != project {
			return nil, ErrSourceOwnership
		}
		lineage[branch] = true
		branch = row.Values["parent_id"]
	}
	return lineage, nil
}

func (index *SourceIndex) validateRevisionPointers(row SourceRow, owner SourceOwner) error {
	branch := row.Values["branch_id"]
	if row.Table == "branches" {
		branch = row.Values["id"]
	}
	lineage, err := index.branchLineage(branch, owner.ProjectID)
	if err != nil {
		return err
	}
	columns := []string{"base_head"}
	if row.Table == "branches" {
		columns = []string{"base_revision", "head_revision"}
	}
	for _, column := range columns {
		id := row.Values[column]
		if id == "" {
			continue
		}
		revision, err := index.sourceRow("ip_revisions", map[string]string{"id": id})
		if err != nil || revision.Values["status"] != "PUBLISHED" || !lineage[revision.Values["branch_id"]] {
			return ErrSourceOwnership
		}
		if column == "base_revision" && (row.Values["parent_id"] == "" || revision.Values["branch_id"] == branch) {
			return ErrSourceOwnership
		}
		if column == "head_revision" && revision.Values["branch_id"] != branch && id != row.Values["base_revision"] {
			return ErrSourceOwnership
		}
	}
	return nil
}

func (index *SourceIndex) validateSemanticTarget(row SourceRow, owner SourceOwner, path map[string]bool) error {
	table := map[string]string{"ip_candidate": "ip_revisions", "artifact_release": "artifact_releases"}[row.Values["target_kind"]]
	if table == "" {
		return ErrSourceOwnership
	}
	target, err := index.single(table, row.Values["target_id"], path)
	if err != nil || target != owner {
		return ErrSourceOwnership
	}
	if row.Table == "semantic_review_acceptances" {
		review, err := index.sourceRow("semantic_review_runs", map[string]string{"id": row.Values["review_id"]})
		if err != nil || review.Values["project_id"] != owner.ProjectID {
			return ErrSourceOwnership
		}
		for _, field := range []string{"target_kind", "target_id", "target_hash", "report_hash"} {
			if review.Values[field] != row.Values[field] {
				return ErrSourceOwnership
			}
		}
	}
	return nil
}
