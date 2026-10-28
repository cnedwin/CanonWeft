package domain

// JSON field order is preserved for existing index and source fingerprints.
type WorkingRef struct {
	Kind    string `json:"kind"`
	ID      string `json:"id"`
	Version string `json:"version"`
}
type PrivatePin struct {
	Ref      WorkingRef `json:"ref"`
	State    string     `json:"state"`
	BaseHead string     `json:"base_head"`
	Part     int        `json:"part"`
	Name     string     `json:"name"`
	Format   string     `json:"format"`
	Hash     string     `json:"hash"`
}
type PrivateOmission struct {
	Ref    WorkingRef `json:"ref"`
	Reason string     `json:"reason"`
}
type PrivateManifest struct {
	Project   string            `json:"project"`
	Branch    string            `json:"branch"`
	Actor     string            `json:"actor"`
	Head      string            `json:"head"`
	Model     string            `json:"model"`
	Parser    string            `json:"parser"`
	Sources   []PrivatePin      `json:"sources"`
	Omissions []PrivateOmission `json:"omissions"`
}
