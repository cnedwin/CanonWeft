package releaseevidence

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"novelstudio/internal/domain"
)

func TestOriginRejectsRetargetingAndContentChanges(t *testing.T) {
	origin := domain.ArtifactOrigin{Kind: "manual", ActorID: "actor", ResourceID: "one", BranchID: "branch", BaseHead: "before", ContentHash: strings.Repeat("a", 64)}
	ref := domain.ArtifactRef{ResourceID: "one", ExpectedHead: "before", Hash: strings.Repeat("a", 64)}
	if err := origin.ValidateTarget("branch", ref); err != nil {
		t.Fatal(err)
	}
	for _, changed := range []domain.ArtifactRef{
		{ResourceID: "two", ExpectedHead: "before", Hash: ref.Hash},
		{ResourceID: "one", ExpectedHead: "later", Hash: ref.Hash},
		{ResourceID: "one", ExpectedHead: "before", Hash: strings.Repeat("b", 64)},
	} {
		if !errors.Is(origin.ValidateTarget("branch", changed), domain.ErrConflict) {
			t.Fatal("retargeted evidence accepted", changed)
		}
	}
	if !errors.Is(origin.ValidateTarget("other", ref), domain.ErrConflict) {
		t.Fatal("cross-branch evidence accepted")
	}
	origin.Generation = &domain.ArtifactGenerationEvidence{GenerationID: "known-ai"}
	if origin.ValidateTarget("branch", ref) == nil {
		t.Fatal("manual label erased known generation evidence")
	}
}

func TestChangedOrUnqualifiedOutputCannotInheritPublicationEligibility(t *testing.T) {
	evidence := &domain.ArtifactGenerationEvidence{GenerationID: "generation", Mode: "QUALIFIED", TextHash: strings.Repeat("a", 64)}
	for _, tc := range []struct {
		content, mode string
		required      bool
	}{
		{strings.Repeat("a", 64), "QUALIFIED", false},
		{strings.Repeat("b", 64), "QUALIFIED", true},
		{strings.Repeat("a", 64), "TRIAL", true},
		{strings.Repeat("a", 64), "UNQUALIFIED", true},
	} {
		evidence.Mode = tc.mode
		origin := domain.ArtifactOrigin{ContentHash: tc.content, Generation: evidence}
		if got := origin.NeedsIndependentReview(); got != tc.required {
			t.Fatalf("content %s / mode %s: review=%v want %v", tc.content, tc.mode, got, tc.required)
		}
	}
	evidence.Mode = "QUALIFIED"
	evidence.BaseIPHead = "old-snapshot"
	if !(domain.ArtifactOrigin{IPHead: "new-snapshot", ContentHash: evidence.TextHash, Generation: evidence}).NeedsIndependentReview() {
		t.Fatal("changed canon snapshot inherited old evidence")
	}
}

// Dropping this selection would turn a tracked AI result into an apparent manual input.
func TestReleaseInputRetainsSelectedGenerationAndParent(t *testing.T) {
	raw := `{"resource_id":"target","text":"候选","origin":{"generation_id":"generation","parent_release_id":"parent"}}`
	var update domain.ResourceUpdate
	if err := json.Unmarshal([]byte(raw), &update); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(update)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"generation_id":"generation"`) || !strings.Contains(string(encoded), `"parent_release_id":"parent"`) {
		t.Fatal("selected generation provenance was silently discarded", string(encoded))
	}
}

func TestLegacyManifestRoundTripDoesNotInventOrigin(t *testing.T) {
	var manifest domain.Manifest
	if err := json.Unmarshal([]byte(`{"schema":1,"branch_id":"branch","expected_ip_head":"","ip_candidate_id":"","artifacts":[{"resource_id":"resource","expected_head":"","hash":"hash","size":3}]}`), &manifest); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "origin") {
		t.Fatal("legacy immutable manifest hash would change", string(encoded))
	}
}
