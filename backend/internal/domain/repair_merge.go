package domain

// MergeContinuityRepair transports checked AI edits through the author's
// unpublished candidate. It never fuzzy-matches text or wins an overlap for AI.
func MergeContinuityRepair(base PatchTarget, proposed string, patches []TextPatch) (string, error) {
	ai, err := ApplyTextPatches(base, patches)
	if err != nil {
		return "", err
	}
	if !validCompensationText(proposed) {
		return "", ErrValidation
	}
	if proposed == base.Text {
		return ai.Text, nil
	}
	if proposed == ai.Text {
		return proposed, nil
	}
	rebased := append([]TextPatch(nil), patches...)
	for i, patch := range patches {
		edit := TextCompensation{StartRune: patch.StartRune, EndRune: patch.EndRune, Expected: string([]rune(base.Text)[patch.StartRune:patch.EndRune]), Replacement: patch.Replacement}
		edit, err = RebaseTextCompensation(edit, base.Text, proposed)
		if err != nil {
			return "", err
		}
		rebased[i].StartRune, rebased[i].EndRune = edit.StartRune, edit.EndRune
		rebased[i].BaseHash = patchHash(proposed)
	}
	target := base
	target.Text, target.Hash = proposed, patchHash(proposed)
	merged, err := ApplyTextPatches(target, rebased)
	return merged.Text, err
}
