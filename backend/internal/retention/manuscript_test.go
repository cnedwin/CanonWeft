package retention

import (
	"encoding/json"
	"novelstudio/internal/retrieval"
	"strings"
	"testing"
)

func TestManuscriptReportDedicatedBoundKeepsOrdinaryLimit(t *testing.T) {
	report := retrieval.ImportReport{Parser: retrieval.ManuscriptParser, Complete: true, Extracted: 1, Text: strings.Repeat("长篇🌙", 350000), Units: []retrieval.ImportUnit{{Locator: "file:whole", Kind: "text", Status: "extracted"}}}
	raw, _ := json.Marshal(report)
	var decoded retrieval.ImportReport
	if len(raw) <= 2<<20 {
		t.Fatal("large report fixture too small")
	}
	if memoryJSON(string(raw), &decoded) == nil {
		t.Fatal("ordinary memory JSON limit was relaxed")
	}
	if err := sourceImportReportDocument(string(raw), &decoded); err != nil || decoded.Text != report.Text {
		t.Fatal("dedicated original report", err)
	}
	report.Parser = "bounded-native-v1"
	raw, _ = json.Marshal(report)
	if sourceImportReportDocument(string(raw), &decoded) == nil {
		t.Fatal("non-manuscript report obtained larger allowance")
	}
	report.Parser = retrieval.ManuscriptParser
	report.Extracted = 2
	raw, _ = json.Marshal(report)
	if sourceImportReportDocument(string(raw), &decoded) == nil {
		t.Fatal("coverage counters inconsistent")
	}
	report.Extracted = 1
	raw, _ = json.Marshal(report)
	duplicate := `{"parser":"other",` + string(raw[1:])
	if sourceImportReportDocument(duplicate, &decoded) == nil {
		t.Fatal("duplicate keys accepted")
	}
}
