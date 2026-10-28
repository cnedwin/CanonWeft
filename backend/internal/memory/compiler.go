// Package memory compiles source-attributed task context. It does not authorize
// access or publish facts: callers must load an authorized immutable snapshot and
// recheck its current permission, revocation and version pins before every use.
package memory

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"regexp"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"
)

const Version = "creative-memory-v1"

var (
	ErrInvalid  = errors.New("记忆输入或来源不完整")
	ErrCoverage = errors.New("必需记忆或前置证据不可用；请刷新来源或调整任务")
	ErrCycle    = errors.New("记忆的证据前置关系成环")
	ErrBudget   = errors.New("必要记忆超过上下文容量；未截断核心内容")
	hashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

type Scope struct {
	Project         string `json:"project"`
	Branch          string `json:"branch"`
	Canon           string `json:"canon"`
	Actor           string `json:"actor"`
	PermissionEpoch string `json:"permission_epoch"`
	Purpose         string `json:"purpose"`
	Perspective     string `json:"perspective"`
	StoryTime       int64  `json:"story_time"`
	NarrativeTime   int64  `json:"narrative_time"`
	Session         string `json:"session"`
	Timeline        string `json:"timeline"`
	Save            string `json:"save,omitempty"`
}
type Source struct {
	Resource string `json:"resource"`
	Revision string `json:"revision"`
	Hash     string `json:"hash"`
	Start    int    `json:"start_rune"`
	End      int    `json:"end_rune"`
}
type Item struct {
	Summary      *Summary `json:"summary,omitempty"`
	ID           string   `json:"id"`
	Revision     string   `json:"revision"`
	Project      string   `json:"project"`
	Branch       string   `json:"branch"`
	Actor        string   `json:"actor,omitempty"`
	Session      string   `json:"session,omitempty"`
	Timeline     string   `json:"timeline,omitempty"`
	Save         string   `json:"save,omitempty"`
	Kind         string   `json:"kind"`
	State        string   `json:"state"`
	Visibility   string   `json:"visibility"`
	KnownBy      []string `json:"known_by,omitempty"`
	From         int64    `json:"from"`
	Until        *int64   `json:"until,omitempty"`
	RevealAt     int64    `json:"reveal_at"`
	Control      bool     `json:"control,omitempty"`
	Pinned       bool     `json:"pinned,omitempty"`
	Text         string   `json:"text"`
	Sources      []Source `json:"sources"`
	Dependencies []string `json:"dependencies,omitempty"`
	Score        float64  `json:"score"`
}
type Request struct {
	Refresh          *RefreshScope `json:"refresh,omitempty"`
	Scope            Scope         `json:"scope"`
	RequiredIDs      []string      `json:"required_ids,omitempty"`
	Budget           int           `json:"budget"`
	OutputReserve    int           `json:"output_reserve"`
	OverheadReserve  int           `json:"overhead_reserve"`
	SourceEpoch      string        `json:"source_epoch"`
	CompilerVersion  string        `json:"compiler_version"`
	ModelVersion     string        `json:"model_version,omitempty"`
	RetrievalVersion string        `json:"retrieval_version,omitempty"`
	QueryHash        string        `json:"query_hash,omitempty"`
}
type RefreshScope struct {
	ID       string `json:"id"`
	Revision string `json:"revision"`
}
type Selected struct {
	Item    Item   `json:"item"`
	Channel string `json:"channel"`
	Reason  string `json:"reason"`
}
type Omission struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
}
type Pack struct {
	Request         Request    `json:"request"`
	Items           []Selected `json:"items"`
	Omissions       []Omission `json:"omissions"`
	OmittedCount    int        `json:"omitted_count"`
	EstimatedTokens int        `json:"estimated_tokens"`
	Hash            string     `json:"hash,omitempty"`
}

func digest(v any) string {
	raw, _ := json.Marshal(v)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// CacheKey is necessary but not sufficient for safe reuse: the caller must
// resolve SourceEpoch and PermissionEpoch from trusted current storage first.
func CacheKey(r Request) string { return "memory:" + digest(r) }

func nonempty(s string) bool {
	return strings.TrimSpace(s) != "" && len(s) <= 256 && utf8.ValidString(s)
}
func validateRequest(r Request) error {
	s := r.Scope
	if !nonempty(s.Project) || !nonempty(s.Branch) || !nonempty(s.Canon) || !nonempty(s.Actor) || !nonempty(s.PermissionEpoch) || !nonempty(s.Timeline) || !nonempty(r.SourceEpoch) || r.CompilerVersion != Version {
		return ErrInvalid
	}
	if !slices.Contains([]string{"author", "writer", "critic", "research", "npc"}, s.Purpose) || r.Budget < 1024 || r.Budget > 262144 || r.OutputReserve < 0 || r.OverheadReserve < 0 || r.OutputReserve+r.OverheadReserve >= r.Budget || len(r.RequiredIDs) > 512 {
		return ErrInvalid
	}
	if (s.Purpose == "npc" || (s.Purpose == "writer" && s.Perspective != "")) && !nonempty(s.Perspective) {
		return ErrInvalid
	}
	if s.Purpose == "npc" && !nonempty(s.Save) {
		return ErrInvalid
	}
	seen := map[string]bool{}
	for _, id := range r.RequiredIDs {
		if !nonempty(id) || seen[id] {
			return ErrInvalid
		}
		seen[id] = true
	}
	return nil
}

// ValidateItem checks the portable persisted contract, without granting access.
func ValidateItem(i Item) error { return validateItem(i) }

func validateItem(i Item) error {
	if !nonempty(i.ID) || !nonempty(i.Revision) || !nonempty(i.Project) || !nonempty(i.Branch) || strings.TrimSpace(i.Text) == "" || len(i.Text) > 256*1024 || !utf8.ValidString(i.Text) || math.IsNaN(i.Score) || math.IsInf(i.Score, 0) {
		return ErrInvalid
	}
	if !slices.Contains([]string{"fact", "belief", "claim", "intent", "hypothesis", "event", "relation", "rule", "theme", "mainline", "highlight", "promise", "state", "summary", "professional", "method", "short_term"}, i.Kind) {
		return ErrInvalid
	}
	if !slices.Contains([]string{"published", "adopted_plan", "candidate", "working", "stale", "revoked"}, i.State) || !slices.Contains([]string{"author", "public", "restricted"}, i.Visibility) || len(i.KnownBy) > 128 || len(i.Dependencies) > 128 || (len(i.Sources) == 0 && (i.Summary == nil || len(i.Summary.Inputs) == 0)) || len(i.Sources) > 128 {
		return ErrInvalid
	}
	if i.Until != nil && *i.Until <= i.From {
		return ErrInvalid
	}
	if i.Visibility == "restricted" && len(i.KnownBy) == 0 {
		return ErrInvalid
	}
	if (i.State == "working" || i.State == "candidate") && (!nonempty(i.Actor) || !nonempty(i.Session)) {
		return ErrInvalid
	}
	for _, s := range i.Sources {
		if !nonempty(s.Resource) || !nonempty(s.Revision) || !hashPattern.MatchString(s.Hash) || s.Start < 0 || s.End <= s.Start {
			return ErrInvalid
		}
	}
	for _, id := range i.Dependencies {
		if !nonempty(id) {
			return ErrInvalid
		}
	}
	return validateSummary(i)
}

// Applies is a scope filter, never a replacement for project authorization.
func Applies(i Item, s Scope) bool { return eligible(i, s) }

func eligible(i Item, s Scope) bool {
	if i.Project != s.Project || i.Branch != s.Branch || i.State == "stale" || i.State == "revoked" {
		return false
	}
	if (i.Timeline != "" && i.Timeline != s.Timeline) || (i.Save != "" && i.Save != s.Save) {
		return false
	}
	if i.State == "working" || i.State == "candidate" {
		return i.Actor == s.Actor && i.Session == s.Session && (s.Purpose == "author" || s.Purpose == "critic" || s.Purpose == "research")
	}
	if s.Purpose == "npc" && (i.Control || i.State != "published") {
		return false
	}
	if i.From > s.StoryTime || (i.Until != nil && s.StoryTime >= *i.Until) {
		return false
	}
	if s.Purpose == "author" || s.Purpose == "critic" || s.Purpose == "research" {
		return true
	}
	if s.Purpose == "writer" && i.Control {
		return true
	}
	return i.State == "published" && s.NarrativeTime >= i.RevealAt && (i.Visibility == "public" || (i.Visibility == "restricted" && slices.Contains(i.KnownBy, s.Perspective)))
}
func channel(i Item, s Scope) string {
	if i.State == "working" || i.State == "candidate" {
		return "unconfirmed"
	}
	if i.Control {
		return "control"
	}
	if i.State == "adopted_plan" {
		return "plan"
	}
	if i.Kind == "professional" {
		return "reference"
	}
	return "evidence"
}
func estimate(p Pack) int {
	// Conservative byte envelope until a qualified provider tokenizer is supplied.
	// Reserve serialized counters/hash and never present this as exact token usage.
	p.EstimatedTokens = 262144
	p.Hash = strings.Repeat("f", 64)
	raw, _ := json.Marshal(p)
	return len(raw)*2 + 512
}

func Compile(r Request, input []Item) (Pack, error) {
	return compile(r, input, false)
}

// CompileLegacyPrivateControl exists only to verify immutable historical
// archives produced before private control proposals were marked unconfirmed.
// Runtime context creation and validation must use Compile instead. Replaying
// the old classification also preserves its exact historical budget decisions.
func CompileLegacyPrivateControl(r Request, input []Item) (Pack, error) {
	return compile(r, input, true)
}

func compile(r Request, input []Item, legacyPrivateControl bool) (Pack, error) {
	empty := Pack{}
	if err := validateRequest(r); err != nil || len(input) > 8192 {
		return empty, ErrInvalid
	}
	// Copy nested slices/strings at this boundary so later caller mutations cannot
	// rewrite the frozen result, sources or dependency closure.
	raw, err := json.Marshal(input)
	if err != nil || len(raw) > 32*1024*1024 {
		return empty, ErrInvalid
	}
	var items []Item
	if json.Unmarshal(raw, &items) != nil {
		return empty, ErrInvalid
	}
	all := map[string]bool{}
	available := map[string]Item{}
	for _, i := range items {
		if validateItem(i) != nil || all[i.ID] {
			return empty, ErrInvalid
		}
		all[i.ID] = true
		if eligible(i, r.Scope) {
			available[i.ID] = i
		}
	}
	required := map[string]bool{}
	for _, id := range r.RequiredIDs {
		required[id] = true
	}
	for id, i := range available {
		if i.Pinned {
			required[id] = true
		}
	}
	roots := make([]string, 0, len(required))
	for id := range required {
		roots = append(roots, id)
	}
	sort.Strings(roots)
	pack := Pack{Request: r, Items: []Selected{}, Omissions: []Omission{}, OmittedCount: 8192}
	baseCost := estimate(pack)
	itemCost := 0
	selected := map[string]bool{}
	closure := func(root string) ([]string, error) {
		result := []string{}
		states := map[string]int{}
		var visit func(string) error
		visit = func(id string) error {
			if states[id] == 1 {
				return ErrCycle
			}
			if states[id] == 2 {
				return nil
			}
			item, ok := available[id]
			if !ok {
				return ErrCoverage
			}
			states[id] = 1
			result = append(result, id)
			deps := append([]string{}, item.Dependencies...)
			sort.Strings(deps)
			for _, dep := range deps {
				if err := visit(dep); err != nil {
					return err
				}
			}
			states[id] = 2
			return nil
		}
		err := visit(root)
		return result, err
	}
	appendGroup := func(ids []string, reason string) {
		for _, id := range ids {
			if !selected[id] {
				item := available[id]
				entry := Selected{Item: item, Channel: channel(item, r.Scope), Reason: reason}
				if legacyPrivateControl && item.Control && (item.State == "candidate" || item.State == "working") {
					entry.Channel = "control"
				}
				encoded, _ := json.Marshal(entry)
				itemCost += (len(encoded) + 1) * 2 // includes a conservative array comma
				pack.Items = append(pack.Items, entry)
				selected[id] = true
			}
		}
	}
	for _, root := range roots {
		ids, err := closure(root)
		if err != nil {
			return empty, err
		}
		appendGroup(ids, "required_or_dependency")
	}
	limit := r.Budget - r.OutputReserve - r.OverheadReserve
	if baseCost+itemCost > limit {
		return empty, ErrBudget
	}
	// Keep diagnostic samples bounded so thousands of omitted historical facts
	// cannot crowd out the story core. The exact count remains in the manifest.
	diagnosticReserve := min(4096, (limit-baseCost-itemCost)/8)
	pack.OmittedCount = 0
	omit := func(id, reason string) {
		pack.OmittedCount++
		if len(pack.Omissions) < 16 {
			pack.Omissions = append(pack.Omissions, Omission{ID: id, Reason: reason})
		}
	}
	optional := make([]Item, 0, len(available))
	for _, i := range available {
		if !selected[i.ID] {
			optional = append(optional, i)
		}
	}
	sort.Slice(optional, func(a, b int) bool {
		if optional[a].Score != optional[b].Score {
			return optional[a].Score > optional[b].Score
		}
		return optional[a].ID < optional[b].ID
	})
	for _, i := range optional {
		if selected[i.ID] {
			continue
		}
		ids, err := closure(i.ID)
		if err != nil {
			omit(i.ID, "dependency_unavailable")
			continue
		}
		before := len(pack.Items)
		beforeCost := itemCost
		appendGroup(ids, "ranked_or_dependency")
		if baseCost+itemCost > limit-diagnosticReserve {
			for _, entry := range pack.Items[before:] {
				delete(selected, entry.Item.ID)
			}
			pack.Items = pack.Items[:before]
			itemCost = beforeCost
			omit(i.ID, "budget")
		}
	}
	// Trim only diagnostic samples, never required evidence. OmittedCount makes
	// sampling explicit; caller can inspect the pinned source epoch for a replay.
	for len(pack.Omissions) > 0 && estimate(pack) > limit {
		pack.Omissions = pack.Omissions[:len(pack.Omissions)-1]
	}
	pack.EstimatedTokens = estimate(pack)
	if pack.EstimatedTokens > limit {
		return empty, ErrBudget
	}
	pack.Hash = digest(pack)
	return pack, nil
}
