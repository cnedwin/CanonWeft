package service

import (
	"context"
	"database/sql"
	"fmt"
	"novelstudio/internal/domain"
	"slices"
	"strconv"
)

func requireMemoryContextTx(ctx context.Context, tx *sql.Tx, project, branch, role, contextID string) error {
	if contextID != "" || !slices.Contains([]string{"writer", "bookkeeper", "critic", "continuity_editor", "style_editor"}, role) {
		return nil
	}
	count, err := adoptedMemoryCountTx(ctx, tx, project, branch)
	if err != nil {
		return err
	}
	if count > 0 {
		return fmt.Errorf("请先获取包含最新记忆的写作上下文: %w", domain.ErrConflict)
	}
	return nil
}

func adoptedMemoryCountTx(ctx context.Context, tx *sql.Tx, project, branch string) (int, error) {
	var count int
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM memory_items i WHERE i.project_id=? AND i.branch_id=? AND (i.state IN ('published','adopted_plan') OR (i.state='candidate' AND EXISTS (SELECT 1 FROM memory_revisions r WHERE r.item_id=i.id AND JSON_UNQUOTE(JSON_EXTRACT(r.document,'$.state')) IN ('published','adopted_plan'))))`, project, branch).Scan(&count)
	return count, err
}

// Memory is part of the same durable context manifest as RAG evidence. Existing
// source-only projects keep their previous envelope; adding memory invalidates
// old contexts so a generation cannot silently skip newly required constraints.
func (s *Knowledge) attachMemory(ctx context.Context, tx *sql.Tx, a domain.Actor, project, branch string, result *ContextPackage) error {
	var count int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM memory_items WHERE project_id=? AND branch_id=?", project, branch).Scan(&count); err != nil {
		return err
	}
	if count == 0 && result.Input.Memory == nil {
		return nil
	}
	p, err := projectTx(ctx, tx, a, project)
	if err != nil {
		return err
	}
	in := MemoryCompileInput{Purpose: result.Input.Purpose, Perspective: result.Input.Perspective, StoryTime: result.Input.StoryTime, NarrativeTime: result.Input.StoryTime, Query: result.Input.Query, Budget: result.Input.TokenBudget}
	if result.Input.Memory != nil {
		in = *result.Input.Memory
		// Caller cannot smuggle author memory into a writer/NPC context.
		in.Purpose, in.Perspective, in.StoryTime = result.Input.Purpose, result.Input.Perspective, result.Input.StoryTime
		if in.Budget == 0 || in.Budget > result.Input.TokenBudget {
			in.Budget = result.Input.TokenBudget
		}
	}
	available, err := availableMemoryBudget(*result, result.Input.TokenBudget)
	if err != nil {
		return err
	}
	if in.Budget > available {
		in.Budget = available
	}
	pack, err := (&Memory{IP: s.IP, Cache: s.MemoryCache}).compileTx(ctx, tx, a, p, project, branch, in)
	if err != nil {
		return err
	}
	result.Memory = &pack
	if pack.Request.Scope.Session != "" {
		session, err := memorySessionTx(ctx, tx, a.ID, project, branch, pack.Request.Scope.Session)
		if err != nil {
			return err
		}
		result.SessionSource = &domain.ContextSessionSource{ID: session.ID, Revision: strconv.FormatInt(session.Revision, 10), Hash: valueHash(session.Document)}
	}
	return nil
}

func (s *Knowledge) validateMemoryContext(ctx context.Context, tx *sql.Tx, actor, project, branch string, result ContextPackage) error {
	a, err := s.IP.Workspace.actorTx(ctx, tx, actor)
	if err != nil {
		return err
	}
	if result.Memory == nil {
		count, countErr := adoptedMemoryCountTx(ctx, tx, project, branch)
		if countErr != nil {
			return countErr
		}
		if count > 0 {
			return domain.ErrConflict
		}
		return nil
	}
	p, err := projectTx(ctx, tx, a, project)
	if err != nil {
		return err
	}
	r := result.Memory.Request
	query := result.Input.Query
	if result.Input.Memory != nil {
		query = result.Input.Memory.Query
	}
	// Use the frozen allocation, not the size of the already-trimmed outer pack.
	in := MemoryCompileInput{Refresh: r.Refresh, Purpose: r.Scope.Purpose, Perspective: r.Scope.Perspective, StoryTime: r.Scope.StoryTime, NarrativeTime: r.Scope.NarrativeTime, Session: r.Scope.Session, Timeline: r.Scope.Timeline, Save: r.Scope.Save, RequiredIDs: r.RequiredIDs, Query: query, Budget: r.Budget}
	current, err := (&Memory{IP: s.IP, Cache: s.MemoryCache}).compileTx(ctx, tx, a, p, project, branch, in)
	if err != nil {
		return err
	}
	if current.Hash != result.Memory.Hash {
		return domain.ErrConflict
	}
	return nil
}
