package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"novelstudio/internal/domain"
	"novelstudio/internal/novel"
	"novelstudio/internal/research"
)

const researchExecutionSchema = "novel-research-execution-v1"

type NovelResearchExecutions struct {
	Research *NovelResearch
	Sources  *ResearchSources
}
type ResearchExecutionCreate struct {
	Provider         string `json:"provider,omitempty"`
	AutoCollectLimit int    `json:"auto_collect_limit"`
	Revision         int64  `json:"revision"`
	Hash             string `json:"hash"`
	Language         string `json:"language"`
	OperationID      string `json:"operation_id"`
}
type ResearchExecutionHit struct {
	research.Hit
	Selected bool   `json:"selected"`
	ImportID string `json:"import_id,omitempty"`
}
type ResearchExecutionQuery struct {
	novel.ResearchQuery
	Done bool                   `json:"done"`
	Hits []ResearchExecutionHit `json:"hits"`
}
type ResearchExecutionDocument struct {
	Provider         string                   `json:"provider,omitempty"`
	Background       bool                     `json:"background,omitempty"`
	AutoCollectLimit int                      `json:"auto_collect_limit"`
	Schema           string                   `json:"schema"`
	ActorID          string                   `json:"actor_id"`
	BookID           string                   `json:"book_id"`
	NotebookID       string                   `json:"notebook_id"`
	NotebookRevision int64                    `json:"notebook_revision"`
	NotebookHash     string                   `json:"notebook_hash"`
	CreateHash       string                   `json:"create_hash"`
	Title            string                   `json:"title"`
	Language         string                   `json:"language"`
	Stage            string                   `json:"stage"`
	Paused           bool                     `json:"paused"`
	Cancelled        bool                     `json:"cancelled"`
	Attempt          string                   `json:"attempt,omitempty"`
	LeaseUntil       int64                    `json:"lease_until,omitempty"`
	Error            string                   `json:"error,omitempty"`
	Queries          []ResearchExecutionQuery `json:"queries"`
}
type ResearchExecution struct {
	BackgroundQueued bool                      `json:"background_queued"`
	ResourceID       string                    `json:"resource_id"`
	Revision         int64                     `json:"revision"`
	Document         ResearchExecutionDocument `json:"document"`
}
type ResearchHitSelection struct {
	QueryIndex int   `json:"query_index"`
	PageID     int64 `json:"page_id"`
}
type ResearchExecutionSelection struct {
	Revision int64                  `json:"revision"`
	Items    []ResearchHitSelection `json:"items"`
}

func validateResearchExecution(d ResearchExecutionDocument) error {
	if d.AutoCollectLimit < 0 || d.AutoCollectLimit > 2 {
		return domain.ErrValidation
	}
	if d.Schema != researchExecutionSchema || !research.ValidSourceLanguage(d.Provider, d.Language) || !slices.Contains([]string{"search", "selection", "collect", "complete"}, d.Stage) || len(d.Queries) < 1 || len(d.Queries) > 30 {
		return domain.ErrValidation
	}
	referenceURLs := map[int64]string{}
	for _, q := range d.Queries {
		if !validName(q.Query, research.QueryLimit(d.Provider)) || len(q.Hits) > 5 {
			return domain.ErrValidation
		}
		seen := map[int64]bool{}
		for _, h := range q.Hits {
			if !research.ValidHit(d.Provider, d.Language, h.Hit) || seen[h.PageID] || !validName(h.Title, 500) || len(h.Snippet) > 16000 {
				return domain.ErrValidation
			}
			seen[h.PageID] = true
			if d.Provider == research.OfficialSource {
				if previous := referenceURLs[h.PageID]; previous != "" && previous != h.URL {
					return domain.ErrValidation
				}
				referenceURLs[h.PageID] = h.URL
			}
		}
	}
	return nil
}
func (s *NovelResearchExecutions) readTx(ctx context.Context, tx *sql.Tx, a domain.Actor, book NovelBook, notebook, id string) (ResearchExecution, ResourceDraft, error) {
	var out ResearchExecution
	if _, _, _, err := s.Research.readTx(ctx, tx, a, book, notebook); err != nil {
		return out, ResourceDraft{}, err
	}
	r, err := resourceTx(ctx, tx, book.ProjectID, id)
	if err != nil {
		return out, ResourceDraft{}, err
	}
	if r.Kind != "note" || r.BranchID != book.BranchID {
		return out, ResourceDraft{}, domain.ErrForbidden
	}
	rd, err := resourceDraftTx(ctx, tx, a.ID, book.ProjectID, id)
	if err != nil {
		return out, rd, err
	}
	if json.Unmarshal([]byte(rd.Text), &out.Document) != nil || out.Document.ActorID != a.ID || out.Document.BookID != book.ID || out.Document.NotebookID != notebook || validateResearchExecution(out.Document) != nil {
		return out, rd, domain.ErrForbidden
	}
	out.ResourceID, out.Revision = id, rd.Revision
	if out.Document.Background {
		var count int
		leased := out.Document.Attempt != "" && out.Document.LeaseUntil > time.Now().Unix()
		err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM outbox WHERE project_id=? AND resource_id=? AND kind='research.step' AND JSON_UNQUOTE(JSON_EXTRACT(payload,'$.hash'))=? AND CAST(JSON_UNQUOTE(JSON_EXTRACT(payload,'$.revision')) AS UNSIGNED)=? AND (processed_at IS NULL OR ?)", book.ProjectID, id, capabilityDigest(out.Document), out.Revision, leased).Scan(&count)
		if err != nil {
			return out, rd, err
		}
		out.BackgroundQueued = count > 0
	}
	return out, rd, nil
}
func (s *NovelResearchExecutions) saveTx(ctx context.Context, tx *sql.Tx, a domain.Actor, book NovelBook, run ResearchExecution, rd ResourceDraft) (ResearchExecution, error) {
	if err := validateResearchExecution(run.Document); err != nil {
		return run, err
	}
	raw, err := json.Marshal(run.Document)
	if err != nil {
		return run, err
	}
	if len(raw) > 220*1024 {
		return run, fmt.Errorf("%w：检索记录过大，请拆分研究问题", domain.ErrValidation)
	}
	rd.Text = string(raw)
	rd.Policy = &domain.KnowledgePolicy{Space: "research", Visibility: "author"}
	rd, err = s.Research.Generations.IP.saveResourceDraftTx(ctx, tx, a, book.ProjectID, run.ResourceID, rd)
	run.Revision = rd.Revision
	return run, err
}
func (s *NovelResearchExecutions) Create(ctx context.Context, actor, bookID, notebook string, in ResearchExecutionCreate) (ResearchExecution, error) {
	var empty ResearchExecution
	if !validName(in.OperationID, 120) || !research.ValidSourceLanguage(in.Provider, in.Language) {
		return empty, domain.ErrValidation
	}
	tx, a, book, err := s.Research.begin(ctx, actor, bookID)
	if err != nil {
		return empty, err
	}
	defer tx.Rollback()
	id := novel.CharacterDesignID(bookID, actor, "research-execution:"+in.OperationID)
	hash := capabilityDigest([]any{notebook, in})
	prior, _, err := s.readTx(ctx, tx, a, book, notebook, id)
	if err == nil {
		if prior.Document.CreateHash != hash {
			return empty, domain.ErrConflict
		}
		return prior, nil
	}
	if err != domain.ErrForbidden {
		return empty, err
	}
	d, _, _, err := s.Research.readTx(ctx, tx, a, book, notebook)
	if err != nil {
		return empty, err
	}
	if d.Revision != in.Revision || d.Hash != in.Hash {
		return empty, domain.ErrConflict
	}
	run := ResearchExecution{ResourceID: id, Document: ResearchExecutionDocument{Provider: in.Provider, Schema: researchExecutionSchema, ActorID: a.ID, BookID: book.ID, NotebookID: notebook, NotebookRevision: d.Revision, NotebookHash: d.Hash, CreateHash: hash, Title: d.Document.Title, Language: in.Language, Stage: "search", Queries: []ResearchExecutionQuery{}}}
	for _, query := range d.Document.SearchPlan {
		run.Document.Queries = append(run.Document.Queries, ResearchExecutionQuery{ResearchQuery: query, Hits: []ResearchExecutionHit{}})
	}
	run.Document.AutoCollectLimit = in.AutoCollectLimit
	if err = validateResearchExecution(run.Document); err != nil {
		return empty, fmt.Errorf("%w：请保存1至30条检索计划，每条检索词不超过240字", err)
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO resources(id,project_id,branch_id,name,kind) VALUES(?,?,?,?,?)", id, book.ProjectID, book.BranchID, "研究检索 · "+d.Document.Title, "note"); err != nil {
		return empty, err
	}
	if err = audit(ctx, tx, a, "resource.create", id); err != nil {
		return empty, err
	}
	if err = captureMetadataSourceTx(ctx, tx, book.ProjectID, "resources", id); err != nil {
		return empty, err
	}
	run, err = s.saveTx(ctx, tx, a, book, run, ResourceDraft{})
	if err != nil {
		return empty, err
	}
	return run, tx.Commit()
}
func (s *NovelResearchExecutions) Read(ctx context.Context, actor, bookID, notebook, id string) (ResearchExecution, error) {
	tx, a, book, err := s.Research.begin(ctx, actor, bookID)
	if err != nil {
		return ResearchExecution{}, err
	}
	defer tx.Rollback()
	run, _, err := s.readTx(ctx, tx, a, book, notebook, id)
	return run, err
}
func (s *NovelResearchExecutions) List(ctx context.Context, actor, bookID, notebook string) ([]ResearchExecution, error) {
	tx, a, book, err := s.Research.begin(ctx, actor, bookID)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, _, _, err = s.Research.readTx(ctx, tx, a, book, notebook); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT r.id FROM resources r JOIN resource_drafts d ON d.resource_id=r.id AND d.actor_id=? WHERE r.project_id=? AND r.branch_id=? AND JSON_UNQUOTE(JSON_EXTRACT(IF(JSON_VALID(d.body),d.body,'{}'),'$.schema'))=? AND JSON_UNQUOTE(JSON_EXTRACT(IF(JSON_VALID(d.body),d.body,'{}'),'$.notebook_id'))=? ORDER BY d.updated_at DESC,r.id DESC LIMIT 100`, a.ID, book.ProjectID, book.BranchID, researchExecutionSchema, notebook)
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
	items := []ResearchExecution{}
	for _, id := range ids {
		r, _, e := s.readTx(ctx, tx, a, book, notebook, id)
		if e != nil {
			return nil, e
		}
		items = append(items, r)
	}
	return items, nil
}
func (s *NovelResearchExecutions) Control(ctx context.Context, actor, bookID, notebook, id, action string) (ResearchExecution, error) {
	tx, a, book, err := s.Research.begin(ctx, actor, bookID)
	if err != nil {
		return ResearchExecution{}, err
	}
	defer tx.Rollback()
	run, rd, err := s.readTx(ctx, tx, a, book, notebook, id)
	if err != nil {
		return run, err
	}
	switch action {
	case "pause":
		run.Document.Paused = true
	case "cancel":
		run.Document.Cancelled = true
	case "resume":
		if run.Document.Cancelled || (run.Document.Attempt != "" && run.Document.LeaseUntil > time.Now().Unix()) {
			return run, domain.ErrConflict
		}
		run.Document.Paused = false
		run.Document.Attempt, run.Document.LeaseUntil = "", 0
	default:
		return run, domain.ErrValidation
	}
	run, err = s.saveTx(ctx, tx, a, book, run, rd)
	if err != nil {
		return run, err
	}
	if action == "resume" && run.Document.Background {
		if err = s.queueResearchStepTx(ctx, tx, book, &run); err != nil {
			return run, err
		}
	}
	return run, tx.Commit()
}
func (s *NovelResearchExecutions) Select(ctx context.Context, actor, bookID, notebook, id string, in ResearchExecutionSelection) (ResearchExecution, error) {
	tx, a, book, err := s.Research.begin(ctx, actor, bookID)
	if err != nil {
		return ResearchExecution{}, err
	}
	defer tx.Rollback()
	run, rd, err := s.readTx(ctx, tx, a, book, notebook, id)
	if err != nil {
		return run, err
	}
	if run.Revision != in.Revision || run.Document.Cancelled || run.Document.Stage == "search" || (run.Document.Attempt != "" && run.Document.LeaseUntil > time.Now().Unix()) {
		return run, domain.ErrConflict
	}
	if len(in.Items) > 150 {
		return run, domain.ErrValidation
	}
	for qi := range run.Document.Queries {
		for hi := range run.Document.Queries[qi].Hits {
			run.Document.Queries[qi].Hits[hi].Selected = false
		}
	}
	for _, pick := range in.Items {
		if pick.QueryIndex < 0 || pick.QueryIndex >= len(run.Document.Queries) {
			return run, domain.ErrValidation
		}
		found := false
		for hi := range run.Document.Queries[pick.QueryIndex].Hits {
			h := &run.Document.Queries[pick.QueryIndex].Hits[hi]
			if h.PageID == pick.PageID {
				h.Selected, found = true, true
			}
		}
		if !found {
			return run, domain.ErrValidation
		}
	}
	run.Document.Stage = researchExecutionStage(run.Document)
	run.Document.Error = ""
	run, err = s.saveTx(ctx, tx, a, book, run, rd)
	if err != nil {
		return run, err
	}
	if run.Document.Background {
		if err = s.queueResearchStepTx(ctx, tx, book, &run); err != nil {
			return run, err
		}
	}
	return run, tx.Commit()
}

func researchExecutionStage(d ResearchExecutionDocument) string {
	anySelected := false
	for _, q := range d.Queries {
		if !q.Done {
			return "search"
		}
		for _, h := range q.Hits {
			if h.Selected {
				anySelected = true
				if h.ImportID == "" {
					return "collect"
				}
			}
		}
	}
	if anySelected {
		return "complete"
	}
	return "selection"
}
