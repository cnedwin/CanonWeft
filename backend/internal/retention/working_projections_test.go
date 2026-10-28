package retention

import (
	"encoding/json"
	"strings"
	"testing"

	"novelstudio/internal/domain"
)

func TestWorkingProjectionEvidenceTracksStateAndAuxiliaryBodies(t *testing.T) {
	rows, _ := contentRootFixture()
	id := strings.Repeat("7", 32)
	delta := domain.BranchDelta{Schema: 1, Operations: []domain.DeltaOperation{}}
	raw, _ := json.Marshal(delta)
	promotion := domain.PromotionInput{Reason: "作者的升格说明"}
	promoted, _ := json.Marshal(promotion)
	rows = append(rows,
		inventoryRow("ip_branch_deltas", map[string]string{"candidate_id": id, "project_id": strings.Repeat("2", 32), "branch_id": strings.Repeat("5", 32), "payload": string(raw), "delta_hash": financialDigest(raw)}),
		inventoryRow("ip_promotions", map[string]string{"candidate_id": id, "payload": string(promoted)}),
		inventoryRow("ip_amendments", map[string]string{"candidate_id": id, "reason": "作者的修订说明"}),
	)
	index, err := NewSourceIndex(rows)
	if err != nil {
		t.Fatal(err)
	}
	refs, err := index.MaterialReferences(strings.Repeat("2", 32))
	if err != nil {
		t.Fatal(err)
	}
	var proof *WorkingProjectionEvidence
	for _, m := range refs {
		if m.Table == "ip_revisions" && m.Field == "payload" {
			proof = m.WorkingProjection
		}
		if (m.Table == "ip_branch_deltas" || m.Table == "ip_promotions") && !m.ContentRoot {
			t.Fatal("adopted auxiliary body not preserved")
		}
	}
	if proof == nil || proof.Kind != "ip_candidate" || len(proof.Dependencies) != 3 {
		t.Fatal("missing projection dependencies", proof)
	}
	request := *proof
	request.State = "PREPARED"
	if matchesWorkingProjection(&request, proof) {
		t.Fatal("post-adoption proof matched old state")
	}
	request.State = ""
	if !matchesWorkingProjection(&request, proof) {
		t.Fatal("same-batch exact DTO digest failed")
	}
	request.Hash = strings.Repeat("f", 64)
	if matchesWorkingProjection(&request, proof) {
		t.Fatal("wrong full projection hash matched")
	}
	encoded, _ := json.Marshal(proof)
	if strings.Contains(string(encoded), "作者的") {
		t.Fatal("private source text copied into proof")
	}
}

func TestIPDraftProjectionRejectsAliasedStoredFieldsWithoutErasingDraft(t *testing.T) {
	rows := sourceOwnershipRows()
	stored := domain.StoredIPDraft{DraftSchema: 2, Snapshot: domain.Snapshot{Entities: []domain.Entity{}, Assertions: []domain.Assertion{}}, AmendmentReason: "未完成的修改"}
	raw, _ := json.Marshal(stored)
	rows = append(rows, inventoryRow("ip_drafts", map[string]string{"branch_id": strings.Repeat("5", 32), "actor_id": strings.Repeat("4", 32), "revision": "3", "payload": string(raw)}))
	index, err := NewSourceIndex(rows)
	if err != nil {
		t.Fatal(err)
	}
	refs, err := index.MaterialReferences(strings.Repeat("2", 32))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range refs {
		if m.Table == "ip_drafts" && m.Field == "payload" {
			found = m.WorkingProjection != nil && m.WorkingProjection.Version == "3" && m.ContentRoot
		}
	}
	if !found {
		t.Fatal("valid draft projection absent")
	}
	rows[len(rows)-1].Values["payload"] = strings.Replace(string(raw), `"amendment_reason"`, `"Amendment_Reason"`, 1)
	index, err = NewSourceIndex(rows)
	if err != nil {
		t.Fatal(err)
	}
	refs, err = index.MaterialReferences(strings.Repeat("2", 32))
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range refs {
		if m.Table == "ip_drafts" && m.Field == "payload" && (!m.ContentRoot || m.WorkingProjection != nil) {
			t.Fatal("invalid legacy draft erased or certified")
		}
	}
}
