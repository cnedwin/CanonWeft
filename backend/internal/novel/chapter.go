package novel

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"unicode/utf8"

	"novelstudio/internal/domain"
)

type ChapterDocument struct {
	References         []ChapterReference   `json:"references,omitempty"`
	Schema             int                  `json:"schema"`
	ChapterID          string               `json:"chapter_id"`
	BodyID             string               `json:"body_id"`
	Detail             domain.DependencyRef `json:"detail"`
	SettingsRevision   int64                `json:"settings_revision"`
	SettingsHash       string               `json:"settings_hash"`
	IPHead             string               `json:"ip_head"`
	StoryTime          int64                `json:"story_time"`
	Perspective        string               `json:"perspective"`
	Title              string               `json:"title"`
	Text               string               `json:"text"`
	WriterGenerationID string               `json:"writer_generation_id,omitempty"`
}

func (d ChapterDocument) Validate() error {
	if len(d.References) > 12 {
		return domain.ErrValidation
	}
	seen := map[string]bool{}
	for _, ref := range d.References {
		if ref.Source.Kind != "resource" || !planID.MatchString(ref.Source.ID) || !planID.MatchString(ref.Source.BranchID) || !planID.MatchString(ref.Source.Revision) || !planHash.MatchString(ref.Source.Hash) || seen[ref.Source.ID] {
			return domain.ErrValidation
		}
		seen[ref.Source.ID] = true
		for _, value := range []string{ref.Quote, ref.Use, ref.Era, ref.Region, ref.Limitations} {
			if !utf8.ValidString(value) || utf8.RuneCountInString(value) > 2000 {
				return domain.ErrValidation
			}
		}
	}
	if d.Schema != 1 || !planID.MatchString(d.ChapterID) || !planID.MatchString(d.BodyID) || d.Detail.Kind != "resource" || !planID.MatchString(d.Detail.ID) || !planID.MatchString(d.Detail.BranchID) || !planID.MatchString(d.Detail.Revision) || !planHash.MatchString(d.Detail.Hash) || d.SettingsRevision < 1 || !planHash.MatchString(d.SettingsHash) || (d.IPHead != "" && !planID.MatchString(d.IPHead)) || d.StoryTime < 0 || d.StoryTime > 9007199254740991 || len(d.Perspective) > 128 || (d.WriterGenerationID != "" && !planID.MatchString(d.WriterGenerationID)) || utf8.RuneCountInString(d.Title) > 300 || utf8.RuneCountInString(d.Text) > 100000 {
		return domain.ErrValidation
	}
	return nil
}

type ChapterReference struct {
	Source      domain.DependencyRef `json:"source"`
	Quote       string               `json:"quote"`
	Use         string               `json:"use"`
	Era         string               `json:"era"`
	Region      string               `json:"region"`
	Limitations string               `json:"limitations"`
}

func ChapterDependencies(d ChapterDocument) domain.DependencyPolicy {
	deps := domain.DependencyPolicy{Inputs: []domain.Dependency{{Kind: "production", Source: d.Detail, Reason: "本章已确认细纲"}}}
	for _, ref := range d.References {
		deps.Inputs = append(deps.Inputs, domain.Dependency{Kind: "evidence", Source: ref.Source, Reason: "本章明确采用的专业参考与出处"})
	}
	return deps
}

func DecodeChapter(raw []byte) (ChapterDocument, error) {
	var d ChapterDocument
	if len(raw) > 512*1024 {
		return d, domain.ErrValidation
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&d) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return d, domain.ErrValidation
	}
	return d, d.Validate()
}

type ChapterCandidate struct {
	Title string   `json:"title"`
	Text  string   `json:"text"`
	Notes []string `json:"notes"`
}

func ChapterOutput(raw []byte) (ChapterCandidate, error) {
	var out ChapterCandidate
	if len(raw) > 512*1024 {
		return out, domain.ErrValidation
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&out) != nil || d.Decode(&struct{}{}) != io.EOF || strings.TrimSpace(out.Text) == "" || utf8.RuneCountInString(out.Text) > 100000 || utf8.RuneCountInString(out.Title) > 300 || out.Notes == nil || len(out.Notes) > 50 {
		return out, domain.ErrValidation
	}
	for _, note := range out.Notes {
		if utf8.RuneCountInString(note) > 2000 {
			return out, domain.ErrValidation
		}
	}
	return out, nil
}
