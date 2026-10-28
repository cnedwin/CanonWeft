package domain

// Operator-declared support for the exact model/account, not a claim that a
// paid qualification ran. Nil means unknown and must never enable image work.
type ImageCapabilities struct {
	Input      bool `json:"image_input"`
	Generate   bool `json:"image_generate"`
	Edit       bool `json:"image_edit"`
	RegionEdit bool `json:"region_edit"`
}

func SupportsImageOperation(c ModelConfig, editing, region bool) bool {
	p := c.ImageCapabilities
	if p == nil || c.Provider != "openai" {
		return false
	}
	if region && (!editing || !p.RegionEdit) {
		return false
	}
	if editing {
		return p.Input && p.Edit
	}
	return p.Generate
}
