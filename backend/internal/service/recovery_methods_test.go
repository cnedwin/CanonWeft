package service

import (
	"encoding/json"
	"strings"
	"testing"

	"novelstudio/internal/domain"
	"novelstudio/internal/skills"
)

func TestRecoveryMethodsRejectBrokenLineageAndDependencyEvidence(t *testing.T) {
	for _, edges := range []map[string][]string{
		{"a": {"missing"}},
		{"a": {"b"}, "b": {"a"}},
		{"a": {"a"}},
	} {
		if err := recoveryMethodGraph(edges); err == nil {
			t.Fatal("broken lineage accepted", edges)
		}
	}
	if err := recoveryMethodGraph(map[string][]string{"a": {""}, "b": {"a"}, "c": {"a", "b"}}); err != nil {
		t.Fatal("shared ancestors treated as a cycle", err)
	}
	methodID, agentID := strings.Repeat("a", 32), strings.Repeat("b", 32)
	packageID, packageHash := strings.Repeat("c", 32), strings.Repeat("1", 64)
	schema := json.RawMessage(`{"type":"object"}`)
	method := CapabilityVersion{ID: methodID, Definition: domain.CapabilityDefinition{Key: "method", Kind: "skill", Name: "方法", Description: "来源检查", Instructions: "保留证据", InputSchema: schema, OutputSchema: schema, PackageID: packageID, PackageHash: packageHash}}
	method.Hash = capabilityVersionHash(method)
	agent := CapabilityVersion{ID: agentID, Definition: domain.CapabilityDefinition{Key: "agent", Kind: "agent", Name: "研究", Description: "核对来源", Instructions: "给出候选", Role: "researcher", InputSchema: schema, OutputSchema: schema, Dependencies: []domain.CapabilityRef{{ID: methodID, Kind: "skill", Hash: method.Hash}}}}
	packages := map[string]SkillVersion{packageID: {ID: packageID, Report: skills.Report{Hash: packageHash}}}
	ids := RecoveryMethodIDs{Packages: map[string]string{packageID: strings.Repeat("d", 32)}, Versions: map[string]string{methodID: strings.Repeat("e", 32), agentID: strings.Repeat("f", 32)}}
	if _, err := remapRecoveryMethods(map[string]CapabilityVersion{methodID: method, agentID: agent}, packages, ids); err != nil {
		t.Fatal("valid methods cannot be copied", err)
	}
	agent.Definition.Dependencies[0].Hash = strings.Repeat("2", 64)
	if _, err := remapRecoveryMethods(map[string]CapabilityVersion{methodID: method, agentID: agent}, packages, ids); err == nil {
		t.Fatal("dependency hash mismatch accepted")
	}
	agent.ParentID = methodID
	agent.Definition.Dependencies[0].Hash = method.Hash
	if _, err := remapRecoveryMethods(map[string]CapabilityVersion{methodID: method, agentID: agent}, packages, ids); err == nil {
		t.Fatal("unrelated capability used as a parent")
	}
}
