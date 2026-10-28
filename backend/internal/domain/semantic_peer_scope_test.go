package domain

import "testing"

func TestSemanticScopeIncludesPeerFactsWithoutCandidateIdentity(t *testing.T) {
	doc := SemanticReviewDocument{Snapshot: Snapshot{Entities: []Entity{}, Assertions: []Assertion{}}, Artifacts: []SemanticReviewArtifact{{Target: ArtifactReviewTarget{ReleaseID: "peer-one", BranchID: "source", ResourceID: "chapter"}, Text: "相同上游正文"}}, Anchors: []SemanticReviewAnchor{{Key: "peer:peer-one:assertion:secret", Text: "真实身份：医师"}}}
	before, err := SemanticContentScopeHash(doc)
	if err != nil {
		t.Fatal(err)
	}
	doc.Artifacts[0].Target.ReleaseID = "peer-two"
	doc.Anchors[0].Key = "peer:peer-two:assertion:secret"
	same, err := SemanticContentScopeHash(doc)
	if err != nil || same != before {
		t.Fatal("candidate identity changed equivalent scope", err)
	}
	doc.Anchors[0].Text = "真实身份：刺客"
	after, err := SemanticContentScopeHash(doc)
	if err != nil || after == before {
		t.Fatal("peer fact change omitted", err)
	}
}
