package retention

import (
	"encoding/json"
	"novelstudio/internal/domain"
	"strings"
	"testing"
)

func TestPeerContentReferencesKeepUnpublishedUpstreamAndRejectMissingPeer(t *testing.T) {
	rows, target := contentRootFixture()
	id := func(value string) string { return strings.Repeat(value, 32) }
	rows[5].Values["project_id"] = id("2")
	peer := domain.Manifest{Schema: 2, ReleaseID: id("b"), BranchID: id("6"), Artifacts: []domain.ArtifactRef{{ResourceID: id("c"), Hash: strings.Repeat("d", 64), Size: 4, Origin: &domain.ArtifactOrigin{Kind: "manual", ActorID: id("4"), ResourceID: id("c"), BranchID: id("6"), ContentHash: strings.Repeat("d", 64)}}}}
	encodedPeer, _ := json.Marshal(peer)
	rows = append(rows, inventoryRow("resources", map[string]string{"id": id("c"), "project_id": id("2"), "branch_id": id("6")}), inventoryRow("artifact_releases", map[string]string{"id": peer.ReleaseID, "project_id": id("2"), "branch_id": id("6"), "status": "PREPARED", "manifest": string(encodedPeer), "content_hash": financialDigest(encodedPeer)}))
	target.PeerReleases = []string{peer.ReleaseID}
	encoded, _ := json.Marshal(target)
	rows[11].Values["manifest"], rows[11].Values["content_hash"], rows[11].Values["adopted_hash"] = string(encoded), financialDigest(encoded), financialDigest(encoded)
	index, err := NewSourceIndex(rows)
	if err != nil {
		t.Fatal(err)
	}
	refs, err := index.AdoptedContentReferences(id("2"))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, ref := range refs {
		if ref.Table == "artifact_releases" && ref.Field == "manifest" && strings.Contains(ref.OwnerKey, peer.ReleaseID) {
			found = true
		}
	}
	if !found {
		t.Fatal("unpublished upstream history lost")
	}
	index, err = NewSourceIndex(rows[:len(rows)-1])
	if err != nil {
		t.Fatal(err)
	}
	if _, err = index.AdoptedContentReferences(id("2")); err == nil {
		t.Fatal("missing peer accepted")
	}
}
