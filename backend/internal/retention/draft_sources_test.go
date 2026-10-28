package retention

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestDraftEvidenceDistinguishesEditsAndCanonicalPolicy(t *testing.T) {
	row := inventoryRow("resource_drafts", map[string]string{"resource_id": strings.Repeat("8", 32), "actor_id": strings.Repeat("4", 32), "revision": "1", "body": "同样的正文"})
	first, err := draftVersionEvidence(row, "body")
	if err != nil || first == nil {
		t.Fatal(first, err)
	}
	row.Values["revision"] = "2"
	second, _ := draftVersionEvidence(row, "body")
	if sameDraftVersion(first, second) || first.ProjectionHash != second.ProjectionHash {
		t.Fatal("business version conflated with body hash")
	}
	row.Values["base_head"] = strings.Repeat("a", 32)
	changedBase, _ := draftVersionEvidence(row, "body")
	if sameDraftVersion(second, changedBase) {
		t.Fatal("source base ignored")
	}
	for _, invalid := range []string{"", "0", "-1", "01", "9223372036854775808"} {
		row.Values["revision"] = invalid
		if value, err := draftVersionEvidence(row, "body"); err != nil || value != nil {
			t.Fatal("legacy evidence fabricated", invalid, value, err)
		}
	}
	policy := inventoryRow("resource_policy_drafts", map[string]string{"revision": "1", "payload": `{"visibility":"author","reveal_at":0,"space":"research"}`})
	a, _ := draftVersionEvidence(policy, "payload")
	policy.Values["payload"] = `{ "space": "research", "visibility": "author", "reveal_at": 0 }`
	b, _ := draftVersionEvidence(policy, "payload")
	if !sameDraftVersion(a, b) {
		t.Fatal("SQL JSON layout changed projection", a, b)
	}
	policy.Values["payload"] = `{"space":"research","Visibility":"author","reveal_at":0}`
	if value, _ := draftVersionEvidence(policy, "payload"); value != nil {
		t.Fatal("aliased policy certified")
	}
}

func TestDraftEvidenceRoundTripPreservesOldRecords(t *testing.T) {
	project := strings.Repeat("2", 32)
	owner, _ := OwnerKey(strings.Repeat("8", 32), strings.Repeat("4", 32))
	id, _ := ReferenceID(project, "resource_drafts", owner, "body")
	hash := financialDigest([]byte("正文"))
	now := time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)
	m := ObservedMaterial{MaterialReference: MaterialReference{ID: id, Table: "resource_drafts", OwnerKey: owner, Field: "body", SourceID: id, SourcePayloadHash: hash, PayloadHash: hash, Storage: Inline, Class: Candidate, ContentRoot: true}, TimeBasis: LegacyRegistered, FirstVerifiedAt: now, ValueFirstVerifiedAt: now}
	old, err := materialReferenceVersion(project, m, Lifecycle{}, now)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(old)
	if strings.Contains(string(raw), "draft_version") {
		t.Fatal("old canonical envelope changed")
	}
	if _, err := decodeReferenceVersion(string(raw), financialDigest(raw)); err != nil {
		t.Fatal(err)
	}
	m.DraftVersion = &DraftVersionEvidence{Version: "7", ProjectionHash: hash}
	current, err := materialReferenceVersion(project, m, Lifecycle{}, now)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(current)
	decoded, err := decodeReferenceVersion(string(raw), financialDigest(raw))
	if err != nil || !sameDraftVersion(decoded.DraftVersion, m.DraftVersion) {
		t.Fatal(decoded, err)
	}
	current.DraftVersion = &DraftVersionEvidence{Version: "7", ProjectionHash: strings.Repeat("f", 64)}
	if validReferenceVersion(current) {
		t.Fatal("different source bytes certified")
	}
}
