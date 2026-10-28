package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
)

// ArtifactOriginInput selects existing server records; clients cannot award
// qualification, choose an origin kind, or submit a review result here.
type ArtifactOriginInput struct {
	GenerationID    string `json:"generation_id,omitempty"`
	ParentReleaseID string `json:"parent_release_id,omitempty"`
}

// ArtifactReviewTarget is a read-only handoff to an independent review run.
// A future review must reload these references and match all hashes, not accept
// a client-supplied pass flag. It cannot use SourceGenerationID as its reviewer.
type ArtifactReviewTarget struct {
	ReleaseID          string `json:"release_id"`
	ManifestHash       string `json:"manifest_hash"`
	ResourceID         string `json:"resource_id"`
	BranchID           string `json:"branch_id"`
	ExpectedHead       string `json:"expected_head"`
	IPHead             string `json:"ip_head"`
	ContentHash        string `json:"content_hash"`
	SourceGenerationID string `json:"source_generation_id"`
}

// ArtifactOrigin is compiled by the release service and covered by the immutable
// manifest hash. Qualification applies to the original output, never an edit.
type ArtifactOrigin struct {
	Kind            string                      `json:"kind"`
	ActorID         string                      `json:"actor_id"`
	ResourceID      string                      `json:"resource_id"`
	BranchID        string                      `json:"branch_id"`
	BaseHead        string                      `json:"base_head"`
	IPHead          string                      `json:"ip_head"`
	ContentHash     string                      `json:"content_hash"`
	ParentReleaseID string                      `json:"parent_release_id,omitempty"`
	ParentHash      string                      `json:"parent_hash,omitempty"`
	ImportID        string                      `json:"import_id,omitempty"`
	ImportHash      string                      `json:"import_hash,omitempty"`
	Generation      *ArtifactGenerationEvidence `json:"generation,omitempty"`
	ReviewRequired  bool                        `json:"review_required"`
	Recovery        []ArtifactRecoveryEvidence  `json:"recovery,omitempty"`
}

// Recovery is historical attribution only. It never grants the old model,
// tool, price, qualification or review permission to execute in the copy.
// These are fingerprints and identities, not references to live source rows.
type ArtifactRecoveryEvidence struct {
	ProjectID      string `json:"project_id"`
	BranchID       string `json:"branch_id"`
	ReleaseID      string `json:"release_id"`
	ResourceID     string `json:"resource_id"`
	ManifestHash   string `json:"manifest_hash"`
	ContentHash    string `json:"content_hash"`
	OriginHash     string `json:"origin_hash"`
	OriginKnown    bool   `json:"origin_known"`
	GenerationHash string `json:"generation_hash,omitempty"`
}

func ArtifactEvidenceHash(value any) string {
	raw, _ := json.Marshal(value)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func (o ArtifactOrigin) HasGeneratedContent() bool {
	return o.Generation != nil || (len(o.Recovery) > 0 && !o.Recovery[len(o.Recovery)-1].OriginKnown)
}

type ArtifactGenerationEvidence struct {
	GenerationID    string `json:"generation_id"`
	CallID          string `json:"call_id"`
	InputHash       string `json:"input_hash"`
	OutputHash      string `json:"output_hash"`
	TextHash        string `json:"text_hash"`
	BaseIPHead      string `json:"base_ip_head"`
	Role            string `json:"role"`
	ConfigID        string `json:"config_id"`
	ConfigRevision  int64  `json:"config_revision"`
	ConfigHash      string `json:"config_hash"`
	Mode            string `json:"mode"`
	AgentVersionID  string `json:"agent_version_id,omitempty"`
	ManifestHash    string `json:"manifest_hash,omitempty"`
	BindingID       string `json:"binding_id,omitempty"`
	QualificationID string `json:"qualification_id,omitempty"`
	TaskKind        string `json:"task_kind,omitempty"`
	SuiteHash       string `json:"suite_hash,omitempty"`
}

// NeedsIndependentReview describes the current artifact, not the original
// execution. An unchanged qualified output is still only a shared-reference
// candidate; this is not a literary/novel quality certification.
func (o ArtifactOrigin) NeedsIndependentReview() bool {
	if len(o.Recovery) > 0 && o.HasGeneratedContent() {
		return true
	}
	return o.Generation != nil && (o.Generation.Mode != "QUALIFIED" || o.Generation.TextHash != o.ContentHash || o.Generation.BaseIPHead != o.IPHead)
}

func (o ArtifactOrigin) ValidateTarget(branch string, ref ArtifactRef) error {
	if o.ResourceID != ref.ResourceID || o.BranchID != branch || o.BaseHead != ref.ExpectedHead || o.ContentHash != ref.Hash {
		return ErrConflict
	}
	if o.ActorID == "" || !slices.Contains([]string{"manual", "import", "generation", "derived", "recovered"}, o.Kind) || !capabilityHash.MatchString(o.ContentHash) {
		return ErrValidation
	}
	if len(o.Recovery) > 128 || (o.Kind == "recovered" && len(o.Recovery) == 0) {
		return ErrValidation
	}
	for _, evidence := range o.Recovery {
		for _, id := range []string{evidence.ProjectID, evidence.BranchID, evidence.ReleaseID, evidence.ResourceID} {
			if !patchHex(id, 32) {
				return ErrValidation
			}
		}
		for _, hash := range []string{evidence.ManifestHash, evidence.ContentHash, evidence.OriginHash} {
			if !capabilityHash.MatchString(hash) {
				return ErrValidation
			}
		}
		if evidence.GenerationHash != "" && (!evidence.OriginKnown || !capabilityHash.MatchString(evidence.GenerationHash)) {
			return ErrValidation
		}
	}
	if len(o.Recovery) > 0 {
		latest := o.Recovery[len(o.Recovery)-1]
		if (o.Generation == nil) != (latest.GenerationHash == "") {
			return ErrConflict
		}
		if o.Generation != nil && ArtifactEvidenceHash(o.Generation) != latest.GenerationHash {
			return ErrConflict
		}
		if !latest.OriginKnown && o.Kind != "recovered" && o.Kind != "derived" {
			return ErrValidation
		}
	}
	if (o.ParentReleaseID == "") != (o.ParentHash == "") || (o.ImportID == "") != (o.ImportHash == "") {
		return ErrValidation
	}
	if o.ReviewRequired != o.NeedsIndependentReview() {
		return ErrConflict
	}
	if o.Generation != nil {
		if o.Kind != "generation" && o.Kind != "derived" {
			return ErrValidation
		}
		g := o.Generation
		if g.GenerationID == "" || g.CallID == "" || !capabilityHash.MatchString(g.InputHash) || !capabilityHash.MatchString(g.OutputHash) || !capabilityHash.MatchString(g.TextHash) || !capabilityHash.MatchString(g.ConfigHash) || !slices.Contains(ModelRoles(), g.Role) || !slices.Contains([]string{"QUALIFIED", "TRIAL", "UNQUALIFIED"}, g.Mode) {
			return ErrValidation
		}
		if o.Kind == "generation" && o.ContentHash != g.TextHash {
			return ErrConflict
		}
	} else if o.Kind == "generation" || (o.Kind == "manual" && o.ImportID != "") || (o.Kind == "import" && o.ImportID == "") {
		return ErrValidation
	}
	return nil
}
