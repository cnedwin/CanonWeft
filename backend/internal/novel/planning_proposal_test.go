package novel

import (
	"strings"
	"testing"
)

func TestPlanningProposalPreservesAuthorityAndExistingIdentities(t *testing.T) {
	oldID := strings.Repeat("a", 32)
	doc := PlanningDocument{Schema: 1, Kind: "volume", Scope: strings.Repeat("b", 32), SettingsRevision: 1, SettingsHash: strings.Repeat("c", 64), Chapters: []PlannedChapter{{ID: oldID, Title: "旧章"}}}
	newID := func(string, int) string { return strings.Repeat("d", 32) }
	result, _, err := ApplyPlanningProposal(doc, []byte(`{"explanation":"保留首章并增加线索章","questions":[],"text":{"summary":"调查推进"},"chapters":[{"id":"`+oldID+`","title":"旧章修订"},{"id":"","title":"新章"}]}`), newID)
	if err != nil || result.Chapters[0].ID != oldID || result.Chapters[1].ID != newID("", 0) || result.SettingsHash != doc.SettingsHash || doc.Chapters[0].Title != "旧章" {
		t.Fatal(result, err)
	}
	for _, bad := range []string{
		`{"explanation":"越权","text":{"settings_hash":"changed"}}`,
		`{"explanation":"越权","text":{},"settings_revision":2}`,
		`{"explanation":"错误类型","text":{},"volumes":[]}`,
		`{"explanation":"冒用外部身份","text":{},"chapters":[{"id":"` + strings.Repeat("e", 32) + `","title":"其他卷的章"}]}`,
		`{"explanation":"重复身份","text":{},"chapters":[{"id":"` + oldID + `"},{"id":"` + oldID + `"}]}`,
	} {
		if _, _, err := ApplyPlanningProposal(doc, []byte(bad), newID); err == nil {
			t.Fatal("unsafe proposal accepted", bad)
		}
	}
}
