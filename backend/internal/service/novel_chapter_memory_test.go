package service

import (
	"encoding/json"
	"strings"
	"testing"

	"novelstudio/internal/domain"
	"novelstudio/internal/memory"
	"novelstudio/internal/novel"
)

func TestChapterMemoryProjectionPreservesMeaningAndVisibility(t *testing.T) {
	bodyID, ledgerID, entryID := strings.Repeat("b", 32), strings.Repeat("c", 32), strings.Repeat("d", 32)
	d := novel.ChapterDocument{ChapterID: strings.Repeat("a", 32), BodyID: bodyID, Text: "他以为钥匙在井里。这只是他的猜测。", StoryTime: 9}
	envelope := chapterReleaseLedger{Schema: 1, Ledger: novel.ChapterLedger{Schema: 1, ChapterID: d.ChapterID, BodyID: bodyID, TextHash: digest(d.Text), LedgerOutput: novel.LedgerOutput{Summary: "人物推测", Entries: []novel.LedgerEntry{{ID: entryID, Kind: "belief", Context: "reality", HolderID: "hero", SubjectID: "hero", Predicate: "钥匙所在", Statement: "他以为钥匙在井里", StoryTime: 2, KnownFrom: 3, Visibility: "restricted", KnownBy: []string{"hero"}, Quotes: []string{"他以为钥匙在井里。"}}}, Notes: []string{}}}}
	raw, _ := json.Marshal(envelope)
	ledgerRef := domain.ArtifactRef{ResourceID: ledgerID, Hash: digest(string(raw)), Size: int64(len(raw))}
	r := domain.Release{ID: strings.Repeat("e", 32), Manifest: domain.Manifest{BranchID: strings.Repeat("f", 32), Artifacts: []domain.ArtifactRef{{ResourceID: bodyID, Hash: digest(d.Text)}, ledgerRef}}}
	items, err := chapterMemoryProjection("project", r, d, envelope, ledgerRef)
	if err != nil || len(items) != 1 {
		t.Fatal(items, err)
	}
	item := items[0]
	if item.Kind != "belief" || item.Visibility != "restricted" || item.From != 2 || item.RevealAt != 9 || item.Control || item.Pinned || len(item.Sources) != 2 || item.Sources[0].Start != 0 || item.Sources[0].End != 9 {
		t.Fatalf("meaning/source changed: %+v", item)
	}
	var meaning novel.LedgerEntry
	if json.Unmarshal([]byte(item.Text), &meaning) != nil || meaning.HolderID != "hero" || meaning.Context != "reality" {
		t.Fatal("lost holder/context")
	}
	if err = memory.ValidateItem(item); err != nil {
		t.Fatal(err)
	}
	scope := memory.Scope{Project: "project", Branch: r.Manifest.BranchID, Purpose: "writer", Perspective: "hero", StoryTime: 9, NarrativeTime: 9}
	if !memory.Applies(item, scope) {
		t.Fatal("informed character cannot use memory")
	}
	scope.Perspective = "outsider"
	if memory.Applies(item, scope) {
		t.Fatal("character knowledge leaked")
	}
	scope.Perspective, scope.NarrativeTime = "hero", 8
	if memory.Applies(item, scope) {
		t.Fatal("flashback knowledge appeared before chapter reveal")
	}
	again, err := chapterMemoryProjection("project", r, d, envelope, ledgerRef)
	if err != nil || again[0].ID != item.ID || again[0].Revision != item.Revision {
		t.Fatal("projection identity unstable", err)
	}
	envelope.Ledger.Entries[0].Quotes = []string{"并不存在的内容"}
	if _, err = chapterMemoryProjection("project", r, d, envelope, ledgerRef); err == nil {
		t.Fatal("invented evidence projected")
	}
}
