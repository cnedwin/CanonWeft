package service

import (
	"encoding/json"
	"novelstudio/internal/domain"
	"novelstudio/internal/novel"
	"strings"
	"testing"
)

func TestNamingInputPinsRealMaterialsAndNames(t *testing.T) {
	all, err := nameMaterials("", "")
	if err != nil || len(all.Records) != 500 || all.License != "CC0-1.0" {
		t.Fatal("materials", err, len(all.Records))
	}
	zh, err := nameMaterials("zh", "")
	if err != nil || len(zh.Records) != 100 {
		t.Fatal("language", err, len(zh.Records))
	}
	d := novel.CharacterDesign{Notes: "朴素的边城守军名字", Genre: "武侠", IDs: []string{"hero"}, Candidate: domain.Snapshot{Entities: []domain.Entity{{ID: "hero", Name: "林舟", Aliases: []string{"守灯人"}}}}, Naming: &novel.NamingBoard{Count: 20, Language: "zh", NameProposal: novel.NameProposal{Candidates: []novel.NameCandidate{{ID: "kept", Name: "杨川", Origin: "invented", Confidence: "unverified", Locked: true}}}}}
	raw, err := namingInput(d, "保持朴素")
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	json.Unmarshal(raw, &value)
	if value["count"] != float64(19) || !strings.Contains(string(raw), "守灯人") || !strings.Contains(string(raw), "source_id") || len([]rune(value["materials"].(string))) > 4000 {
		t.Fatal("missing names/materials or wrong count", string(raw))
	}
}
