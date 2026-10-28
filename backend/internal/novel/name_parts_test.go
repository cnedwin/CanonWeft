package novel

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestNamePartsSurviveCandidateEditing(t *testing.T) {
	var c NameCandidate
	if err := json.Unmarshal([]byte(`{"name":"林舟","parts":{"surname":"林","given_name":"舟","courtesy_name":"守砚","art_name":"听雨客","nickname":"小舟","title":"守灯人","original":"Lin Zhou","transliteration":"林舟"}}`), &c); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(c)
	if !strings.Contains(string(raw), "courtesy_name") || !strings.Contains(string(raw), "听雨客") {
		t.Fatal("independent name fields discarded")
	}
}
