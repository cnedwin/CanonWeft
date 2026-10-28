package retrieval

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestImportPlanEmptyDOCXProducesPendingCoverage(t *testing.T) {
	data := planDOCX(t, "", "")
	p, err := PlanImport(context.Background(), "empty.docx", data, "disabled")
	if err != nil || p.Count != 1 || p.Native == nil || p.Native.Complete || p.Native.Pending != 1 || p.Native.Units[0].Status != "pending" {
		t.Fatal("empty document would never finish", p, err)
	}
}

func TestImportPlanRejectsUnpersistableNativeCoverage(t *testing.T) {
	for _, name := range []string{"word/header" + strings.Repeat("a", 600) + ".xml", "word/header" + strings.Repeat("a", 440) + ".xml"} {
		data := planDOCX(t, name, strings.Repeat("<ins/>", 4000))
		if _, err := PlanImport(context.Background(), "annotations.docx", data, "disabled"); !errors.Is(err, ErrImport) {
			t.Fatal("invalid or oversized coverage would retry forever", len(name), err)
		}
	}
}

func planDOCX(t *testing.T, extraName, extraBody string) []byte {
	t.Helper()
	var data bytes.Buffer
	writer := zip.NewWriter(&data)
	files := map[string]string{"word/document.xml": "<document><body><p/></body></document>"}
	if extraName != "" {
		files[extraName] = "<document>" + extraBody + "</document>"
	}
	for name, body := range files {
		file, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = file.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

func TestImportPlanNativeInputRetainsCompleteCoverage(t *testing.T) {
	p, err := PlanImport(context.Background(), "世界.md", []byte("第一段🌙\n第二段"), "disabled")
	if err != nil || p.Native == nil || p.Count != 1 || p.Native.Text != "第一段🌙\n第二段" || !p.Native.Complete {
		t.Fatal(p, err)
	}
	if _, err := PlanImport(context.Background(), "method.exe", []byte("file"), "disabled"); !errors.Is(err, ErrImport) {
		t.Fatal("unsupported file accepted", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := PlanImport(ctx, "世界.md", []byte("正文"), "disabled"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestDocumentPlanAndUnitActualRuntime(t *testing.T) {
	if os.Getenv("STUDIO_TEST_INGEST") != "1" {
		t.Skip("requires local parser runtime")
	}
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "tools", "ingest", "tests", "fixtures", "mixed.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := PlanImport(context.Background(), "mixed.pdf", data, "enabled")
	if err != nil || plan.Native != nil || plan.Count != 2 {
		t.Fatal(plan, err)
	}
	second, err := ExtractImportUnit(context.Background(), "mixed.pdf", data, plan, 1, "enabled")
	if err != nil || second.Locator != "page:2" || second.Kind != "ocr" || second.Status != "extracted" || !strings.Contains(second.Text, "星河") {
		t.Fatal(second, err)
	}
	for _, index := range []int{-1, 2} {
		if _, err := ExtractImportUnit(context.Background(), "mixed.pdf", data, plan, index, "enabled"); !errors.Is(err, ErrImport) {
			t.Fatal("out of range processed", err)
		}
	}
	plan.Parser = "different-parser"
	if _, err := ExtractImportUnit(context.Background(), "mixed.pdf", data, plan, 0, "enabled"); !errors.Is(err, ErrImport) {
		t.Fatal("parser drift accepted", err)
	}
}
