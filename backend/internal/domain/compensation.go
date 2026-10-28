package domain

import "unicode/utf8"

type TextCompensation struct {
	StartRune   int    `json:"start_rune"`
	EndRune     int    `json:"end_rune"`
	Expected    string `json:"expected"`
	Replacement string `json:"replacement"`
}

// textDifference encloses every changed code point in one interval. This is
// deliberately conservative: disjoint edits in one historical save can force a
// manual conflict, but cannot be mistaken for permission to erase later work.
func textDifference(before, after []rune) (int, int, int) {
	start := 0
	for start < len(before) && start < len(after) && before[start] == after[start] {
		start++
	}
	oldEnd, newEnd := len(before), len(after)
	for oldEnd > start && newEnd > start && before[oldEnd-1] == after[newEnd-1] {
		oldEnd--
		newEnd--
	}
	return start, oldEnd, newEnd
}

func validCompensationText(text string) bool {
	return utf8.ValidString(text) && len(text) <= MaxPatchedTextBytes
}

// PlanTextCompensation describes the inverse of one published text change.
// It is a candidate only; the caller must pin the exact published ancestry.
func PlanTextCompensation(original, published string) (TextCompensation, error) {
	if !validCompensationText(original) || !validCompensationText(published) || original == published {
		return TextCompensation{}, ErrValidation
	}
	before, after := []rune(original), []rune(published)
	start, oldEnd, newEnd := textDifference(before, after)
	return TextCompensation{StartRune: start, EndRune: newEnd, Expected: string(after[start:newEnd]), Replacement: string(before[start:oldEnd])}, nil
}

// RebaseTextCompensation transports an inverse range through exactly one
// confirmed later revision. No substring searching or fuzzy matching is used.
// Intersecting edits and ambiguous insertions require an explicit human repair.
func RebaseTextCompensation(edit TextCompensation, previous, next string) (TextCompensation, error) {
	if !validCompensationText(previous) || !validCompensationText(next) || !validCompensationText(edit.Replacement) {
		return TextCompensation{}, ErrValidation
	}
	before, after := []rune(previous), []rune(next)
	if edit.StartRune < 0 || edit.EndRune < edit.StartRune || edit.EndRune > len(before) || string(before[edit.StartRune:edit.EndRune]) != edit.Expected {
		return TextCompensation{}, ErrNotReady
	}
	if previous == next {
		return edit, nil
	}
	start, oldEnd, newEnd := textDifference(before, after)
	if (edit.StartRune == edit.EndRune && start <= edit.StartRune && oldEnd >= edit.StartRune) || (start == oldEnd && start >= edit.StartRune && start <= edit.EndRune) {
		return TextCompensation{}, ErrConflict
	}
	if oldEnd <= edit.StartRune {
		shift := newEnd - oldEnd
		edit.StartRune += shift
		edit.EndRune += shift
	} else if start < edit.EndRune {
		return TextCompensation{}, ErrConflict
	}
	if edit.StartRune < 0 || edit.EndRune > len(after) || string(after[edit.StartRune:edit.EndRune]) != edit.Expected {
		return TextCompensation{}, ErrNotReady
	}
	return edit, nil
}
