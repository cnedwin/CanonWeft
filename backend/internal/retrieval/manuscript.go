package retrieval

import (
	"bytes"
	"context"
	"encoding/json"
	"path"
	"strings"
	"unicode/utf8"
)

const ManuscriptParser = "manuscript-native-v1"
const ManuscriptMaxText = 8 << 20
const ManuscriptMaxReport = 14 << 20

func ValidateManuscriptReport(report ImportReport) error {
	if report.Parser != ManuscriptParser || report.Research != nil || report.NovelEdition != nil || report.NovelImage != nil || report.CharacterImage != nil || len(report.Text) > ManuscriptMaxText || !utf8.ValidString(report.Text) || len(report.Units) == 0 || len(report.Units) > 10000 {
		return ErrImport
	}
	extracted, pending := 0, 0
	for _, u := range report.Units {
		if len(u.Locator) > 4096 || len(u.Kind) > 100 || len(u.Reason) > 8192 || len(u.Text) > maxExtracted || !utf8.ValidString(u.Text) {
			return ErrImport
		}
		switch u.Status {
		case "extracted":
			extracted++
		case "pending":
			pending++
		default:
			return ErrImport
		}
	}
	if extracted != report.Extracted || pending != report.Pending || report.Complete != (pending == 0 && extracted > 0) {
		return ErrImport
	}
	raw, err := json.Marshal(report)
	if err != nil || len(raw) > ManuscriptMaxReport {
		return ErrImport
	}
	return nil
}

// ExtractManuscript reuses the bounded material parsers while giving native
// long-form text its own profile. Ordinary material/OCR limits do not change.
func ExtractManuscript(ctx context.Context, name string, data []byte) (ImportReport, error) {
	report := ImportReport{Parser: ManuscriptParser, Units: []ImportUnit{}}
	if ctx.Err() != nil {
		return report, ctx.Err()
	}
	if len(data) == 0 || len(data) > 32<<20 {
		return report, ErrImport
	}
	switch strings.ToLower(path.Ext(name)) {
	case ".txt", ".md":
		if !utf8.Valid(data) || bytes.ContainsRune(data, 0) {
			return report, ErrImport
		}
		text := strings.TrimPrefix(string(data), "\ufeff")
		if len(text) > ManuscriptMaxText || strings.TrimSpace(text) == "" {
			return report, ErrImport
		}
		report.Units = append(report.Units, ImportUnit{Locator: "file:whole", Kind: "text", Status: "extracted", Text: text})
	case ".docx":
		if err := extractDOCX(ctx, data, &report); err != nil {
			return report, err
		}
	default:
		v, err := Extract(ctx, name, data)
		if err != nil {
			return report, err
		}
		v.Parser = ManuscriptParser
		return v, nil
	}
	texts := []string{}
	size := 0
	for _, unit := range report.Units {
		if unit.Status == "extracted" {
			size += len(unit.Text) + 2
			if size > ManuscriptMaxText+2 {
				return report, ErrImport
			}
			report.Extracted++
			texts = append(texts, unit.Text)
		} else {
			report.Pending++
		}
	}
	report.Text = strings.Join(texts, "\n\n")
	// Long prose is stored once. Unit locators/status remain as coverage evidence;
	// the immutable original and joined parsed text are the retained source.
	for i := range report.Units {
		report.Units[i].Text = ""
	}
	report.Complete = report.Pending == 0 && report.Extracted > 0
	return report, nil
}
