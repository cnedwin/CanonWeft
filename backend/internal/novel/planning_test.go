package novel

import (
	"strings"
	"testing"
)

func TestPlanningRequiresRealStructureAndUniqueChapterIdentity(t *testing.T) {
	doc := PlanningDocument{Schema: 1, Kind: "volume", Scope: strings.Repeat("a", 32), SettingsRevision: 1, SettingsHash: strings.Repeat("b", 64), Title: "第一卷", Summary: "失踪事件与药师会", Mainline: "寻找师父", Chapters: []PlannedChapter{{ID: strings.Repeat("c", 32), Title: "雨夜", Summary: "发现线索", Perspective: "林舟", Goal: "查明踪迹", Events: "进入药铺", Change: "从旁观到参与", Transition: "线索指向旧城"}}}
	if err := doc.Validate(true); err != nil {
		t.Fatal(err)
	}
	doc.Chapters = append(doc.Chapters, doc.Chapters[0])
	if doc.Validate(true) == nil {
		t.Fatal("duplicate chapter identity accepted")
	}
	doc.Chapters = nil
	if doc.Validate(true) == nil {
		t.Fatal("empty volume accepted as planned")
	}
	if doc.Validate(false) != nil {
		t.Fatal("unfinished private draft rejected")
	}
}

func TestPlanningDetailRequiresSceneCausalityAndDoesNotAllowUnknownKind(t *testing.T) {
	doc := PlanningDocument{Schema: 1, Kind: "detail", Scope: strings.Repeat("a", 32), SettingsRevision: 1, SettingsHash: strings.Repeat("b", 64), Title: "雨夜细纲", Summary: "寻找师父", Scenes: []PlannedScene{{ID: strings.Repeat("c", 32), Time: "第一日夜", Place: "药铺", Characters: "林舟", Knowledge: "只知师父失踪", Goal: "找到足迹", Conflict: "药铺被封", Action: "绕到后门", Outcome: "找到纸条", Emotion: "疑惑", Foreshadow: "纸条暗号", Prerequisites: "先得知师父失踪", Hook: "纸条署名是自己", Words: 2000}}}
	if doc.Validate(true) != nil {
		t.Fatal("valid detail rejected")
	}
	doc.Scenes[0].Knowledge = ""
	if doc.Validate(true) == nil {
		t.Fatal("missing perspective knowledge accepted")
	}
	doc.Kind = "chapter"
	if doc.Validate(false) == nil {
		t.Fatal("unknown document kind accepted")
	}
}
