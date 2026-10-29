package service

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strings"
	"unicode/utf8"

	"novelstudio/internal/domain"
	"novelstudio/internal/novel"
)

const storyLedgerPageSize = 5
const storyLedgerPageBytes = 1 << 20
const storyLedgerItemBytes = 768 << 10

type NovelStoryLedgerPerson struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type NovelStoryLedgerItem struct {
	ChapterID            string                   `json:"chapter_id"`
	ControlID            string                   `json:"control_id"`
	LedgerID             string                   `json:"ledger_id"`
	ChapterTitle         string                   `json:"chapter_title"`
	Origin               string                   `json:"origin"`
	LedgerRevision       int64                    `json:"ledger_revision"`
	LedgerHash           string                   `json:"ledger_hash"`
	SourceHash           string                   `json:"source_hash"`
	CurrentHash          string                   `json:"current_hash"`
	CurrentRevision      int64                    `json:"current_revision"`
	CurrentReady         bool                     `json:"current_ready"`
	SourceMatchesCurrent bool                     `json:"source_matches_current"`
	Reason               string                   `json:"reason"`
	Summary              string                   `json:"summary"`
	Entries              []novel.LedgerEntry      `json:"entries"`
	People               []NovelStoryLedgerPerson `json:"people"`
}

type NovelStoryLedgerPage struct {
	BookID     string                 `json:"book_id"`
	Source     string                 `json:"source"`
	Items      []NovelStoryLedgerItem `json:"items"`
	NextCursor string                 `json:"next_cursor"`
	Scanned    int                    `json:"scanned"`
	Notes      []string               `json:"notes"`
}

// StoryLedger reads the current page under one project authority snapshot. It
// creates no ledger, draft, audit, memory, task, or model request. Planning is
// deliberately not an admission gate for already saved narrative evidence.
func (s *NovelChapters) StoryLedger(ctx context.Context, actor, bookID, source, after string) (NovelStoryLedgerPage, error) {
	out := NovelStoryLedgerPage{BookID: bookID, Source: source, Items: []NovelStoryLedgerItem{}, Notes: []string{}}
	if !lowerHex32(bookID) || !slices.Contains([]string{"working", "published"}, source) || (after != "" && !lowerHex32(after)) {
		return out, domain.ErrValidation
	}
	tx, a, book, err := (&NovelPlanning{IP: s.IP}).begin(ctx, actor, bookID, true)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	if after != "" {
		var found string
		err = tx.QueryRowContext(ctx, "SELECT id FROM resources WHERE id=? AND project_id=? AND branch_id=? AND kind='novel_chapter_ledger'", after, book.ProjectID, book.BranchID).Scan(&found)
		if err == sql.ErrNoRows {
			return out, domain.ErrForbidden
		}
		if err != nil {
			return out, err
		}
	}
	rows, err := tx.QueryContext(ctx, "SELECT id,name FROM resources WHERE project_id=? AND branch_id=? AND kind='novel_chapter_ledger' AND id>? ORDER BY id LIMIT 6", book.ProjectID, book.BranchID, after)
	if err != nil {
		return out, err
	}
	type row struct{ id, name string }
	records := []row{}
	for rows.Next() {
		var r row
		if err = rows.Scan(&r.id, &r.name); err != nil {
			rows.Close()
			return out, err
		}
		records = append(records, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	people := map[string]string{}
	branch, err := branchTx(ctx, tx, book.ProjectID, book.BranchID)
	if err != nil {
		return out, err
	}
	snapshot, err := snapshotTx(ctx, tx, book.ProjectID, branch.Head)
	if err != nil {
		if !storyLedgerUnavailable(err) {
			return out, err
		}
		out.Notes = append(out.Notes, "当前人物身份不可读；仍保留账本中的原人物ID与名称。")
	} else {
		for _, e := range snapshot.Entities {
			people[e.ID] = e.Name
		}
	}
	pageBytes := 2048
	last := ""
	for i, r := range records {
		if i == storyLedgerPageSize {
			out.NextCursor = last
			break
		}
		item := NovelStoryLedgerItem{LedgerID: r.id, Origin: "unavailable", Entries: []novel.LedgerEntry{}, People: []NovelStoryLedgerPerson{}}
		const prefix = "剧情账本 · "
		if strings.HasPrefix(r.name, prefix) && lowerHex32(strings.TrimPrefix(r.name, prefix)) {
			item.ChapterID = strings.TrimPrefix(r.name, prefix)
			err = s.storyLedgerItemTx(ctx, tx, a, book, source, people, &item)
		} else {
			err = domain.ErrValidation
		}
		if err != nil {
			if !storyLedgerUnavailable(err) {
				return out, err
			}
			item.Origin = "unavailable"
			item.Entries = []novel.LedgerEntry{}
			item.People = []NovelStoryLedgerPerson{}
			item.Summary = ""
			item.SourceMatchesCurrent = false
			item.Reason = "账本或其完整原文来源不可用，未展示未经核验的记录。"
		}
		raw, e := json.Marshal(item)
		if e != nil {
			return out, e
		}
		if len(raw) > storyLedgerItemBytes {
			item.Origin = "unavailable"
			item.Entries = []novel.LedgerEntry{}
			item.People = []NovelStoryLedgerPerson{}
			item.Summary = ""
			item.SourceMatchesCurrent = false
			item.Reason = "单章账本超过本页读取容量，未截断或冒充完整记录。"
			raw, _ = json.Marshal(item)
		}
		if pageBytes+len(raw) > storyLedgerPageBytes {
			out.NextCursor = last
			break
		}
		out.Items = append(out.Items, item)
		out.Scanned++
		last = r.id
		pageBytes += len(raw) + 1
	}
	if out.NextCursor != "" {
		out.Notes = append(out.Notes, "仍有账本尚未扫描；筛选仅覆盖已读取记录。")
	}
	return out, nil
}

func storyLedgerUnavailable(err error) bool {
	return errors.Is(err, domain.ErrForbidden) || errors.Is(err, domain.ErrNotReady) || errors.Is(err, domain.ErrConflict) || errors.Is(err, domain.ErrValidation) || errors.Is(err, domain.ErrMaterialExpired) || errors.Is(err, sql.ErrNoRows)
}

func storyLedgerHash(value string) bool {
	return len(value) == 64 && strings.Trim(value, "0123456789abcdef") == ""
}

func storyLedgerDecode(raw []byte, value any) error {
	if len(raw) > storyLedgerItemBytes {
		return domain.ErrNotReady
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(value) != nil || d.Decode(&struct{}{}) != io.EOF {
		return domain.ErrNotReady
	}
	return nil
}

func (s *NovelChapters) storyLedgerItemTx(ctx context.Context, tx *sql.Tx, a domain.Actor, book NovelBook, source string, people map[string]string, out *NovelStoryLedgerItem) error {
	var control string
	if err := tx.QueryRowContext(ctx, "SELECT id FROM resources WHERE project_id=? AND branch_id=? AND kind='novel_chapter_control' AND name=?", book.ProjectID, book.BranchID, chapterName("章节工作稿", out.ChapterID)).Scan(&control); err != nil {
		return err
	}
	out.ControlID = control
	ledgerResource, err := resourceTx(ctx, tx, book.ProjectID, out.LedgerID)
	if err != nil {
		return err
	}
	var doc novel.ChapterLedger
	var original *novel.ChapterDocument
	if source == "working" {
		var size int
		err = tx.QueryRowContext(ctx, "SELECT OCTET_LENGTH(body) FROM resource_drafts WHERE resource_id=? AND actor_id=?", out.LedgerID, a.ID).Scan(&size)
		if err != nil && err != sql.ErrNoRows {
			return err
		}
		if err == nil {
			if size > storyLedgerItemBytes {
				out.Reason = "私人账本超过单章读取容量，未截断或展示不完整记录。"
				return nil
			}
			stored, e := resourceDraftTx(ctx, tx, a.ID, book.ProjectID, out.LedgerID)
			if e != nil {
				return e
			}
			if e = rejectExpiredRuntime([]byte(stored.Text), "resource_drafts.body"); e != nil {
				return e
			}
			if e = storyLedgerDecode([]byte(stored.Text), &doc); e != nil {
				return e
			}
			out.Origin = "private"
			out.LedgerRevision = stored.Revision
			out.LedgerHash = digest(stored.Text)
		}
	}
	if out.Origin != "private" {
		if ledgerResource.Head == "" {
			out.Reason = "尚无当前作者保存的私人账本或正式采用账本。"
			return nil
		}
		var e error
		doc, original, e = s.storyLedgerPublishedTx(ctx, tx, book, control, ledgerResource)
		if e != nil {
			return e
		}
		out.Origin = "published"
		out.LedgerRevision = ledgerResource.Revision
		out.LedgerHash = ledgerResource.Hash
	}
	if doc.Source.BookID != book.ID || doc.Source.ResourceID != control || doc.ChapterID != out.ChapterID || !storyLedgerHash(doc.Source.Hash) || doc.Source.Revision < 1 || !lowerHex32(doc.BodyID) || !storyLedgerHash(doc.TextHash) {
		return domain.ErrValidation
	}
	if err = validateStoryLedgerStructure(doc); err != nil {
		return err
	}
	out.SourceHash = doc.Source.Hash
	current, e := s.draftTx(ctx, tx, a, book, control)
	if e != nil {
		if !storyLedgerUnavailable(e) {
			return e
		}
		out.Reason = "当前章稿不可核验；保留原账本，不将其当作当前稿事实。"
	} else {
		out.CurrentHash = current.Hash
		out.CurrentRevision = current.Revision
		out.CurrentReady = current.Ready
		out.ChapterTitle = current.Document.Title
		out.SourceMatchesCurrent = current.Hash == doc.Source.Hash && current.Document.ChapterID == doc.ChapterID && current.Document.BodyID == doc.BodyID && digest(current.Document.Text) == doc.TextHash
		if out.SourceMatchesCurrent {
			original = &current.Document
		} else {
			out.Reason = "当前章稿与账本原依据不同；引文仍出现不代表记录仍然适用。"
		}
		if !current.Ready {
			if out.Reason != "" {
				out.Reason += " "
			}
			out.Reason += "当前写作依据待核对：" + current.Reason
		}
	}
	// A private interpretation may still cite the intact adopted chapter even
	// after the author edits the current draft. Reuse that exact public source;
	// never mine transient retention materials as ordinary version history.
	if original == nil && out.Origin == "private" && ledgerResource.Head != "" {
		published, frozen, readErr := s.storyLedgerPublishedTx(ctx, tx, book, control, ledgerResource)
		if readErr == nil && published.Source.Hash == doc.Source.Hash {
			original = frozen
		} else if readErr != nil && !storyLedgerUnavailable(readErr) {
			return readErr
		}
	}
	if original != nil {
		if original.ChapterID != doc.ChapterID || original.BodyID != doc.BodyID || digest(original.Text) != doc.TextHash {
			return domain.ErrValidation
		}
		if err = novel.ValidateEditedLedger(&doc, original.Text); err != nil {
			return err
		}
		if out.ChapterTitle == "" {
			out.ChapterTitle = original.Title
		}
	} else {
		if out.Reason != "" {
			out.Reason += " "
		}
		out.Reason += "旧私人章稿未能重新读取，所列原引文及偏移仅保留账本当时记录。"
	}
	out.Summary = doc.Summary
	out.Entries = doc.Entries
	referenced := map[string]bool{}
	for _, entry := range doc.Entries {
		for _, id := range append([]string{entry.SubjectID, entry.ObjectID, entry.HolderID}, entry.KnownBy...) {
			if id != "" {
				referenced[id] = true
			}
		}
	}
	ids := make([]string, 0, len(referenced))
	for id := range referenced {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		if name, exists := people[id]; exists {
			out.People = append(out.People, NovelStoryLedgerPerson{ID: id, Name: name})
		}
	}
	return nil
}

// Validate the saved interpretation even when the old private prose no longer
// exists. Exact evidence offsets are only recalculated against readable prose.
func validateStoryLedgerStructure(doc novel.ChapterLedger) error {
	if doc.Schema != 1 || !lowerHex32(doc.ChapterID) || !lowerHex32(doc.BodyID) || !storyLedgerHash(doc.TextHash) || strings.TrimSpace(doc.Summary) == "" || utf8.RuneCountInString(doc.Summary) > 2000 || doc.Entries == nil || len(doc.Entries) > 128 || doc.Notes == nil || len(doc.Notes) > 50 {
		return domain.ErrValidation
	}
	if doc.GenerationID != "" && !lowerHex32(doc.GenerationID) {
		return domain.ErrValidation
	}
	for _, note := range doc.Notes {
		if utf8.RuneCountInString(note) > 2000 {
			return domain.ErrValidation
		}
	}
	seen := map[string]bool{}
	for _, e := range doc.Entries {
		if !lowerHex32(e.ID) || seen[e.ID] || !slices.Contains([]string{"fact", "belief", "claim", "intent", "hypothesis", "event", "relation", "state", "promise", "highlight", "summary"}, e.Kind) || !slices.Contains([]string{"reality", "flashback", "dream", "hypothetical", "plan"}, e.Context) || strings.TrimSpace(e.Statement) == "" || utf8.RuneCountInString(e.Statement) > 4000 || len(e.Quotes) < 1 || len(e.Quotes) > 8 || len(e.Evidence) != len(e.Quotes) || e.StoryTime < 0 || e.KnownFrom < 0 || e.StoryTime > 9007199254740991 || e.KnownFrom > 9007199254740991 || !slices.Contains([]string{"author", "public", "restricted"}, e.Visibility) || len(e.KnownBy) > 128 || len(e.SubjectID) > 128 || len(e.ObjectID) > 128 || len(e.HolderID) > 128 || utf8.RuneCountInString(e.SubjectName) > 300 || utf8.RuneCountInString(e.Predicate) > 200 {
			return domain.ErrValidation
		}
		seen[e.ID] = true
		if slices.Contains([]string{"plan", "dream", "hypothetical"}, e.Context) && !slices.Contains([]string{"belief", "claim", "intent", "hypothesis", "promise", "summary"}, e.Kind) {
			return domain.ErrValidation
		}
		if e.Visibility == "restricted" && len(e.KnownBy) == 0 {
			return domain.ErrValidation
		}
		for i, q := range e.Quotes {
			if strings.TrimSpace(q) == "" || utf8.RuneCountInString(q) > 8000 || e.Evidence[i].Quote != q || e.Evidence[i].Start < 0 || e.Evidence[i].End-e.Evidence[i].Start != utf8.RuneCountInString(q) {
				return domain.ErrValidation
			}
		}
	}
	return nil
}

// Published data comes exclusively from an intact immutable joint release.
// Neither a private chapter nor a private ledger can fill missing artifacts.
func (s *NovelChapters) storyLedgerPublishedTx(ctx context.Context, tx *sql.Tx, book NovelBook, control string, ledger domain.Resource) (novel.ChapterLedger, *novel.ChapterDocument, error) {
	var empty novel.ChapterLedger
	release, err := releaseTx(ctx, tx, book.ProjectID, ledger.Head)
	if err != nil {
		return empty, nil, err
	}
	if release.Status != "PUBLISHED" || release.Manifest.BranchID != book.BranchID {
		return empty, nil, domain.ErrNotReady
	}
	refs := map[string]domain.ArtifactRef{}
	for _, ref := range release.Manifest.Artifacts {
		if _, duplicate := refs[ref.ResourceID]; duplicate {
			return empty, nil, domain.ErrNotReady
		}
		refs[ref.ResourceID] = ref
	}
	read := func(id, kind string) ([]byte, error) {
		ref, exists := refs[id]
		if !exists || ref.Size < 0 || ref.Size > storyLedgerItemBytes {
			return nil, domain.ErrNotReady
		}
		r, e := resourceTx(ctx, tx, book.ProjectID, id)
		if e != nil {
			return nil, e
		}
		if r.Kind != kind || r.BranchID != book.BranchID || r.Head != release.ID || r.Hash != ref.Hash {
			return nil, domain.ErrNotReady
		}
		raw, e := s.IP.Blobs.ReadObject(ctx, book.ProjectID, ref.Hash)
		if e != nil || digest(string(raw)) != ref.Hash || int64(len(raw)) != ref.Size {
			return nil, domain.ErrNotReady
		}
		return raw, nil
	}
	controlRaw, e := read(control, "novel_chapter_control")
	if e != nil {
		return empty, nil, e
	}
	chapter, e := novel.DecodeChapter(controlRaw)
	if e != nil {
		return empty, nil, e
	}
	ledgerRaw, e := read(ledger.ID, "novel_chapter_ledger")
	if e != nil {
		return empty, nil, e
	}
	bodyRaw, e := read(chapter.BodyID, "novel_chapter_body")
	if e != nil {
		return empty, nil, e
	}
	var envelope chapterReleaseLedger
	if e = storyLedgerDecode(ledgerRaw, &envelope); e != nil {
		return empty, nil, e
	}
	doc := envelope.Ledger
	raw, _ := json.Marshal(doc)
	if envelope.Schema != 1 || !lowerHex32(envelope.ActorID) || envelope.LedgerRevision < 1 || envelope.LedgerBaseHead != refs[ledger.ID].ExpectedHead || doc.Source.BaseHead != refs[control].ExpectedHead || digest(string(raw)) != envelope.LedgerHash || doc.Source.Hash != refs[control].Hash || doc.Source.ResourceID != control || !bytes.Equal(bodyRaw, []byte(chapter.Text)) || doc.TextHash != digest(chapter.Text) {
		return empty, nil, domain.ErrNotReady
	}
	body, err := resourceTx(ctx, tx, book.ProjectID, chapter.BodyID)
	if err != nil {
		return empty, nil, err
	}
	if body.Name != chapterName("章节正文", chapter.ChapterID) {
		return empty, nil, domain.ErrNotReady
	}
	return doc, &chapter, nil
}
