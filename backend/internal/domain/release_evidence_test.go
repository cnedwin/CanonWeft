package domain

import (
	"strings"
	"testing"
)

func recoveryOriginFixture() (ArtifactOrigin, ArtifactRef) {
	hash := strings.Repeat("a", 64)
	g := &ArtifactGenerationEvidence{
		GenerationID: "historical-generation", CallID: "historical-call",
		InputHash: hash, OutputHash: hash, TextHash: hash, ConfigHash: hash,
		Role: "researcher", Mode: "QUALIFIED",
	}
	origin := ArtifactOrigin{
		Kind: "generation", ActorID: "author", ResourceID: "copy-resource",
		BranchID: "copy-branch", ContentHash: hash, Generation: g, ReviewRequired: true,
		Recovery: []ArtifactRecoveryEvidence{{
			ProjectID: strings.Repeat("1", 32), BranchID: strings.Repeat("2", 32),
			ReleaseID: strings.Repeat("3", 32), ResourceID: strings.Repeat("4", 32),
			ManifestHash: hash, ContentHash: hash, OriginHash: hash,
			OriginKnown: true, GenerationHash: ArtifactEvidenceHash(g),
		}},
	}
	return origin, ArtifactRef{ResourceID: origin.ResourceID, Hash: hash}
}

func TestRecoveredQualifiedOriginRequiresFreshReview(t *testing.T) {
	origin, ref := recoveryOriginFixture()
	if err := origin.ValidateTarget(origin.BranchID, ref); err != nil {
		t.Fatal(err)
	}
	origin.ReviewRequired = false
	if err := origin.ValidateTarget(origin.BranchID, ref); err == nil {
		t.Fatal("historical qualification granted publication without fresh review")
	}
	origin.Recovery = nil
	if err := origin.ValidateTarget(origin.BranchID, ref); err != nil {
		t.Fatalf("ordinary qualified source behavior changed: %v", err)
	}
}

func TestRecoveredGenerationEvidenceCannotBeReplacedOrErased(t *testing.T) {
	for _, mutation := range []string{"replace", "erase", "invalid-proof"} {
		t.Run(mutation, func(t *testing.T) {
			origin, ref := recoveryOriginFixture()
			switch mutation {
			case "replace":
				origin.Generation.CallID = "different-call"
			case "erase":
				origin.Generation, origin.Kind = nil, "manual"
				origin.ReviewRequired = false
			case "invalid-proof":
				origin.Recovery[0].ManifestHash = "missing"
			}
			if err := origin.ValidateTarget(origin.BranchID, ref); err == nil {
				t.Fatal("invalid historical evidence accepted")
			}
		})
	}
}

func TestUnknownRecoveredOriginRemainsReviewableAfterEditingAndRecovery(t *testing.T) {
	origin, ref := recoveryOriginFixture()
	origin.Kind, origin.Generation = "recovered", nil
	origin.Recovery[0].OriginKnown = false
	origin.Recovery[0].GenerationHash = ""
	origin.Recovery = append(origin.Recovery, origin.Recovery[0])
	for _, kind := range []string{"recovered", "derived"} {
		origin.Kind = kind
		if !origin.HasGeneratedContent() || !origin.NeedsIndependentReview() {
			t.Fatal("unknown history lost its review requirement")
		}
		if err := origin.ValidateTarget(origin.BranchID, ref); err != nil {
			t.Fatal(err)
		}
	}
	origin.Kind, origin.ReviewRequired = "manual", false
	if err := origin.ValidateTarget(origin.BranchID, ref); err == nil {
		t.Fatal("unknown recovered history became a trusted manual source")
	}
}
