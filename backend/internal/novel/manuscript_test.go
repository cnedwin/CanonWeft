package novel

import (
	"strings"
	"testing"
)

func TestManuscriptLayoutExactCoverageAndHeadings(t *testing.T) {
	text := "卷首文字🌙\r\n第一卷 雾城\r\n第一章 夜雨\r\n林舟到来。\r\n\r\n第2章 回信\n原文不改。\n# Chapter 3: Door\nEnd."
	parts, err := SplitManuscript(text)
	if err != nil || len(parts) != 4 {
		t.Fatal("chapter heading layout", len(parts), err)
	}
	if parts[1].Title != "第一章 夜雨" || parts[1].Volume != "第一卷 雾城" || parts[3].Title != "Chapter 3: Door" {
		t.Fatal("heading labels", parts)
	}
	if err = ValidateManuscriptParts(parts, len([]rune(text))); err != nil {
		t.Fatal(err)
	}
	var joined strings.Builder
	for _, p := range parts {
		joined.WriteString(string([]rune(text)[p.Start:p.End]))
	}
	if joined.String() != text {
		t.Fatal("source whitespace or Unicode changed")
	}
	bad := append([]ManuscriptPart(nil), parts...)
	bad[1].Start++
	if ValidateManuscriptParts(bad, len([]rune(text))) == nil {
		t.Fatal("source gap allowed")
	}
	bad = append([]ManuscriptPart(nil), parts...)
	bad[1].ID = bad[0].ID
	if ValidateManuscriptParts(bad, len([]rune(text))) == nil {
		t.Fatal("duplicate part identity")
	}
}
func TestManuscriptManyHeadingsBoundedBeforeIdentityWork(t *testing.T) {
	text := strings.Repeat("第1章\n"+strings.Repeat("x", 4000)+"\n", 2000)
	parts, err := SplitManuscript(text)
	if err != nil || len(parts) != 2000 {
		t.Fatal("valid maximum layout", err, len(parts))
	}
	if _, err = SplitManuscript(strings.Repeat("第1章\n", 2001)); err == nil {
		t.Fatal("excess heading layout accepted")
	}
}
