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

const manuscriptKind = "novel_manuscript_import"
const manuscriptProgressKind = "novel_manuscript_progress"
const manuscriptSchema = "novel-manuscript-import-v1"

func manuscriptPrivateKind(kind string) bool {
	return kind == manuscriptKind || kind == manuscriptProgressKind
}

type ManuscriptDocument struct {
	Schema        string                 `json:"schema"`
	BookID        string                 `json:"book_id"`
	SourceID      string                 `json:"source_id"`
	SourceHash    string                 `json:"source_hash"`
	TextHash      string                 `json:"text_hash"`
	Total         int                    `json:"total"`
	Complete      bool                   `json:"complete"`
	AcceptPartial bool                   `json:"accept_partial"`
	Parts         []novel.ManuscriptPart `json:"parts"`
	OperationID   string                 `json:"operation_id"`
	RequestHash   string                 `json:"request_hash"`
	CreatedAt     string                 `json:"created_at"`
	Recovered     bool                   `json:"recovered,omitempty"`
}
type ManuscriptProgress struct {
	Schema        string `json:"schema"`
	BatchID       string `json:"batch_id"`
	PartID        string `json:"part_id"`
	SpecHash      string `json:"spec_hash"`
	Status        string `json:"status"`
	Reason        string `json:"reason"`
	ChapterID     string `json:"chapter_id"`
	ControlID     string `json:"control_id"`
	SavedRevision int64  `json:"saved_revision"`
	SavedHash     string `json:"saved_hash"`
	OperationID   string `json:"operation_id"`
	RequestHash   string `json:"request_hash"`
	Revision      int64  `json:"revision"`
	UpdatedAt     string `json:"updated_at"`
}
type ManuscriptBatch struct {
	ID       string               `json:"id"`
	Revision int64                `json:"revision"`
	Hash     string               `json:"hash"`
	Document ManuscriptDocument   `json:"document"`
	Progress []ManuscriptProgress `json:"progress"`
}
type ManuscriptCreate struct {
	SourceID    string `json:"source_id"`
	OperationID string `json:"operation_id"`
}
type ManuscriptLayout struct {
	Revision      int64                  `json:"revision"`
	Hash          string                 `json:"hash"`
	Parts         []novel.ManuscriptPart `json:"parts"`
	AcceptPartial bool                   `json:"accept_partial"`
	OperationID   string                 `json:"operation_id"`
}
type ManuscriptApply struct {
	Revision         int64  `json:"revision"`
	Hash             string `json:"hash"`
	ProgressRevision int64  `json:"progress_revision"`
	OperationID      string `json:"operation_id"`
	AllowDuplicate   bool   `json:"allow_duplicate"`
	ConfirmReplace   bool   `json:"confirm_replace"`
}
type ManuscriptMatch struct {
	ControlID string `json:"control_id"`
	ChapterID string `json:"chapter_id"`
	Title     string `json:"title"`
	Exact     bool   `json:"exact"`
	Revision  int64  `json:"revision"`
	Hash      string `json:"hash"`
	BaseHead  string `json:"base_head"`
}
type ManuscriptPreview struct {
	PartID     string            `json:"part_id"`
	Text       string            `json:"text"`
	Start      int               `json:"start"`
	End        int               `json:"end"`
	Next       int               `json:"next"`
	Characters int               `json:"characters"`
	Bytes      int               `json:"bytes"`
	Importable bool              `json:"importable"`
	Matches    []ManuscriptMatch `json:"matches"`
	Uncertain  int               `json:"uncertain"`
	Notes      []string          `json:"notes"`
}

func manuscriptID(values ...string) string { return digest(strings.Join(values, "\x00"))[:32] }
func manuscriptPartHash(p novel.ManuscriptPart) string {
	raw, _ := json.Marshal(p)
	return digest(string(raw))
}

func (s *NovelManuscripts) privateSaveTx(ctx context.Context, tx *sql.Tx, a domain.Actor, book NovelBook, id, kind, name string, revision int64, raw []byte) (int64, error) {
	if len(raw) > 950<<10 {
		return 0, domain.ErrValidation
	}
	r, err := resourceTx(ctx, tx, book.ProjectID, id)
	if err != nil && err != domain.ErrForbidden {
		return 0, err
	}
	if err == domain.ErrForbidden {
		if revision != 0 {
			return 0, domain.ErrConflict
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO resources(id,project_id,branch_id,name,kind) VALUES(?,?,?,?,?)", id, book.ProjectID, book.BranchID, name, kind); err != nil {
			return 0, err
		}
		if err = audit(ctx, tx, a, "resource.create", id); err != nil {
			return 0, err
		}
		if err = captureMetadataSourceTx(ctx, tx, book.ProjectID, "resources", id); err != nil {
			return 0, err
		}
	} else if r.Kind != kind || r.BranchID != book.BranchID {
		return 0, domain.ErrForbidden
	}
	current, err := resourceDraftTx(ctx, tx, a.ID, book.ProjectID, id)
	if err != nil {
		return 0, err
	}
	if current.Revision != revision {
		return 0, domain.ErrConflict
	}
	sources := []retention.SourceKey{{Table: "resource_drafts", Primary: map[string]string{"resource_id": id, "actor_id": a.ID}, IfPresent: true}, {Table: "resource_policy_drafts", Primary: map[string]string{"resource_id": id, "actor_id": a.ID}, IfPresent: true}}
	if err = s.IP.captureSourceValuesTx(ctx, tx, book.ProjectID, sources...); err != nil {
		return 0, err
	}
	revision++
	if _, err = tx.ExecContext(ctx, "INSERT INTO resource_drafts(resource_id,actor_id,revision,base_head,body) VALUES(?,?,?,'',?) ON DUPLICATE KEY UPDATE revision=VALUES(revision),body=VALUES(body),updated_at=UTC_TIMESTAMP(6)", id, a.ID, revision, raw); err != nil {
		return 0, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO resource_policy_drafts(resource_id,actor_id,revision,payload) VALUES(?,?,?,?) ON DUPLICATE KEY UPDATE revision=VALUES(revision),payload=VALUES(payload)`, id, a.ID, revision, `{"space":"working","visibility":"author"}`); err != nil {
		return 0, err
	}
	for i := range sources {
		sources[i].IfPresent = false
	}
	return revision, s.IP.captureSourceValuesTx(ctx, tx, book.ProjectID, sources...)
}

func (s *NovelManuscripts) readTx(ctx context.Context, tx *sql.Tx, a domain.Actor, book NovelBook, id string) (ManuscriptBatch, error) {
	out := ManuscriptBatch{ID: id, Progress: []ManuscriptProgress{}}
	r, err := resourceTx(ctx, tx, book.ProjectID, id)
	if err != nil {
		return out, err
	}
	if r.Kind != manuscriptKind || r.BranchID != book.BranchID {
		return out, domain.ErrForbidden
	}
	d, err := resourceDraftTx(ctx, tx, a.ID, book.ProjectID, id)
	if err != nil {
		return out, err
	}
	if d.Revision == 0 {
		return out, domain.ErrForbidden
	}
	if len(d.Text) > 950<<10 || rejectExpiredRuntime([]byte(d.Text), "resource_drafts.body") != nil || decodeRecoveryDocument([]byte(d.Text), &out.Document) != nil {
		return out, domain.ErrNotReady
	}
	doc := out.Document
	if doc.BookID != book.ID {
		return out, domain.ErrForbidden
	}
	if doc.Schema != manuscriptSchema || !lowerHex32(doc.SourceID) || !hexHash(doc.SourceHash) || !hexHash(doc.TextHash) || doc.Total < 1 || novel.ValidateManuscriptParts(doc.Parts, doc.Total) != nil {
		return out, domain.ErrNotReady
	}
	out.Revision = d.Revision
	out.Hash = digest(d.Text)
	rows, err := tx.QueryContext(ctx, `SELECT d.body,d.revision FROM resources r JOIN resource_drafts d ON d.resource_id=r.id AND d.actor_id=? WHERE r.project_id=? AND r.branch_id=? AND r.kind=? AND r.name LIKE ? ORDER BY r.id LIMIT 2001`, a.ID, book.ProjectID, book.BranchID, manuscriptProgressKind, "导入进度 · "+id+" · %")
	if err != nil {
		return out, err
	}
	defer rows.Close()
	seen := map[string]bool{}
	for rows.Next() {
		var p ManuscriptProgress
		var raw string
		var rev int64
		if err = rows.Scan(&raw, &rev); err != nil {
			return out, err
		}
		if len(raw) > 16<<10 || rejectExpiredRuntime([]byte(raw), "resource_drafts.body") != nil || decodeRecoveryDocument([]byte(raw), &p) != nil || p.Schema != "novel-manuscript-progress-v1" || p.BatchID != id || seen[p.PartID] || p.Revision != rev {
			return out, domain.ErrNotReady
		}
		seen[p.PartID] = true
		out.Progress = append(out.Progress, p)
	}
	return out, rows.Err()
}
func (s *NovelManuscripts) Read(ctx context.Context, actor, bookID, id string) (ManuscriptBatch, error) {
	tx, a, book, err := (&NovelPlanning{IP: s.IP}).begin(ctx, actor, bookID, false)
	if err != nil {
		return ManuscriptBatch{}, err
	}
	defer tx.Rollback()
	return s.readTx(ctx, tx, a, book, id)
}
func (s *NovelManuscripts) BySource(ctx context.Context, actor, bookID, sourceID string) (*ManuscriptBatch, error) {
	tx, a, book, err := (&NovelPlanning{IP: s.IP}).begin(ctx, actor, bookID, false)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	source, err := importTx(ctx, tx, a.ID, book.ProjectID, book.BranchID, sourceID)
	if err != nil {
		return nil, err
	}
	id := manuscriptID(a.ID, book.ID, source.Hash, digest(source.Report.Text))
	var count int
	if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM resources WHERE id=? AND project_id=?", id, book.ProjectID).Scan(&count); err != nil {
		return nil, err
	}
	if count == 0 {
		return nil, nil
	}
	b, err := s.readTx(ctx, tx, a, book, id)
	return &b, err
}
func (s *NovelManuscripts) List(ctx context.Context, actor, bookID, after string) ([]ManuscriptBatch, error) {
	if after != "" && !lowerHex32(after) {
		return nil, domain.ErrValidation
	}
	tx, a, book, err := (&NovelPlanning{IP: s.IP}).begin(ctx, actor, bookID, false)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT r.id FROM resources r JOIN resource_drafts d ON d.resource_id=r.id AND d.actor_id=? WHERE r.project_id=? AND r.branch_id=? AND r.kind=? AND r.id>? AND JSON_UNQUOTE(JSON_EXTRACT(IF(JSON_VALID(d.body),d.body,'{}'),'$.book_id'))=? ORDER BY r.id LIMIT 51`, a.ID, book.ProjectID, book.BranchID, manuscriptKind, after, book.ID)
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	out := []ManuscriptBatch{}
	for _, id := range ids {
		d, e := resourceDraftTx(ctx, tx, a.ID, book.ProjectID, id)
		if e != nil {
			return nil, e
		}
		v := ManuscriptBatch{ID: id, Revision: d.Revision, Hash: digest(d.Text), Progress: []ManuscriptProgress{}}
		if len(d.Text) > 950<<10 || rejectExpiredRuntime([]byte(d.Text), "resource_drafts.body") != nil || decodeRecoveryDocument([]byte(d.Text), &v.Document) != nil || v.Document.Schema != manuscriptSchema || v.Document.BookID != book.ID {
			return nil, domain.ErrNotReady
		}
		v.Document.Parts = []novel.ManuscriptPart{}
		out = append(out, v)
	}
	return out, nil
}
func (s *NovelManuscripts) Create(ctx context.Context, actor, bookID string, in ManuscriptCreate) (ManuscriptBatch, error) {
	if !lowerHex32(in.SourceID) || !validName(in.OperationID, 100) {
		return ManuscriptBatch{}, domain.ErrValidation
	}
	tx, a, book, err := (&NovelPlanning{IP: s.IP}).begin(ctx, actor, bookID, true)
	if err != nil {
		return ManuscriptBatch{}, err
	}
	defer tx.Rollback()
	source, err := importTx(ctx, tx, a.ID, book.ProjectID, book.BranchID, in.SourceID)
	if err != nil {
		return ManuscriptBatch{}, err
	}
	text := source.Report.Text
	parts, err := novel.SplitManuscript(text)
	if err != nil {
		return ManuscriptBatch{}, fmt.Errorf("%w：没有可分章的文本或超过容量，请保留原稿并调整格式/大小", domain.ErrValidation)
	}
	id := manuscriptID(a.ID, book.ID, source.Hash, digest(text))
	var count int
	if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM resources WHERE id=?", id).Scan(&count); err != nil {
		return ManuscriptBatch{}, err
	}
	if count > 0 {
		return s.readTx(ctx, tx, a, book, id)
	}
	var retained int
	if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM import_drafts WHERE import_id=? AND actor_id=?", source.ID, a.ID).Scan(&retained); err != nil {
		return ManuscriptBatch{}, err
	}
	if retained == 0 {
		b, e := branchTx(ctx, tx, book.ProjectID, book.BranchID)
		if e != nil {
			return ManuscriptBatch{}, e
		}
		keep := domain.ImportDraftContent{BaseIPHead: b.Head, OperationID: "manuscript-source-" + source.ID, Policy: &domain.KnowledgePolicy{Space: "working", Visibility: "author"}}
		payload, _ := json.Marshal(keep)
		if _, err = tx.ExecContext(ctx, "INSERT INTO import_drafts(import_id,actor_id,project_id,revision,payload,content_hash) VALUES(?,?,?,1,?,?)", source.ID, a.ID, book.ProjectID, payload, digest(string(payload))); err != nil {
			return ManuscriptBatch{}, err
		}
		if err = s.IP.captureSourceValuesTx(ctx, tx, book.ProjectID, retention.SourceKey{Table: "import_drafts", Primary: map[string]string{"import_id": source.ID, "actor_id": a.ID}}); err != nil {
			return ManuscriptBatch{}, err
		}
	}
	doc := ManuscriptDocument{Schema: manuscriptSchema, BookID: book.ID, SourceID: source.ID, SourceHash: source.Hash, TextHash: digest(text), Total: len([]rune(text)), Complete: source.Report.Complete, Parts: parts, OperationID: in.OperationID, RequestHash: capabilityDigest(in), CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	raw, _ := json.Marshal(doc)
	rev, err := s.privateSaveTx(ctx, tx, a, book, id, manuscriptKind, "长稿导入 · "+id, 0, raw)
	if err != nil {
		return ManuscriptBatch{}, err
	}
	if err = tx.Commit(); err != nil {
		return ManuscriptBatch{}, err
	}
	return ManuscriptBatch{ID: id, Revision: rev, Hash: digest(string(raw)), Document: doc, Progress: []ManuscriptProgress{}}, nil
}

func (s *NovelManuscripts) Save(ctx context.Context, actor, bookID, id string, in ManuscriptLayout) (ManuscriptBatch, error) {
	if !validName(in.OperationID, 100) {
		return ManuscriptBatch{}, domain.ErrValidation
	}
	tx, a, book, err := (&NovelPlanning{IP: s.IP}).begin(ctx, actor, bookID, true)
	if err != nil {
		return ManuscriptBatch{}, err
	}
	defer tx.Rollback()
	b, err := s.readTx(ctx, tx, a, book, id)
	if err != nil {
		return b, err
	}
	if b.Document.Recovered {
		return b, domain.ErrNotReady
	}
	hash := capabilityDigest(in)
	if b.Document.OperationID == in.OperationID {
		if b.Document.RequestHash != hash {
			return b, domain.ErrConflict
		}
		return b, nil
	}
	if b.Revision != in.Revision || b.Hash != in.Hash {
		return b, domain.ErrConflict
	}
	if novel.ValidateManuscriptParts(in.Parts, b.Document.Total) != nil {
		return b, fmt.Errorf("%w：分章必须完整覆盖已解析原文，位置不能重叠或遗漏；最多2000段", domain.ErrValidation)
	}
	// Imported or intentionally skipped ranges cannot be silently re-imported.
	byID := map[string]novel.ManuscriptPart{}
	for _, p := range in.Parts {
		byID[p.ID] = p
	}
	for _, p := range b.Progress {
		if p.Status == "done" || p.Status == "skipped" {
			next, ok := byID[p.PartID]
			if !ok || manuscriptPartHash(next) != p.SpecHash {
				return b, fmt.Errorf("%w：已完成分章必须保留，不能重切或重复写入", domain.ErrConflict)
			}
		}
	}
	b.Document.Parts = in.Parts
	b.Document.AcceptPartial = in.AcceptPartial
	b.Document.OperationID = in.OperationID
	b.Document.RequestHash = hash
	raw, _ := json.Marshal(b.Document)
	b.Revision, err = s.privateSaveTx(ctx, tx, a, book, id, manuscriptKind, "长稿导入 · "+id, b.Revision, raw)
	if err != nil {
		return b, err
	}
	b.Hash = digest(string(raw))
	return b, tx.Commit()
}
func manuscriptPart(b ManuscriptBatch, id string) (novel.ManuscriptPart, *ManuscriptProgress, error) {
	for _, p := range b.Document.Parts {
		if p.ID == id {
			for i := range b.Progress {
				if b.Progress[i].PartID == id {
					return p, &b.Progress[i], nil
				}
			}
			return p, nil, nil
		}
	}
	return novel.ManuscriptPart{}, nil, domain.ErrValidation
}

// Preview is paged, read-only and bounded. The entire original remains downloadable.
func (s *NovelManuscripts) Preview(ctx context.Context, actor, bookID, id, partID string, offset int) (ManuscriptPreview, error) {
	out := ManuscriptPreview{PartID: partID, Matches: []ManuscriptMatch{}, Notes: []string{}}
	tx, a, book, err := (&NovelPlanning{IP: s.IP}).begin(ctx, actor, bookID, false)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	b, err := s.readTx(ctx, tx, a, book, id)
	if err != nil {
		return out, err
	}
	p, _, err := manuscriptPart(b, partID)
	if err != nil {
		return out, err
	}
	source, err := manuscriptSourceText(ctx, tx, a, book, b.Document.SourceID, b.Document.SourceHash, b.Document.TextHash)
	if err != nil {
		return out, err
	}
	runes := []rune(source.Report.Text)
	if len(runes) != b.Document.Total {
		return out, domain.ErrNotReady
	}
	if offset < 0 || offset >= p.End-p.Start {
		return out, domain.ErrValidation
	}
	out.Start = p.Start + offset
	out.End = min(p.End, out.Start+20000)
	out.Text = string(runes[out.Start:out.End])
	if out.End < p.End {
		out.Next = out.End - p.Start
	}
	out.Characters = p.End - p.Start
	text := string(runes[p.Start:p.End])
	out.Bytes = len(text)
	out.Importable = out.Characters <= 100000 && out.Bytes <= 250<<10
	out.Matches, out.Uncertain, err = s.duplicatesTx(ctx, tx, a, book, p.Title, text)
	if !out.Importable {
		out.Notes = append(out.Notes, "本段超过单章容量，请先手动分章。")
	}
	if out.Uncertain > 0 {
		out.Notes = append(out.Notes, "有现存章稿来源不可完整核对，不能当作没有重复。")
	}
	return out, err
}

func (s *NovelManuscripts) duplicatesTx(ctx context.Context, tx *sql.Tx, a domain.Actor, book NovelBook, title, text string) ([]ManuscriptMatch, int, error) {
	// SQL filters metadata before verified reads; malformed and public-only
	// sources are still inspected, rather than being reported as no match.
	rows, err := tx.QueryContext(ctx, `SELECT r.id,r.name FROM resources r LEFT JOIN resource_drafts d ON d.resource_id=r.id AND d.actor_id=? WHERE r.project_id=? AND r.branch_id=? AND r.kind='novel_chapter_control' AND (d.resource_id IS NULL OR NOT JSON_VALID(d.body) OR JSON_UNQUOTE(JSON_EXTRACT(IF(JSON_VALID(d.body),d.body,'{}'),'$.title'))=? OR SHA2(JSON_UNQUOTE(JSON_EXTRACT(IF(JSON_VALID(d.body),d.body,'{}'),'$.text')),256)=?) ORDER BY r.id LIMIT 201`, a.ID, book.ProjectID, book.BranchID, title, digest(text))
	if err != nil {
		return nil, 0, err
	}
	type row struct{ id, name string }
	values := []row{}
	for rows.Next() {
		var v row
		if err = rows.Scan(&v.id, &v.name); err != nil {
			rows.Close()
			return nil, 0, err
		}
		values = append(values, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, 0, err
	}
	out := []ManuscriptMatch{}
	uncertain := 0
	for i, v := range values {
		if i == 200 {
			uncertain++
			break
		}
		chapter := strings.TrimPrefix(v.name, "章节工作稿 · ")
		item := NovelProseSearchItem{ControlID: v.id, ChapterID: chapter}
		saved, e := (&NovelChapters{IP: s.IP}).savedProseTx(ctx, tx, a, book, "working", &item)
		if e != nil {
			if !proseSearchUnavailable(e) {
				return nil, 0, e
			}
			uncertain++
			continue
		}
		if item.Origin == "" {
			continue
		}
		if saved != text && item.ChapterTitle != title {
			continue
		}
		d, e := (&NovelChapters{IP: s.IP}).draftTx(ctx, tx, a, book, v.id)
		if e != nil {
			return nil, 0, e
		}
		if len(out) >= 20 {
			uncertain++
			continue
		}
		out = append(out, ManuscriptMatch{ControlID: v.id, ChapterID: chapter, Title: item.ChapterTitle, Exact: saved == text, Revision: d.Revision, Hash: d.Hash, BaseHead: d.BaseHead})
	}
	return out, uncertain, nil
}

func (s *NovelManuscripts) Apply(ctx context.Context, actor, bookID, id, partID string, in ManuscriptApply) (ManuscriptProgress, error) {
	var out ManuscriptProgress
	if !validName(in.OperationID, 100) {
		return out, domain.ErrValidation
	}
	tx, a, book, err := (&NovelPlanning{IP: s.IP}).begin(ctx, actor, bookID, true)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	b, err := s.readTx(ctx, tx, a, book, id)
	if err != nil {
		return out, err
	}
	p, old, err := manuscriptPart(b, partID)
	if err != nil {
		return out, err
	}
	requestHash := capabilityDigest(in)
	if old != nil && old.OperationID == in.OperationID {
		if old.RequestHash != requestHash {
			return out, domain.ErrConflict
		}
		return *old, nil
	}
	if b.Document.Recovered {
		return out, fmt.Errorf("%w：恢复副本仅保留导入记录；请重新选择原稿建立新导入，避免重复写入", domain.ErrNotReady)
	}
	if b.Revision != in.Revision || b.Hash != in.Hash {
		return out, domain.ErrConflict
	}
	rev := int64(0)
	if old != nil {
		rev = old.Revision
		if old.Status == "done" || old.Status == "skipped" {
			return *old, nil
		}
	}
	if rev != in.ProgressRevision {
		return out, domain.ErrConflict
	}
	source, err := manuscriptSourceText(ctx, tx, a, book, b.Document.SourceID, b.Document.SourceHash, b.Document.TextHash)
	if err != nil {
		return out, err
	}
	out = ManuscriptProgress{Schema: "novel-manuscript-progress-v1", BatchID: id, PartID: partID, SpecHash: manuscriptPartHash(p), Status: "failed", OperationID: in.OperationID, RequestHash: requestHash, Revision: rev + 1, UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	runes := []rune(source.Report.Text)
	if len(runes) != b.Document.Total {
		return out, domain.ErrNotReady
	}
	text := string(runes[p.Start:p.End])
	if !b.Document.Complete && !b.Document.AcceptPartial {
		out.Reason = "解析有遗漏，请先核对并明确接受仅导入已解析部分。"
	} else if p.Mode == "skip" {
		out.Status = "skipped"
	} else if utf8.RuneCountInString(text) > 100000 || len(text) > 250<<10 {
		out.Reason = "本段超过单章容量，请手动分章后重试。"
	} else {
		matches, uncertain, e := s.duplicatesTx(ctx, tx, a, book, p.Title, text)
		if e != nil {
			return out, e
		}
		exact := false
		for _, v := range matches {
			if v.Exact {
				exact = true
			}
		}
		if p.Mode == "new" && (exact || uncertain > 0) && !in.AllowDuplicate {
			out.Reason = "存在相同正文或未完成的来源核对；请选择跳过、明确另存新章或核对后替换。"
		} else if p.Mode == "replace" && !in.ConfirmReplace {
			out.Reason = "替换现存章稿需要明确确认，旧稿将先留存。"
		} else {
			out, err = s.applyChapterTx(ctx, tx, a, book, b, p, text, out)
			if err != nil {
				return out, err
			}
		}
	}
	raw, _ := json.Marshal(out)
	_, err = s.privateSaveTx(ctx, tx, a, book, manuscriptID(a.ID, id, p.ID), manuscriptProgressKind, "导入进度 · "+id+" · "+p.ID, rev, raw)
	if err != nil {
		return out, err
	}
	return out, tx.Commit()
}

func (s *NovelManuscripts) applyChapterTx(ctx context.Context, tx *sql.Tx, a domain.Actor, book NovelBook, b ManuscriptBatch, p novel.ManuscriptPart, text string, out ManuscriptProgress) (ManuscriptProgress, error) {
	chapters := &NovelChapters{IP: s.IP}
	if p.Mode == "replace" {
		d, err := chapters.draftTx(ctx, tx, a, book, p.Target.ControlID)
		if err != nil {
			return out, err
		}
		if d.Revision != p.Target.Revision || d.Hash != p.Target.Hash || d.BaseHead != p.Target.BaseHead {
			out.Reason = "目标章稿已变化，请重新预览选择，当前正文未替换。"
			return out, nil
		}
		d.Document.Title = p.Title
		d.Document.Text = text
		d.HistoryReason = "before_replace"
		d, err = chapters.saveTx(ctx, tx, a, book, p.Target.ControlID, d)
		if err != nil {
			return out, err
		}
		out.ControlID = d.Resource.ID
		out.ChapterID = d.Document.ChapterID
		out.SavedRevision = d.Revision
		out.SavedHash = d.Hash
	} else {
		chapterID := manuscriptID(a.ID, b.ID, p.ID, "chapter")
		control := manuscriptID(chapterID, "control")
		body := manuscriptID(chapterID, "body")
		check, err := newPlanningCheck(ctx, tx, s.IP, book.ProjectID, book.BranchID)
		if err != nil {
			return out, err
		}
		doc := novel.ChapterDocument{Schema: 1, Unplanned: true, ChapterID: chapterID, BodyID: body, SettingsRevision: check.settingsRevision, SettingsHash: check.settingsHash, IPHead: check.head, Title: p.Title, Text: text}
		if err = doc.Validate(); err != nil {
			return out, err
		}
		raw, _ := json.Marshal(doc)
		if len(raw) > 256<<10 {
			out.Reason = "本段编码后的保存体积超过单章容量，请分章。"
			return out, nil
		}
		for _, r := range []domain.Resource{{ID: control, Kind: "novel_chapter_control", Name: chapterName("章节工作稿", chapterID)}, {ID: body, Kind: "novel_chapter_body", Name: chapterName("章节正文", chapterID)}} {
			if _, err = tx.ExecContext(ctx, "INSERT INTO resources(id,project_id,branch_id,name,kind) VALUES(?,?,?,?,?)", r.ID, book.ProjectID, book.BranchID, r.Name, r.Kind); err != nil {
				return out, err
			}
			if err = audit(ctx, tx, a, "resource.create", r.ID); err != nil {
				return out, err
			}
			if err = captureMetadataSourceTx(ctx, tx, book.ProjectID, "resources", r.ID); err != nil {
				return out, err
			}
		}
		d, err := s.IP.saveResourceDraftTx(ctx, tx, a, book.ProjectID, control, ResourceDraft{Text: string(raw), Policy: &domain.KnowledgePolicy{Space: "working", Visibility: "author"}})
		if err != nil {
			return out, err
		}
		out.ControlID = control
		out.ChapterID = chapterID
		out.SavedRevision = d.Revision
		out.SavedHash = digest(string(raw))
	}
	out.Status = "done"
	out.Reason = ""
	return out, nil
}
