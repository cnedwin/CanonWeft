package service

import "novelstudio/internal/domain"

func applyStructuredContext(p *ContextPackage, snapshot domain.Snapshot, optionalBytes int) error {
	in := p.Input
	selected, err := domain.SelectStructured(snapshot, domain.StructuredRequest{
		Purpose: in.Purpose, Perspective: in.Perspective, Query: in.Query, StoryTime: in.StoryTime,
		Scope: in.Structured, OptionalBytes: optionalBytes,
	})
	if err != nil {
		return err
	}
	p.Facts, p.Hidden, p.Entities, p.Rules = selected.Facts, selected.Hidden, selected.Entities, selected.Rules
	p.Structured = &selected.Selection
	return nil
}

func hasOptionalStructure(p ContextPackage) bool {
	return p.Structured != nil && (p.Structured.SelectedAssertions > p.Structured.RequiredAssertions || p.Structured.SelectedEntities > p.Structured.RequiredEntities)
}
