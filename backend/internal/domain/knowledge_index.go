package domain

// Field order is part of the canonical manifest digest used by existing indexes.
type KnowledgeSourcePin struct {
	Policy *KnowledgePolicy `json:"policy,omitempty"`
	ID     string           `json:"id"`
	Name   string           `json:"name"`
	Head   string           `json:"head"`
	Hash   string           `json:"hash"`
}

type KnowledgeIndexManifest struct {
	Branch  string               `json:"branch"`
	IPHead  string               `json:"ip_head"`
	Model   string               `json:"model"`
	Parser  string               `json:"parser"`
	Sources []KnowledgeSourcePin `json:"sources"`
}
