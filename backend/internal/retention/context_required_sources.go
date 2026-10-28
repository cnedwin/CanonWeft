package retention

import (
	"context"
	"encoding/json"
	"unicode/utf8"

	"novelstudio/internal/domain"
	"novelstudio/internal/skills"
)

func (b *contextSourceBuilder) required(ctx context.Context, input domain.RequiredInput, read ReadSourceObject) error {
	ref := input.Dependency.Source
	if !id32.MatchString(ref.Revision) || !hash64.MatchString(ref.Hash) {
		return ErrSourceOwnership
	}
	switch ref.Kind {
	case "resource":
		resource, err := b.index.sourceRow("resources", map[string]string{"id": ref.ID})
		if err != nil || resource.Values["project_id"] != b.project || resource.Values["branch_id"] != ref.BranchID {
			return ErrSourceOwnership
		}
		release, err := b.index.sourceRow("artifact_releases", map[string]string{"id": ref.Revision})
		var manifest domain.Manifest
		if err != nil || release.Values["project_id"] != b.project || release.Values["branch_id"] != ref.BranchID || contentJSON([]byte(release.Values["manifest"]), &manifest, release.Values["content_hash"]) != nil {
			return ErrSourceOwnership
		}
		for _, artifact := range manifest.Artifacts {
			if artifact.ResourceID != ref.ID {
				continue
			}
			node := struct {
				Hash         string
				Policy       *domain.KnowledgePolicy
				Dependencies *domain.DependencyPolicy
			}{artifact.Hash, artifact.Policy, artifact.Dependencies}
			space := "evidence"
			if artifact.Policy != nil {
				space = artifact.Policy.Space
			}
			if stableHash(node) != ref.Hash || !utf8.ValidString(input.Text) || financialDigest([]byte(input.Text)) != artifact.Hash || int64(len(input.Text)) != artifact.Size || input.Space != space {
				return ErrSourceOwnership
			}
			field, err := ArtifactField(ref.ID, false)
			if err != nil {
				return err
			}
			if err := b.link(release, "manifest", ""); err != nil {
				return err
			}
			if err := b.link(resource, "name", financialDigest([]byte(input.Name))); err != nil {
				return err
			}
			return b.link(release, field, financialDigest([]byte(artifact.Hash)))
		}
		return ErrSourceOwnership
	case "skill":
		row, err := b.index.sourceRow("skill_packages", map[string]string{"id": ref.ID})
		if err != nil || row.Values["project_id"] != b.project || ref.Revision != ref.ID || ref.BranchID != "" || row.Values["bundle_hash"] != ref.Hash || input.Space != "methods" {
			return ErrSourceOwnership
		}
		data, err := read(ctx, b.project, ref.Hash)
		if err != nil {
			return err
		}
		var bundle skills.Bundle
		if contentJSON(data, &bundle, ref.Hash) != nil || bundle.Validate() != nil {
			return ErrSourceOwnership
		}
		text, err := skills.ReferenceText(bundle)
		if err != nil || text != input.Text {
			return ErrSourceOwnership
		}
		if err := b.link(row, "report", ""); err != nil {
			return err
		}
		var report skills.Report
		if authorDocument(row.Values["report"], &report) != nil || input.Name != report.Name {
			return ErrSourceOwnership
		}
		return b.link(row, "bundle_hash", "")
	default:
		snapshot, err := b.ip(ref.Revision, ref.BranchID)
		if err != nil {
			return err
		}
		var value any
		switch ref.Kind {
		case "entity":
			for _, v := range snapshot.Entities {
				if v.ID == ref.ID {
					value = v
				}
			}
		case "assertion":
			for _, v := range snapshot.Assertions {
				if v.ID == ref.ID {
					value = v
				}
			}
		case "rule":
			for _, v := range snapshot.Rules {
				if v.ID == ref.ID {
					value = v
				}
			}
		case "event":
			for _, v := range snapshot.Events {
				if v.ID == ref.ID {
					value = v
				}
			}
		case "mapping":
			for _, v := range snapshot.Mappings {
				if v.ID == ref.ID {
					value = v
				}
			}
		}
		if value == nil || stableHash(value) != ref.Hash || input.Space != "constraints" {
			return ErrSourceOwnership
		}
		raw, err := json.Marshal(value)
		if err != nil || string(raw) != input.Text {
			return ErrSourceOwnership
		}
		return nil
	}
}
