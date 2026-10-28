package retention

import (
	"time"

	"novelstudio/internal/domain"
)

// MaterialSource is a fixed schema dependency awaiting an exact journal pin.
// These direct links are deliberately PARTIAL: a manifest's complete content
// lineage must still be proved before an inventory can authorize expiration.
type MaterialSource struct {
	ReferenceID       string
	PayloadHash       string
	DraftVersion      *DraftVersionEvidence
	WorkingProjection *WorkingProjectionEvidence
	Revision          int64
	NotAfter          time.Time
	Unresolved        bool
	ResolvedAt        time.Time
}

func (index *SourceIndex) attachKnownMaterialSources(project string, refs []MaterialReference) error {
	for i := range refs {
		ref := &refs[i]
		if ref.ContentRoot {
			continue
		}
		row, ok := index.rows[ref.Table][ref.OwnerKey]
		if !ok {
			return ErrSourceOwnership
		}
		var parentTable, parentField, parentID, payload string
		switch ref.Table {
		case "platform_tasks":
			if ref.Field != "input_hash" {
				continue
			}
			candidate, err := index.sourceRow("ip_revisions", map[string]string{"id": row.Values["candidate_id"]})
			var snapshot domain.Snapshot
			if err != nil || candidate.Values["branch_id"] != row.Values["branch_id"] || candidate.Values["base_head"] != row.Values["base_head"] || candidate.Values["content_hash"] != ref.ObjectHash || contentJSON([]byte(candidate.Values["payload"]), &snapshot, ref.ObjectHash) != nil {
				return ErrSourceOwnership
			}
			parentTable, parentField, parentID = "ip_revisions", "payload", candidate.Values["id"]
			payload = candidate.Values["payload"]
		case "working_chunks":
			parentTable, parentField, parentID = "working_indexes", "manifest", row.Values["index_id"]
		case "knowledge_chunks":
			parentTable, parentField, parentID = "knowledge_indexes", "manifest", row.Values["index_id"]
		case "model_call_receipts":
			if ref.Field != "payload.output_hash" {
				continue
			}
			parentTable, parentField, parentID = "model_calls", "output_hash", row.Values["call_id"]
			payload = ref.ObjectHash
		default:
			continue
		}
		parent, err := index.sourceRow(parentTable, map[string]string{"id": parentID})
		if err != nil {
			return err
		}
		owner, err := index.Owner(parentTable, parent.Values)
		if err != nil || owner.Shared || owner.ProjectID != project {
			return ErrSourceOwnership
		}
		key, err := RowOwnerKey(parentTable, parent.Values)
		if err != nil {
			return err
		}
		id, err := ReferenceID(project, parentTable, key, parentField)
		if err != nil {
			return err
		}
		if parentField == "manifest" {
			payload = parent.Values[parentField]
		}
		ref.Sources = []MaterialSource{{ReferenceID: id, PayloadHash: financialDigest([]byte(payload))}}
	}
	return index.attachDiscoverySources(project, refs)
}
