package retrieval

import (
	"strings"
	"unicode/utf8"
)

// The bounded editor keeps every page anchor, including pages whose text cannot
// fit. A full coverage report is not synonymous with complete text extraction.
func LimitImportUnit(existing []ImportUnit, next ImportUnit) ImportUnit {
	size := len(next.Text)
	for _, u := range existing {
		size += len(u.Text) + 2
	}
	if size > maxExtracted {
		next.Text = ""
		next.Status = "pending"
		next.Reason = documentReason("text_limit")
		next.Confidence = nil
	}
	return next
}

func CompleteImportReport(parser string, units []ImportUnit) (ImportReport, error) {
	report := ImportReport{Parser: parser, Units: units}
	if (parser != DocumentParser && parser != "bounded-native-v1") || len(units) == 0 || len(units) > 10000 {
		return report, ErrImport
	}
	seen := map[string]bool{}
	parts := []string{}
	size := 0
	for _, u := range units {
		if u.Locator == "" || len(u.Locator) > 512 || seen[u.Locator] || len(u.Kind) > 64 || !utf8.ValidString(u.Text) || len(u.Reason) > 2000 {
			return report, ErrImport
		}
		if u.Confidence != nil && (*u.Confidence < 0 || *u.Confidence > 1) {
			return report, ErrImport
		}
		seen[u.Locator] = true
		size += len(u.Text)
		if u.Status == "extracted" {
			if strings.TrimSpace(u.Text) == "" {
				return report, ErrImport
			}
			report.Extracted++
			parts = append(parts, u.Text)
		} else if u.Status == "pending" {
			report.Pending++
		} else {
			return report, ErrImport
		}
	}
	report.Text = strings.Join(parts, "\n\n")
	if size > maxExtracted || len(report.Text) > maxExtracted {
		return report, ErrImport
	}
	report.Complete = report.Pending == 0 && report.Extracted > 0
	return report, nil
}
