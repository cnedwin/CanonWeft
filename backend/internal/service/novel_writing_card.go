package service

import (
	"context"
	"encoding/json"
	"fmt"

	"novelstudio/internal/domain"
	"novelstudio/internal/novel"
)

type NovelWritingCard = novel.WritingCard
type NovelWritingPlanCue = novel.WritingPlanCue
type NovelWritingCardPreview = novel.WritingCardPreview

func buildNovelWritingCard(setup novel.Setup, draft novel.ChapterDocument, inputs []domain.RequiredInput) (NovelWritingCard, error) {
	return novel.BuildWritingCard(setup, draft, inputs)
}
func novelWritingCardQuery(card NovelWritingCard, notes string) string {
	return novel.WritingCardQuery(card, notes)
}

func attachNovelWritingCard(raw []byte, card NovelWritingCard) ([]byte, error) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return nil, domain.ErrValidation
	}
	value, err := json.Marshal(card)
	if err != nil {
		return nil, err
	}
	fields["writing_card"] = value
	return json.Marshal(fields)
}

// The same four immutable inputs serve both the human card and the writer.
func chapterWritingInputs(check *planningCheck, chapter string) ([]domain.RequiredInput, error) {
	inputs := []domain.RequiredInput{}
	add := func(resource domain.Resource) error {
		raw, err := check.ip.Blobs.ReadObject(check.ctx, check.project, resource.Hash)
		if err != nil || digest(string(raw)) != resource.Hash {
			return domain.ErrNotReady
		}
		inputs = append(inputs, domain.RequiredInput{Dependency: domain.Dependency{Kind: "production", Reason: "本章已确认的作者规划", Source: planPin(resource)}, Name: resource.Name, Space: "working", Text: string(raw), Hidden: true})
		return nil
	}
	for _, kind := range []string{"world", "book", "detail"} {
		scope := ""
		if kind == "detail" {
			scope = chapter
		}
		resource, plan, err := check.current(kind, scope)
		if err != nil {
			return nil, err
		}
		if err = add(resource); err != nil {
			return nil, err
		}
		if kind == "book" {
			found := false
			for _, volume := range plan.Volumes {
				resource, plan, err := check.current("volume", volume.ID)
				if err != nil {
					return nil, err
				}
				for _, item := range plan.Chapters {
					if item.ID == chapter {
						if found {
							return nil, domain.ErrValidation
						}
						found = true
						if err = add(resource); err != nil {
							return nil, err
						}
					}
				}
			}
			if !found {
				return nil, domain.ErrNotReady
			}
		}
	}
	return inputs, nil
}

func (s *NovelChapters) WritingCard(ctx context.Context, actor, bookID, id string) (NovelWritingCardPreview, error) {
	var out NovelWritingCardPreview
	tx, a, book, err := (&NovelPlanning{IP: s.IP}).begin(ctx, actor, bookID, true)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	draft, err := s.draftTx(ctx, tx, a, book, id)
	if err != nil {
		return out, err
	}
	if !draft.Ready {
		return out, fmt.Errorf("%w：%s", domain.ErrConflict, draft.Reason)
	}
	check, err := newPlanningCheck(ctx, tx, s.IP, book.ProjectID, book.BranchID)
	if err != nil {
		return out, err
	}
	inputs, err := chapterWritingInputs(check, draft.Document.ChapterID)
	if err != nil {
		return out, err
	}
	card, err := buildNovelWritingCard(book.Setup, draft.Document, inputs)
	return NovelWritingCardPreview{Revision: draft.Revision, Hash: draft.Hash, Card: card}, err
}
