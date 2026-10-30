package novel

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"novelstudio/internal/domain"
)

// ManuscriptPart uses Unicode code-point offsets in the immutable parsed source.
// Titles are metadata: the source range, including its heading, is kept verbatim.
type ManuscriptPart struct {
	ID     string            `json:"id"`
	Start  int               `json:"start"`
	End    int               `json:"end"`
	Title  string            `json:"title"`
	Volume string            `json:"volume"`
	Mode   string            `json:"mode"`
	Target *ManuscriptTarget `json:"target,omitempty"`
}
type ManuscriptTarget struct {
	ControlID string `json:"control_id"`
	Revision  int64  `json:"revision"`
	Hash      string `json:"hash"`
	BaseHead  string `json:"base_head"`
}

var manuscriptHeading = regexp.MustCompile(`(?i)^(?:第[零〇一二三四五六七八九十百千万两\d０-９]+[章回节]|chapter\s+[\divxlcdm]+)(?:\b|[\s:：、.．]|$)`)
var manuscriptChineseHeading = regexp.MustCompile(`^第[零〇一二三四五六七八九十百千万两\d０-９]+[章回节]`)
var manuscriptVolume = regexp.MustCompile(`^(?:第[零〇一二三四五六七八九十百千万两\d０-９]+卷|卷[零〇一二三四五六七八九十百千万两\d０-９]+)`)

func ManuscriptPartID(sourceHash string, start, end int) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("%s:%d:%d", sourceHash, start, end)))
	return hex.EncodeToString(h[:16])
}

func SplitManuscript(text string) ([]ManuscriptPart, error) {
	if !utf8.ValidString(text) || strings.TrimSpace(text) == "" || len(text) > 8<<20 {
		return nil, domain.ErrValidation
	}
	parts := []ManuscriptPart{}
	offset := 0
	volume := ""
	start := 0
	title := "卷首 / 未分章原稿"
	partVolume := ""
	for _, line := range strings.SplitAfter(text, "\n") {
		label := strings.TrimSpace(line)
		label = strings.TrimSpace(strings.TrimLeft(label, "#"))
		if utf8.RuneCountInString(label) <= 300 {
			if manuscriptVolume.MatchString(label) {
				volume = label
			}
			if manuscriptHeading.MatchString(label) || manuscriptChineseHeading.MatchString(label) {
				if offset > start {
					parts = append(parts, ManuscriptPart{Start: start, End: offset, Title: title, Volume: partVolume, Mode: "new"})
					if len(parts) >= 2000 {
						return nil, domain.ErrValidation
					}
				}
				start = offset
				title = label
				partVolume = volume
			}
		}
		offset += utf8.RuneCountInString(line)
	}
	if offset > start {
		parts = append(parts, ManuscriptPart{Start: start, End: offset, Title: title, Volume: partVolume, Mode: "new"})
	}
	sourceHash := sha256.Sum256([]byte(text))
	sourceKey := hex.EncodeToString(sourceHash[:])
	for i := range parts {
		parts[i].ID = ManuscriptPartID(sourceKey, parts[i].Start, parts[i].End)
	}
	return parts, ValidateManuscriptParts(parts, offset)
}

func ValidateManuscriptParts(parts []ManuscriptPart, total int) error {
	if len(parts) == 0 || len(parts) > 2000 || total < 1 {
		return domain.ErrValidation
	}
	ordered := append([]ManuscriptPart(nil), parts...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Start < ordered[j].Start })
	seen := map[string]bool{}
	end := 0
	for _, p := range ordered {
		if !planID.MatchString(p.ID) || seen[p.ID] || p.Start != end || p.End <= p.Start || p.End > total || !utf8.ValidString(p.Title) || strings.TrimSpace(p.Title) == "" || utf8.RuneCountInString(p.Title) > 300 || !utf8.ValidString(p.Volume) || utf8.RuneCountInString(p.Volume) > 120 {
			return domain.ErrValidation
		}
		if p.Mode != "new" && p.Mode != "skip" && p.Mode != "replace" {
			return domain.ErrValidation
		}
		if p.Mode == "replace" {
			if p.Target == nil || !planID.MatchString(p.Target.ControlID) || p.Target.Revision < 0 || !planHash.MatchString(p.Target.Hash) || (p.Target.BaseHead != "" && !planID.MatchString(p.Target.BaseHead)) {
				return domain.ErrValidation
			}
		} else if p.Target != nil {
			return domain.ErrValidation
		}
		seen[p.ID] = true
		end = p.End
	}
	if end != total {
		return domain.ErrValidation
	}
	return nil
}
