package semanticreview_test

import (
	"novelstudio/internal/domain"
	"testing"
)

func TestNegativeScopeIgnoresProposalIdentityTimestampsAndObjectOrderOnly(t *testing.T) {
	doc := domain.SemanticReviewDocument{Schema: 1, Target: domain.SemanticReviewTarget{Kind: "artifact_release", ID: "r1", Hash: "manifest1", BranchID: "branch", ExpectedIPHead: "base"}, Snapshot: domain.Snapshot{Entities: []domain.Entity{{ID: "a", Name: "甲"}, {ID: "b", Name: "乙"}}, Assertions: []domain.Assertion{{ID: "fact", Subject: "a", Value: "医师", RecordedAt: "time1", EffectiveFrom: 9007199254740993}}}, Artifacts: []domain.SemanticReviewArtifact{{Target: domain.ArtifactReviewTarget{ReleaseID: "r1", ManifestHash: "manifest1", ResourceID: "resource", ExpectedHead: "text-base", SourceGenerationID: "source"}, Text: "实际内容"}}}
	original, err := domain.SemanticContentScopeHash(doc)
	if err != nil {
		t.Fatal(err)
	}
	doc.Target.ID, doc.Target.Hash = "r2", "manifest2"
	doc.Artifacts[0].Target.SourceGenerationID = "new-execution-same-material"
	doc.Artifacts[0].Target.ReleaseID, doc.Artifacts[0].Target.ManifestHash = "r2", "manifest2"
	doc.Snapshot.Assertions[0].RecordedAt = "time2"
	doc.Snapshot.Entities[0], doc.Snapshot.Entities[1] = doc.Snapshot.Entities[1], doc.Snapshot.Entities[0]
	same, err := domain.SemanticContentScopeHash(doc)
	if err != nil || same != original {
		t.Fatal("administrative identity erased negative equivalence", err)
	}
	for name, mutate := range map[string]func(*domain.SemanticReviewDocument){"text": func(d *domain.SemanticReviewDocument) { d.Artifacts[0].Text += "真实修改" }, "context": func(d *domain.SemanticReviewDocument) { d.Target.ExpectedIPHead = "new-base" }, "time": func(d *domain.SemanticReviewDocument) { d.Snapshot.Assertions[0].EffectiveFrom++ }} {
		t.Run(name, func(t *testing.T) {
			copy := doc
			copy.Artifacts = append([]domain.SemanticReviewArtifact{}, doc.Artifacts...)
			copy.Snapshot.Assertions = append([]domain.Assertion{}, doc.Snapshot.Assertions...)
			mutate(&copy)
			hash, e := domain.SemanticContentScopeHash(copy)
			if e != nil || hash == original {
				t.Fatal("real content or context change collapsed", e)
			}
		})
	}
	if doc.Snapshot.Assertions[0].RecordedAt != "time2" {
		t.Fatal("scope calculation mutated source")
	}
}
