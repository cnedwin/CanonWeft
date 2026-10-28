package service

import (
	"strings"
	"testing"
)

func TestExtractionItemKeepsNonFactMeaning(t *testing.T) {
	for _, tc := range []struct {
		kind, context, want string
		control             bool
	}{
		{"fact", "reality", "fact", false},
		{"belief", "reality", "belief", false},
		{"claim", "reality", "claim", false},
		{"fact", "dream", "hypothesis", false},
		{"relation", "hypothetical", "hypothesis", false},
		{"state", "plan", "intent", true},
		{"foreshadow", "plan", "promise", true},
		{"theme_check", "reality", "summary", true},
	} {
		t.Run(tc.kind+"/"+tc.context, func(t *testing.T) {
			c := extractionCandidate{Kind: tc.kind, Context: tc.context, Statement: "人物记忆", Holder: "lin-1", Disposition: "conflict"}
			item := extractionItem(c, nil)
			if item.Kind != tc.want || item.Control != tc.control || item.State != "candidate" || item.Visibility != "author" || item.Pinned {
				t.Fatalf("authority or semantic widening: %+v", item)
			}
			if !strings.Contains(item.Text, "lin-1") || !strings.Contains(item.Text, "conflict") {
				t.Fatal("lost holder or review finding")
			}
		})
	}
	c := extractionCandidate{Kind: "fact", Context: "reality", Evidence: []extractionAnchor{{ResourceID: "plan"}}}
	if !extractionItem(c, map[string]extractionSource{"plan": {Kind: "planning"}}).Control {
		t.Fatal("planning source entered evidence")
	}
}
