package novel

import (
	"encoding/json"
	"strings"
	"testing"

	"novelstudio/internal/domain"
)

func TestChapterDraftContract(t *testing.T) {
	id := strings.Repeat("a", 32)
	hash := strings.Repeat("b", 64)
	d := ChapterDocument{Schema: 1, ChapterID: id, BodyID: id, Detail: domain.DependencyRef{Kind: "resource", ID: id, BranchID: id, Revision: id, Hash: hash}, SettingsRevision: 1, SettingsHash: hash, StoryTime: 1, Title: "雨夜", Text: ""}
	raw, _ := json.Marshal(d)
	if _, err := DecodeChapter(raw); err != nil {
		t.Fatal("empty private draft must be editable", err)
	}
	for _, input := range [][]byte{append(append([]byte{}, raw...), []byte("{}")...), []byte(strings.Replace(string(raw), `"schema":1`, `"schema":1,"publish":true`, 1))} {
		if _, err := DecodeChapter(input); err == nil {
			t.Fatal("accepted trailing or unknown authority fields")
		}
	}
	d.StoryTime = 9007199254740992
	if d.Validate() == nil {
		t.Fatal("accepted imprecise browser story position")
	}
}

func TestChapterCandidateNotesAndBoundaries(t *testing.T) {
	oversized, _ := json.Marshal(ChapterCandidate{Title: "雨夜", Text: "正文", Notes: []string{strings.Repeat("字", 2001)}})
	if _, err := ChapterOutput(oversized); err == nil {
		t.Fatal("accepted notes beyond output contract")
	}
	out, err := ChapterOutput([]byte(`{"title":"雨夜","text":"林舟推开了门。","notes":["本次只续写开场，尚未完成全章。"]}`))
	if err != nil || len(out.Notes) != 1 || out.Text != "林舟推开了门。" {
		t.Fatal("lost text or author-facing limitations", out, err)
	}
	for _, raw := range []string{
		`{"title":"雨夜","text":" ","notes":[]}`,
		`{"title":"雨夜","text":"正文"}`,
		`{"title":"雨夜","text":"正文","notes":[],"adopt":true}`,
		`{"title":"雨夜","text":"正文","notes":[]} {}`,
	} {
		if _, err := ChapterOutput([]byte(raw)); err == nil {
			t.Fatal("accepted malformed candidate", raw)
		}
	}
}
