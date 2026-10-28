package service

import (
	"strings"
	"testing"

	"novelstudio/internal/domain"
)

func TestRecoveryCompositionRemapsAllParentsWithoutMutatingSource(t *testing.T) {
	ids := RecoveryResourceIDs{RecoveryIPIDs: RecoveryIPIDs{Branches: map[string]string{"branch": "new-branch"}, Revisions: map[string]string{}}, Releases: map[string]string{"group": "new-group", "a": "new-a", "b": "new-b"}}
	original := map[string]domain.Manifest{"group": {BranchID: "branch", CompositionParents: []string{"a", "b"}}}
	result, err := remapRecoveryManifests(original, nil, ids)
	if err != nil || strings.Join(result["group"].CompositionParents, ",") != "new-a,new-b" || original["group"].CompositionParents[0] != "a" {
		t.Fatal("composition ancestry not remapped safely", result, err)
	}
	delete(ids.Releases, "b")
	if _, err = remapRecoveryManifests(original, nil, ids); err == nil {
		t.Fatal("lost parent silently omitted")
	}
}

func TestRecoveryRemapsPeerReleaseIdentityWithoutChangingOriginal(t *testing.T) {
	ids := RecoveryResourceIDs{RecoveryIPIDs: RecoveryIPIDs{Branches: map[string]string{"branch": "copy-branch"}, Revisions: map[string]string{}}, Releases: map[string]string{"target": "copy-target", "peer": "copy-peer"}}
	original := map[string]domain.Manifest{"target": {BranchID: "branch", PeerReleases: []string{"peer"}}}
	result, err := remapRecoveryManifests(original, nil, ids)
	if err != nil || len(result["target"].PeerReleases) != 1 || result["target"].PeerReleases[0] != "copy-peer" || original["target"].PeerReleases[0] != "peer" {
		t.Fatal("peer history remap", result, err)
	}
	delete(ids.Releases, "peer")
	if _, err = remapRecoveryManifests(original, nil, ids); err == nil {
		t.Fatal("missing peer copy accepted")
	}
}

func TestRecoveryPlanRejectsMissingVersionsAndTamperedHistoricalGraph(t *testing.T) {
	for _, fault := range []string{"missing-version", "wrong-node-hash", "wrong-graph-hash", "unknown-frozen-node"} {
		t.Run(fault, func(t *testing.T) {
			branch, resource, release := strings.Repeat("1", 32), strings.Repeat("2", 32), strings.Repeat("3", 32)
			ids := RecoveryResourceIDs{
				RecoveryIPIDs: RecoveryIPIDs{SourceProject: strings.Repeat("4", 32), TargetProject: strings.Repeat("5", 32), Branches: map[string]string{branch: strings.Repeat("6", 32)}, Revisions: map[string]string{}},
				Resources:     map[string]string{resource: strings.Repeat("7", 32)}, Releases: map[string]string{release: strings.Repeat("8", 32)},
			}
			artifact := domain.ArtifactRef{ResourceID: resource, Hash: strings.Repeat("a", 64)}
			ref := domain.DependencyRef{Kind: "resource", BranchID: branch, ID: resource, Revision: release, Hash: resourceNodeHash(artifact.Hash, nil, nil)}
			graph := &domain.DependencyGraph{Entries: []domain.DependencyEntry{{Ref: ref}}, Frozen: map[string]bool{}}
			plan := domain.ChangePlan{ID: strings.Repeat("9", 32), BranchID: branch, ReleaseID: release, GraphSnapshot: graph, GraphHash: valueHash(graph), Changes: []domain.DependencyChange{{After: ref}}}
			original := map[string]domain.Manifest{release: {BranchID: branch, Artifacts: []domain.ArtifactRef{artifact}}}
			copy := artifact
			copy.ResourceID = ids.Resources[resource]
			copied := map[string]domain.Manifest{release: {BranchID: ids.Branches[branch], Artifacts: []domain.ArtifactRef{copy}}}
			switch fault {
			case "missing-version":
				delete(ids.Releases, release)
			case "wrong-node-hash":
				plan.Changes[0].After.Hash = strings.Repeat("b", 64)
			case "wrong-graph-hash":
				graph.Entries[0].Name = "被篡改的历史名称"
			case "unknown-frozen-node":
				graph.Frozen["resource:unknown:unknown"] = true
				plan.GraphHash = valueHash(graph)
			}
			if err := remapRecoveryPlan(&plan, original, copied, ids); err == nil {
				t.Fatal("invalid historical plan accepted", fault)
			}
		})
	}
}

func TestRecoveryPlanPreservesAdditionalReviewScope(t *testing.T) {
	branch, resource, release := strings.Repeat("1", 32), strings.Repeat("2", 32), strings.Repeat("3", 32)
	ids := RecoveryResourceIDs{RecoveryIPIDs: RecoveryIPIDs{SourceProject: strings.Repeat("4", 32), TargetProject: strings.Repeat("5", 32), Branches: map[string]string{branch: strings.Repeat("6", 32)}, Revisions: map[string]string{}}, Resources: map[string]string{resource: strings.Repeat("7", 32)}, Releases: map[string]string{release: strings.Repeat("8", 32)}}
	artifact := domain.ArtifactRef{ResourceID: resource, Hash: strings.Repeat("a", 64)}
	ref := domain.DependencyRef{Kind: "resource", BranchID: branch, ID: resource, Revision: release, Hash: resourceNodeHash(artifact.Hash, nil, nil)}
	graph := &domain.DependencyGraph{Entries: []domain.DependencyEntry{{Ref: ref}}, Frozen: map[string]bool{}}
	plan := domain.ChangePlan{ID: strings.Repeat("9", 32), BranchID: branch, GraphSnapshot: graph, GraphHash: valueHash(graph), AdditionalReview: []domain.ChangeReviewScope{{Node: ref, Reason: "保留未登记依赖场景的作者检查决定"}}}
	plan.Items, _ = domain.ChangeReviewItems(plan.Changes, *graph, plan.AdditionalReview)
	original := map[string]domain.Manifest{release: {BranchID: branch, Artifacts: []domain.ArtifactRef{artifact}}}
	copy := artifact
	copy.ResourceID = ids.Resources[resource]
	copied := map[string]domain.Manifest{release: {BranchID: ids.Branches[branch], Artifacts: []domain.ArtifactRef{copy}}}
	if err := remapRecoveryPlan(&plan, original, copied, ids); err != nil {
		t.Fatal(err)
	}
	if len(plan.Items) != 1 || plan.Items[0].Node.ID != ids.Resources[resource] || plan.AdditionalReview[0].Node != plan.Items[0].Node || plan.AdditionalReview[0].Reason == "" {
		t.Fatal("restored plan lost additional scope", plan)
	}
}
