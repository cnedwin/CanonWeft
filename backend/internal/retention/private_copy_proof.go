package retention

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"novelstudio/internal/domain"
	"novelstudio/internal/retrieval"
)

// Called with the locked SQL snapshot and the exact verified version closure,
// before any reference writes. Missing old evidence blocks cleanup; malformed
// copies abort the entire registration transaction. Neither grants read access.
func (index *SourceIndex) verifyPrivateCopies(ctx context.Context, project string, roots map[string]ReferenceVersion, history map[referenceVersionKey]ReferenceVersion) (int, error) {
	chunks := map[string]map[string][]SourceRow{}
	for _, row := range index.rows["working_chunks"] {
		id, key := row.Values["index_id"], row.Values["source_key"]
		if chunks[id] == nil {
			chunks[id] = map[string][]SourceRow{}
		}
		chunks[id][key] = append(chunks[id][key], row)
	}
	// The full text digest permits a literal proof to be shared across records.
	// It cannot invent an old edit/version: the exact business pin below must
	// already exist and match before this fallback is considered.
	proofs := map[string]*TextCopyEvidence{}
	for _, v := range history {
		if v.ProjectID != project || !validTextCopyEvidence(v) {
			return 0, ErrInvalidInventory
		}
		if p := v.TextProof; p != nil {
			if old, ok := proofs[p.SourceHash]; ok && *old != *p {
				return 0, ErrInvalidInventory
			}
			proofs[p.SourceHash] = p
		}
	}
	missing := 0
	for _, row := range index.rows["working_indexes"] {
		if row.Values["project_id"] != project {
			continue
		}
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		if index.replacedColumn(project, row, "manifest") {
			count := 0
			for _, partRows := range chunks[row.Values["id"]] {
				for _, chunk := range partRows {
					for _, field := range []string{"content", "vector", "source_name"} {
						if !index.replacedColumn(project, chunk, field) {
							return 0, ErrSourceOwnership
						}
					}
					count++
				}
			}
			if row.Values["chunks"] != strconv.Itoa(count) {
				return 0, ErrSourceOwnership
			}
			continue
		}
		var manifest domain.PrivateManifest
		if authorDocument(row.Values["manifest"], &manifest) != nil || stableHash(manifest) != row.Values["id"] || manifest.Project != project || manifest.Branch != row.Values["branch_id"] || manifest.Actor != row.Values["actor_id"] || manifest.Model != row.Values["model_revision"] || manifest.Parser != "private-"+textCopyParser || len(manifest.Sources) > 4096 {
			return 0, ErrSourceOwnership
		}
		key, err := RowOwnerKey(row.Table, row.Values)
		if err != nil {
			return 0, err
		}
		id, err := ReferenceID(project, row.Table, key, "manifest")
		current, ok := roots[id]
		if err != nil || !ok || current.Reference.PayloadHash != financialDigest([]byte(row.Values["manifest"])) {
			return 0, ErrInvalidInventory
		}
		pins := map[string]ReferenceSource{}
		for _, pin := range current.Reference.Sources {
			pins[pin.ReferenceID] = pin
		}
		seen := map[string]bool{}
		total := 0
		for _, pin := range manifest.Sources {
			if err := ctx.Err(); err != nil {
				return 0, err
			}
			sourceKey := stableHash(pin)
			if seen[sourceKey] {
				return 0, ErrSourceOwnership
			}
			seen[sourceKey] = true
			source, err := index.privateInputSource(project, manifest, pin)
			if err != nil {
				return 0, err
			}
			var proof *TextCopyEvidence
			if ref, pinned := pins[source.ReferenceID]; pinned {
				parent, ok := history[pinKey(ref)]
				if !ok || parent.Reference.PayloadHash != ref.PayloadHash || !sourceRequestMatches(source, parent.DraftVersion, parent.WorkingProjection, parent.Reference.PayloadHash) {
					return 0, ErrInvalidInventory
				}
				proof = proofs[pin.Hash]
				if proof == nil {
					missing++
				}
			}
			// An absent pin is already counted by prepareMaterialVersions.
			rows := chunks[row.Values["id"]][sourceKey]
			erased := map[string]ExpiredProjection{}
			for _, chunk := range rows {
				owner, err := RowOwnerKey(chunk.Table, chunk.Values)
				if err != nil {
					return 0, err
				}
				for _, field := range []string{"content", "vector", "source_name"} {
					id, err := ReferenceID(project, chunk.Table, owner, field)
					if err != nil {
						return 0, err
					}
					if p, ok := index.expiryProofs[id]; ok {
						erased[chunk.Values["id"]+"."+field] = p
					}
				}
			}
			if err := verifyPrivatePart(row.Values["id"], sourceKey, pin, rows, proof, erased); err != nil {
				return 0, err
			}
			total += len(rows)
		}
		for key := range chunks[row.Values["id"]] {
			if !seen[key] {
				return 0, ErrSourceOwnership
			}
		}
		if row.Values["chunks"] != strconv.Itoa(total) {
			return 0, ErrSourceOwnership
		}
	}
	return missing, nil
}

func verifyPrivatePart(indexID, sourceKey string, pin domain.PrivatePin, rows []SourceRow, proof *TextCopyEvidence, verified ...map[string]ExpiredProjection) error {
	erased := map[string]ExpiredProjection{}
	if len(verified) > 0 {
		erased = verified[0]
	}
	parts := make([]textSegmentProof, 0, len(rows))
	for _, row := range rows {
		v := row.Values
		start, startErr := strconv.Atoi(v["start_rune"])
		end, endErr := strconv.Atoi(v["end_rune"])
		if startErr != nil || endErr != nil || start < 0 || start > 32<<20 || end <= start || end-start > 512 || start%480 != 0 || strconv.Itoa(start) != v["start_rune"] || strconv.Itoa(end) != v["end_rune"] {
			return ErrSourceOwnership
		}
		id := financialDigest([]byte(fmt.Sprintf("%s:%s:%d", indexID, sourceKey, start)))
		if v["id"] != id || v["index_id"] != indexID || v["source_key"] != sourceKey || v["source_hash"] != pin.Hash || v["ref_kind"] != pin.Ref.Kind || v["ref_id"] != pin.Ref.ID || v["ref_version"] != pin.Ref.Version || v["part_number"] != strconv.Itoa(pin.Part) {
			return ErrSourceOwnership
		}
		textHash := financialDigest([]byte(v["content"]))
		if p, ok := erased[id+".content"]; ok {
			textHash = p.OriginalPayloadHash
		} else if !utf8.ValidString(v["content"]) || utf8.RuneCountInString(v["content"]) != end-start || strings.TrimSpace(v["content"]) == "" {
			return ErrSourceOwnership
		}
		if p, ok := erased[id+".source_name"]; ok {
			if p.OriginalPayloadHash != financialDigest([]byte(pin.Name)) {
				return ErrSourceOwnership
			}
		} else if v["source_name"] != pin.Name {
			return ErrSourceOwnership
		}
		if p, ok := erased[id+".vector"]; ok {
			if p.OriginalPayloadHash != v["vector_hash"] {
				return ErrSourceOwnership
			}
		} else {
			if financialDigest([]byte(v["vector"])) != v["vector_hash"] {
				return ErrSourceOwnership
			}
			if _, err := retrieval.UnpackVector([]byte(v["vector"])); err != nil {
				return ErrSourceOwnership
			}
		}
		parts = append(parts, textSegmentProof{start, end, textHash})
	}
	sort.Slice(parts, func(i, j int) bool { return parts[i].Start < parts[j].Start })
	for i, part := range parts {
		if i > 0 && parts[i-1].Start == part.Start {
			return ErrSourceOwnership
		}
		if proof != nil && part.End != min(part.Start+512, proof.Runes) {
			return ErrSourceOwnership
		}
	}
	if proof != nil && (proof.SourceHash != pin.Hash || proof.Chunks != len(parts) || proof.Digest != splitProofDigest(pin.Hash, proof.Runes, parts)) {
		return ErrSourceOwnership
	}
	return nil
}
