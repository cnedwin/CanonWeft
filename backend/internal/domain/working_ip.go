package domain

import (
	"encoding/json"
	"time"
)

// Shared working projections preserve existing JSON field order.
type MergeInput struct {
	ChangeReasons map[string]string `json:"change_reasons"`
	BaseHead      string            `json:"base_head"`
	UpstreamHead  string            `json:"upstream_head"`
	OperationID   string            `json:"operation_id"`
	Resolutions   map[string]string `json:"resolutions"`
}

type PromotionInput struct {
	ChangeReasons map[string]string  `json:"change_reasons"`
	SourceBranch  string             `json:"source_branch"`
	SourceHead    string             `json:"source_head"`
	BaseHead      string             `json:"base_head"`
	OperationID   string             `json:"operation_id"`
	Reason        string             `json:"reason"`
	Selection     PromotionSelection `json:"selection"`
}

type IPBranchActionDraft struct {
	Merge     *MergeInput     `json:"merge,omitempty"`
	Promotion *PromotionInput `json:"promotion,omitempty"`
}

type IPDraft struct {
	BranchActions   *IPBranchActionDraft `json:"branch_actions,omitempty"`
	ChangeReasons   map[string]string    `json:"change_reasons,omitempty"`
	Revision        int64                `json:"revision"`
	BaseHead        string               `json:"base_head"`
	Snapshot        Snapshot             `json:"snapshot"`
	Amendment       bool                 `json:"amendment"`
	AmendmentReason string               `json:"amendment_reason"`
}

type StoredIPDraft struct {
	BranchActions   *IPBranchActionDraft `json:"branch_actions,omitempty"`
	ChangeReasons   map[string]string    `json:"change_reasons,omitempty"`
	DraftSchema     int                  `json:"draft_schema"`
	Snapshot        Snapshot             `json:"snapshot"`
	Amendment       bool                 `json:"amendment"`
	AmendmentReason string               `json:"amendment_reason"`
}

type MaterialRetention struct {
	State     string    `json:"state"`
	ExpiredAt time.Time `json:"expired_at"`
}

type IPCandidate struct {
	Retention              *MaterialRetention `json:"retention,omitempty"`
	Delta                  *BranchDelta       `json:"delta,omitempty"`
	RequiresSemanticReview bool               `json:"requires_semantic_review"`
	ID                     string             `json:"id"`
	BranchID               string             `json:"branch_id"`
	BaseHead               string             `json:"base_head"`
	Hash                   string             `json:"hash"`
	Status                 string             `json:"status"`
	Snapshot               Snapshot           `json:"snapshot"`
	Checks                 []RuleResult       `json:"checks"`
	AmendmentReason        string             `json:"amendment_reason"`
	Promotion              *PromotionInput    `json:"promotion,omitempty"`
}

// DecodeIPDraft preserves the legacy and schema1/2 editor projection. Callers
// independently validate ownership, schema integrity and adoption eligibility.
func DecodeIPDraft(payload []byte, revision int64, base string) (IPDraft, error) {
	d := IPDraft{Revision: revision, BaseHead: base, Snapshot: Snapshot{Entities: []Entity{}, Assertions: []Assertion{}}}
	var stored StoredIPDraft
	err := json.Unmarshal(payload, &stored)
	if err == nil && (stored.DraftSchema == 1 || stored.DraftSchema == 2) {
		d.BranchActions, d.ChangeReasons = stored.BranchActions, stored.ChangeReasons
		d.Snapshot, d.Amendment, d.AmendmentReason = stored.Snapshot, stored.Amendment, stored.AmendmentReason
	} else if err == nil {
		err = json.Unmarshal(payload, &d.Snapshot)
	}
	return d, err
}
