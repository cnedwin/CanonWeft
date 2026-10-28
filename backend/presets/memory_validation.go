package presets

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"

	"novelstudio/internal/domain"
)

type memoryScope struct {
	BranchID      string   `json:"branch_id"`
	StoryTime     int      `json:"story_time"`
	KnownFrom     int      `json:"known_from"`
	PerspectiveID string   `json:"perspective_id"`
	Visibility    string   `json:"visibility"`
	KnownBy       []string `json:"known_by"`
}

type memoryAnchor struct {
	ResourceID string `json:"resource_id"`
	Revision   string `json:"revision"`
	Hash       string `json:"hash"`
	Start      int    `json:"start_rune"`
	End        int    `json:"end_rune"`
	Quote      string `json:"quote"`
}

type memorySource struct {
	Kind       string      `json:"kind"`
	Control    bool        `json:"control"`
	ResourceID string      `json:"resource_id"`
	Revision   string      `json:"revision"`
	Hash       string      `json:"hash"`
	Text       string      `json:"text"`
	Scope      memoryScope `json:"scope"`
}

type fidelityRequirement struct {
	ID       string         `json:"id"`
	Category string         `json:"category"`
	Evidence []memoryAnchor `json:"source_evidence"`
}

type memoryContractInput struct {
	SummaryTarget *struct {
		Level string `json:"level"`
	} `json:"summary_target"`
	ProjectID      string                `json:"project_id"`
	BaseRevision   string                `json:"base_revision"`
	Scope          memoryScope           `json:"scope"`
	Sources        []memorySource        `json:"sources"`
	TargetComplete bool                  `json:"target_complete"`
	TargetIDs      []string              `json:"target_resource_ids"`
	Requirements   []fidelityRequirement `json:"requirements"`
}

type memoryCandidate struct {
	Kind         string         `json:"kind"`
	SummaryLevel string         `json:"summary_level"`
	Context      string         `json:"context"`
	ID           string         `json:"id"`
	Scope        memoryScope    `json:"scope"`
	Evidence     []memoryAnchor `json:"evidence"`
}

type fidelityCheck struct {
	RequirementID     string         `json:"requirement_id"`
	Category          string         `json:"category"`
	Result            string         `json:"result"`
	SourceEvidence    []memoryAnchor `json:"source_evidence"`
	TargetEvidence    []memoryAnchor `json:"target_evidence"`
	SearchedTargetIDs []string       `json:"searched_target_ids"`
	Link              *struct {
		SetupID  string `json:"setup_requirement_id"`
		PayoffID string `json:"payoff_requirement_id"`
	} `json:"setup_payoff_link"`
}

// ValidateMemoryProposal checks a frozen input/output pair without any model or
// database access. Callers must separately prove authorization and that sources
// match authoritative immutable revisions. Success never confirms a proposal.
func ValidateMemoryProposal(name string, input, output json.RawMessage) error {
	if name != "memory-consolidation" && name != "adaptation-fidelity-review" {
		return domain.ErrValidation
	}
	bundle, err := Bundle(name)
	if err != nil {
		return err
	}
	for _, item := range []struct {
		kind string
		raw  json.RawMessage
	}{{"input", input}, {"output", output}} {
		schema, err := domain.CompileCapabilitySchema(bundle.Find(item.kind + ".schema.json"))
		if err != nil {
			return err
		}
		var value any
		if err := json.Unmarshal(item.raw, &value); err != nil {
			return err
		}
		if err := schema.Validate(value); err != nil {
			return fmt.Errorf("%s: %w", item.kind, err)
		}
	}
	var in memoryContractInput
	var out struct {
		ProjectID    string            `json:"project_id"`
		BaseRevision string            `json:"base_revision"`
		Scope        memoryScope       `json:"scope"`
		Candidates   []memoryCandidate `json:"candidates"`
		Findings     []struct {
			CandidateIDs []string       `json:"candidate_ids"`
			Evidence     []memoryAnchor `json:"evidence"`
		} `json:"findings"`
		Verdict string          `json:"verdict"`
		Checks  []fidelityCheck `json:"checks"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return err
	}
	if err := json.Unmarshal(output, &out); err != nil {
		return err
	}
	if in.ProjectID != out.ProjectID || in.BaseRevision != out.BaseRevision || !reflect.DeepEqual(in.Scope, out.Scope) || !scopeContains(in.Scope, in.Scope) {
		return fmt.Errorf("snapshot or output scope mismatch: %w", domain.ErrValidation)
	}
	sources := make(map[string]memorySource, len(in.Sources))
	for _, source := range in.Sources {
		digest := sha256.Sum256([]byte(source.Text))
		_, exists := sources[source.ResourceID]
		if exists || hex.EncodeToString(digest[:]) != source.Hash || !scopeContains(in.Scope, source.Scope) {
			return fmt.Errorf("invalid or invisible source: %w", domain.ErrValidation)
		}
		sources[source.ResourceID] = source
	}
	validateEvidence := func(anchors []memoryAnchor, scope memoryScope) error {
		for _, anchor := range anchors {
			source, ok := sources[anchor.ResourceID]
			runes := []rune(source.Text)
			if !ok || anchor.Revision != source.Revision || anchor.Hash != source.Hash || anchor.Start < 0 || anchor.End <= anchor.Start || anchor.End > len(runes) || string(runes[anchor.Start:anchor.End]) != anchor.Quote {
				return fmt.Errorf("source revision/hash/rune mismatch: %w", domain.ErrValidation)
			}
			if !inheritsScope(scope, source.Scope) {
				return fmt.Errorf("proposal widens source visibility/time: %w", domain.ErrValidation)
			}
		}
		return nil
	}
	ids := map[string]bool{}
	for _, candidate := range out.Candidates {
		if ids[candidate.ID] || !scopeContains(in.Scope, candidate.Scope) {
			return fmt.Errorf("duplicate candidate or invalid scope: %w", domain.ErrValidation)
		}
		ids[candidate.ID] = true
		if err := validateEvidence(candidate.Evidence, candidate.Scope); err != nil {
			return err
		}
		if name == "memory-consolidation" && in.SummaryTarget != nil {
			if candidate.Kind != "summary" || candidate.SummaryLevel != in.SummaryTarget.Level || candidate.Scope.StoryTime != in.Scope.StoryTime || (candidate.Context != "reality" && candidate.Context != "plan") {
				return fmt.Errorf("summary target mismatch: %w", domain.ErrValidation)
			}
			covered := map[string]bool{}
			for _, anchor := range candidate.Evidence {
				covered[anchor.ResourceID] = true
			}
			for _, source := range in.Sources {
				if !covered[source.ResourceID] || ((source.Control || source.Kind == "planning") && candidate.Context != "plan") {
					return fmt.Errorf("summary omits source or promotes plan: %w", domain.ErrValidation)
				}
			}
		}
	}
	for _, finding := range out.Findings {
		for _, id := range finding.CandidateIDs {
			if !ids[id] {
				return fmt.Errorf("unknown finding candidate: %w", domain.ErrValidation)
			}
		}
		if err := validateEvidence(finding.Evidence, out.Scope); err != nil {
			return err
		}
	}
	if name == "memory-consolidation" {
		return nil
	}
	requirements := map[string]fidelityRequirement{}
	categories := map[string]bool{}
	for _, req := range in.Requirements {
		if _, exists := requirements[req.ID]; exists {
			return domain.ErrValidation
		}
		requirements[req.ID] = req
		categories[req.Category] = true
		if err := validateEvidence(req.Evidence, in.Scope); err != nil {
			return err
		}
		for _, anchor := range req.Evidence {
			if slices.Contains(in.TargetIDs, anchor.ResourceID) {
				return domain.ErrValidation
			}
		}
	}
	for _, id := range in.TargetIDs {
		if _, ok := sources[id]; !ok {
			return domain.ErrValidation
		}
	}
	seen := map[string]bool{}
	failed := false
	for _, check := range out.Checks {
		req, ok := requirements[check.RequirementID]
		if !ok || seen[req.ID] || check.Category != req.Category || !sameStringSet(in.TargetIDs, check.SearchedTargetIDs) {
			return fmt.Errorf("incomplete or duplicate coverage: %w", domain.ErrValidation)
		}
		seen[req.ID] = true
		if err := validateEvidence(check.SourceEvidence, out.Scope); err != nil {
			return err
		}
		if err := validateEvidence(check.TargetEvidence, out.Scope); err != nil {
			return err
		}
		for _, anchor := range check.SourceEvidence {
			if !slices.Contains(req.Evidence, anchor) {
				return domain.ErrValidation
			}
		}
		for _, anchor := range check.TargetEvidence {
			if !slices.Contains(in.TargetIDs, anchor.ResourceID) {
				return domain.ErrValidation
			}
		}
		if check.Result == "MISSING" && !in.TargetComplete {
			return domain.ErrValidation
		}
		if check.Result == "MISSING" || check.Result == "CHANGED" {
			failed = true
		}
		if check.Link != nil {
			if requirements[check.Link.SetupID].Category != "highlight_setup" || requirements[check.Link.PayoffID].Category != "highlight_payoff" || (check.Category == "highlight_setup" && check.Link.SetupID != req.ID) || (check.Category == "highlight_payoff" && check.Link.PayoffID != req.ID) {
				return fmt.Errorf("invalid setup/payoff pair: %w", domain.ErrValidation)
			}
		}
	}
	if len(seen) != len(requirements) || (out.Verdict == "PASS" && (!in.TargetComplete || len(categories) != 6)) || (out.Verdict == "FAIL" && !failed) {
		return fmt.Errorf("unsupported overall verdict: %w", domain.ErrValidation)
	}
	return nil
}

func sameStringSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for _, value := range a {
		if !slices.Contains(b, value) {
			return false
		}
	}
	return true
}

// Input scope is the authorized audience and retrieval cutoff, not permission
// to broaden evidence. KnownFrom is independent of story-time (e.g. flashbacks).
func scopeContains(request, item memoryScope) bool {
	return request.BranchID == item.BranchID && request.PerspectiveID == item.PerspectiveID && item.StoryTime <= request.StoryTime && item.KnownFrom <= request.StoryTime && (item.Visibility == "public" || slices.Contains(item.KnownBy, request.PerspectiveID))
}

func inheritsScope(proposal, source memoryScope) bool {
	rank := map[string]int{"public": 0, "restricted": 1, "private": 2}
	if proposal.BranchID != source.BranchID || proposal.PerspectiveID != source.PerspectiveID || proposal.KnownFrom < source.KnownFrom || rank[proposal.Visibility] < rank[source.Visibility] {
		return false
	}
	if source.Visibility != "public" {
		for _, viewer := range proposal.KnownBy {
			if !slices.Contains(source.KnownBy, viewer) {
				return false
			}
		}
	}
	return true
}
