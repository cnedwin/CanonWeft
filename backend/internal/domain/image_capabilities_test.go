package domain

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestImageCapabilityRoutingDoesNotInferGenerationFromVision(t *testing.T) {
	c := ModelConfig{Provider: "openai", Name: "test", Model: "test", BaseURL: "https://api.openai.com/v1", Enabled: true}
	old, _ := json.Marshal(c)
	if strings.Contains(string(old), "image_capabilities") {
		t.Fatal("legacy model encoding changed")
	}
	if SupportsImageOperation(c, false, false) {
		t.Fatal("unknown image model enabled")
	}
	c.ImageCapabilities = &ImageCapabilities{Input: true}
	if SupportsImageOperation(c, false, false) || SupportsImageOperation(c, true, false) {
		t.Fatal("vision-only enabled generation")
	}
	c.ImageCapabilities.Generate = true
	if !SupportsImageOperation(c, false, false) || SupportsImageOperation(c, true, false) {
		t.Fatal("generation/edit not separated")
	}
	c.ImageCapabilities.Edit = true
	if !SupportsImageOperation(c, true, false) || SupportsImageOperation(c, true, true) {
		t.Fatal("edit/region not separated")
	}
	c.ImageCapabilities.RegionEdit = true
	if !SupportsImageOperation(c, true, true) || SupportsImageOperation(c, false, true) {
		t.Fatal("region needs reference editing")
	}
	c.ImageCapabilities.Input = false
	if ValidateModelConfig(c) == nil {
		t.Fatal("inconsistent capabilities accepted")
	}
}
