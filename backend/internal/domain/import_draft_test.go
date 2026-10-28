package domain

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestImportDraftRejectsInvalidKnowledgeUse(t *testing.T) {
	for _, policy := range []string{
		`{"space":"working","visibility":"public","reveal_at":0}`,
		`{"space":"unknown","visibility":"author","reveal_at":0}`,
	} {
		var draft ImportDraftContent
		if err := json.Unmarshal([]byte(`{"text":"未来剧情","operation_id":"import-use","policy":`+policy+`}`), &draft); err != nil {
			t.Fatal(err)
		}
		if draft.Validate() == nil {
			t.Fatal("invalid knowledge use accepted", policy)
		}
	}
}

func TestImportDraftLegacyKnowledgeUseIsNotRewritten(t *testing.T) {
	var draft ImportDraftContent
	if err := json.Unmarshal([]byte(`{"text":"旧稿","operation_id":"legacy-import"}`), &draft); err != nil {
		t.Fatal(err)
	}
	if draft.Validate() != nil || draft.Policy != nil {
		t.Fatal("legacy policy changed")
	}
	raw, err := json.Marshal(draft)
	if err != nil || strings.Contains(string(raw), `"policy"`) {
		t.Fatal("legacy serialization gained policy", err)
	}
}
