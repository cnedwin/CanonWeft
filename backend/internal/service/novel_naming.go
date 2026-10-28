package service

import (
	"context"
	"encoding/json"
	"fmt"
	"novelstudio/internal/domain"
	"novelstudio/internal/novel"
	"novelstudio/presets"
	"slices"
	"strings"
)

func (s *NovelCharacters) PrepareNames(ctx context.Context, actor, bookID, id string, in NovelPlanningAssistInput) (Generation, error) {
	return s.prepareCharacter(ctx, actor, bookID, id, in, true)
}
func (s *NovelCharacters) NamingSuggestion(ctx context.Context, actor, bookID, id, task string) (CharacterDesignSuggestion, error) {
	return s.characterSuggestion(ctx, actor, bookID, id, task, true)
}
func (s *NovelCharacters) InstallNaming(ctx context.Context, actor, bookID string) (domain.CapabilityRef, error) {
	return s.installAgent(ctx, actor, bookID, "", "character-naming")
}

type NameMaterial struct {
	SourceImportID  string   `json:"source_import_id,omitempty"`
	ID              string   `json:"entity_id"`
	Label           string   `json:"label"`
	Language        string   `json:"label_language"`
	Kind            string   `json:"kind"`
	URL             string   `json:"source_url"`
	Row             int      `json:"source_row"`
	Pronunciation   string   `json:"pronunciation,omitempty"`
	Original        string   `json:"original,omitempty"`
	Transliteration string   `json:"transliteration,omitempty"`
	Culture         string   `json:"culture,omitempty"`
	Era             string   `json:"era,omitempty"`
	Tags            []string `json:"tags,omitempty"`
	License         string   `json:"license,omitempty"`
	CapturedAt      string   `json:"captured_at,omitempty"`
	Evidence        string   `json:"evidence,omitempty"`
	ReviewState     string   `json:"review_state,omitempty"`
}
type NameMaterials struct {
	CapturedAt  string         `json:"captured_at"`
	License     string         `json:"license"`
	Repository  string         `json:"source_repository"`
	Commit      string         `json:"source_commit"`
	Limitations []string       `json:"limitations"`
	Records     []NameMaterial `json:"records"`
}

func nameMaterials(language, query string) (NameMaterials, error) {
	var out NameMaterials
	if !slices.Contains([]string{"", "zh", "en", "ja", "fr", "de"}, language) || len([]rune(query)) > 120 {
		return out, domain.ErrValidation
	}
	bundle, err := presets.Bundle("character-naming")
	if err != nil {
		return out, err
	}
	if err = json.Unmarshal(bundle.Find("references/surname-seeds.json"), &out); err != nil {
		return out, err
	}
	for i := range out.Records {
		if out.Records[i].License == "" {
			out.Records[i].License = out.License
		}
	}
	out.Records = slices.DeleteFunc(out.Records, func(r NameMaterial) bool {
		return (language != "" && r.Language != language) || !strings.Contains(novel.NameKey(r.Label), novel.NameKey(query))
	})
	return out, nil
}
func (s *NovelCharacters) NameMaterials(ctx context.Context, actor, bookID, language, query string) (NameMaterials, error) {
	if len([]rune(language)) > 30 || len([]rune(query)) > 120 {
		return NameMaterials{}, domain.ErrValidation
	}
	catalog, err := s.NameCatalog(ctx, actor, bookID)
	return filterNameMaterials(catalog.Document, language, query), err
}
func namingSources(board novel.NamingBoard) ([]novel.NameSource, error) {
	materials, err := nameMaterials(board.Language, board.MaterialQuery)
	if err != nil {
		return nil, err
	}
	return namingMaterialSources(board, materials), nil
}
func namingMaterialSources(board novel.NamingBoard, materials NameMaterials) []novel.NameSource {
	materials = filterNameMaterials(materials, board.Language, board.MaterialQuery)
	slices.SortStableFunc(materials.Records, func(a, b NameMaterial) int {
		left, right := a.ReviewState == "author_edited", b.ReviewState == "author_edited"
		if left == right {
			return 0
		}
		if left {
			return -1
		}
		return 1
	})
	out := []novel.NameSource{}
	for _, r := range materials.Records {
		candidate := novel.NameSource{SourceID: r.ID + "-" + r.Language, URL: r.URL, Locator: fmt.Sprintf("wiki_names.csv.gz row %d; %s", r.Row, materials.Commit), Evidence: r.Label + "; surname; label_language=" + r.Language + "; culture/era unknown", CapturedAt: materials.CapturedAt}
		if r.Row == 0 || r.ReviewState == "author_edited" {
			candidate.Locator = "作者素材记录 " + r.ID
			candidate.Evidence = fmt.Sprintf("%s; kind=%s; original=%s; transliteration=%s; culture=%s; era=%s; license=%s; state=%s; %s", r.Label, r.Kind, r.Original, r.Transliteration, r.Culture, r.Era, r.License, r.ReviewState, r.Evidence)
			candidate.CapturedAt = r.CapturedAt
			if candidate.URL == "" {
				candidate.URL = "studio:name-material/" + r.ID
			}
		}
		next := append(slices.Clone(out), candidate)
		raw, _ := json.Marshal(next)
		if len([]rune(string(raw))) > 3400 {
			break
		}
		out = next
	}
	return out
}
func namingInput(d novel.CharacterDesign, notes string, supplied ...[]novel.NameSource) (json.RawMessage, error) {
	board := d.Naming
	if board == nil {
		return nil, domain.ErrValidation
	}
	if err := novel.ValidateNamingBoard(board); err != nil {
		return nil, err
	}
	count := board.Count
	for _, c := range board.Candidates {
		if c.Locked || c.Favorite {
			count--
		}
	}
	if count < 1 {
		return nil, fmt.Errorf("%w：候选已全部锁定或收藏，请增加目标数量或解除保留", domain.ErrValidation)
	}
	sources, err := namingSources(*board)
	if len(supplied) > 0 {
		sources = supplied[0]
		err = nil
	}
	if err != nil {
		return nil, err
	}
	raw, _ := json.Marshal(sources)
	names := []string{}
	roles := []string{}
	for _, e := range d.Candidate.Entities {
		names = append(names, e.Name)
		names = append(names, e.Aliases...)
		if slices.Contains(d.IDs, e.ID) {
			roles = append(roles, e.Name+"："+e.Description)
		}
	}
	if len(names) > 100 {
		names = names[:100]
	}
	constraints := slices.Clone(board.Constraints)
	if constraints == nil {
		constraints = []string{}
	}
	if board.NameKind != "" {
		constraints = append(constraints, "目标名字类型："+board.NameKind+"；不同部分分别填入parts，不擅自拼接原文与音译。")
	}
	culture, era, style := board.Culture, board.Era, d.Genre
	if strings.TrimSpace(culture) == "" {
		culture = "按作者要求推断；不确定性明确列出"
	}
	if strings.TrimSpace(era) == "" {
		era = "未指定；不编造历史典故"
	}
	if strings.TrimSpace(style) == "" {
		style = "按作者要求"
	}
	return json.Marshal(map[string]any{"prompt": d.Notes, "medium": "novel", "count": count, "style": style, "culture": culture, "era": era, "roles": roles, "existing_names": names, "constraints": constraints, "materials": "仅下列素材记录可作为sources；不代表已验证词源或热门排名，未复核单条页面。existing_names最多列100项，完整角色与锁定/收藏候选见必需工作草稿。\n" + string(raw), "feedback": notes})
}
