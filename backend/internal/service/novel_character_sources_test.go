package service

import (
	"novelstudio/internal/domain"
	"testing"
)

func TestCharacterSourceContextRequiresFullInput(t *testing.T) {
	pack := domain.ContextPackage{}
	if validateCharacterSourceContext(nil, pack) != nil {
		t.Fatal("legacy document rejected")
	}
	if validateCharacterSourceContext([]string{"source"}, pack) == nil {
		t.Fatal("missing full source accepted")
	}
	pack.Required = []domain.RequiredInput{{Dependency: domain.Dependency{Source: domain.DependencyRef{Kind: "resource", ID: "source"}}}}
	if e := validateCharacterSourceContext([]string{"source"}, pack); e != nil {
		t.Fatal(e)
	}
	if validateCharacterSourceContext([]string{"different"}, pack) == nil {
		t.Fatal("unrelated source accepted")
	}
}
