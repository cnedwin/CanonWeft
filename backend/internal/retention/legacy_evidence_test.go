package retention

import (
	"strings"
	"testing"
)

func TestLegacyEvidenceHashIsOptionalClosedDigestMetadata(t *testing.T) {
	if err := ValidateSummary(EvidenceSummary{LegacyEvidenceHash: strings.Repeat("a", 64)}, Inline); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"PRIVATE_CANARY", "EXPIRED", strings.Repeat("A", 64), strings.Repeat("a", 63)} {
		if ValidateSummary(EvidenceSummary{LegacyEvidenceHash: bad}, Inline) == nil {
			t.Fatal("free text accepted as conversion evidence hash")
		}
	}
}
