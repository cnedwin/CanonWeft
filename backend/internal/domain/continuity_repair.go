package domain

import (
	"encoding/json"
	"io"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

type continuityEdit struct {
	ResourceID   string  `json:"resource_id"`
	BaseRevision string  `json:"base_revision"`
	BaseHash     string  `json:"base_hash"`
	StartRune    *int    `json:"start_rune"`
	EndRune      *int    `json:"end_rune"`
	ExpectedText *string `json:"expected_text"`
	Replacement  *string `json:"replacement"`
	Reason       string  `json:"reason"`
}

// ContinuityRepair is a checked preview, never a coverage certificate or an
// author decision. Unaddressed includes other branches and non-text impacts.
type ContinuityRepair struct {
	DependencyOptions      []RepairDependencyOption   `json:"dependency_options,omitempty"`
	Decisions              []RepairDependencyDecision `json:"decisions,omitempty"`
	BranchID               string                     `json:"branch_id"`
	BaseHead               string                     `json:"base_head"`
	SourceReleaseID        string                     `json:"source_release_id,omitempty"`
	Preserved              []RepairPreservedArtifact  `json:"preserved,omitempty"`
	Conflicts              []RepairConflict           `json:"conflicts,omitempty"`
	SaveBlockedReason      string                     `json:"save_blocked_reason,omitempty"`
	GenerationID           string                     `json:"generation_id"`
	PlanID                 string                     `json:"plan_id"`
	Summary                string                     `json:"summary"`
	Group                  TextPatchGroup             `json:"group"`
	Items                  []PatchedResource          `json:"items"`
	Unaddressed            []ImpactItem               `json:"unaddressed"`
	Untracked              []DependencyRef            `json:"untracked"`
	OutsideRecordedClosure []DependencyRef            `json:"outside_recorded_closure"`
}

type RepairDependencyDecision struct {
	ResourceID string        `json:"resource_id"`
	Source     DependencyRef `json:"source"`
	Kind       string        `json:"kind"`
	Action     string        `json:"action"`
	Reason     string        `json:"reason"`
}

type RepairDependencyOption struct {
	ResourceID string        `json:"resource_id"`
	Name       string        `json:"name"`
	Source     DependencyRef `json:"source"`
	Kind       string        `json:"kind"`
	Deleted    bool          `json:"deleted"`
}

func ContinuityRepairGroupID(generation string) string {
	return patchHash("continuity-repair-v1\x00" + generation)[:32]
}

// CompileContinuityRepair never trusts model-supplied approval, group identity,
// slice hashes or coverage claims. Offsets refer to the original Unicode text.
func CompileContinuityRepair(text, generation string, source ChangeReviewContext) (ContinuityRepair, error) {
	result := ContinuityRepair{GenerationID: generation, PlanID: source.Plan.ID, Items: []PatchedResource{}, Unaddressed: []ImpactItem{}, Untracked: source.Plan.Untracked, OutsideRecordedClosure: source.Plan.OutsideRecordedClosure}
	result.BranchID, result.BaseHead = source.Plan.BranchID, source.Plan.BaseHead
	if source.FocusBranchID != "" {
		result.BranchID, result.BaseHead = source.FocusBranchID, source.FocusBaseHead
	}
	if !patchHex(generation, 32) || len(text) > 1024*1024 || !utf8.ValidString(text) {
		return result, ErrValidation
	}
	var output struct {
		Schema  int              `json:"schema"`
		Status  string           `json:"status"`
		PlanID  string           `json:"plan_id"`
		Summary string           `json:"summary"`
		Patches []continuityEdit `json:"patches"`
	}
	decoder := json.NewDecoder(strings.NewReader(text))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&output) != nil || decoder.Decode(new(any)) != io.EOF || output.Schema != 1 || output.Status != "CANDIDATE" || output.PlanID != source.Plan.ID || output.Patches == nil || len(output.Patches) > MaxTextPatches || strings.TrimSpace(output.Summary) == "" || utf8.RuneCountInString(output.Summary) > 4000 {
		return result, ErrValidation
	}
	result.Summary = output.Summary
	result.Group = TextPatchGroup{ID: ContinuityRepairGroupID(generation), Members: []string{}, Patches: []TextPatch{}}
	allowed := map[string]bool{}
	for _, item := range source.Plan.Items {
		if item.Node.Kind == "resource" && item.Node.BranchID == result.BranchID {
			allowed[item.Node.ID] = true
		}
	}
	targets := map[string]ChangeReviewTarget{}
	for _, target := range source.Targets {
		targets[target.Ref.ID] = target
	}
	used := map[string]bool{}
	for i, edit := range output.Patches {
		target, ok := targets[edit.ResourceID]
		if !ok || !allowed[edit.ResourceID] || target.Ref.BranchID != result.BranchID || edit.BaseRevision != target.Ref.Revision || edit.BaseHash != target.BodyHash {
			return result, ErrConflict
		}
		runes := []rune(target.Text)
		if edit.StartRune == nil || edit.EndRune == nil || edit.ExpectedText == nil || edit.Replacement == nil || *edit.StartRune < 0 || *edit.EndRune < *edit.StartRune || *edit.EndRune > len(runes) {
			return result, ErrValidation
		}
		if string(runes[*edit.StartRune:*edit.EndRune]) != *edit.ExpectedText {
			return result, ErrConflict
		}
		result.Group.Patches = append(result.Group.Patches, TextPatch{ID: patchHash(result.Group.ID + ":" + strconv.Itoa(i))[:32], GroupID: result.Group.ID, ResourceID: edit.ResourceID, BaseRevision: edit.BaseRevision, BaseHash: edit.BaseHash, StartRune: *edit.StartRune, EndRune: *edit.EndRune, ExpectedSliceHash: patchHash(*edit.ExpectedText), Replacement: *edit.Replacement, Reason: edit.Reason})
		used[edit.ResourceID] = true
	}
	for _, item := range source.Plan.Items {
		if item.Node.Kind != "resource" || item.Node.BranchID != result.BranchID || !used[item.Node.ID] {
			result.Unaddressed = append(result.Unaddressed, item)
		}
	}
	if len(used) == 0 {
		return result, nil
	}
	for id := range used {
		result.Group.Members = append(result.Group.Members, id)
	}
	sort.Strings(result.Group.Members)
	base := make([]PatchTarget, 0, len(used))
	for _, id := range result.Group.Members {
		t := targets[id]
		base = append(base, PatchTarget{ResourceID: id, Revision: t.Ref.Revision, Hash: t.BodyHash, Text: t.Text})
	}
	var err error
	result.Items, err = ApplyTextPatchGroup(base, result.Group)
	return result, err
}

type RepairPreservedArtifact struct {
	ResourceID string `json:"resource_id"`
	Name       string `json:"name"`
	Text       string `json:"text"`
	Merged     bool   `json:"merged"`
}

type RepairConflict struct {
	ResourceID string `json:"resource_id"`
	Name       string `json:"name"`
	Original   string `json:"original"`
	Proposed   string `json:"proposed"`
	AIText     string `json:"ai_text"`
}
