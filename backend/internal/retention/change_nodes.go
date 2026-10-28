package retention

import (
	"context"
	"encoding/json"
	"slices"
	"strings"

	"novelstudio/internal/domain"
	"novelstudio/internal/skills"
)

type changeNode struct {
	name     string
	resource SourceRow
	artifact domain.ArtifactRef
	text     string
}

type changeSourceGraph struct {
	b      *contextSourceBuilder
	read   ReadSourceObject
	nodes  map[domain.DependencyRef]changeNode
	values map[string]map[string]any
}

func changeResourceHash(a domain.ArtifactRef) string {
	return stableHash(struct {
		Hash         string
		Policy       *domain.KnowledgePolicy
		Dependencies *domain.DependencyPolicy
	}{a.Hash, a.Policy, a.Dependencies})
}

func (g *changeSourceGraph) manifest(id string) (SourceRow, domain.Manifest, error) {
	row, err := g.b.index.sourceRow("artifact_releases", map[string]string{"id": id})
	var manifest domain.Manifest
	if err != nil || row.Values["project_id"] != g.b.project || contentJSON([]byte(row.Values["manifest"]), &manifest, row.Values["content_hash"]) != nil || row.Values["branch_id"] != manifest.BranchID {
		return row, manifest, ErrSourceOwnership
	}
	return row, manifest, g.b.link(row, "manifest", "")
}

func (g *changeSourceGraph) artifact(id, resource string) (domain.ArtifactRef, string, error) {
	_, manifest, err := g.manifest(id)
	if err != nil {
		return domain.ArtifactRef{}, "", err
	}
	for _, a := range manifest.Artifacts {
		if a.ResourceID == resource {
			return a, manifest.BranchID, nil
		}
	}
	return domain.ArtifactRef{}, "", ErrSourceOwnership
}

func (g *changeSourceGraph) node(ctx context.Context, ref domain.DependencyRef) (changeNode, error) {
	if node, ok := g.nodes[ref]; ok {
		return node, nil
	}
	var node changeNode
	if !id32.MatchString(ref.Revision) || !hash64.MatchString(ref.Hash) || len(g.nodes) >= 4096 {
		return node, ErrSourceOwnership
	}
	switch ref.Kind {
	case "resource":
		resource, err := g.b.index.sourceRow("resources", map[string]string{"id": ref.ID})
		if err != nil || resource.Values["project_id"] != g.b.project || resource.Values["branch_id"] != ref.BranchID {
			return node, ErrSourceOwnership
		}
		a, branch, err := g.artifact(ref.Revision, ref.ID)
		if err != nil || branch != ref.BranchID || changeResourceHash(a) != ref.Hash {
			return node, ErrSourceOwnership
		}
		body, err := g.read(ctx, g.b.project, a.Hash)
		if err != nil || financialDigest(body) != a.Hash || int64(len(body)) != a.Size {
			return node, ErrSourceOwnership
		}
		row, err := g.b.index.sourceRow("artifact_releases", map[string]string{"id": ref.Revision})
		if err != nil {
			return node, err
		}
		field, err := ArtifactField(ref.ID, false)
		if err != nil {
			return node, err
		}
		if err := g.b.link(row, field, financialDigest([]byte(a.Hash))); err != nil {
			return node, err
		}
		node = changeNode{resource: resource, artifact: a, text: string(body)}
	case "skill":
		if ref.ID != ref.Revision || ref.BranchID != "" {
			return node, ErrSourceOwnership
		}
		if _, err := g.b.capabilityPackage(ctx, ref.ID, ref.Hash, g.read); err != nil {
			return node, err
		}
		row, err := g.b.index.sourceRow("skill_packages", map[string]string{"id": ref.ID})
		var report skills.Report
		if err != nil || authorDocument(row.Values["report"], &report) != nil {
			return node, ErrSourceOwnership
		}
		node.name = report.Name
	default:
		key := ref.BranchID + ":" + ref.Revision
		values, ok := g.values[key]
		if !ok {
			snapshot, err := g.b.ip(ref.Revision, ref.BranchID)
			if err != nil {
				return node, err
			}
			values = domain.SnapshotValues(snapshot)
			g.values[key] = values
		}
		value, ok := values[ref.Kind+":"+ref.ID]
		if !ok || stableHash(value) != ref.Hash {
			return node, ErrSourceOwnership
		}
		switch value := value.(type) {
		case domain.Entity:
			node.name = value.Name
		case domain.Assertion:
			node.name = value.Subject + " · " + value.Predicate
		case domain.WorldRule:
			node.name = value.Name
		case domain.StoryEvent:
			node.name = value.Name
		case domain.EntityMapping:
			node.name = "实体映射 · " + ref.ID
		default:
			return node, ErrSourceOwnership
		}
	}
	g.nodes[ref] = node
	return node, nil
}

func (g *changeSourceGraph) label(node changeNode, name string) error {
	if node.resource.Table != "" {
		return g.b.link(node.resource, "name", financialDigest([]byte(name)))
	}
	if node.name != name {
		return ErrSourceOwnership
	}
	return nil
}

func sourceSnapshotChanges(branch, beforeID, afterID string, before, after domain.Snapshot) []domain.DependencyChange {
	refs := map[string]domain.DependencyChange{}
	for key, value := range domain.SnapshotValues(before) {
		kind, id, _ := strings.Cut(key, ":")
		refs[key] = domain.DependencyChange{Before: domain.DependencyRef{Kind: kind, ID: id, BranchID: branch, Revision: beforeID, Hash: stableHash(value)}}
	}
	for key, value := range domain.SnapshotValues(after) {
		kind, id, _ := strings.Cut(key, ":")
		change := refs[key]
		change.After = domain.DependencyRef{Kind: kind, ID: id, BranchID: branch, Revision: afterID, Hash: stableHash(value)}
		refs[key] = change
	}
	result := []domain.DependencyChange{}
	for _, change := range refs {
		if change.Before.Hash != change.After.Hash {
			result = append(result, change)
		}
	}
	sortSourceChanges(result)
	return result
}

func sortSourceChanges(changes []domain.DependencyChange) {
	slices.SortFunc(changes, func(a, b domain.DependencyChange) int {
		return strings.Compare(a.Before.Key()+a.After.Key(), b.Before.Key()+b.After.Key())
	})
}

func resourceDifference(ref domain.DependencyRef, name string, before, after changeNode) domain.DependencyDifference {
	diff := domain.DependencyDifference{Ref: ref, Name: name, Fields: []domain.ChangedField{}}
	if before.artifact.Hash != after.artifact.Hash {
		diff.Fields = append(diff.Fields, domain.ChangedField{Name: "text", Before: before.text, After: after.text})
	}
	for _, item := range []struct {
		name          string
		before, after any
	}{
		{"policy", before.artifact.Policy, after.artifact.Policy},
		{"dependencies", before.artifact.Dependencies, after.artifact.Dependencies},
	} {
		old, _ := json.Marshal(item.before)
		newer, _ := json.Marshal(item.after)
		if string(old) != string(newer) {
			diff.Fields = append(diff.Fields, domain.ChangedField{Name: item.name, Before: string(old), After: string(newer)})
		}
	}
	return diff
}
