package backup

import (
	"encoding/json"

	"novelstudio/internal/retention"
)

// Read proof only from its independently checked journal and actual owner row.
// An envelope marker or a summary embedded in frozen JSON is never sufficient.
func readArchiveGenerationEvidence(business Data, history *archiveReferenceHistory) (map[string]retention.ExpiredProjection, error) {
	return readArchiveFrozenEvidence(business, history, "generation_tasks")
}

func readArchiveFrozenEvidence(business Data, history *archiveReferenceHistory, table string) (map[string]retention.ExpiredProjection, error) {
	if table != "generation_tasks" && table != "qualification_runs" && table != "recipe_runs" && table != "recipe_steps" {
		return nil, errExpiredArchiveEvidence
	}
	return readArchivePayloadEvidence(business, history, table, "frozen")
}

func readArchivePayloadEvidence(business Data, history *archiveReferenceHistory, table, field string) (map[string]retention.ExpiredProjection, error) {
	if !((field == "frozen" && (table == "generation_tasks" || table == "qualification_runs" || table == "recipe_runs" || table == "recipe_steps")) || (field == "payload" && table == "context_packages") || len(retention.TransientColumns(table+"."+field)) > 0) {
		return nil, errExpiredArchiveEvidence
	}
	if history == nil {
		return nil, errExpiredArchiveEvidence
	}
	owners, err := newRetentionArchiveOwners(business)
	if err != nil {
		return nil, err
	}
	result := map[string]retention.ExpiredProjection{}
	for key, row := range owners.rows[table] {
		owner, err := owners.field(table, key, field)
		if err != nil {
			return nil, err
		}
		var header struct {
			State string `json:"state"`
		}
		if json.Unmarshal(owner.Value, &header) != nil && len(retention.TransientColumns(table+"."+field)) == 0 {
			return nil, errExpiredArchiveEvidence
		}
		id, err := retention.ReferenceID(owner.ProjectID, owner.Table, key, field)
		if err != nil {
			return nil, err
		}
		v, exists := history.Current[id]
		if !exists || v.Reference.State != retention.Expired {
			if header.State == "EXPIRED" {
				return nil, errExpiredArchiveEvidence
			}
			continue
		}
		record, exists := history.Expiry[archiveVersionKey(id, v.Reference.Revision)]
		if !exists || v.Expiration == nil {
			return nil, errExpiredArchiveEvidence
		}
		ref := retention.StoredReference{ID: id, ProjectID: v.ProjectID, Table: v.Table, OwnerKey: v.OwnerKey, Field: v.Field, Storage: v.Storage,
			State: v.Reference.State, Revision: v.Reference.Revision, EvidenceRevision: v.Reference.Revision, PayloadHash: v.Reference.PayloadHash, ObjectHash: v.Reference.ObjectHash,
			ProjectionHash: v.Expiration.EvidenceHash, Deadline: v.Deadline, ExpiredAt: v.Reference.ExpiredAt, EvidenceExpiredAt: v.Reference.ExpiredAt}
		proof, err := ValidateExpiredSourceJournal(ref, owner, record)
		if err != nil {
			return nil, err
		}
		rowID := row.Values["id"]
		if table == "generation_previews" {
			rowID = row.Values["generation_id"]
		}
		if table == "qualification_approvals" {
			rowID = row.Values["run_id"]
		}
		if rowID == "" {
			// Private drafts have composite primary keys. Never merge multiple
			// authors' proofs under the empty id or a resource-only identifier.
			rowID = key
		}
		result[rowID] = proof
	}
	return result, nil
}

type recipeGenerationIdentity struct {
	run, step, input, baseHead, context, role, agent, manifest, configHash, priceHash string
}

func expiredRecipeGenerationMatches(proof retention.ExpiredProjection, want recipeGenerationIdentity) bool {
	if retention.ValidateProjectionForRule(proof, "generation_tasks.frozen") != nil {
		return false
	}
	binding := proof.Summary.Generation
	return binding != nil && binding.RecipeRunID == want.run && binding.RecipeStepID == want.step && binding.RecipeInputHash == want.input &&
		binding.BaseHead == want.baseHead && binding.ContextID == want.context && binding.Role == want.role && binding.AgentVersionID == want.agent &&
		binding.ManifestHash == want.manifest && binding.PriceHash == want.priceHash && proof.Summary.ConfigHash == want.configHash
}
