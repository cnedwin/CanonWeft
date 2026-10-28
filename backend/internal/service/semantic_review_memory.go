package service

import (
	"context"
	"encoding/json"

	"novelstudio/internal/domain"
	"novelstudio/internal/novel"
)

// A review of a fixed candidate still needs current continuity evidence. Use
// the same durable context and invalidation gate as writing, never an exemption
// from requireMemoryContextTx. Chapter time comes from the frozen target.
func (s *SemanticReviews) reviewMemoryContext(ctx context.Context, review SemanticReview, raw []byte) (string, error) {
	actor := domain.Actor{ID: review.ActorID}
	tx, actor, _, err := s.Generations.IP.begin(ctx, actor, review.ProjectID, false)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	count, err := adoptedMemoryCountTx(ctx, tx, review.ProjectID, review.BranchID)
	if err != nil || count == 0 {
		return "", err
	}
	if s.Knowledge == nil || s.Knowledge.Embedder == nil {
		return "", domain.ErrNotReady
	}
	var document domain.SemanticReviewDocument
	if json.Unmarshal(raw, &document) != nil {
		return "", domain.ErrValidation
	}
	// Non-chapter reviews inspect current published knowledge across the branch.
	// Chapter reviews are restricted to their actual narrative position.
	storyTime := int64(9007199254740991)
	query := "复核当前候选的人物、事件、因果、世界规则与已发布记忆的一致性"
	for _, artifact := range document.Artifacts {
		resource, err := resourceTx(ctx, tx, review.ProjectID, artifact.Target.ResourceID)
		if err != nil {
			return "", err
		}
		if resource.Kind != "novel_chapter_control" {
			continue
		}
		chapter, err := novel.DecodeChapter([]byte(artifact.Text))
		if err != nil {
			return "", err
		}
		storyTime = chapter.StoryTime
		text := []rune(chapter.Title + " " + chapter.Text)
		if len(text) > 800 {
			text = text[:800]
		}
		query += " " + string(text)
	}
	tx.Rollback()
	pack, err := s.Knowledge.Search(ctx, actor, review.ProjectID, review.BranchID, SearchInput{Purpose: "critic", StoryTime: storyTime, Query: query, Limit: 12, TokenBudget: 65536})
	if err != nil {
		return "", err
	}
	if pack.IPHead != review.Frozen.ExpectedIPHead {
		return "", domain.ErrConflict
	}
	return pack.ID, nil
}
