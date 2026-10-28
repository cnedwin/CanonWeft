package retention

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

func TestEvidenceIdentityBindsOneReferenceRevisionWithoutMutableProjection(t *testing.T) {
	ref := strings.Repeat("a", 64)
	got, err := EvidenceID(ref, 17)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal([]string{"retention-evidence-v1", ref, "17"})
	sum := sha256.Sum256(raw)
	if got != hex.EncodeToString(sum[:]) {
		t.Fatal("evidence identity encoding drift")
	}
	same, _ := EvidenceID(ref, 17)
	next, _ := EvidenceID(ref, 18)
	other, _ := EvidenceID(strings.Repeat("b", 64), 17)
	if same != got || next == got || other == got {
		t.Fatal("reference/revision identity is not stable and distinct")
	}
	for _, v := range []struct {
		ref      string
		revision int64
	}{{ref, 0}, {ref, -1}, {strings.ToUpper(ref), 17}, {"bad", 17}} {
		if _, err := EvidenceID(v.ref, v.revision); err == nil {
			t.Fatal("invalid evidence identity accepted")
		}
	}
}
