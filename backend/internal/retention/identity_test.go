package retention

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

func TestReferenceIdentityUsesCanonicalTypedOwnerTupleAndLocalFieldPath(t *testing.T) {
	project, id := strings.Repeat("a", 32), strings.Repeat("b", 32)
	key, err := OwnerKey(id, "2", DigestKeyPart("private operation"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(key, "private operation") {
		t.Fatal("free-form key leaked")
	}
	got, err := ReferenceID(project, "generation_tasks", key, "frozen.input_hash")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal([]string{project, "generation_tasks", key, "frozen.input_hash"})
	want := sha256.Sum256(raw)
	if got != hex.EncodeToString(want[:]) {
		t.Fatal("identity encoding is not the documented tuple")
	}
	for _, invalid := range []string{"[\"" + id + "\", \"2\"]", `[]`, `["private operation"]`, `["02"]`, `[2]`} {
		if _, err := ReferenceID(project, "generation_tasks", invalid, "frozen.input_hash"); err == nil {
			t.Fatal("noncanonical owner key accepted", invalid)
		}
	}
	if _, err := ReferenceID(project, "generation_tasks", key, "generation_tasks.frozen.input_hash"); err == nil {
		t.Fatal("full rule accepted as local field path")
	}
	if _, err := OwnerKey("raw private text"); err == nil {
		t.Fatal("unhashed free-form key accepted")
	}
	if rule, ok := Rule("generation_tasks.frozen.input_hash"); !ok || rule.Storage != Blob {
		t.Fatal("exported rule lookup differs")
	}
}

func TestArtifactReferencesUseConcreteResourceFieldWithoutOrdinalAliases(t *testing.T) {
	project, release := strings.Repeat("a", 32), strings.Repeat("b", 32)
	owner, _ := RowOwnerKey("artifact_releases", map[string]string{"id": release})
	first, err := ArtifactField(strings.Repeat("c", 32), false)
	if err != nil {
		t.Fatal(err)
	}
	second, err := ArtifactField(strings.Repeat("d", 32), false)
	if err != nil {
		t.Fatal(err)
	}
	a, err := ReferenceID(project, "artifact_releases", owner, first)
	if err != nil {
		t.Fatal(err)
	}
	b, err := ReferenceID(project, "artifact_releases", owner, second)
	if err != nil || a == b {
		t.Fatal("array references collided")
	}
	rule, ok := Rule("artifact_releases." + first)
	if !ok || rule.Key != "artifact_releases."+first || rule.Storage != Blob {
		t.Fatal("concrete rule lost field identity")
	}
	for _, field := range []string{"manifest.artifacts.hash", "manifest.artifacts.original_hash", "manifest.artifacts.0.hash", "manifest.artifacts." + strings.Repeat("C", 32) + ".hash", "manifest.artifacts." + strings.Repeat("c", 32) + ".content"} {
		if _, err := ReferenceID(project, "artifact_releases", owner, field); err == nil {
			t.Fatal("ambiguous array identity accepted", field)
		}
	}
	if _, err := ArtifactField("0", false); err == nil {
		t.Fatal("ordinal array key accepted")
	}
}
