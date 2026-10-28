package novel

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestCharacterSourceSelectionBounds(t *testing.T) {
	raw := `{"schema":"character-design-v1","book_id":"11111111111111111111111111111111","ids":["p"],"base":{"entities":[],"assertions":[]},"candidate":{"entities":[{"id":"p","kind":"character","name":"甲"}],"assertions":[]},"source_ids":REPLACE}`
	cases := []struct {
		name, sources string
		valid         bool
	}{
		{"empty", `[]`, true},
		{"one", `["22222222222222222222222222222222"]`, true},
		{"duplicate", `["22222222222222222222222222222222","22222222222222222222222222222222"]`, false},
		{"invalid", `["not-a-resource"]`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var d CharacterDesign
			if e := json.Unmarshal([]byte(strings.Replace(raw, "REPLACE", tc.sources, 1)), &d); e != nil {
				t.Fatal(e)
			}
			if e := ValidateCharacterDesign(d); (e == nil) != tc.valid {
				t.Fatalf("valid=%v, error=%v", tc.valid, e)
			}
		})
	}
	var many []string
	for i := 0; i < 13; i++ {
		many = append(many, fmt.Sprintf("%032x", i+1))
	}
	encoded, _ := json.Marshal(many)
	var tooMany CharacterDesign
	_ = json.Unmarshal([]byte(strings.Replace(raw, "REPLACE", string(encoded), 1)), &tooMany)
	if ValidateCharacterDesign(tooMany) == nil {
		t.Fatal("more than twelve sources accepted")
	}
}
