package memory

import "testing"

func TestNarratorMemoryExcludesCharacterSecrets(t *testing.T) {
	public, secret, control := testItem("public"), testItem("secret"), testItem("theme")
	secret.Visibility, secret.KnownBy = "restricted", []string{"hero"}
	control.Control, control.Visibility, control.Pinned = true, "author", true
	r := Request{Scope: testScope(), Budget: 16384, SourceEpoch: "e1", CompilerVersion: Version}
	r.Scope.Perspective = ""
	p, err := Compile(r, []Item{public, secret, control})
	if err != nil {
		t.Fatal("narrator memory", err)
	}
	seen := map[string]string{}
	for _, item := range p.Items {
		seen[item.Item.ID] = item.Channel
	}
	if seen["public"] != "evidence" || seen["secret"] != "" || seen["theme"] != "control" {
		t.Fatal("narrator crossed knowledge boundary", seen)
	}
	r.Scope.Purpose, r.Scope.Save = "npc", "save"
	if _, err := Compile(r, []Item{public}); err == nil {
		t.Fatal("NPC without perspective accepted")
	}
}
