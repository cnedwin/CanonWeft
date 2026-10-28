package retention

import (
	"context"
	"encoding/json"
	"strings"

	"novelstudio/internal/domain"
	"novelstudio/internal/novel"
)

// Rebuild review anchors from immutable release pins, not the current planning
// heads. This preserves provenance after edits and on backup restoration.
func (b *contextSourceBuilder) chapterReviewPlans(ctx context.Context, manifest domain.Manifest, doc *domain.SemanticReviewDocument, read ReadSourceObject) error {
	for _, ref := range manifest.Artifacts {
		resource, err := b.index.sourceRow("resources", map[string]string{"id": ref.ResourceID})
		if err != nil {
			return err
		}
		if resource.Values["kind"] != "novel_chapter_control" {
			continue
		}
		raw, err := read(ctx, b.project, ref.Hash)
		if err != nil || financialDigest(raw) != ref.Hash {
			return ErrSourceOwnership
		}
		chapter, err := novel.DecodeChapter(raw)
		if err != nil {
			return ErrSourceOwnership
		}
		for _, citation := range chapter.References {
			material, err := b.chapterResearchMaterial(ctx, citation.Source, read)
			if err != nil {
				return err
			}
			content, err := json.Marshal(struct {
				Reference novel.ChapterReference `json:"reference"`
				Material  domain.RequiredInput   `json:"material"`
			}{citation, material})
			if err != nil {
				return err
			}
			key := "chapter-research:" + ref.ResourceID + ":" + citation.Source.ID
			doc.RequiredCoverage = append(doc.RequiredCoverage, key)
			doc.Anchors = append(doc.Anchors, domain.SemanticReviewAnchor{Key: key, TargetKey: key, Text: string(content)})
		}
		add := func(pin domain.DependencyRef) (novel.PlanningDocument, error) {
			var plan novel.PlanningDocument
			r, err := b.index.sourceRow("resources", map[string]string{"id": pin.ID})
			if err != nil || r.Values["project_id"] != b.project || pin.BranchID != b.branch {
				return plan, ErrSourceOwnership
			}
			if !strings.HasPrefix(r.Values["kind"], "novel_plan_") {
				return plan, nil
			}
			release, err := b.index.sourceRow("artifact_releases", map[string]string{"id": pin.Revision})
			var parent domain.Manifest
			if err != nil || contentJSON([]byte(release.Values["manifest"]), &parent, release.Values["content_hash"]) != nil {
				return plan, ErrSourceOwnership
			}
			for _, artifact := range parent.Artifacts {
				if artifact.ResourceID != pin.ID {
					continue
				}
				body, err := read(ctx, b.project, artifact.Hash)
				if err != nil || json.Unmarshal(body, &plan) != nil {
					return plan, ErrSourceOwnership
				}
				input := domain.RequiredInput{Dependency: domain.Dependency{Kind: "production", Source: pin, Reason: "本章已确认的作者规划"}, Name: r.Values["name"], Space: "working", Hidden: true, Text: string(body)}
				if err = b.required(ctx, input, read); err != nil {
					return plan, err
				}
				key := "chapter-plan:" + pin.ID
				doc.RequiredCoverage = append(doc.RequiredCoverage, key)
				doc.Anchors = append(doc.Anchors, domain.SemanticReviewAnchor{Key: key, TargetKey: key, Text: string(body)})
				return plan, nil
			}
			return plan, ErrSourceOwnership
		}
		detail, err := add(chapter.Detail)
		if err != nil {
			return err
		}
		for _, pin := range detail.Inputs {
			if _, err = add(pin); err != nil {
				return err
			}
		}
	}
	return nil
}

func (b *contextSourceBuilder) chapterResearchMaterial(ctx context.Context, pin domain.DependencyRef, read ReadSourceObject) (domain.RequiredInput, error) {
	var material domain.RequiredInput
	r, err := b.index.sourceRow("resources", map[string]string{"id": pin.ID})
	if err != nil || pin.Kind != "resource" || pin.BranchID != b.branch || r.Values["project_id"] != b.project {
		return material, ErrSourceOwnership
	}
	release, err := b.index.sourceRow("artifact_releases", map[string]string{"id": pin.Revision})
	var manifest domain.Manifest
	if err != nil || contentJSON([]byte(release.Values["manifest"]), &manifest, release.Values["content_hash"]) != nil {
		return material, ErrSourceOwnership
	}
	for _, artifact := range manifest.Artifacts {
		if artifact.ResourceID != pin.ID {
			continue
		}
		body, err := read(ctx, b.project, artifact.Hash)
		if err != nil {
			return material, err
		}
		space := "evidence"
		if artifact.Policy != nil {
			space = artifact.Policy.Space
		}
		material = domain.RequiredInput{Dependency: domain.Dependency{Kind: "evidence", Source: pin, Reason: "本章明确采用的专业参考与出处"}, Name: r.Values["name"], Space: space, Hidden: space == "working", Text: string(body)}
		return material, b.required(ctx, material, read)
	}
	return material, ErrSourceOwnership
}
