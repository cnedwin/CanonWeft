package retrieval

import (
	"archive/zip"
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestManuscriptProfileAcceptsLongUTF8WithoutRelaxingMaterials(t *testing.T) {
	text := "第一章 雨夜\r\n" + strings.Repeat("保留原稿🌙。\r\n", 30000)
	raw := []byte("\ufeff" + text)
	if _, err := Extract(context.Background(), "原稿.txt", raw); err == nil {
		t.Fatal("ordinary material limit was relaxed")
	}
	report, err := ExtractManuscript(context.Background(), "原稿.txt", raw)
	if err != nil || !report.Complete || report.Text != text || report.Parser != ManuscriptParser {
		t.Fatal("long manuscript lost original text", err)
	}
	if _, err = ExtractManuscript(context.Background(), "bad.txt", []byte{255}); err == nil {
		t.Fatal("invalid UTF8 accepted")
	}
}
func TestManuscriptDOCXReusesCoverageAndStructuralBounds(t *testing.T) {
	var file bytes.Buffer
	z := zip.NewWriter(&file)
	w, err := z.Create("word/document.xml")
	if err != nil {
		t.Fatal(err)
	}
	paragraph := "<w:p><w:r><w:t>" + strings.Repeat("原稿", 25000) + "</w:t></w:r></w:p>"
	w.Write([]byte(`<w:document xmlns:w="urn:test"><w:body>` + strings.Repeat(paragraph, 3) + `</w:body></w:document>`))
	z.Close()
	report, err := ExtractManuscript(context.Background(), "long.docx", file.Bytes())
	if err != nil || !report.Complete || len(report.Text) <= maxExtracted || len(report.Units) != 3 {
		t.Fatal("long native Word extraction", err)
	}
	for _, unit := range report.Units {
		if unit.Text != "" {
			t.Fatal("long prose stored twice")
		}
	}
}
