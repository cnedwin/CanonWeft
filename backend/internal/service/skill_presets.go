package service

import (
	"context"
	"novelstudio/internal/domain"
	"novelstudio/internal/skills"
	"novelstudio/presets"
)

type SkillPreset struct {
	Name   string        `json:"name"`
	Report skills.Report `json:"report"`
	Source SkillSource   `json:"source"`
	Issue  string        `json:"issue,omitempty"`
}

func (s *Skills) Presets(ctx context.Context, a domain.Actor, project string) ([]SkillPreset, error) {
	tx, _, _, err := s.IP.begin(ctx, a, project, false)
	if err != nil {
		return nil, err
	}
	tx.Rollback()
	names, err := presets.Names()
	if err != nil {
		return nil, domain.ErrNotReady
	}
	result := []SkillPreset{}
	for _, name := range names {
		b, readErr := presets.Bundle(name)
		if readErr != nil {
			return nil, domain.ErrNotReady
		}
		report, readErr := skills.Analyze(b)
		if readErr != nil {
			return nil, domain.ErrNotReady
		}
		result = append(result, SkillPreset{Name: name, Report: report, Source: SkillSource{Kind: "builtin", Path: name}})
	}
	external, err := presets.ExternalCatalog()
	if err != nil {
		return nil, domain.ErrNotReady
	}
	for _, entry := range external {
		report := entry.Report
		if entry.Issue != "" {
			report = skills.Report{Name: entry.Name, Description: entry.Issue, Qualification: "STATIC_FAILED", Files: []skills.FileInfo{}}
		}
		result = append(result, SkillPreset{Name: entry.ID, Report: report, Source: externalSkillSource(entry.Source), Issue: entry.Issue})
	}
	return result, nil
}
func externalSkillSource(source skills.RemoteSource) SkillSource {
	return SkillSource{Kind: "github", Repository: source.Repository, Commit: source.Commit, Path: source.Path, URL: source.URL}
}
func (s *Skills) InstallPreset(ctx context.Context, a domain.Actor, project, name, operation string) (SkillVersion, error) {
	b, err := presets.Bundle(name)
	if err != nil {
		var source skills.RemoteSource
		b, source, err = presets.ExternalBundle(name)
		if err != nil {
			return SkillVersion{}, domain.ErrValidation
		}
		return s.save(ctx, a, project, operation, "", "", b, externalSkillSource(source))
	}
	return s.save(ctx, a, project, operation, "", "", b, SkillSource{Kind: "builtin", Path: name})
}
