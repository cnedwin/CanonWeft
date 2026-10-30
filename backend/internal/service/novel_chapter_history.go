package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"novelstudio/internal/domain"
	"novelstudio/internal/novel"
	"novelstudio/internal/retention"
)

const chapterHistoryKind = "novel_chapter_history"
const chapterHistorySchema = "novel-chapter-history-v1"

// Source pins describe the archived manuscript only. They never grant current
// planning, generation or publication eligibility, including after recovery.
type ChapterHistoryMeta struct {
	ID             string `json:"id"`
	ChapterID      string `json:"chapter_id"`
	SourceRevision int64  `json:"source_revision"`
	SourceHash     string `json:"source_hash"`
	SourceBaseHead string `json:"source_base_head"`
	TextHash       string `json:"text_hash"`
	Title          string `json:"title"`
	Characters     int    `json:"characters"`
	Kind           string `json:"kind"`
	Note           string `json:"note"`
	CreatedAt      string `json:"created_at"`
	OperationID    string `json:"operation_id,omitempty"`
}
type ChapterHistoryDocument struct {
	ChapterHistoryMeta
	Schema      string `json:"schema"`
	Text        string `json:"text"`
	RequestHash string `json:"request_hash,omitempty"`
}
type ChapterHistoryPage struct {
	Items []ChapterHistoryMeta `json:"items"`
	Next  string               `json:"next"`
}
type ChapterHistoryInput struct {
	Revision    int64  `json:"revision"`
	Hash        string `json:"hash"`
	Note        string `json:"note"`
	OperationID string `json:"operation_id"`
}
type chapterHistorySource struct {
	document   novel.ChapterDocument
	revision   int64
	hash, base string
}

func chapterHistoryPrefix(chapter string) string { return "章节修订 · " + chapter + " · " }
func historyControlTx(ctx context.Context, tx *sql.Tx, book NovelBook, control string) (domain.Resource, string, error) {
	r, err := resourceTx(ctx, tx, book.ProjectID, control)
	if err != nil {
		return r, "", err
	}
	prefix := "章节工作稿 · "
	if r.BranchID != book.BranchID || r.Kind != "novel_chapter_control" {
		return r, "", domain.ErrForbidden
	}
	chapter := strings.TrimPrefix(r.Name, prefix)
	if chapter == r.Name || !lowerHex32(chapter) {
		return r, "", domain.ErrNotReady
	}
	var count int
	if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM resources WHERE project_id=? AND branch_id=? AND kind='novel_chapter_control' AND name=?", book.ProjectID, book.BranchID, r.Name).Scan(&count); err != nil {
		return r, "", err
	}
	if count != 1 {
		return r, "", domain.ErrNotReady
	}
	return r, chapter, nil
}

func decodeChapterHistory(raw string) (ChapterHistoryDocument, error) {
	var d ChapterHistoryDocument
	if len(raw) > 512*1024 || rejectExpiredRuntime([]byte(raw), "resource_drafts.body") != nil {
		return d, domain.ErrNotReady
	}
	if decodeRecoveryDocument([]byte(raw), &d) != nil || d.Schema != chapterHistorySchema || !lowerHex32(d.ChapterID) || !lowerHex32(d.ID) || !hexHash(d.SourceHash) || d.SourceRevision < 0 || (d.SourceBaseHead != "" && !lowerHex32(d.SourceBaseHead)) || d.TextHash != digest(d.Text) || !utf8.ValidString(d.Text) || utf8.RuneCountInString(d.Text) > 100000 || d.Characters != utf8.RuneCountInString(d.Text) || utf8.RuneCountInString(d.Title) > 300 || utf8.RuneCountInString(d.Note) > 120 {
		return d, domain.ErrNotReady
	}
	if d.Kind != "auto" && d.Kind != "manual" && d.Kind != "before_restore" && d.Kind != "before_replace" {
		return d, domain.ErrNotReady
	}
	if _, err := time.Parse(time.RFC3339Nano, d.CreatedAt); err != nil {
		return d, domain.ErrNotReady
	}
	return d, nil
}
func hexHash(v string) bool {
	if len(v) != 64 {
		return false
	}
	for _, c := range v {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func readHistoryTx(ctx context.Context, tx *sql.Tx, a domain.Actor, book NovelBook, chapter, id string) (ChapterHistoryDocument, error) {
	var raw, name string
	var revision int64
	err := tx.QueryRowContext(ctx, `SELECT d.body,r.name,d.revision FROM resources r JOIN resource_drafts d ON d.resource_id=r.id WHERE r.id=? AND r.project_id=? AND r.branch_id=? AND r.kind=? AND d.actor_id=?`, id, book.ProjectID, book.BranchID, chapterHistoryKind, a.ID).Scan(&raw, &name, &revision)
	if err == sql.ErrNoRows {
		return ChapterHistoryDocument{}, domain.ErrForbidden
	}
	if err != nil {
		return ChapterHistoryDocument{}, err
	}
	d, err := decodeChapterHistory(raw)
	// The embedded original ID deliberately survives project-copy recovery. The
	// physical resource ID may be rebound; stable chapter identity is unchanged.
	if err != nil || revision != 1 || d.ChapterID != chapter || name != chapterHistoryPrefix(chapter)+d.ID {
		return d, domain.ErrNotReady
	}
	d.ID = id
	return d, nil
}

func (s *NovelChapters) History(ctx context.Context, actor, bookID, control, after, operation string) (ChapterHistoryPage, error) {
	out := ChapterHistoryPage{Items: []ChapterHistoryMeta{}}
	if (after != "" && !lowerHex32(after)) || (operation != "" && !validName(operation, 120)) || after != "" && operation != "" {
		return out, domain.ErrValidation
	}
	tx, a, book, err := (&NovelPlanning{IP: s.IP}).begin(ctx, actor, bookID, true)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	_, chapter, err := historyControlTx(ctx, tx, book, control)
	if err != nil {
		return out, err
	}
	where := `r.project_id=? AND r.branch_id=? AND r.kind=? AND d.actor_id=? AND r.name LIKE ?`
	args := []any{book.ProjectID, book.BranchID, chapterHistoryKind, a.ID, chapterHistoryPrefix(chapter) + "%"}
	if after != "" {
		if _, err = readHistoryTx(ctx, tx, a, book, chapter, after); err != nil {
			return out, err
		}
		where += ` AND (r.created_at,r.id)<(SELECT created_at,id FROM resources WHERE id=?)`
		args = append(args, after)
	}
	if operation != "" {
		where += ` AND JSON_UNQUOTE(JSON_EXTRACT(d.body,'$.operation_id'))=?`
		args = append(args, operation)
	}
	// Metadata is projected in SQL, so browsing does not transfer or allocate
	// the bodies of a page of long chapters. Exact text is verified on demand.
	rows, err := tx.QueryContext(ctx, `SELECT r.id,JSON_OBJECT('chapter_id',JSON_EXTRACT(d.body,'$.chapter_id'),'source_revision',JSON_EXTRACT(d.body,'$.source_revision'),'source_hash',JSON_EXTRACT(d.body,'$.source_hash'),'source_base_head',JSON_EXTRACT(d.body,'$.source_base_head'),'text_hash',JSON_EXTRACT(d.body,'$.text_hash'),'title',JSON_EXTRACT(d.body,'$.title'),'characters',JSON_EXTRACT(d.body,'$.characters'),'kind',JSON_EXTRACT(d.body,'$.kind'),'note',JSON_EXTRACT(d.body,'$.note'),'created_at',JSON_EXTRACT(d.body,'$.created_at'),'operation_id',COALESCE(JSON_EXTRACT(d.body,'$.operation_id'),JSON_QUOTE(''))) FROM resources r JOIN resource_drafts d ON d.resource_id=r.id WHERE `+where+` ORDER BY r.created_at DESC,r.id DESC LIMIT 31`, args...)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, raw string
		if err = rows.Scan(&id, &raw); err != nil {
			return out, err
		}
		var m ChapterHistoryMeta
		if json.Unmarshal([]byte(raw), &m) != nil || m.ChapterID != chapter || !hexHash(m.TextHash) || !hexHash(m.SourceHash) || m.Characters < 0 || m.Characters > 100000 {
			return out, domain.ErrNotReady
		}
		m.ID = id
		out.Items = append(out.Items, m)
	}
	if err = rows.Err(); err != nil {
		return out, err
	}
	if len(out.Items) > 30 {
		out.Items = out.Items[:30]
		out.Next = out.Items[29].ID
	}
	return out, nil
}
func (s *NovelChapters) HistoryText(ctx context.Context, actor, bookID, control, id string) (ChapterHistoryDocument, error) {
	if !lowerHex32(id) {
		return ChapterHistoryDocument{}, domain.ErrValidation
	}
	tx, a, book, err := (&NovelPlanning{IP: s.IP}).begin(ctx, actor, bookID, true)
	if err != nil {
		return ChapterHistoryDocument{}, err
	}
	defer tx.Rollback()
	_, chapter, err := historyControlTx(ctx, tx, book, control)
	if err != nil {
		return ChapterHistoryDocument{}, err
	}
	return readHistoryTx(ctx, tx, a, book, chapter, id)
}
func (s *NovelChapters) Checkpoint(ctx context.Context, actor, bookID, control string, in ChapterHistoryInput) (ChapterHistoryDocument, error) {
	var empty ChapterHistoryDocument
	if in.Revision < 0 || !hexHash(in.Hash) || !validName(in.OperationID, 120) || !utf8.ValidString(in.Note) || utf8.RuneCountInString(in.Note) > 120 {
		return empty, domain.ErrValidation
	}
	tx, a, book, err := (&NovelPlanning{IP: s.IP}).begin(ctx, actor, bookID, true)
	if err != nil {
		return empty, err
	}
	defer tx.Rollback()
	_, chapter, err := historyControlTx(ctx, tx, book, control)
	if err != nil {
		return empty, err
	}
	id := digest("chapter-checkpoint:" + a.ID + ":" + bookID + ":" + control + ":" + in.OperationID)[:32]
	var exists int
	if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM resources WHERE id=?", id).Scan(&exists); err != nil {
		return empty, err
	}
	requestHash := capabilityDigest(in)
	if exists > 0 {
		prior, e := readHistoryTx(ctx, tx, a, book, chapter, id)
		if e != nil {
			return empty, e
		}
		if prior.RequestHash != requestHash {
			return empty, domain.ErrConflict
		}
		return prior, nil
	}
	current, err := s.draftTx(ctx, tx, a, book, control)
	if err != nil {
		return empty, err
	}
	if current.Revision != in.Revision || current.Hash != in.Hash || (current.Revision == 0 && current.BaseHead == "") {
		return empty, domain.ErrConflict
	}
	source := chapterHistorySource{current.Document, current.Revision, current.Hash, current.BaseHead}
	result, err := s.IP.insertChapterHistoryTx(ctx, tx, a, book.ProjectID, book.BranchID, id, source, "manual", in.Note, in.OperationID, requestHash)
	if err != nil {
		return empty, err
	}
	return result, tx.Commit()
}

func (s *IP) insertChapterHistoryTx(ctx context.Context, tx *sql.Tx, a domain.Actor, project, branch, id string, source chapterHistorySource, kind, note, op, request string) (ChapterHistoryDocument, error) {
	d := ChapterHistoryDocument{ChapterHistoryMeta: ChapterHistoryMeta{ID: id, ChapterID: source.document.ChapterID, SourceRevision: source.revision, SourceHash: source.hash, SourceBaseHead: source.base, TextHash: digest(source.document.Text), Title: source.document.Title, Characters: utf8.RuneCountInString(source.document.Text), Kind: kind, Note: note, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), OperationID: op}, Schema: chapterHistorySchema, Text: source.document.Text, RequestHash: request}
	raw, err := json.Marshal(d)
	if err != nil {
		return d, err
	}
	if _, err = decodeChapterHistory(string(raw)); err != nil {
		return d, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO resources(id,project_id,branch_id,name,kind,created_at) VALUES(?,?,?,?,?,UTC_TIMESTAMP(6))", id, project, branch, chapterHistoryPrefix(d.ChapterID)+id, chapterHistoryKind); err != nil {
		return d, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO resource_drafts(resource_id,actor_id,revision,base_head,body,updated_at) VALUES(?,?,1,'',?,UTC_TIMESTAMP(6))", id, a.ID, raw); err != nil {
		return d, err
	}
	policy, err := json.Marshal(&domain.KnowledgePolicy{Space: "working", Visibility: "author"})
	if err != nil {
		return d, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO resource_policy_drafts(resource_id,actor_id,revision,payload) VALUES(?,?,1,?)", id, a.ID, policy); err != nil {
		return d, err
	}
	if err = captureMetadataSourceTx(ctx, tx, project, "resources", id); err != nil {
		return d, err
	}
	if err = s.captureSourceValuesTx(ctx, tx, project, retention.SourceKey{Table: "resource_drafts", Primary: map[string]string{"resource_id": id, "actor_id": a.ID}}, retention.SourceKey{Table: "resource_policy_drafts", Primary: map[string]string{"resource_id": id, "actor_id": a.ID}}); err != nil {
		return d, err
	}
	if err = audit(ctx, tx, a, "novel.chapter.checkpoint", id); err != nil {
		return d, err
	}
	return d, nil
}

// Called under the normal resource/draft locks, in the same transaction as
// the new saved manuscript. A failed save cannot leave a claimed checkpoint.
func (s *IP) checkpointChapterSaveTx(ctx context.Context, tx *sql.Tx, a domain.Actor, project string, r domain.Resource, in ResourceDraft) error {
	if r.Kind != "novel_chapter_control" {
		return nil
	}
	chapter := strings.TrimPrefix(r.Name, "章节工作稿 · ")
	if !lowerHex32(chapter) {
		return domain.ErrNotReady
	}
	next, err := novel.DecodeChapter([]byte(in.Text))
	if err != nil {
		return nil
	}
	if next.ChapterID != chapter {
		return domain.ErrNotReady
	}
	current, err := resourceDraftTx(ctx, tx, a.ID, project, r.ID)
	if err != nil {
		return err
	}
	source := chapterHistorySource{next, in.Revision + 1, digest(in.Text), in.BaseHead}
	if current.Revision > 0 {
		previous, e := novel.DecodeChapter([]byte(current.Text))
		if e != nil {
			return e
		}
		if previous.Text == next.Text {
			return nil
		}
		// Recipe and generation adoption can also use the shared draft writer.
		// A different writer candidate is a critical replacement even without
		// the editor's optional transient reason.
		if in.historyReason == "" && previous.WriterGenerationID != next.WriterGenerationID {
			in.historyReason = "before_replace"
		}
		source = chapterHistorySource{previous, current.Revision, digest(current.Text), current.BaseHead}
	} else if in.historySource != nil {
		source = *in.historySource
	}
	if source.document.Text == "" && in.historyReason == "" {
		source = chapterHistorySource{next, in.Revision + 1, digest(in.Text), in.BaseHead}
	}
	if source.document.Text == "" && in.historyReason == "" {
		return nil
	}
	kind := in.historyReason
	if kind == "" {
		kind = "auto"
	}
	var lastText string
	var recent bool
	err = tx.QueryRowContext(ctx, `SELECT JSON_UNQUOTE(JSON_EXTRACT(d.body,'$.text_hash')),r.created_at>UTC_TIMESTAMP(6)-INTERVAL 5 MINUTE FROM resources r JOIN resource_drafts d ON d.resource_id=r.id WHERE r.project_id=? AND r.branch_id=? AND r.kind=? AND d.actor_id=? AND r.name LIKE ? ORDER BY r.created_at DESC,r.id DESC LIMIT 1`, project, r.BranchID, chapterHistoryKind, a.ID, chapterHistoryPrefix(chapter)+"%").Scan(&lastText, &recent)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	if lastText == digest(source.document.Text) || (kind == "auto" && recent) {
		return nil
	}
	id := digest(fmt.Sprintf("chapter-auto:%s:%s:%d:%s:%s", a.ID, r.ID, source.revision, source.hash, kind))[:32]
	_, err = s.insertChapterHistoryTx(ctx, tx, a, project, r.BranchID, id, source, kind, "", "", "")
	return err
}
