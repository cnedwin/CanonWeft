package novel

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
	"unicode/utf8"

	"novelstudio/internal/domain"
)

type ChapterQuote struct {
	Start int    `json:"start_rune"`
	End   int    `json:"end_rune"`
	Quote string `json:"quote"`
}
type LedgerEntry struct {
	ID          string         `json:"id,omitempty"`
	Kind        string         `json:"kind"`
	Context     string         `json:"context"`
	SubjectID   string         `json:"subject_id"`
	SubjectName string         `json:"subject_name"`
	HolderID    string         `json:"holder_id"`
	ObjectID    string         `json:"object_id"`
	Predicate   string         `json:"predicate"`
	Statement   string         `json:"statement"`
	StoryTime   int64          `json:"story_time"`
	KnownFrom   int64          `json:"known_from"`
	Visibility  string         `json:"visibility"`
	KnownBy     []string       `json:"known_by"`
	Quotes      []string       `json:"quotes"`
	Evidence    []ChapterQuote `json:"evidence,omitempty"`
}
type LedgerOutput struct {
	Summary string        `json:"summary"`
	Entries []LedgerEntry `json:"entries"`
	Notes   []string      `json:"notes"`
}
type ChapterLedger struct {
	Schema       int                       `json:"schema"`
	Source       domain.NovelChapterSource `json:"source"`
	ChapterID    string                    `json:"chapter_id"`
	BodyID       string                    `json:"body_id"`
	TextHash     string                    `json:"text_hash"`
	GenerationID string                    `json:"generation_id,omitempty"`
	LedgerOutput
}

func (entry LedgerEntry) validate() error {
	if !slices.Contains([]string{"fact", "belief", "claim", "intent", "hypothesis", "event", "relation", "state", "promise", "highlight", "summary"}, entry.Kind) || !slices.Contains([]string{"reality", "flashback", "dream", "hypothetical", "plan"}, entry.Context) {
		return domain.ErrValidation
	}
	if slices.Contains([]string{"plan", "dream", "hypothetical"}, entry.Context) && !slices.Contains([]string{"belief", "claim", "intent", "hypothesis", "promise", "summary"}, entry.Kind) {
		return fmt.Errorf("%w：计划、梦境和假设不能记为已发生事实", domain.ErrValidation)
	}
	if strings.TrimSpace(entry.Statement) == "" || utf8.RuneCountInString(entry.Statement) > 4000 || utf8.RuneCountInString(entry.Predicate) > 200 || utf8.RuneCountInString(entry.SubjectName) > 300 || len(entry.SubjectID) > 128 || len(entry.HolderID) > 128 || len(entry.ObjectID) > 128 {
		return domain.ErrValidation
	}
	if entry.StoryTime < 0 || entry.StoryTime > 9007199254740991 || entry.KnownFrom < 0 || entry.KnownFrom > 9007199254740991 || !slices.Contains([]string{"public", "author", "restricted"}, entry.Visibility) || len(entry.KnownBy) > 128 || (entry.Visibility == "restricted" && len(entry.KnownBy) == 0) {
		return domain.ErrValidation
	}
	return nil
}

// Models quote text rather than guessing Unicode offsets. Ambiguous excerpts
// must be expanded; silently selecting their first occurrence can change meaning.
func ledgerEvidence(text string, quotes []string) ([]ChapterQuote, error) {
	if len(quotes) < 1 || len(quotes) > 8 {
		return nil, domain.ErrValidation
	}
	result := make([]ChapterQuote, 0, len(quotes))
	for _, quote := range quotes {
		if strings.TrimSpace(quote) == "" || utf8.RuneCountInString(quote) > 8000 {
			return nil, domain.ErrValidation
		}
		start := strings.Index(text, quote)
		if start < 0 || strings.Index(text[start+utf8.RuneLen([]rune(quote)[0]):], quote) >= 0 {
			return nil, fmt.Errorf("%w：账本引文在正文中不存在或出现多次，请补充完整上下文", domain.ErrValidation)
		}
		at := utf8.RuneCountInString(text[:start])
		result = append(result, ChapterQuote{Start: at, End: at + utf8.RuneCountInString(quote), Quote: quote})
	}
	return result, nil
}
func validateLedgerOutput(out LedgerOutput) error {
	if strings.TrimSpace(out.Summary) == "" || utf8.RuneCountInString(out.Summary) > 2000 || out.Entries == nil || len(out.Entries) > 128 || out.Notes == nil || len(out.Notes) > 50 {
		return domain.ErrValidation
	}
	for _, note := range out.Notes {
		if utf8.RuneCountInString(note) > 2000 {
			return domain.ErrValidation
		}
	}
	return nil
}
func ParseLedgerOutput(raw []byte, text, task string) (LedgerOutput, error) {
	var out LedgerOutput
	if len(raw) > 512*1024 || !planID.MatchString(task) {
		return out, domain.ErrValidation
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&out) != nil || d.Decode(&struct{}{}) != io.EOF || validateLedgerOutput(out) != nil {
		return out, domain.ErrValidation
	}
	for i := range out.Entries {
		e := &out.Entries[i]
		if e.ID != "" || len(e.Evidence) != 0 {
			return out, domain.ErrValidation
		}
		if err := e.validate(); err != nil {
			return out, err
		}
		quotes, err := ledgerEvidence(text, e.Quotes)
		if err != nil {
			return out, err
		}
		h := sha256.Sum256([]byte(fmt.Sprintf("chapter-ledger:%s:%d", task, i)))
		e.ID, e.Evidence = hex.EncodeToString(h[:16]), quotes
	}
	return out, nil
}

// ValidateEditedLedger recalculates evidence from the actual current chapter.
// The caller validates source/version/task ownership and entity IDs separately.
func ValidateEditedLedger(doc *ChapterLedger, text string) error {
	if doc.Schema != 1 || !planID.MatchString(doc.ChapterID) || !planID.MatchString(doc.BodyID) || !planHash.MatchString(doc.TextHash) || (doc.GenerationID != "" && !planID.MatchString(doc.GenerationID)) || validateLedgerOutput(doc.LedgerOutput) != nil {
		return domain.ErrValidation
	}
	seen := map[string]bool{}
	for i := range doc.Entries {
		e := &doc.Entries[i]
		if !planID.MatchString(e.ID) || seen[e.ID] {
			return domain.ErrValidation
		}
		seen[e.ID] = true
		if err := e.validate(); err != nil {
			return err
		}
		quotes, err := ledgerEvidence(text, e.Quotes)
		if err != nil {
			return err
		}
		e.Evidence = quotes
	}
	return nil
}
