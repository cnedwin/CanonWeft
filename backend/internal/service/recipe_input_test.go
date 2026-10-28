package service

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRecipeTextInputPreservesLargeNumbersAndRejectsAmbiguity(t *testing.T) {
	text := `{"counter":9007199254740993,"ratio":0.12345678901234567890123456789}`
	want := text
	var raw json.RawMessage
	if err := recipeTextInput(&raw, &text); err != nil || string(raw) != want || text != "" {
		t.Fatal("input changed", string(raw), err)
	}
	for _, entry := range []struct{ raw, text string }{{`{"x":1}`, `{"x":2}`}, {"", `{"x":1,"x":2}`}, {"", `[1]`}, {"", `{"x":"\ud800"}`}, {"", strings.Repeat("x", 256*1024+1)}} {
		raw, text := json.RawMessage(entry.raw), entry.text
		if recipeTextInput(&raw, &text) == nil {
			t.Fatal("ambiguous or malformed text accepted")
		}
	}
}
