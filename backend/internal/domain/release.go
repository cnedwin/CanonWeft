package domain

type Resource struct {
	Dependencies *DependencyPolicy `json:"dependencies,omitempty"`
	Policy       *KnowledgePolicy  `json:"policy,omitempty"`
	ID           string            `json:"id"`
	BranchID     string            `json:"branch_id"`
	Name         string            `json:"name"`
	Kind         string            `json:"kind"`
	Head         string            `json:"head"`
	Hash         string            `json:"hash"`
	Revision     int64             `json:"revision"`
	Text         string            `json:"text"`
}
type ResourceUpdate struct {
	Origin              *ArtifactOriginInput `json:"origin,omitempty"`
	Dependencies        *DependencyPolicy    `json:"dependencies,omitempty"`
	FrozenChangeReason  string               `json:"frozen_change_reason,omitempty"`
	Policy              *KnowledgePolicy     `json:"policy,omitempty"`
	PartialAcknowledged bool                 `json:"partial_acknowledged,omitempty"`
	SourceImportID      string               `json:"source_import_id,omitempty"`
	ResourceID          string               `json:"resource_id"`
	ExpectedHead        string               `json:"expected_head"`
	Text                string               `json:"text"`
}
type ArtifactRef struct {
	Origin              *ArtifactOrigin   `json:"origin,omitempty"`
	Dependencies        *DependencyPolicy `json:"dependencies,omitempty"`
	FrozenChangeReason  string            `json:"frozen_change_reason,omitempty"`
	Policy              *KnowledgePolicy  `json:"policy,omitempty"`
	PartialAcknowledged bool              `json:"partial_acknowledged,omitempty"`
	SourceImportID      string            `json:"source_import_id,omitempty"`
	OriginalHash        string            `json:"original_hash,omitempty"`
	ResourceID          string            `json:"resource_id"`
	ExpectedHead        string            `json:"expected_head"`
	Hash                string            `json:"hash"`
	Size                int64             `json:"size"`
}
type ReleasePin struct {
	ReleaseID    string `json:"release_id"`
	ManifestHash string `json:"manifest_hash"`
}

type ReleaseInput struct {
	PeerReleases   []ReleasePin     `json:"peer_releases,omitempty"`
	PatchGroup     *TextPatchGroup  `json:"patch_group,omitempty"`
	BranchID       string           `json:"branch_id"`
	ExpectedIPHead string           `json:"expected_ip_head"`
	IPCandidateID  string           `json:"ip_candidate_id"`
	OperationID    string           `json:"operation_id"`
	Updates        []ResourceUpdate `json:"updates"`
}
type Manifest struct {
	PeerReleases       []string        `json:"peer_releases,omitempty"`
	CompositionParents []string        `json:"composition_parents,omitempty"`
	PatchParents       []string        `json:"patch_parents,omitempty"`
	PatchGroup         *TextPatchGroup `json:"patch_group,omitempty"`
	ReleaseID          string          `json:"release_id,omitempty"`
	Schema             int             `json:"schema"`
	BranchID           string          `json:"branch_id"`
	ExpectedIPHead     string          `json:"expected_ip_head"`
	IPCandidateID      string          `json:"ip_candidate_id"`
	Artifacts          []ArtifactRef   `json:"artifacts"`
}
type Release struct {
	Retention              *MaterialRetention     `json:"retention,omitempty"`
	RequiresSemanticReview bool                   `json:"requires_semantic_review,omitempty"`
	ID                     string                 `json:"id"`
	Hash                   string                 `json:"hash"`
	Status                 string                 `json:"status"`
	Manifest               Manifest               `json:"manifest"`
	Contents               []ResourceUpdate       `json:"contents,omitempty"`
	ReviewTargets          []ArtifactReviewTarget `json:"review_targets,omitempty"`
}
