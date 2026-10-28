package retention

import (
	"context"

	"novelstudio/internal/domain"
)

func (index *SourceIndex) attachChangePlanSources(ctx context.Context, project string, materials []ObservedMaterial, read ReadSourceObject) error {
	for i := range materials {
		m := &materials[i]
		if m.Table != "change_plans" || m.Field != "payload" {
			continue
		}
		row := index.rows[m.Table][m.OwnerKey]
		var plan domain.ChangePlan
		if contentJSON([]byte(row.Values["payload"]), &plan, row.Values["content_hash"]) != nil || plan.ID != row.Values["id"] || plan.BranchID != row.Values["branch_id"] || plan.CandidateID != row.Values["candidate_id"] || !hash64.MatchString(plan.GraphHash) {
			return ErrSourceOwnership
		}
		b := contextSourceBuilder{index: index, project: project, branch: plan.BranchID, m: &m.MaterialReference}
		graph := changeSourceGraph{b: &b, read: read, nodes: map[domain.DependencyRef]changeNode{}, values: map[string]map[string]any{}}
		if err := graph.plan(ctx, plan); err != nil {
			return err
		}
	}
	return nil
}

func (g *changeSourceGraph) plan(ctx context.Context, plan domain.ChangePlan) error {
	if _, err := g.b.ip(plan.BaseHead, plan.BranchID); err != nil {
		return err
	}
	changes := []domain.DependencyChange{}
	differences := []domain.DependencyDifference{}
	if plan.CandidateID != "" {
		candidate, err := g.b.index.sourceRow("ip_revisions", map[string]string{"id": plan.CandidateID})
		if err != nil || candidate.Values["branch_id"] != plan.BranchID {
			return ErrSourceOwnership
		}
		before, err := g.b.ip(candidate.Values["base_head"], plan.BranchID)
		if err != nil {
			return err
		}
		after, err := g.b.ip(plan.CandidateID, plan.BranchID)
		if err != nil {
			return err
		}
		changes = sourceSnapshotChanges(plan.BranchID, candidate.Values["base_head"], plan.CandidateID, before, after)
		differences = domain.DependencyDifferences(changes, before, after)
	}
	if plan.ReleaseID != "" {
		_, manifest, err := g.manifest(plan.ReleaseID)
		if err != nil || manifest.BranchID != plan.BranchID || manifest.IPCandidateID != plan.CandidateID {
			return ErrSourceOwnership
		}
		for _, a := range manifest.Artifacts {
			afterRef := domain.DependencyRef{Kind: "resource", BranchID: plan.BranchID, ID: a.ResourceID, Revision: plan.ReleaseID, Hash: changeResourceHash(a)}
			after, err := g.node(ctx, afterRef)
			if err != nil {
				return err
			}
			var beforeRef domain.DependencyRef
			var before changeNode
			if a.ExpectedHead != "" {
				prior, branch, err := g.artifact(a.ExpectedHead, a.ResourceID)
				if err != nil || branch != plan.BranchID {
					return ErrSourceOwnership
				}
				beforeRef = domain.DependencyRef{Kind: "resource", BranchID: plan.BranchID, ID: a.ResourceID, Revision: a.ExpectedHead, Hash: changeResourceHash(prior)}
				before, err = g.node(ctx, beforeRef)
				if err != nil {
					return err
				}
			}
			if beforeRef.Hash == afterRef.Hash {
				continue
			}
			changes = append(changes, domain.DependencyChange{Before: beforeRef, After: afterRef})
			// Names are mutable author text. Pin the copied name's original value;
			// never replace it with today's label or discard its historical proof.
			if len(differences) >= len(plan.Differences) {
				return ErrSourceOwnership
			}
			name := plan.Differences[len(differences)].Name
			if err := g.label(after, name); err != nil {
				return err
			}
			differences = append(differences, resourceDifference(afterRef, name, before, after))
		}
	}
	sortSourceChanges(changes)
	if stableHash(changes) != stableHash(plan.Changes) || !sameSourceDifferences(differences, plan.Differences) {
		return ErrSourceOwnership
	}
	if plan.SourceEventID != "" {
		event, err := g.b.index.sourceRow("outbox", map[string]string{"id": plan.SourceEventID})
		if err != nil || event.Values["project_id"] != g.b.project || event.Values["kind"] != plan.SourceEventKind || (plan.SourceEventKind != "ip.published" && plan.SourceEventKind != "release.published") {
			return ErrSourceOwnership
		}
		source := plan.CandidateID
		if plan.SourceEventKind == "release.published" {
			source = plan.ReleaseID
		}
		if source == "" || event.Values["resource_id"] != source {
			return ErrSourceOwnership
		}
	}
	if plan.GraphSnapshot == nil {
		// Old writers retained a digest only. Validate every available reference
		// without reconstructing a different historical graph from current heads.
		g.b.m.UnboundSources++
		refs := append([]domain.DependencyRef{}, plan.Untracked...)
		refs = append(refs, plan.OutsideRecordedClosure...)
		for _, item := range plan.Items {
			refs = append(refs, item.Node)
			for _, link := range item.Via {
				refs = append(refs, link.Source, link.Target)
			}
		}
		for _, ref := range refs {
			if _, err := g.node(ctx, ref); err != nil {
				return err
			}
		}
		return nil
	}
	return g.frozenGraph(ctx, plan)
}

func sameSourceDifferences(left, right []domain.DependencyDifference) bool {
	// Older automatic plans omitted the empty differences field.
	return (len(left) == 0 && len(right) == 0) || stableHash(left) == stableHash(right)
}

func (g *changeSourceGraph) frozenGraph(ctx context.Context, plan domain.ChangePlan) error {
	graph := plan.GraphSnapshot
	if stableHash(graph) != plan.GraphHash {
		return ErrSourceOwnership
	}
	links := []domain.DependencyLink{}
	frozen := map[string]bool{}
	untracked := []domain.DependencyRef{}
	seen := map[string]bool{}
	for _, entry := range graph.Entries {
		if seen[entry.Ref.Key()] {
			return ErrSourceOwnership
		}
		seen[entry.Ref.Key()] = true
		node, err := g.node(ctx, entry.Ref)
		if err != nil {
			return err
		}
		if err := g.label(node, entry.Name); err != nil {
			return err
		}
		if entry.Ref.Kind == "skill" {
			if entry.BranchName != "方法库" {
				return ErrSourceOwnership
			}
		} else {
			branch, err := g.b.index.sourceRow("branches", map[string]string{"id": entry.Ref.BranchID})
			if err != nil || branch.Values["project_id"] != g.b.project {
				return ErrSourceOwnership
			}
			if err := g.b.link(branch, "name", financialDigest([]byte(entry.BranchName))); err != nil {
				return err
			}
		}
		if entry.Ref.Kind != "resource" {
			continue
		}
		policy := node.artifact.Dependencies
		if policy == nil || len(policy.Inputs) == 0 {
			untracked = append(untracked, entry.Ref)
		}
		if policy != nil {
			frozen[entry.Ref.Key()] = policy.Frozen
			for _, d := range policy.Inputs {
				if _, err := g.node(ctx, d.Source); err != nil {
					return err
				}
				links = append(links, domain.DependencyLink{Source: d.Source, Target: entry.Ref, Kind: d.Kind, Reason: d.Reason, Pinned: d.Pinned})
			}
		}
	}
	if stableHash(links) != stableHash(graph.Links) || stableHash(frozen) != stableHash(graph.Frozen) || stableHash(untracked) != stableHash(graph.Untracked) || stableHash(untracked) != stableHash(plan.Untracked) {
		return ErrSourceOwnership
	}
	items, err := domain.ChangeReviewItems(plan.Changes, *graph, plan.AdditionalReview)
	if err != nil {
		return ErrSourceOwnership
	}
	if stableHash(items) != stableHash(plan.Items) {
		return ErrSourceOwnership
	}
	affected := map[string]bool{}
	for _, change := range plan.Changes {
		affected[change.Before.Key()], affected[change.After.Key()] = true, true
	}
	for _, item := range items {
		affected[item.Node.Key()] = true
	}
	outside := []domain.DependencyRef{}
	for _, entry := range graph.Entries {
		if entry.Ref.Kind == "resource" && !affected[entry.Ref.Key()] {
			outside = append(outside, entry.Ref)
		}
	}
	if stableHash(outside) != stableHash(plan.OutsideRecordedClosure) {
		return ErrSourceOwnership
	}
	return nil
}
