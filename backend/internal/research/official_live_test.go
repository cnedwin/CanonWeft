package research

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestOfficialReferenceLive(t *testing.T) {
	if os.Getenv("STUDIO_LIVE_RESEARCH") != "1" {
		t.Skip("opt-in free official-source check")
	}
	const target = "https://www.archives.gov/founding-docs/declaration-transcript"
	doc, err := NewOfficial(nil).CollectURL(context.Background(), "en", target)
	if err != nil {
		t.Fatal("official robots/limited article fetch", err)
	}
	var receipt struct {
		Schema  string `json:"schema"`
		Excerpt string `json:"excerpt"`
	}
	if json.Unmarshal(doc.Raw, &receipt) != nil || receipt.Schema != "official-reference-receipt-v1" || len([]rune(receipt.Excerpt)) > 160 || doc.TextComplete || !strings.Contains(doc.Title, "Declaration") {
		t.Fatal("official receipt boundary", doc.Provenance)
	}
	t.Logf("Free official capture: title=%s extent=%s version=%s receipt_bytes=%d; no HTML full text stored", doc.Title, doc.Extent, doc.Version, len(doc.Raw))
}
