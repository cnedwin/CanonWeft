package novel

import (
	"testing"
)

func TestResearchFactsRequireExactProvidedEvidence(t *testing.T) {
	source := ResearchSource{ID: "source-1", Revision: "revision-1", Hash: "hash", URL: "studio:resource/source-1", Locator: "revision-1", CapturedAt: "2026-10-27", Name: "原件"}
	doc := ResearchNotebook{Title: "港口考据", Question: "市舶流程", Genre: "历史", Era: "宋", Region: "泉州", ChapterNeed: "商船靠岸", Sources: []ResearchSource{source}, Cards: []ResearchFact{{ID: "card-1", Claim: "办理商舶事务", Status: "supported", EraScope: "宋", RegionScope: "待核", Limitations: "具体口岸须核对", WritingUse: "用于靠岸场景", ReviewAfter: "时代改变", Sources: []ResearchCitation{{SourceID: source.ID, URL: source.URL, Locator: source.Locator, Evidence: "办理商舶事务", CapturedAt: source.CapturedAt}}}}}
	if err := ValidateResearch(doc, map[string]string{source.ID: "原件：市舶司办理商舶事务。"}); err != nil {
		t.Fatal(err)
	}
	doc.Cards[0].Sources[0].Evidence = "当日免税放行"
	if err := ValidateResearch(doc, map[string]string{source.ID: "原件：市舶司办理商舶事务。"}); err == nil {
		t.Fatal("invented evidence accepted")
	}
	doc.Cards[0].Sources = nil
	if err := ValidateResearch(doc, nil); err == nil {
		t.Fatal("supported without evidence")
	}
	doc.Cards[0].Status = "unverified"
	if err := ValidateResearch(doc, nil); err != nil {
		t.Fatal("explicit uncertainty rejected", err)
	}
}
