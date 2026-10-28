package retention

import (
	"context"
	"encoding/json"
	"unicode/utf8"

	"novelstudio/internal/domain"
	"novelstudio/internal/retrieval"
)

const textCopyParser = "unicode-512-overlap32-v1"

// A compact certificate of the exact source split. Contains no text or vector;
// it proves literal copies only, never model quality or complete lineage.
type TextCopyEvidence struct {
	Schema     int    `json:"schema"`
	Parser     string `json:"parser"`
	SourceHash string `json:"source_hash"`
	Runes      int    `json:"runes"`
	Chunks     int    `json:"chunks"`
	Digest     string `json:"digest"`
}

type textSegmentProof struct {
	Start int    `json:"start"`
	End   int    `json:"end"`
	Hash  string `json:"hash"`
}

func splitProofDigest(source string, runes int, parts []textSegmentProof) string {
	return stableHash(struct {
		Parser string             `json:"parser"`
		Source string             `json:"source"`
		Runes  int                `json:"runes"`
		Parts  []textSegmentProof `json:"parts"`
	}{textCopyParser, source, runes, parts})
}

func textCopyEvidence(text string) (*TextCopyEvidence, error) {
	if len(text) > 32<<20 {
		return nil, ErrSourceOwnership
	}
	chunks, err := retrieval.Split(text)
	if err != nil {
		return nil, ErrSourceOwnership
	}
	parts := make([]textSegmentProof, 0, len(chunks))
	for _, chunk := range chunks {
		parts = append(parts, textSegmentProof{chunk.Start, chunk.End, financialDigest([]byte(chunk.Text))})
	}
	hash, runes := financialDigest([]byte(text)), utf8.RuneCountInString(text)
	return &TextCopyEvidence{1, textCopyParser, hash, runes, len(parts), splitProofDigest(hash, runes, parts)}, nil
}

func validTextCopyEvidence(v ReferenceVersion) bool {
	p := v.TextProof
	if p == nil {
		return true // Old observations are readable, not retroactively certified.
	}
	if p.Schema != 1 || p.Parser != textCopyParser || !hash64.MatchString(p.SourceHash) || !hash64.MatchString(p.Digest) || p.Runes < 0 || p.Runes > 32<<20 || p.Chunks < 0 {
		return false
	}
	windows := 0
	if p.Runes > 0 {
		windows = 1
		if p.Runes > 512 {
			windows += (p.Runes - 512 + 479) / 480
		}
	}
	if p.Chunks > windows {
		return false
	}
	if v.WorkingProjection != nil {
		return p.SourceHash == v.WorkingProjection.Hash
	}
	return (v.Table == "resource_drafts" || v.Table == "resource_policy_drafts") && v.Field == draftField(v.Table) && v.DraftVersion != nil && p.SourceHash == v.DraftVersion.ProjectionHash
}

func (index *SourceIndex) attachDraftTextProofs(materials []MaterialReference) error {
	for i := range materials {
		m := &materials[i]
		if m.DraftVersion == nil || (m.Table != "resource_drafts" && m.Table != "resource_policy_drafts") {
			continue
		}
		text := index.rows[m.Table][m.OwnerKey].Values[m.Field]
		if m.Table == "resource_policy_drafts" {
			var policy *domain.KnowledgePolicy
			if authorDocument(text, &policy) != nil {
				return ErrSourceOwnership
			}
			raw, err := json.Marshal(policy)
			if err != nil {
				return err
			}
			text = string(raw)
		}
		proof, err := textCopyEvidence(text)
		if err != nil || proof.SourceHash != m.DraftVersion.ProjectionHash {
			return ErrSourceOwnership
		}
		m.TextProof = proof
	}
	return nil
}

// Shared within one scan: both published and private indexes may use the same
// object. Caps apply to unique bytes across both checks, not per reference.
func boundedSourceReader(project string, read ReadSourceObject) ReadSourceObject {
	cache := map[string][]byte{}
	total := 0
	return func(ctx context.Context, owner, hash string) ([]byte, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if owner != project || !hash64.MatchString(hash) {
			return nil, ErrSourceOwnership
		}
		if data, ok := cache[hash]; ok {
			return data, nil
		}
		if read == nil {
			return nil, ErrSourceOwnership
		}
		data, err := read(ctx, owner, hash)
		if err != nil || len(data) > 32<<20 || len(data) > (64<<20)-total || financialDigest(data) != hash {
			return nil, ErrSourceOwnership
		}
		cache[hash] = data
		total += len(data)
		return data, nil
	}
}

func (index *SourceIndex) attachArtifactTextProofs(ctx context.Context, project string, materials []ObservedMaterial, read ReadSourceObject) error {
	proofs := map[string]*TextCopyEvidence{}
	for i := range materials {
		m := &materials[i]
		if m.WorkingProjection == nil || m.WorkingProjection.Kind != "release_artifact" {
			continue
		}
		row := index.rows[m.Table][m.OwnerKey]
		var manifest domain.Manifest
		if contentJSON([]byte(row.Values["manifest"]), &manifest, row.Values["content_hash"]) != nil {
			return ErrSourceOwnership
		}
		size := int64(-1)
		for _, artifact := range manifest.Artifacts {
			field, err := ArtifactField(artifact.ResourceID, false)
			if err == nil && field == m.Field && artifact.Hash == m.ObjectHash {
				size = artifact.Size
				break
			}
		}
		data, err := read(ctx, project, m.ObjectHash)
		if err != nil || int64(len(data)) != size {
			return ErrSourceOwnership
		}
		// Binary author artifacts stay preserved. They cannot certify a text
		// copy and must not be silently decoded with replacement characters.
		if !utf8.Valid(data) {
			continue
		}
		proof, ok := proofs[m.ObjectHash]
		if !ok {
			proof, err = textCopyEvidence(string(data))
			if err != nil {
				return err
			}
			proofs[m.ObjectHash] = proof
		}
		m.TextProof = proof
	}
	return nil
}
