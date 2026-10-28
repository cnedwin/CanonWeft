package novel

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"novelstudio/internal/domain"
)

// A proposal contains author-visible edits only. Versions, provenance and
// adoption are owned by the application, never supplied by the model.
type PlanningProposal struct {
	Explanation string            `json:"explanation"`
	Questions   []string          `json:"questions"`
	Text        map[string]string `json:"text"`
	Volumes     *[]PlannedVolume  `json:"volumes,omitempty"`
	Chapters    *[]PlannedChapter `json:"chapters,omitempty"`
	Scenes      *[]PlannedScene   `json:"scenes,omitempty"`
}

func ApplyPlanningProposal(original PlanningDocument, raw []byte, newID func(string, int) string) (PlanningDocument, PlanningProposal, error) {
	var p PlanningProposal
	if len(raw) > 256*1024 {
		return original, p, domain.ErrValidation
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&p) != nil || d.Decode(&struct{}{}) != io.EOF || strings.TrimSpace(p.Explanation) == "" || len(p.Questions) > 50 {
		return original, p, domain.ErrValidation
	}
	result := original
	fields := map[string]*string{"title": &result.Title, "summary": &result.Summary, "open_questions": &result.OpenQuestions}
	if original.Kind == "world" || original.Kind == "book" || original.Kind == "volume" {
		fields["mainline"] = &result.Mainline
	}
	if original.Kind == "world" || original.Kind == "book" {
		fields["theme"] = &result.Theme
		fields["ending"] = &result.Ending
	}
	if original.Kind == "world" {
		for k, v := range map[string]*string{"world_rules": &result.WorldRules, "costs": &result.Costs, "boundaries": &result.Boundaries, "setting": &result.Setting, "factions": &result.Factions, "characters": &result.Characters, "arcs": &result.Arcs, "turning_points": &result.TurningPoints} {
			fields[k] = v
		}
	}
	for field, value := range p.Text {
		ptr := fields[field]
		if ptr == nil {
			return original, p, fmt.Errorf("%w：规划助手不能修改此字段 %s", domain.ErrValidation, field)
		}
		*ptr = value
	}
	if (p.Volumes != nil && original.Kind != "book") || (p.Chapters != nil && original.Kind != "volume") || (p.Scenes != nil && original.Kind != "detail") {
		return original, p, domain.ErrValidation
	}
	// Existing identities can be retained or explicitly removed in the preview.
	// New entries receive deterministic server IDs, so preview retries agree.
	known := map[string]bool{}
	for _, v := range original.Volumes {
		known[v.ID] = true
	}
	for _, v := range original.Chapters {
		known[v.ID] = true
	}
	for _, v := range original.Scenes {
		known[v.ID] = true
	}
	assign := func(id *string, kind string, index int) error {
		if *id == "" {
			*id = newID(kind, index)
			return nil
		}
		if !known[*id] {
			return domain.ErrValidation
		}
		return nil
	}
	if p.Volumes != nil {
		result.Volumes = append([]PlannedVolume{}, (*p.Volumes)...)
		for i := range result.Volumes {
			if err := assign(&result.Volumes[i].ID, "volume", i); err != nil {
				return original, p, err
			}
		}
	}
	if p.Chapters != nil {
		result.Chapters = append([]PlannedChapter{}, (*p.Chapters)...)
		for i := range result.Chapters {
			if err := assign(&result.Chapters[i].ID, "chapter", i); err != nil {
				return original, p, err
			}
		}
	}
	if p.Scenes != nil {
		result.Scenes = append([]PlannedScene{}, (*p.Scenes)...)
		for i := range result.Scenes {
			if err := assign(&result.Scenes[i].ID, "scene", i); err != nil {
				return original, p, err
			}
		}
	}
	if err := result.Validate(false); err != nil {
		return original, p, err
	}
	return result, p, nil
}
