package retention

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"novelstudio/internal/domain"
)

func TestContextExpiryKeepsOnlyVerifiedIdentity(t *testing.T) {
	pack := domain.ContextPackage{ID: strings.Repeat("a", 32), IndexID: strings.Repeat("b", 64), IPHead: strings.Repeat("c", 32), Input: domain.SearchInput{Query: "PRIVATE_CONTEXT_QUERY"}}
	raw, _ := json.Marshal(pack)
	summary, err := ContextEvidence(raw, financialDigest(raw))
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
	project := strings.Repeat("d", 32)
	key, _ := RowOwnerKey("context_packages", map[string]string{"id": pack.ID})
	id, _ := ReferenceID(project, "context_packages", key, "payload")
	expiry := Expiry{ReferenceID: id, Revision: 1, Rule: "context_packages.payload", PayloadHash: financialDigest(raw), Storage: Inline, Deadline: at}
	proof, err := ProjectExpired(expiry, raw, summary, at)
	if err != nil {
		t.Fatal(err)
	}
	evidence, _ := json.Marshal(proof)
	if bytes.Contains(evidence, []byte("PRIVATE_CONTEXT_QUERY")) {
		t.Fatal("query retained in evidence")
	}
	ref := StoredReference{ID: id, ProjectID: project, Table: "context_packages", OwnerKey: key, Field: "payload", Storage: Inline, State: Expired, Revision: 2, EvidenceRevision: 2, PayloadHash: expiry.PayloadHash, ProjectionHash: financialDigest(evidence), Deadline: at, ExpiredAt: at, EvidenceExpiredAt: at}
	owner := StoredOwner{ProjectID: project, Table: "context_packages", Key: key, Field: "payload", ContentHash: summary.Context.ContentHash, ContextIndexID: pack.IndexID, Value: evidence}
	if _, err := ValidateStoredProjection(ref, owner, evidence); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*StoredOwner){
		func(o *StoredOwner) { o.ContentHash = strings.Repeat("e", 64) },
		func(o *StoredOwner) { o.ContextIndexID = strings.Repeat("e", 64) },
		func(o *StoredOwner) {
			o.Key, _ = RowOwnerKey("context_packages", map[string]string{"id": strings.Repeat("e", 32)})
		},
	} {
		changed := owner
		mutate(&changed)
		if _, err := ValidateStoredProjection(ref, changed, evidence); err == nil {
			t.Fatal("context owner mismatch admitted")
		}
	}
	changed := *summary.Context
	changed.IPHead = strings.Repeat("f", 32)
	if _, err := ProjectExpired(expiry, raw, EvidenceSummary{Context: &changed}, at); err == nil {
		t.Fatal("forged context binding admitted")
	}
	if _, err := ProjectExpired(expiry, raw, EvidenceSummary{}, at); err == nil {
		t.Fatal("missing context binding admitted")
	}
	if ValidateSummary(EvidenceSummary{Context: summary.Context, InputHash: strings.Repeat("e", 64)}, Inline) == nil {
		t.Fatal("unrelated metadata admitted")
	}
	if ValidateProjectionForRule(proof, "recipe_steps.frozen") == nil {
		t.Fatal("context proof accepted for different owner")
	}
}
