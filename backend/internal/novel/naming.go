package novel

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
	"novelstudio/internal/domain"
)

type NameSource struct {
	SourceID   string `json:"source_id"`
	URL        string `json:"url"`
	Locator    string `json:"locator"`
	Evidence   string `json:"evidence"`
	CapturedAt string `json:"captured_at"`
}
type NameCandidate struct {
	Parts         *NameParts   `json:"parts,omitempty"`
	ID            string       `json:"id,omitempty"`
	Name          string       `json:"name"`
	Pronunciation string       `json:"pronunciation"`
	Origin        string       `json:"origin"`
	Tags          []string     `json:"tags"`
	Rationale     string       `json:"rationale"`
	Sources       []NameSource `json:"sources"`
	Conflicts     []string     `json:"conflicts"`
	Confidence    string       `json:"confidence"`
	Locked        bool         `json:"locked,omitempty"`
	Favorite      bool         `json:"favorite,omitempty"`
}
type NameParts struct {
	Surname         string `json:"surname,omitempty"`
	GivenName       string `json:"given_name,omitempty"`
	CourtesyName    string `json:"courtesy_name,omitempty"`
	ArtName         string `json:"art_name,omitempty"`
	Nickname        string `json:"nickname,omitempty"`
	Title           string `json:"title,omitempty"`
	Original        string `json:"original,omitempty"`
	Transliteration string `json:"transliteration,omitempty"`
}
type NameProposal struct {
	Candidates []NameCandidate `json:"candidates"`
	Gaps       []string        `json:"gaps"`
	Checks     []string        `json:"checks"`
}
type NamingBoard struct {
	Catalog       *NameCatalogPin `json:"catalog,omitempty"`
	NameKind      string          `json:"name_kind,omitempty"`
	Count         int             `json:"count"`
	Culture       string          `json:"culture"`
	Era           string          `json:"era"`
	Constraints   []string        `json:"constraints"`
	Language      string          `json:"language"`
	MaterialQuery string          `json:"material_query"`
	GenerationID  string          `json:"generation_id,omitempty"`
	NameProposal
}
type NameCatalogPin struct {
	ResourceID string `json:"resource_id"`
	Revision   int64  `json:"revision"`
	Hash       string `json:"hash"`
}

// Exact normalization is deterministic; phonetic/traditional-Chinese ambiguity
// remains a separately disclosed model review, never an unsupported guarantee.
func NameKey(name string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return unicode.ToLower(r)
	}, norm.NFKC.String(name))
}
func ValidateNamingBoard(b *NamingBoard) error {
	if b == nil {
		return nil
	}
	if !slices.Contains([]string{"", "full_name", "surname", "given_name", "courtesy_name", "art_name", "nickname", "title"}, b.NameKind) {
		return domain.ErrValidation
	}
	if b.Count < 1 || b.Count > 100 || len(b.Candidates) > 100 || len(b.Constraints) > 100 || len([]rune(b.Culture)) > 4000 || len([]rune(b.Era)) > 4000 || len([]rune(b.MaterialQuery)) > 120 || !slices.Contains([]string{"", "zh", "en", "ja", "fr", "de"}, b.Language) {
		return domain.ErrValidation
	}
	ids := map[string]bool{}
	for _, c := range b.Candidates {
		if c.Parts != nil {
			for _, part := range []string{c.Parts.Surname, c.Parts.GivenName, c.Parts.CourtesyName, c.Parts.ArtName, c.Parts.Nickname, c.Parts.Title, c.Parts.Original, c.Parts.Transliteration} {
				if len([]rune(part)) > 200 {
					return domain.ErrValidation
				}
			}
		}
		if c.ID == "" || len(c.ID) > 128 || ids[c.ID] || strings.TrimSpace(c.Name) == "" || utf8.RuneCountInString(c.Name) > 120 || len([]rune(c.Pronunciation)) > 200 || !slices.Contains([]string{"invented", "collected", "adapted"}, c.Origin) || !slices.Contains([]string{"unverified", "supported"}, c.Confidence) {
			return domain.ErrValidation
		}
		ids[c.ID] = true
		if (c.Origin != "invented" || c.Confidence == "supported") && len(c.Sources) == 0 {
			return domain.ErrValidation
		}
	}
	raw, _ := json.Marshal(b)
	if len(raw) > 128*1024 {
		return fmt.Errorf("%w：起名候选超过128KB，请减少本批数量或说明长度", domain.ErrValidation)
	}
	return nil
}

func MergeNameProposal(b NamingBoard, p NameProposal, generation string, existing []domain.Entity, allowed []NameSource) (NamingBoard, error) {
	if len(p.Candidates) > b.Count || len(p.Gaps) > 100 || len(p.Checks) > 100 {
		return b, domain.ErrValidation
	}
	out := b
	out.Candidates = []NameCandidate{}
	seen := map[string]bool{}
	for _, c := range b.Candidates {
		if c.Locked || c.Favorite {
			out.Candidates = append(out.Candidates, c)
			seen[NameKey(c.Name)] = true
		}
	}
	if len(out.Candidates) > b.Count {
		return b, domain.ErrValidation
	}
	for i, c := range p.Candidates {
		for _, source := range c.Sources {
			if !slices.Contains(allowed, source) {
				return b, fmt.Errorf("%w：候选引用了未提供的名字来源", domain.ErrValidation)
			}
		}
		if len(c.Sources) == 0 && (c.Origin != "invented" || c.Confidence != "unverified") {
			return b, domain.ErrValidation
		}
		key := NameKey(c.Name)
		if seen[key] {
			continue
		}
		if len(out.Candidates) >= b.Count {
			break
		}
		c.ID = CharacterDesignID(generation, "name", fmt.Sprint(i))
		c.Locked = false
		c.Favorite = false
		for _, e := range existing {
			for _, name := range append([]string{e.Name}, e.Aliases...) {
				if NameKey(name) == key {
					c.Conflicts = append(c.Conflicts, "与现有角色姓名/别名重合："+e.Name)
					break
				}
			}
		}
		seen[key] = true
		out.Candidates = append(out.Candidates, c)
	}
	out.GenerationID = generation
	out.Gaps = slices.Clone(p.Gaps)
	out.Checks = slices.Clone(p.Checks)
	if len(out.Candidates) < b.Count {
		out.Gaps = append(out.Gaps, fmt.Sprintf("去重后共%d个候选，目标%d个；可补充条件再生成。", len(out.Candidates), b.Count))
	}
	out.Checks = append(out.Checks, "程序已检查本批及当前角色姓名/别名的Unicode规范化、空白和大小写重名；简繁、近音与跨作品重名仍需人工核对。")
	return out, ValidateNamingBoard(&out)
}
