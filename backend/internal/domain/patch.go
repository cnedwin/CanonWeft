package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

// PatchTarget is an immutable snapshot. Applying a patch produces a candidate;
// authorization, review and publication remain responsibilities of the service.
type PatchTarget struct {
	ResourceID string `json:"resource_id"`
	Revision   string `json:"revision"`
	Hash       string `json:"hash"`
	Text       string `json:"text"`
}

// TextPatch offsets count Unicode code points in the original snapshot, never
// bytes or positions in the result of an earlier patch.
type TextPatch struct {
	ID                string `json:"id"`
	GroupID           string `json:"group_id"`
	ResourceID        string `json:"resource_id"`
	BaseRevision      string `json:"base_revision"`
	BaseHash          string `json:"base_hash"`
	StartRune         int    `json:"start_rune"`
	EndRune           int    `json:"end_rune"`
	ExpectedSliceHash string `json:"expected_slice_hash"`
	Replacement       string `json:"replacement"`
	Reason            string `json:"reason"`
}

type TextPatchResult struct {
	Text    string      `json:"text"`
	Hash    string      `json:"hash"`
	Applied []TextPatch `json:"applied"`
}

const MaxPatchedTextBytes = 256 * 1024
const MaxTextPatches = 128

func patchHex(value string, length int) bool {
	if len(value) != length {
		return false
	}
	for _, c := range value {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func patchHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

// ApplyTextPatches validates the complete set before producing any changed text.
// It deliberately does not fuzzy-match anchors: a newer draft requires a newly
// prepared candidate, so the author's intervening edits cannot be overwritten.
func ApplyTextPatches(target PatchTarget, patches []TextPatch) (TextPatchResult, error) {
	fail := func(kind error, detail string) (TextPatchResult, error) {
		return TextPatchResult{}, fmt.Errorf("%w: %s", kind, detail)
	}
	if !patchHex(target.ResourceID, 32) || !patchHex(target.Revision, 32) || !patchHex(target.Hash, 64) || !utf8.ValidString(target.Text) || len(target.Text) > MaxPatchedTextBytes {
		return fail(ErrValidation, "invalid patch target")
	}
	if patchHash(target.Text) != target.Hash {
		return fail(ErrNotReady, "patch target integrity mismatch")
	}
	if len(patches) == 0 || len(patches) > MaxTextPatches {
		return fail(ErrValidation, "invalid patch count")
	}
	source := []rune(target.Text)
	ordered := append([]TextPatch(nil), patches...)
	seen := make(map[string]bool, len(patches))
	resultBytes := len(target.Text)
	for _, p := range ordered {
		if !patchHex(p.ID, 32) || !patchHex(p.GroupID, 32) || !patchHex(p.ResourceID, 32) || !patchHex(p.BaseRevision, 32) || !patchHex(p.BaseHash, 64) || !patchHex(p.ExpectedSliceHash, 64) || seen[p.ID] || p.GroupID != ordered[0].GroupID {
			return fail(ErrValidation, "invalid patch identity or group")
		}
		seen[p.ID] = true
		if !utf8.ValidString(p.Replacement) || !utf8.ValidString(p.Reason) || len(p.Replacement) > MaxPatchedTextBytes || strings.TrimSpace(p.Reason) == "" || utf8.RuneCountInString(p.Reason) > 2000 {
			return fail(ErrValidation, "invalid patch replacement or reason")
		}
		if p.ResourceID != target.ResourceID || p.BaseRevision != target.Revision || p.BaseHash != target.Hash {
			return fail(ErrConflict, "patch snapshot is stale or belongs to another resource")
		}
		if p.StartRune < 0 || p.EndRune < p.StartRune || p.EndRune > len(source) {
			return fail(ErrValidation, "invalid patch range")
		}
		original := string(source[p.StartRune:p.EndRune])
		if patchHash(original) != p.ExpectedSliceHash {
			return fail(ErrConflict, "patch anchor mismatch")
		}
		if original == p.Replacement {
			return fail(ErrValidation, "patch makes no change")
		}
		resultBytes += len(p.Replacement) - len(original)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].StartRune < ordered[j].StartRune })
	for i := 1; i < len(ordered); i++ {
		if ordered[i].StartRune < ordered[i-1].EndRune || ordered[i].StartRune == ordered[i-1].StartRune {
			return fail(ErrValidation, "overlapping or ambiguous patch ranges")
		}
	}
	if resultBytes > MaxPatchedTextBytes {
		return fail(ErrValidation, "patched text exceeds limit")
	}
	var out strings.Builder
	out.Grow(resultBytes)
	position := 0
	for _, p := range ordered {
		out.WriteString(string(source[position:p.StartRune]))
		out.WriteString(p.Replacement)
		position = p.EndRune
	}
	out.WriteString(string(source[position:]))
	result := out.String()
	if result == target.Text {
		return fail(ErrValidation, "patch set makes no change")
	}
	return TextPatchResult{Text: result, Hash: patchHash(result), Applied: ordered}, nil
}
