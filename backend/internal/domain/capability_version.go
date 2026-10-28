package domain

// CapabilityVersion is the immutable wire projection used by execution and
// historical provenance. Availability and qualification are separate records.
type CapabilityVersion struct {
	Retention        *MaterialRetention   `json:"retention,omitempty"`
	ID               string               `json:"id"`
	Hash             string               `json:"hash"`
	ParentID         string               `json:"parent_id"`
	Definition       CapabilityDefinition `json:"definition"`
	InputSchemaText  string               `json:"input_schema_text"`
	OutputSchemaText string               `json:"output_schema_text"`
	CreatedAt        string               `json:"created_at"`
}
