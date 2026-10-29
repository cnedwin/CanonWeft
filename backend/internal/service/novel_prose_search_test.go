package service

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestNovelProseSearchLiteralUnicodeAndLimits(t *testing.T) {
	for _, tt := range []struct {
		text, query string
		starts      []int
	}{{"🙂甲Aa甲aa甲", "甲", []int{1, 4, 7}}, {"aaaaa", "aa", []int{0, 2}}, {"Aa aa", "aa", []int{3}}, {"x[.*]y", "[.*]", []int{1}}, {"e\u0301", "é", nil}, {"<b>甲</b>", "<b>", []int{0}}} {
		matches, total := proseSearchMatches(tt.text, tt.query)
		if total != len(tt.starts) || len(matches) != total {
			t.Fatalf("literal matches %q: got %d, want %d", tt.query, total, len(tt.starts))
		}
		runes := []rune(tt.text)
		for i, start := range tt.starts {
			m := matches[i]
			if m.Start != start || m.End != start+utf8.RuneCountInString(tt.query) || m.Quote != tt.query || string(runes[m.Start:m.End]) != tt.query {
				t.Fatalf("codepoint positions changed: %+v", m)
			}
		}
	}
	matches, total := proseSearchMatches(strings.Repeat("🙂", 80), "🙂")
	if total != 80 || len(matches) != 50 || matches[49].Start != 49 || utf8.RuneCountInString(matches[40].Before) != 32 || utf8.RuneCountInString(matches[0].After) != 32 {
		t.Fatal("bounded summaries lost complete count or codepoint context", total, len(matches))
	}
}

func TestNovelProseSearchRejectsInvalidInput(t *testing.T) {
	for _, query := range []string{"", " \r\n\t", strings.Repeat("🙂", 121), string([]byte{0xff})} {
		if validateProseSearchInput(NovelProseSearchInput{Source: "working", Query: query}) == nil {
			t.Fatal("invalid query accepted")
		}
	}
	for _, in := range []NovelProseSearchInput{{Source: "other", Query: "甲"}, {Source: "working", Query: "甲", After: "bad"}} {
		if validateProseSearchInput(in) == nil {
			t.Fatal("invalid source/cursor accepted")
		}
	}
	if validateProseSearchInput(NovelProseSearchInput{Source: "published", Query: " 甲 "}) != nil || validateProseSearchInput(NovelProseSearchInput{Source: "working", Query: strings.Repeat("🙂", 120)}) != nil {
		t.Fatal("valid literal query rejected or trimmed")
	}
}
