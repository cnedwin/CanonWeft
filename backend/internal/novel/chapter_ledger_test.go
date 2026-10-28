package novel

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestChapterLedgerEvidenceAndMeaning(t *testing.T) {
	text := "林舟想：师父一定还活着。门后只剩一盏灯。"
	output := LedgerOutput{Summary: "林舟推测师父尚在人世", Entries: []LedgerEntry{{Kind: "belief", Context: "reality", SubjectName: "林舟", Predicate: "推测", Statement: "林舟相信师父仍然活着", Quotes: []string{"林舟想：师父一定还活着。"}, Visibility: "author"}}, Notes: []string{}}
	raw, _ := json.Marshal(output)
	v, err := ParseLedgerOutput(raw, text, strings.Repeat("a", 32))
	if err != nil || len(v.Entries) != 1 || v.Entries[0].Kind != "belief" || v.Entries[0].ID == "" || len(v.Entries[0].Evidence) != 1 {
		t.Fatal("lost belief or source evidence", v, err)
	}
	anchor := v.Entries[0].Evidence[0]
	if string([]rune(text)[anchor.Start:anchor.End]) != output.Entries[0].Quotes[0] {
		t.Fatal("quote offsets are not Unicode positions")
	}
	again, _ := ParseLedgerOutput(raw, text, strings.Repeat("a", 32))
	if again.Entries[0].ID != v.Entries[0].ID {
		t.Fatal("candidate preview changed identity")
	}
	output.Entries[0].Quotes = []string{"师父已获救。"}
	raw, _ = json.Marshal(output)
	if _, err := ParseLedgerOutput(raw, text, strings.Repeat("a", 32)); err == nil {
		t.Fatal("invented source quote accepted")
	}
	output.Entries[0].Quotes = []string{"门后"}
	raw, _ = json.Marshal(output)
	if _, err := ParseLedgerOutput(raw, "门后有人。门后无人。", strings.Repeat("a", 32)); err == nil {
		t.Fatal("ambiguous evidence silently used first occurrence")
	}
}

func TestChapterLedgerCannotPromotePlansOrDreamsToFacts(t *testing.T) {
	for _, context := range []string{"plan", "dream", "hypothetical"} {
		v := LedgerOutput{Summary: "回忆", Entries: []LedgerEntry{{Kind: "fact", Context: context, Statement: "师父已回来", Quotes: []string{"他梦见师父回来。"}, Visibility: "public"}}, Notes: []string{}}
		raw, _ := json.Marshal(v)
		if _, err := ParseLedgerOutput(raw, "他梦见师父回来。", strings.Repeat("b", 32)); err == nil {
			t.Fatal("non-reality became fact", context)
		}
	}
	if _, err := ParseLedgerOutput([]byte(`{"summary":"无变化","entries":[],"notes":[],"publish":true}`), "正文", strings.Repeat("b", 32)); err == nil {
		t.Fatal("unknown authority field accepted")
	}
}
