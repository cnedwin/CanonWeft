package models

// A creative assistant's source pin is separate from the image rendering task.
// Both use the same generation authority, but only an Image request can draw.
type CreativeSource struct {
	Kind     string              `json:"kind"`
	Visual   *CharacterImageTask `json:"visual,omitempty"`
	SetupID  string              `json:"setup_id,omitempty"`
	Revision int64               `json:"revision,omitempty"`
	Hash     string              `json:"hash,omitempty"`
}
