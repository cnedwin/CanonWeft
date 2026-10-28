package domain

import (
	"strings"
	"testing"
)

func TestAliasExpansionPreservesOriginalAndAmbiguousIdentities(t *testing.T) {
	entities := []Entity{{ID: "a", Name: "林青", Aliases: []string{"青衣客"}}, {ID: "b", Name: "顾北", Aliases: []string{"青衣客"}}, {ID: "c", Name: "局外人", Aliases: []string{"幽影"}}}
	expanded, ids := ExpandQuery("青衣客的宝剑", entities)
	if !strings.HasPrefix(expanded, "青衣客的宝剑") || !strings.Contains(expanded, "林青") || !strings.Contains(expanded, "顾北") || strings.Contains(expanded, "局外人") || len(ids) != 2 {
		t.Fatalf("alias expansion lost ambiguity or invented matches: %q %v", expanded, ids)
	}
	if result, _ := ExpandQuery("无人匹配", entities); result != "无人匹配" {
		t.Fatal(result)
	}
}
