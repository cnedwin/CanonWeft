package service

import "testing"

func TestNameCatalogEvidenceAndBoundaries(t *testing.T) {
	seed, err := nameMaterials("", "")
	if err != nil || validateNameMaterials(seed) != nil {
		t.Fatal("bundled catalog rejected", err)
	}
	seed.Records[0].URL = "javascript:alert(1)"
	if validateNameMaterials(seed) == nil {
		t.Fatal("active URL accepted")
	}
	seed.Records[0].URL = ""
	seed.Records = append(seed.Records, seed.Records[0])
	if validateNameMaterials(seed) == nil {
		t.Fatal("duplicate source identity accepted")
	}
}
