package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"unicode/utf8"

	"novelstudio/internal/domain"
	"novelstudio/internal/novel"
)

const reviewDispositionKind = "novel_review_dispositions"

type ReviewDispositionChapter struct {
	ChapterID   string `json:"chapter_id"`
	ResourceID  string `json:"resource_id"`
	Revision    int64  `json:"revision"`
	Hash        string `json:"hash"`
	BaseHead    string `json:"base_head"`
	CurrentHead string `json:"current_head"`
	Available   bool   `json:"available"`
	Reason      string `json:"reason"`
}

type ReviewDispositionEntry struct {
	SourceStepID     string                     `json:"source_step_id"`
	SourceOutputHash string                     `json:"source_output_hash"`
	PassKey          string                     `json:"pass_key"`
	FindingID        string                     `json:"finding_id"`
	Status           string                     `json:"status"`
	Note             string                     `json:"note"`
	OperationID      string                     `json:"operation_id"`
	Chapters         []ReviewDispositionChapter `json:"chapters"`
}

type ReviewDispositionView struct {
	ReviewDispositionEntry
	Stale  bool   `json:"stale"`
	Reason string `json:"reason"`
}

type NovelReviewDispositions struct {
	ResourceID      string                     `json:"resource_id"`
	Revision        int64                      `json:"revision"`
	Hash            string                     `json:"hash"`
	Entries         []ReviewDispositionView    `json:"entries"`
	CurrentChapters []ReviewDispositionChapter `json:"current_chapters"`
}

type NovelReviewDispositionInput struct {
	Revision         int64                      `json:"revision"`
	Hash             string                     `json:"hash"`
	SourceStepID     string                     `json:"source_step_id"`
	SourceOutputHash string                     `json:"source_output_hash"`
	FindingID        string                     `json:"finding_id"`
	Status           string                     `json:"status"`
	Note             string                     `json:"note"`
	ExpectedChapters []ReviewDispositionChapter `json:"expected_chapters"`
	OperationID      string                     `json:"operation_id"`
}

type reviewDispositionDocument struct {
	Schema      string                  `json:"schema"`
	ActorID     string                  `json:"actor_id"`
	BookID      string                  `json:"book_id"`
	RunID       string                  `json:"run_id"`
	Entry       *ReviewDispositionEntry `json:"entry,omitempty"`
	RequestHash string                  `json:"request_hash,omitempty"`
}

type reviewDispositionNotes struct {
	resource  string
	revision  int64
	entries   []ReviewDispositionEntry
	resources map[string]string
}

func dispositionName(run string) string          { return "审稿整改 · " + run }
func dispositionKey(step, finding string) string { return step + ":" + finding }
func dispositionEntryName(run, step, finding string) string {
	return dispositionName(run) + " · " + digest(dispositionKey(step, finding))[:32]
}

func (s *NovelBookReviews) Dispositions(ctx context.Context, actor, bookID, run string) (NovelReviewDispositions, error) {
	report, err := s.Report(ctx, actor, bookID, run)
	if err != nil {
		return NovelReviewDispositions{}, err
	}
	tx, a, book, err := (&NovelPlanning{IP: s.Generations.IP}).begin(ctx, actor, bookID, true)
	if err != nil {
		return NovelReviewDispositions{}, err
	}
	defer tx.Rollback()
	current, err := recipeTx(ctx, tx, book.ProjectID, run, true)
	if err != nil {
		return NovelReviewDispositions{}, err
	}
	if current.ActorID != a.ID || current.BranchID != book.BranchID {
		return NovelReviewDispositions{}, domain.ErrForbidden
	}
	notes, err := readDispositionNotesTx(ctx, tx, a, book, run)
	if err != nil {
		return NovelReviewDispositions{}, err
	}
	return s.dispositionViewTx(ctx, tx, a, book, report, current, notes)
}

func readDispositionNotesTx(ctx context.Context, tx *sql.Tx, actor domain.Actor, book NovelBook, run string) (reviewDispositionNotes, error) {
	out := reviewDispositionNotes{entries: []ReviewDispositionEntry{}, resources: map[string]string{}}
	root := dispositionName(run)
	rows, err := tx.QueryContext(ctx, "SELECT id,name FROM resources WHERE project_id=? AND branch_id=? AND kind=? AND (name=? OR name LIKE ?) ORDER BY name,id", book.ProjectID, book.BranchID, reviewDispositionKind, root, root+" · %")
	if err != nil {
		return out, err
	}
	type noteRow struct{ id, name string }
	ids := []noteRow{}
	for rows.Next() {
		var v noteRow
		if err = rows.Scan(&v.id, &v.name); err != nil {
			rows.Close()
			return out, err
		}
		ids = append(ids, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	for _, row := range ids {
		rd, e := resourceDraftTx(ctx, tx, actor.ID, book.ProjectID, row.id)
		if e != nil {
			return out, e
		}
		if rd.Revision == 0 {
			continue
		}
		if e = rejectExpiredRuntime([]byte(rd.Text), "resource_drafts.body"); e != nil {
			return out, e
		}
		var doc reviewDispositionDocument
		if decodeRecoveryDocument([]byte(rd.Text), &doc) != nil || doc.ActorID != actor.ID || doc.BookID != book.ID || doc.RunID != run {
			return out, domain.ErrNotReady
		}
		if row.name == root {
			if doc.Schema != "novel-review-disposition-root-v1" || doc.Entry != nil || out.resource != "" {
				return out, domain.ErrNotReady
			}
			out.resource, out.revision = row.id, rd.Revision
		} else {
			if doc.Schema != "novel-review-disposition-entry-v1" || doc.Entry == nil || row.name != dispositionEntryName(run, doc.Entry.SourceStepID, doc.Entry.FindingID) {
				return out, domain.ErrNotReady
			}
			if !validDispositionEntry(*doc.Entry) {
				return out, domain.ErrNotReady
			}
			key := dispositionKey(doc.Entry.SourceStepID, doc.Entry.FindingID)
			if out.resources[key] != "" {
				return out, domain.ErrNotReady
			}
			out.resources[key] = row.id
			out.entries = append(out.entries, *doc.Entry)
		}
	}
	if out.resource == "" && len(out.entries) > 0 {
		return out, domain.ErrNotReady
	}
	slices.SortFunc(out.entries, func(a, b ReviewDispositionEntry) int {
		return strings.Compare(dispositionKey(a.SourceStepID, a.FindingID), dispositionKey(b.SourceStepID, b.FindingID))
	})
	return out, nil
}

func validDispositionEntry(entry ReviewDispositionEntry) bool {
	if !lowerHex32(entry.SourceStepID) || len(entry.SourceOutputHash) != 64 || strings.Trim(entry.SourceOutputHash, "0123456789abcdef") != "" || !validName(entry.FindingID, 80) || !validName(entry.PassKey, 120) || !validName(entry.OperationID, 120) || !slices.Contains([]string{"open", "revised", "confirmed", "deferred"}, entry.Status) || !utf8.ValidString(entry.Note) || utf8.RuneCountInString(entry.Note) > 2000 || len(entry.Chapters) < 1 || len(entry.Chapters) > 6 {
		return false
	}
	seen := map[string]bool{}
	for _, pin := range entry.Chapters {
		if seen[pin.ChapterID] || !lowerHex32(pin.ChapterID) || !lowerHex32(pin.ResourceID) || pin.Revision < 0 || (pin.Available && (len(pin.Hash) != 64 || strings.Trim(pin.Hash, "0123456789abcdef") != "")) || (!pin.Available && (pin.Hash != "" || pin.Revision != 0 || entry.Status == "confirmed")) {
			return false
		}
		seen[pin.ChapterID] = true
	}
	return true
}

func dispositionSnapshotHash(notes reviewDispositionNotes) string {
	if notes.resource == "" {
		return ""
	}
	return capabilityDigest(struct {
		Revision int64
		Entries  []ReviewDispositionEntry
	}{notes.revision, notes.entries})
}

func (s *NovelBookReviews) dispositionChaptersTx(ctx context.Context, tx *sql.Tx, actor domain.Actor, book NovelBook, report NovelBookReviewReport) ([]ReviewDispositionChapter, error) {
	items := []ReviewDispositionChapter{}
	for _, source := range report.Preview.Chapters {
		if !source.Included {
			continue
		}
		pin := ReviewDispositionChapter{ChapterID: source.ID, ResourceID: source.ResourceID, Reason: "当前作者章稿暂不可读取"}
		d, err := (&NovelChapters{IP: s.Generations.IP}).draftTx(ctx, tx, actor, book, source.ResourceID)
		if err == nil && d.Document.ChapterID == source.ID {
			pin.Revision, pin.Hash, pin.BaseHead, pin.CurrentHead, pin.Available, pin.Reason = d.Revision, d.Hash, d.BaseHead, d.Resource.Head, true, d.Reason
		}
		items = append(items, pin)
	}
	return items, nil
}

func (s *NovelBookReviews) dispositionViewTx(ctx context.Context, tx *sql.Tx, actor domain.Actor, book NovelBook, report NovelBookReviewReport, run RecipeExecution, notes reviewDispositionNotes) (NovelReviewDispositions, error) {
	out := NovelReviewDispositions{ResourceID: notes.resource, Revision: notes.revision, Hash: dispositionSnapshotHash(notes), Entries: []ReviewDispositionView{}, CurrentChapters: []ReviewDispositionChapter{}}
	chapters, err := s.dispositionChaptersTx(ctx, tx, actor, book, report)
	if err != nil {
		return out, err
	}
	out.CurrentChapters = chapters
	current := map[string]ReviewDispositionChapter{}
	for _, pin := range chapters {
		current[pin.ChapterID] = pin
	}
	for _, entry := range notes.entries {
		view := ReviewDispositionView{ReviewDispositionEntry: entry}
		sourceCurrent := false
		for _, step := range run.Steps {
			if step.ID == entry.SourceStepID && step.Active && step.State == "COMPLETED" && step.OutputHash == entry.SourceOutputHash {
				sourceCurrent = true
			}
		}
		validated := false
		required := map[string]bool{}
		for _, pass := range report.Passes {
			if pass.StepID == entry.SourceStepID && pass.OutputHash == entry.SourceOutputHash {
				for _, finding := range pass.Result.Findings {
					if finding.ID == entry.FindingID {
						validated = true
						for _, evidence := range finding.Evidence {
							required[evidence.ChapterID] = true
						}
					}
				}
			}
		}
		if !sourceCurrent || !validated {
			view.Stale = true
			view.Reason = "原报告段落已换为新尝试，或原问题依据已不可复核。"
		}
		for _, pin := range entry.Chapters {
			now, ok := current[pin.ChapterID]
			if !ok || !required[pin.ChapterID] || !now.Available || !reflect.DeepEqual(pin, now) {
				view.Stale = true
				view.Reason = "问题引用的章稿已变化或不可读取；原人工处置仅对应保存时的版本。"
				break
			}
		}
		if len(entry.Chapters) == 0 || (validated && len(entry.Chapters) != len(required)) {
			view.Stale = true
			view.Reason = "原处置没有完整章稿版本依据。"
		}
		out.Entries = append(out.Entries, view)
	}
	return out, nil
}

func dispositionFinding(report NovelBookReviewReport, in NovelReviewDispositionInput) (NovelBookReviewPass, novel.BookReviewFinding, error) {
	for _, pass := range report.Passes {
		if pass.StepID != in.SourceStepID || pass.OutputHash != in.SourceOutputHash {
			continue
		}
		for _, finding := range pass.Result.Findings {
			if finding.ID == in.FindingID {
				return pass, finding, nil
			}
		}
	}
	for _, step := range report.Run.Steps {
		if step.ID == in.SourceStepID && (!step.Active || step.OutputHash != in.SourceOutputHash || step.State != "COMPLETED") {
			return NovelBookReviewPass{}, novel.BookReviewFinding{}, domain.ErrConflict
		}
	}
	return NovelBookReviewPass{}, novel.BookReviewFinding{}, domain.ErrForbidden
}

func createDispositionNoteTx(ctx context.Context, tx *sql.Tx, actor domain.Actor, book NovelBook, name string) (string, error) {
	id := NewID()
	if _, err := tx.ExecContext(ctx, "INSERT INTO resources(id,project_id,branch_id,name,kind) VALUES(?,?,?,?,?)", id, book.ProjectID, book.BranchID, name, reviewDispositionKind); err != nil {
		return "", err
	}
	if err := audit(ctx, tx, actor, "resource.create", id); err != nil {
		return "", err
	}
	if err := captureMetadataSourceTx(ctx, tx, book.ProjectID, "resources", id); err != nil {
		return "", err
	}
	return id, nil
}

func (s *NovelBookReviews) SaveDisposition(ctx context.Context, actor, bookID, runID string, in NovelReviewDispositionInput) (NovelReviewDispositions, error) {
	var empty NovelReviewDispositions
	if !slices.Contains([]string{"open", "revised", "confirmed", "deferred"}, in.Status) || in.Revision < 0 || !validName(in.OperationID, 120) || !utf8.ValidString(in.Note) || utf8.RuneCountInString(in.Note) > 2000 {
		return empty, domain.ErrValidation
	}
	report, err := s.Report(ctx, actor, bookID, runID)
	if err != nil {
		return empty, err
	}
	pass, finding, err := dispositionFinding(report, in)
	if err != nil {
		return empty, err
	}
	tx, a, book, err := (&NovelPlanning{IP: s.Generations.IP}).begin(ctx, actor, bookID, true)
	if err != nil {
		return empty, err
	}
	defer tx.Rollback()
	run, err := recipeTx(ctx, tx, book.ProjectID, runID)
	if err != nil {
		return empty, err
	}
	if run.ActorID != a.ID || run.BranchID != book.BranchID {
		return empty, domain.ErrForbidden
	}
	step, err := recipeStepByID(run, in.SourceStepID)
	if err != nil || !step.Active || step.State != "COMPLETED" || step.OutputHash != in.SourceOutputHash {
		return empty, domain.ErrConflict
	}
	notes, err := readDispositionNotesTx(ctx, tx, a, book, runID)
	if err != nil {
		return empty, err
	}
	if notes.revision != in.Revision || dispositionSnapshotHash(notes) != in.Hash {
		return empty, domain.ErrConflict
	}
	for _, prior := range notes.entries {
		if prior.OperationID == in.OperationID {
			return empty, domain.ErrConflict
		}
	}
	current, err := s.dispositionChaptersTx(ctx, tx, a, book, report)
	if err != nil {
		return empty, err
	}
	wanted := map[string]bool{}
	for _, evidence := range finding.Evidence {
		wanted[evidence.ChapterID] = true
	}
	actual := []ReviewDispositionChapter{}
	for _, pin := range current {
		if wanted[pin.ChapterID] {
			if !pin.Available && in.Status == "confirmed" {
				return empty, domain.ErrConflict
			}
			actual = append(actual, pin)
		}
	}
	if len(actual) != len(wanted) || len(in.ExpectedChapters) != len(wanted) {
		return empty, domain.ErrConflict
	}
	slices.SortFunc(actual, func(a, b ReviewDispositionChapter) int { return strings.Compare(a.ChapterID, b.ChapterID) })
	expected := append([]ReviewDispositionChapter(nil), in.ExpectedChapters...)
	slices.SortFunc(expected, func(a, b ReviewDispositionChapter) int { return strings.Compare(a.ChapterID, b.ChapterID) })
	if !reflect.DeepEqual(actual, expected) {
		return empty, fmt.Errorf("%w：问题涉及的章稿已变化，请重新读取当前版本", domain.ErrConflict)
	}
	if notes.resource == "" {
		notes.resource, err = createDispositionNoteTx(ctx, tx, a, book, dispositionName(runID))
		if err != nil {
			return empty, err
		}
	}
	key := dispositionKey(in.SourceStepID, in.FindingID)
	entryID := notes.resources[key]
	if entryID == "" {
		entryID, err = createDispositionNoteTx(ctx, tx, a, book, dispositionEntryName(runID, in.SourceStepID, in.FindingID))
		if err != nil {
			return empty, err
		}
	}
	entry := ReviewDispositionEntry{SourceStepID: in.SourceStepID, SourceOutputHash: in.SourceOutputHash, PassKey: pass.Key, FindingID: in.FindingID, Status: in.Status, Note: in.Note, OperationID: in.OperationID, Chapters: actual}
	doc := reviewDispositionDocument{Schema: "novel-review-disposition-entry-v1", ActorID: a.ID, BookID: bookID, RunID: runID, Entry: &entry, RequestHash: capabilityDigest(in)}
	raw, err := json.Marshal(doc)
	if err != nil {
		return empty, err
	}
	rd, err := resourceDraftTx(ctx, tx, a.ID, book.ProjectID, entryID)
	if err != nil {
		return empty, err
	}
	rd.Text = string(raw)
	rd.Policy = &domain.KnowledgePolicy{Space: "working", Visibility: "author"}
	if _, err = s.Generations.IP.saveResourceDraftTx(ctx, tx, a, book.ProjectID, entryID, rd); err != nil {
		return empty, err
	}
	root := reviewDispositionDocument{Schema: "novel-review-disposition-root-v1", ActorID: a.ID, BookID: bookID, RunID: runID}
	raw, err = json.Marshal(root)
	if err != nil {
		return empty, err
	}
	_, err = s.Generations.IP.saveResourceDraftTx(ctx, tx, a, book.ProjectID, notes.resource, ResourceDraft{Revision: notes.revision, Text: string(raw), Policy: &domain.KnowledgePolicy{Space: "working", Visibility: "author"}})
	if err != nil {
		return empty, err
	}
	notes, err = readDispositionNotesTx(ctx, tx, a, book, runID)
	if err != nil {
		return empty, err
	}
	result, err := s.dispositionViewTx(ctx, tx, a, book, report, run, notes)
	if err != nil {
		return empty, err
	}
	if err = tx.Commit(); err != nil {
		return empty, err
	}
	return result, nil
}
