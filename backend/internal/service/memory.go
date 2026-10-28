package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"novelstudio/internal/domain"
	"novelstudio/internal/memory"
	"novelstudio/internal/retention"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

type Memory struct {
	IP    *IP
	Cache memory.Cache
}

type MemorySessionDocument struct {
	Title    string   `json:"title"`
	Goal     string   `json:"goal"`
	Notes    string   `json:"notes"`
	FocusIDs []string `json:"focus_ids"`
}
type MemorySession struct {
	Retention *RuntimeRetention     `json:"retention,omitempty"`
	ID        string                `json:"id"`
	Revision  int64                 `json:"revision"`
	Document  MemorySessionDocument `json:"document"`
}
type MemorySessionInput struct {
	OperationID      string                `json:"operation_id"`
	ID               string                `json:"id"`
	ExpectedRevision int64                 `json:"expected_revision"`
	Document         MemorySessionDocument `json:"document"`
}
type MemoryList struct {
	Expired  []ExpiredMemory     `json:"expired,omitempty"`
	Refresh  []MemoryRefreshTask `json:"refresh_tasks,omitempty"`
	Items    []memory.Item       `json:"items"`
	Sessions []MemorySession     `json:"sessions"`
}
type ExpiredMemory struct {
	ID        string            `json:"id"`
	Kind      string            `json:"kind"`
	Retention *RuntimeRetention `json:"retention"`
}
type MemoryInput struct {
	Item             memory.Item `json:"item"`
	ExpectedRevision string      `json:"expected_revision"`
	OperationID      string      `json:"operation_id"`
}
type MemoryAction struct {
	ExpectedRevision string `json:"expected_revision"`
	OperationID      string `json:"operation_id"`
}
type MemoryCompileInput = domain.MemoryCompileInput

func memoryItemTx(ctx context.Context, tx *sql.Tx, actor, project, branch, id string) (memory.Item, error) {
	var item memory.Item
	var raw []byte
	err := tx.QueryRowContext(ctx, "SELECT document FROM memory_items WHERE id=? AND project_id=? AND branch_id=? AND (actor_id IS NULL OR actor_id=?) FOR UPDATE", id, project, branch, actor).Scan(&raw)
	if err == sql.ErrNoRows {
		return item, domain.ErrForbidden
	}
	if err == nil {
		if err := rejectExpiredRuntime(raw, "memory_items.document"); err != nil {
			return item, err
		}
		err = json.Unmarshal(raw, &item)
	}
	return item, err
}
func memoryItemsTx(ctx context.Context, tx *sql.Tx, actor, project, branch string, history ...*[]ExpiredMemory) ([]memory.Item, error) {
	rows, err := tx.QueryContext(ctx, "SELECT id,kind,document FROM memory_items WHERE project_id=? AND branch_id=? AND (actor_id IS NULL OR actor_id=?) ORDER BY id LIMIT 8193", project, branch, actor)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []memory.Item{}
	erased := []ExpiredMemory{}
	for rows.Next() {
		var item memory.Item
		var raw []byte
		var id, kind string
		if err = rows.Scan(&id, &kind, &raw); err != nil {
			return nil, err
		}
		if expiryErr := rejectExpiredRuntime(raw, "memory_items.document"); expiryErr != nil {
			if expiryErr != domain.ErrMaterialExpired {
				return nil, expiryErr
			}
			erased = append(erased, ExpiredMemory{ID: id, Kind: kind})
			continue
		}
		if err = json.Unmarshal(raw, &item); err != nil {
			return nil, domain.ErrNotReady
		}
		items = append(items, item)
	}
	if len(items)+len(erased) > 8192 {
		return nil, fmt.Errorf("请缩小分支记忆范围: %w", domain.ErrContextBudget)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for i := range erased {
		proof, err := runtimeTransientTx(ctx, tx, project, "memory_items", "document", erased[i].ID)
		if err != nil {
			return nil, err
		}
		erased[i].Retention = proof
	}
	if len(history) > 0 {
		*history[0] = erased
	}
	return items, nil
}
func memorySessionTx(ctx context.Context, tx *sql.Tx, actor, project, branch, id string) (MemorySession, error) {
	var session MemorySession
	var raw []byte
	err := tx.QueryRowContext(ctx, "SELECT id,revision,document FROM memory_sessions WHERE id=? AND project_id=? AND branch_id=? AND actor_id=? FOR UPDATE", id, project, branch, actor).Scan(&session.ID, &session.Revision, &raw)
	if err == sql.ErrNoRows {
		return session, domain.ErrForbidden
	}
	if err == nil {
		if err := rejectExpiredRuntime(raw, "memory_sessions.document"); err != nil {
			return session, err
		}
		err = json.Unmarshal(raw, &session.Document)
	}
	return session, err
}

func (s *Memory) List(ctx context.Context, a domain.Actor, project, branch string) (MemoryList, error) {
	result := MemoryList{Items: []memory.Item{}, Sessions: []MemorySession{}}
	tx, a, _, err := s.IP.begin(ctx, a, project, false)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	if _, err = branchTx(ctx, tx, project, branch); err != nil {
		return result, err
	}
	result.Items, err = memoryItemsTx(ctx, tx, a.ID, project, branch, &result.Expired)
	if err != nil {
		return result, err
	}
	sourceMemo := map[string]memorySourceInfo{}
	for i := range result.Items {
		if result.Items[i].State == "revoked" {
			continue
		}
		if _, err = s.validateSourcesMemo(ctx, tx, project, branch, result.Items[i], sourceMemo); err != nil {
			if !errors.Is(err, domain.ErrConflict) && !errors.Is(err, domain.ErrForbidden) && !errors.Is(err, domain.ErrNotReady) && !errors.Is(err, domain.ErrValidation) {
				return result, err
			}
			result.Items[i].State = "stale"
		}
	}
	result.Refresh, err = s.refreshTasksTx(ctx, tx, project, branch, result.Items, sourceMemo)
	if err != nil {
		return result, err
	}
	rows, err := tx.QueryContext(ctx, "SELECT id,revision,document FROM memory_sessions WHERE project_id=? AND branch_id=? AND actor_id=? ORDER BY updated_at DESC,id LIMIT 256", project, branch, a.ID)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		var session MemorySession
		var raw []byte
		if err = rows.Scan(&session.ID, &session.Revision, &raw); err != nil {
			return result, err
		}
		if expiredErr := rejectExpiredRuntime(raw, "memory_sessions.document"); expiredErr != nil {
			if expiredErr != domain.ErrMaterialExpired {
				return result, expiredErr
			}
			session.Retention = &RuntimeRetention{State: "EXPIRED"}
			result.Sessions = append(result.Sessions, session)
			continue
		}
		if err = json.Unmarshal(raw, &session.Document); err != nil {
			return result, domain.ErrNotReady
		}
		result.Sessions = append(result.Sessions, session)
	}
	if err := rows.Err(); err != nil {
		return result, err
	}
	if err := rows.Close(); err != nil {
		return result, err
	}
	for i := range result.Sessions {
		if result.Sessions[i].Retention == nil {
			continue
		}
		proof, err := runtimeTransientTx(ctx, tx, project, "memory_sessions", "document", result.Sessions[i].ID)
		if err != nil {
			return result, err
		}
		result.Sessions[i].Retention = proof
	}
	return result, nil
}

func (s *Memory) SaveSession(ctx context.Context, a domain.Actor, project, branch string, in MemorySessionInput) (MemorySession, error) {
	if in.Document.FocusIDs == nil {
		in.Document.FocusIDs = []string{}
	}
	result := MemorySession{ID: in.ID, Revision: in.ExpectedRevision + 1, Document: in.Document}
	if !validName(in.OperationID, 120) || !validName(in.Document.Title, 160) || utf8.RuneCountInString(in.Document.Goal) > 8000 || utf8.RuneCountInString(in.Document.Notes) > 32768 || !utf8.ValidString(in.Document.Title+in.Document.Goal+in.Document.Notes) || len(in.Document.FocusIDs) > 128 || in.ExpectedRevision < 0 {
		return result, domain.ErrValidation
	}
	seen := map[string]bool{}
	for _, id := range in.Document.FocusIDs {
		if !lowerHex32(id) || seen[id] {
			return result, domain.ErrValidation
		}
		seen[id] = true
	}
	if (in.ID == "") != (in.ExpectedRevision == 0) {
		return result, domain.ErrValidation
	}
	if in.ID != "" && !lowerHex32(in.ID) {
		return result, domain.ErrValidation
	}
	tx, a, _, err := s.IP.begin(ctx, a, project, true)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	if _, err = branchTx(ctx, tx, project, branch); err != nil {
		return result, err
	}
	requestHash := valueHash(struct {
		Kind, Project, Branch string
		Input                 MemorySessionInput
	}{"memory.session", project, branch, in})
	if hash, id, receiptErr := operationReceiptTx(ctx, tx, a, in.OperationID); receiptErr == nil {
		if hash != requestHash {
			return result, domain.ErrConflict
		}
		// Return the current checkpoint; an old successful retry must never
		// overwrite edits made after its original commit.
		return memorySessionTx(ctx, tx, a.ID, project, branch, id)
	} else if receiptErr != sql.ErrNoRows {
		return result, receiptErr
	}
	for _, id := range in.Document.FocusIDs {
		if _, err = memoryItemTx(ctx, tx, a.ID, project, branch, id); err != nil {
			return result, err
		}
	}
	if in.ID == "" {
		result.ID = NewID()
	} else {
		previous, loadErr := memorySessionTx(ctx, tx, a.ID, project, branch, in.ID)
		if loadErr != nil {
			return result, loadErr
		}
		if previous.Revision != in.ExpectedRevision {
			return result, domain.ErrConflict
		}
	}
	source := retention.SourceKey{Table: "memory_sessions", Primary: map[string]string{"id": result.ID}, IfPresent: in.ID == ""}
	if err := s.IP.captureSourceValuesTx(ctx, tx, project, source); err != nil {
		return result, err
	}
	raw, _ := json.Marshal(in.Document)
	if in.ID == "" {
		_, err = tx.ExecContext(ctx, "INSERT INTO memory_sessions(id,project_id,branch_id,actor_id,revision,document) VALUES(?,?,?,?,?,?)", result.ID, project, branch, a.ID, result.Revision, raw)
	} else {
		_, err = tx.ExecContext(ctx, "UPDATE memory_sessions SET revision=?,document=?,updated_at=CURRENT_TIMESTAMP(6) WHERE id=?", result.Revision, raw, result.ID)
	}
	if err != nil {
		return result, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO operations(actor_id,operation_id,kind,request_hash,result_id) VALUES(?,?,'memory.session',?,?)", a.ID, in.OperationID, requestHash, result.ID); err != nil {
		return result, err
	}
	source.IfPresent = false
	if err := s.IP.captureSourceValuesTx(ctx, tx, project, source); err != nil {
		return result, err
	}
	if err = audit(ctx, tx, a, "memory.session", result.ID); err != nil {
		return result, err
	}
	return result, tx.Commit()
}

func (s *Memory) ReadSession(ctx context.Context, a domain.Actor, project, branch, id string) (MemorySession, error) {
	if !lowerHex32(id) {
		return MemorySession{}, domain.ErrValidation
	}
	tx, a, _, err := s.IP.begin(ctx, a, project, false)
	if err != nil {
		return MemorySession{}, err
	}
	defer tx.Rollback()
	if _, err = branchTx(ctx, tx, project, branch); err != nil {
		return MemorySession{}, err
	}
	return memorySessionTx(ctx, tx, a.ID, project, branch, id)
}

// validateSources prevents a derived summary from weakening the confidentiality
// or reveal time of any underlying source. Historical revisions remain stored,
// but only current source pins may enter a new task.
func (s *Memory) validateSources(ctx context.Context, tx *sql.Tx, project, branch string, item memory.Item) ([]domain.KnowledgePolicy, error) {
	return s.validateSourcesMemo(ctx, tx, project, branch, item, map[string]memorySourceInfo{})
}

type memorySourceInfo struct {
	ChapterProjection map[string]memory.Item
	Resource          domain.Resource
	Runes             int
	Item              *memory.Item
	Checked           bool
	Err               error
}

func (s *Memory) validateSourcesMemo(ctx context.Context, tx *sql.Tx, project, branch string, item memory.Item, memo map[string]memorySourceInfo) ([]domain.KnowledgePolicy, error) {
	if policies, handled, err := s.chapterProjectionPoliciesTx(ctx, tx, project, branch, item, memo); handled || err != nil {
		return policies, err
	}
	policies, err := s.summarySourcesTx(ctx, tx, project, branch, item, memo)
	if err != nil {
		return nil, err
	}
	for _, source := range item.Sources {
		info, loaded := memo[source.Resource]
		if !loaded {
			if len(memo) >= 8192 {
				return nil, domain.ErrContextBudget
			}
			resource, err := resourceTx(ctx, tx, project, source.Resource)
			if err != nil {
				return nil, err
			}
			data, err := s.IP.Blobs.ReadObject(ctx, project, resource.Hash)
			if err != nil {
				return nil, domain.ErrNotReady
			}
			info = memorySourceInfo{Resource: resource, Runes: utf8.RuneCount(data)}
			memo[source.Resource] = info
		}
		resource := info.Resource
		if resource.BranchID != branch || resource.Head != source.Revision || resource.Hash != source.Hash {
			return nil, domain.ErrConflict
		}
		if source.Start < 0 || source.End <= source.Start || source.End > info.Runes {
			return nil, domain.ErrValidation
		}
		policy := domain.DefaultKnowledgePolicy()
		if resource.Policy != nil {
			policy = *resource.Policy
		}
		if item.Visibility == "public" && policy.Visibility != "public" {
			return nil, domain.ErrValidation
		}
		if item.Visibility == "restricted" {
			if policy.Visibility == "author" {
				return nil, domain.ErrValidation
			}
			if policy.Visibility == "restricted" {
				for _, id := range item.KnownBy {
					if !slices.Contains(policy.KnownBy, id) {
						return nil, domain.ErrValidation
					}
				}
			}
		}
		if item.RevealAt < policy.RevealAt {
			return nil, domain.ErrValidation
		}
		reference := item.Visibility == "author" && ((item.Kind == "professional" && policy.Space == "research") || (item.Kind == "method" && policy.Space == "methods"))
		if policy.Space != "evidence" && !item.Control && !reference {
			return nil, domain.ErrValidation
		}
		policies = append(policies, policy)
	}
	return policies, nil
}

func (s *Memory) Save(ctx context.Context, a domain.Actor, project, branch string, in MemoryInput) (memory.Item, error) {
	item := in.Item
	if !validName(in.OperationID, 120) || (item.ID == "") != (in.ExpectedRevision == "") {
		return item, domain.ErrValidation
	}
	tx, a, _, err := s.IP.begin(ctx, a, project, true)
	if err != nil {
		return item, err
	}
	defer tx.Rollback()
	if _, err = branchTx(ctx, tx, project, branch); err != nil {
		return item, err
	}
	requestHash := valueHash(struct {
		Project, Branch string
		Input           MemoryInput
	}{project, branch, in})
	if hash, id, receiptErr := operationReceiptTx(ctx, tx, a, in.OperationID); receiptErr == nil {
		if hash != requestHash {
			return item, domain.ErrConflict
		}
		return memoryItemTx(ctx, tx, a.ID, project, branch, id)
	} else if receiptErr != sql.ErrNoRows {
		return item, receiptErr
	}
	parent := ""
	if item.ID != "" {
		previous, loadErr := memoryItemTx(ctx, tx, a.ID, project, branch, item.ID)
		if loadErr != nil {
			return item, loadErr
		}
		if previous.Revision != in.ExpectedRevision {
			return item, domain.ErrConflict
		}
		parent = previous.Revision
	} else {
		item.ID = NewID()
	}
	if _, err = memorySessionTx(ctx, tx, a.ID, project, branch, item.Session); err != nil {
		return item, err
	}
	item.Project, item.Branch, item.Actor, item.State, item.Revision = project, branch, a.ID, "candidate", NewID()
	if err = memory.ValidateItem(item); err != nil {
		return item, domain.ErrValidation
	}
	if _, err = s.validateSources(ctx, tx, project, branch, item); err != nil {
		return item, err
	}
	for _, id := range item.Dependencies {
		if id == item.ID {
			return item, domain.ErrValidation
		}
		if _, err = memoryItemTx(ctx, tx, a.ID, project, branch, id); err != nil {
			return item, err
		}
	}
	if err = s.persist(ctx, tx, a, item, parent, in.OperationID, requestHash); err != nil {
		return item, err
	}
	return item, tx.Commit()
}

func (s *Memory) Transition(ctx context.Context, a domain.Actor, project, branch, id, action string, in MemoryAction) (memory.Item, error) {
	var item memory.Item
	if !slices.Contains([]string{"adopt", "revoke"}, action) || !validName(in.OperationID, 120) || in.ExpectedRevision == "" {
		return item, domain.ErrValidation
	}
	tx, a, _, err := s.IP.begin(ctx, a, project, true)
	if err != nil {
		return item, err
	}
	defer tx.Rollback()
	if _, err = branchTx(ctx, tx, project, branch); err != nil {
		return item, err
	}
	requestHash := valueHash(struct {
		Project, Branch, ID, Action string
		Input                       MemoryAction
	}{project, branch, id, action, in})
	if hash, result, receiptErr := operationReceiptTx(ctx, tx, a, in.OperationID); receiptErr == nil {
		if hash != requestHash {
			return item, domain.ErrConflict
		}
		return memoryItemTx(ctx, tx, a.ID, project, branch, result)
	} else if receiptErr != sql.ErrNoRows {
		return item, receiptErr
	}
	item, err = memoryItemTx(ctx, tx, a.ID, project, branch, id)
	if err != nil {
		return item, err
	}
	if item.Revision != in.ExpectedRevision {
		return item, domain.ErrConflict
	}
	parent := item.Revision
	if action == "adopt" {
		if item.State != "candidate" {
			return item, domain.ErrConflict
		}
		if _, err = s.validateSources(ctx, tx, project, branch, item); err != nil {
			return item, err
		}
		for _, dep := range item.Dependencies {
			dependency, loadErr := memoryItemTx(ctx, tx, a.ID, project, branch, dep)
			if loadErr != nil {
				return item, loadErr
			}
			if dependency.State != "published" && dependency.State != "adopted_plan" {
				return item, domain.ErrConflict
			}
		}
		item.State = "published"
		if item.Control || slices.Contains([]string{"theme", "mainline", "highlight", "promise"}, item.Kind) {
			item.State = "adopted_plan"
			item.Control = true
			item.Visibility = "author"
			item.KnownBy = nil
		}
		item.Actor, item.Session = "", ""
	} else {
		item.State = "revoked"
	}
	item.Revision = NewID()
	if err = s.persist(ctx, tx, a, item, parent, in.OperationID, requestHash); err != nil {
		return item, err
	}
	return item, tx.Commit()
}

func (s *Memory) persist(ctx context.Context, tx *sql.Tx, a domain.Actor, item memory.Item, parent, op, requestHash string) error {
	source := retention.SourceKey{Table: "memory_items", Primary: map[string]string{"id": item.ID}, IfPresent: parent == ""}
	if err := s.IP.captureSourceValuesTx(ctx, tx, item.Project, source); err != nil {
		return err
	}
	raw, err := json.Marshal(item)
	if err != nil {
		return err
	}
	if parent == "" {
		_, err = tx.ExecContext(ctx, "INSERT INTO memory_items(id,project_id,branch_id,head_revision,kind,state,actor_id,session_id,document) VALUES(?,?,?,?,?,?,NULLIF(?,''),NULLIF(?,''),?)", item.ID, item.Project, item.Branch, item.Revision, item.Kind, item.State, item.Actor, item.Session, raw)
	} else {
		_, err = tx.ExecContext(ctx, "UPDATE memory_items SET head_revision=?,kind=?,state=?,actor_id=NULLIF(?,''),session_id=NULLIF(?,''),document=?,updated_at=CURRENT_TIMESTAMP(6) WHERE id=?", item.Revision, item.Kind, item.State, item.Actor, item.Session, raw, item.ID)
	}
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO memory_revisions(id,project_id,item_id,parent_id,document,actor_id) VALUES(?,?,?,NULLIF(?,''),?,?)", item.Revision, item.Project, item.ID, parent, raw, a.ID)
	if err != nil {
		return err
	}
	// The new current document and its immutable revision must both exist
	// before ownership validation. Proof failure rolls back the state change.
	source.IfPresent = false
	if err = s.IP.captureSourceValuesTx(ctx, tx, item.Project, source); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO operations(actor_id,operation_id,kind,request_hash,result_id) VALUES(?,?,'memory.update',?,?)", a.ID, op, requestHash, item.ID)
	if err != nil {
		return err
	}
	return audit(ctx, tx, a, "memory.update", item.ID)
}

func (s *Memory) Compile(ctx context.Context, a domain.Actor, project, branch string, in MemoryCompileInput) (memory.Pack, error) {
	tx, a, p, err := s.IP.begin(ctx, a, project, false)
	if err != nil {
		return memory.Pack{}, err
	}
	defer tx.Rollback()
	return s.compileTx(ctx, tx, a, p, project, branch, in)
}

func (s *Memory) compileTx(ctx context.Context, tx *sql.Tx, a domain.Actor, p domain.Project, project, branch string, in MemoryCompileInput) (memory.Pack, error) {
	if in.Budget == 0 {
		in.Budget = 65536
	}
	if in.Timeline == "" {
		in.Timeline = "main"
	}
	if in.Purpose == "" {
		in.Purpose = "author"
	}
	if len(in.Query) > 8000 {
		return memory.Pack{}, domain.ErrValidation
	}
	scope := memory.Scope{Project: project, Branch: branch, Actor: a.ID, Purpose: in.Purpose, Perspective: in.Perspective, StoryTime: in.StoryTime, NarrativeTime: in.NarrativeTime, Session: in.Session, Timeline: in.Timeline, Save: in.Save}
	b, err := branchTx(ctx, tx, project, branch)
	if err != nil {
		return memory.Pack{}, err
	}
	if in.Purpose == "writer" || in.Purpose == "npc" {
		snapshot, loadErr := snapshotTx(ctx, tx, project, b.Head)
		if loadErr != nil {
			return memory.Pack{}, loadErr
		}
		// Narrator writing has no character identity. An explicit viewpoint or
		// NPC still requires a real active character, matching structured context.
		if (in.Purpose == "npc" || in.Perspective != "") && !slices.ContainsFunc(snapshot.Entities, func(e domain.Entity) bool {
			return e.ID == in.Perspective && e.Kind == "character" && e.Status != "disabled"
		}) {
			return memory.Pack{}, domain.ErrValidation
		}
		// A private replacement draft must not silently remove a shared core.
		if loadErr = checkPendingMemoryCoreTx(ctx, tx, scope); loadErr != nil {
			return memory.Pack{}, loadErr
		}
	}
	items, err := memoryItemsTx(ctx, tx, a.ID, project, branch)
	if err != nil {
		return memory.Pack{}, err
	}
	refreshEpoch := ""
	if in.Refresh != nil {
		if in.Purpose != "critic" || !lowerHex32(in.Refresh.ID) || !lowerHex32(in.Refresh.Revision) {
			return memory.Pack{}, domain.ErrValidation
		}
		target, loadErr := memoryItemTx(ctx, tx, a.ID, project, branch, in.Refresh.ID)
		if loadErr != nil {
			return memory.Pack{}, loadErr
		}
		if target.Revision != in.Refresh.Revision || target.Summary == nil || target.State == "revoked" {
			return memory.Pack{}, domain.ErrConflict
		}
		refreshEpoch = valueHash(items)
		items = refreshContextItems(items, target.ID)
	}
	var session MemorySession
	if in.Session != "" {
		session, err = memorySessionTx(ctx, tx, a.ID, project, branch, in.Session)
		if err != nil {
			return memory.Pack{}, err
		}
	}
	required := append([]string(nil), in.RequiredIDs...)
	for _, id := range session.Document.FocusIDs {
		if !slices.Contains(required, id) {
			required = append(required, id)
		}
	}
	// Filter before source IO, ranking and cache fingerprints. An unrelated
	// timeline or another private session cannot invalidate this task's memory.
	applicable := items[:0]
	for _, item := range items {
		if memory.Applies(item, scope) {
			applicable = append(applicable, item)
		}
	}
	items = applicable
	// Revalidate all source heads before touching the cache. A stale required
	// plan blocks the task; optional stale material simply cannot be selected.
	sourceMemo := map[string]memorySourceInfo{}
	for index := range items {
		item := &items[index]
		if item.State == "revoked" {
			continue
		}
		if _, sourceErr := s.validateSourcesMemo(ctx, tx, project, branch, *item, sourceMemo); sourceErr != nil {
			if !errors.Is(sourceErr, domain.ErrConflict) && !errors.Is(sourceErr, domain.ErrForbidden) && !errors.Is(sourceErr, domain.ErrNotReady) && !errors.Is(sourceErr, domain.ErrValidation) {
				return memory.Pack{}, sourceErr
			}
			if item.Pinned && memory.Applies(*item, scope) && (item.State == "published" || item.State == "adopted_plan") && !slices.Contains(required, item.ID) {
				required = append(required, item.ID)
			}
			item.State = "stale"
		}
		item.Score = memoryRelevance(in.Query, item.Text)
	}
	canon := b.Head
	if canon == "" {
		canon = "empty"
	}
	req := memory.Request{Refresh: in.Refresh, Scope: memory.Scope{Project: project, Branch: branch, Canon: canon, Actor: a.ID, PermissionEpoch: valueHash(struct {
		Actor   domain.Actor
		Project domain.Project
	}{a, p}), Purpose: in.Purpose, Perspective: in.Perspective, StoryTime: in.StoryTime, NarrativeTime: in.NarrativeTime, Session: in.Session, Timeline: in.Timeline, Save: in.Save}, RequiredIDs: required, Budget: in.Budget, SourceEpoch: valueHash(struct {
		Items        []memory.Item
		Session      MemorySession
		RefreshEpoch string `json:"refresh_epoch,omitempty"`
	}{items, session, refreshEpoch}), CompilerVersion: memory.Version, RetrievalVersion: "memory-lexical-v1", QueryHash: digest(in.Query)}
	key := memory.CacheKey(req)
	if s.Cache != nil {
		if raw, ok, cacheErr := s.Cache.Get(ctx, key); cacheErr == nil && ok {
			var pack memory.Pack
			if json.Unmarshal(raw, &pack) == nil {
				return pack, nil
			}
		}
	}
	pack, err := memory.Compile(req, items)
	if err != nil {
		if errors.Is(err, memory.ErrBudget) {
			return pack, fmt.Errorf("%s: %w", err, domain.ErrContextBudget)
		}
		if errors.Is(err, memory.ErrInvalid) {
			return pack, domain.ErrValidation
		}
		return pack, fmt.Errorf("%s: %w", err, domain.ErrConflict)
	}
	if s.Cache != nil {
		raw, _ := json.Marshal(pack)
		_ = s.Cache.Put(ctx, key, raw, 2*time.Minute)
	}
	return pack, nil
}

func checkPendingMemoryCoreTx(ctx context.Context, tx *sql.Tx, scope memory.Scope) error {
	rows, err := tx.QueryContext(ctx, `SELECT r.document FROM memory_items i JOIN memory_revisions r ON r.item_id=i.id WHERE i.project_id=? AND i.branch_id=? AND i.state='candidate' AND JSON_UNQUOTE(JSON_EXTRACT(r.document,'$.state')) IN ('published','adopted_plan') ORDER BY r.created_at DESC,r.id DESC`, scope.Project, scope.Branch)
	if err != nil {
		return err
	}
	defer rows.Close()
	seen := map[string]bool{}
	for rows.Next() {
		var raw []byte
		var prior memory.Item
		if err = rows.Scan(&raw); err != nil {
			return err
		}
		if json.Unmarshal(raw, &prior) != nil {
			return domain.ErrNotReady
		}
		if seen[prior.ID] {
			continue
		}
		seen[prior.ID] = true
		if prior.Pinned && memory.Applies(prior, scope) {
			return fmt.Errorf("核心记忆正在修改，请确认或撤回候选后继续: %w", domain.ErrConflict)
		}
	}
	return rows.Err()
}

func memoryRelevance(query, text string) float64 {
	query = strings.ToLower(strings.TrimSpace(query))
	text = strings.ToLower(text)
	if query == "" {
		return 0
	}
	if strings.Contains(text, query) {
		return 1
	}
	runes := []rune(query)
	matched, total := 0, 0
	for i := 0; i+1 < len(runes); i++ {
		total++
		if strings.Contains(text, string(runes[i:i+2])) {
			matched++
		}
	}
	if total == 0 {
		return 0
	}
	return float64(matched) / float64(total)
}
