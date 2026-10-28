package retrieval

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
)

const DocumentParser = "pdfium-5.14.0+rapidocr-1.4.4-cpu-v2"

// Native results are inexpensive bounded extraction. Document plans contain no
// OCR text: each expensive page is processed and acknowledged independently.
type ImportPlan struct {
	Parser string        `json:"parser"`
	Count  int           `json:"count"`
	Native *ImportReport `json:"native,omitempty"`
}

func DocumentImport(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".pdf", ".png", ".jpg", ".jpeg", ".webp":
		return true
	}
	return false
}

func PlanImport(ctx context.Context, name string, data []byte, ocr string) (ImportPlan, error) {
	var plan ImportPlan
	if err := ctx.Err(); err != nil {
		return plan, err
	}
	if len(data) == 0 || len(data) > 32*1024*1024 || (ocr != "enabled" && ocr != "disabled") {
		return plan, ErrImport
	}
	if DocumentImport(name) {
		report, count, err := extractDocumentPart(ctx, name, data, "plan", 0, ocr)
		return ImportPlan{Parser: report.Parser, Count: count}, err
	}
	report, err := Extract(ctx, name, data)
	if err != nil {
		return plan, err
	}
	if len(report.Units) == 0 {
		report.Units = []ImportUnit{{Locator: "file:whole", Kind: "text", Status: "pending", Reason: "文档没有可读文本，请核对原文件"}}
	}
	// Validate before returning a resumable plan. A valid empty Word document or
	// a large annotation report must not become an endlessly retried checkpoint.
	report, err = CompleteImportReport(report.Parser, report.Units)
	if err != nil {
		return plan, ErrImport
	}
	raw, err := json.Marshal(report)
	if err != nil || len(raw) > 2*1024*1024 {
		return plan, ErrImport
	}
	checkpoint, err := json.Marshal(struct {
		Plan  ImportPlan   `json:"plan"`
		Units []ImportUnit `json:"units"`
	}{ImportPlan{Parser: report.Parser, Count: len(report.Units)}, report.Units})
	if err != nil || len(checkpoint) > 2*1024*1024 {
		return plan, ErrImport
	}
	return ImportPlan{Parser: report.Parser, Count: len(report.Units), Native: &report}, nil
}

func ExtractImportUnit(ctx context.Context, name string, data []byte, plan ImportPlan, index int, ocr string) (ImportUnit, error) {
	var unit ImportUnit
	if !DocumentImport(name) || plan.Native != nil || plan.Parser != DocumentParser || plan.Count < 1 || plan.Count > 100 || index < 0 || index >= plan.Count || len(data) == 0 || len(data) > 32*1024*1024 {
		return unit, ErrImport
	}
	report, _, err := extractDocumentPart(ctx, name, data, "unit", index, ocr)
	if err != nil {
		return unit, err
	}
	if len(report.Units) != 1 {
		return unit, ErrImport
	}
	return report.Units[0], nil
}
