package service

import (
	"context"
	"fmt"
	"time"

	"novelstudio/internal/domain"
	"novelstudio/internal/novel"
	"novelstudio/internal/research"
)

// One bounded source operation per checkpoint (official links also check robots).
// No model calls, paid credentials,
// or network access inside a database transaction. Manual and background callers
// share the same persisted execution and interruption protection.
func (s *NovelResearchExecutions) Step(ctx context.Context, actor, bookID, notebook, id string, revision int64) (ResearchExecution, error) {
	return s.step(ctx, actor, bookID, notebook, id, revision, "")
}
func (s *NovelResearchExecutions) step(ctx context.Context, actor, bookID, notebook, id string, revision int64, eventID string) (ResearchExecution, error) {
	tx, a, book, err := s.Research.begin(ctx, actor, bookID)
	if err != nil {
		return ResearchExecution{}, err
	}
	defer tx.Rollback()
	run, rd, err := s.readTx(ctx, tx, a, book, notebook, id)
	if err != nil {
		return run, err
	}
	if eventID == "" && run.Document.Background {
		return run, domain.ErrConflict
	}
	if eventID != "" {
		if err = s.checkResearchEventTx(ctx, tx, eventID, book, run); err != nil {
			return run, err
		}
		if run.Document.Paused || run.Document.Cancelled {
			return run, errResearchSuperseded
		}
		if run.Document.Attempt != "" && run.Document.LeaseUntil > time.Now().Unix() {
			return run, errResearchBusy
		}
	}
	if run.Revision != revision || run.Document.Paused || run.Document.Cancelled || (run.Document.Attempt != "" && run.Document.LeaseUntil > time.Now().Unix()) {
		return run, domain.ErrConflict
	}
	stage := researchExecutionStage(run.Document)
	if stage == "selection" || stage == "complete" {
		return run, nil
	}
	queryIndex := -1
	var page int64
	for qi, q := range run.Document.Queries {
		if stage == "search" && !q.Done {
			queryIndex = qi
			break
		}
		if stage == "collect" {
			for _, h := range q.Hits {
				if h.Selected && h.ImportID == "" {
					queryIndex, page = qi, h.PageID
					break
				}
			}
			if queryIndex >= 0 {
				break
			}
		}
	}
	if queryIndex < 0 {
		return run, domain.ErrNotReady
	}
	query := run.Document.Queries[queryIndex].Query
	referenceURL := ""
	// The first search that found a page owns its stable import receipt, even
	// when the author selects a later duplicate result first.
	if stage == "collect" {
		found := false
		for _, q := range run.Document.Queries {
			for _, h := range q.Hits {
				if h.PageID == page {
					query, found = q.Query, true
					if run.Document.Provider == research.OfficialSource {
						referenceURL = h.URL
					}
					break
				}
			}
			if found {
				break
			}
		}
	}
	attempt := NewID()
	run.Document.Attempt, run.Document.LeaseUntil, run.Document.Error = attempt, time.Now().Add(time.Minute).Unix(), ""
	run, err = s.saveTx(ctx, tx, a, book, run, rd)
	if err != nil {
		return run, err
	}
	if eventID != "" {
		if err = s.advanceResearchClaimTx(ctx, tx, eventID, run); err != nil {
			return run, err
		}
	}
	if err = tx.Commit(); err != nil {
		return run, err
	}
	var hits []research.Hit
	var imported SourceImport
	var requestErr error
	if stage == "search" {
		hits, requestErr = s.Sources.SearchIn(ctx, a, book.ProjectID, book.BranchID, run.Document.Provider, run.Document.Language, query)
	} else {
		operation := novel.CharacterDesignID(id, run.Document.Language, fmt.Sprint(page))
		imported, requestErr = s.Sources.Collect(ctx, a, book.ProjectID, book.BranchID, ResearchCollectInput{ReferenceURL: referenceURL, Provider: run.Document.Provider, Language: run.Document.Language, PageID: page, Query: query, OperationID: operation})
	}
	tx, a, book, err = s.Research.begin(ctx, actor, bookID)
	if err != nil {
		return run, err
	}
	defer tx.Rollback()
	run, rd, err = s.readTx(ctx, tx, a, book, notebook, id)
	if err != nil {
		return run, err
	}
	if run.Document.Attempt != attempt {
		return run, nil
	}
	if eventID != "" && !run.Document.Paused && !run.Document.Cancelled {
		if err = s.checkResearchEventTx(ctx, tx, eventID, book, run); err != nil {
			return run, err
		}
	}
	run.Document.Attempt, run.Document.LeaseUntil = "", 0
	if !run.Document.Paused && !run.Document.Cancelled {
		if requestErr != nil {
			run.Document.Error = "公开来源暂未返回可用结果，进度已保留，可稍后继续。"
		} else if stage == "search" {
			run.Document.Queries[queryIndex].Done = true
			run.Document.Queries[queryIndex].Hits = []ResearchExecutionHit{}
			for _, hit := range hits {
				run.Document.Queries[queryIndex].Hits = append(run.Document.Queries[queryIndex].Hits, ResearchExecutionHit{Hit: hit})
			}
			if researchExecutionStage(run.Document) == "selection" && run.Document.AutoCollectLimit > 0 {
				for qi := range run.Document.Queries {
					for hi := range run.Document.Queries[qi].Hits {
						run.Document.Queries[qi].Hits[hi].Selected = hi < run.Document.AutoCollectLimit
					}
				}
			}
		} else {
			for qi := range run.Document.Queries {
				for hi := range run.Document.Queries[qi].Hits {
					h := &run.Document.Queries[qi].Hits[hi]
					if h.PageID == page {
						h.ImportID = imported.ID
					}
				}
			}
		}
	}
	run.Document.Stage = researchExecutionStage(run.Document)
	run, err = s.saveTx(ctx, tx, a, book, run, rd)
	if err != nil {
		return run, err
	}
	if eventID != "" {
		if err = s.queueResearchStepTx(ctx, tx, book, &run); err != nil {
			return run, err
		}
	}
	return run, tx.Commit()
}
