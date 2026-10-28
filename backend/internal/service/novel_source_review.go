package service

import (
	"context"
	"encoding/json"
	"strings"

	"novelstudio/internal/domain"
)

type NovelSourceReviewItem struct {
	ResourceID string   `json:"resource_id"`
	Name       string   `json:"name"`
	Kind       string   `json:"kind"`
	Status     string   `json:"status"`
	ChapterID  string   `json:"chapter_id,omitempty"`
	Reasons    []string `json:"reasons"`
}
type NovelSourceReviewPage struct {
	Items      []NovelSourceReviewItem `json:"items"`
	NextCursor string                  `json:"next_cursor,omitempty"`
	Scanned    int                     `json:"scanned"`
}

// Bounded, read-only inspection under the same book/actor scope as its editors.
// Published material stays in the existing change-plan workflow; this also
// reveals stale private chapters and notebooks that have no published graph node.
func (s *NovelResearch) SourceReview(ctx context.Context, actor, bookID, after string) (NovelSourceReviewPage, error) {
	out := NovelSourceReviewPage{Items: []NovelSourceReviewItem{}}
	if after != "" && !lowerHex32(after) {
		return out, domain.ErrValidation
	}
	tx, a, book, err := s.begin(ctx, actor, bookID)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, "SELECT id FROM resources WHERE project_id=? AND branch_id=? AND id>? AND (kind IN ('note','source','novel_chapter_control') OR kind LIKE 'novel_plan_%') ORDER BY id LIMIT 101", book.ProjectID, book.BranchID, after)
	if err != nil {
		return out, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return out, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	if len(ids) > 100 {
		out.NextCursor = ids[99]
		ids = ids[:100]
	}
	out.Scanned = len(ids)
	ip := s.Generations.IP
	for _, id := range ids {
		r, err := resourceTx(ctx, tx, book.ProjectID, id)
		if err != nil {
			return out, err
		}
		item := NovelSourceReviewItem{ResourceID: id, Name: r.Name, Kind: "material", Status: "review", Reasons: []string{}}
		switch {
		case r.Kind == "novel_chapter_control":
			d, err := (&NovelChapters{IP: ip}).draftTx(ctx, tx, a, book, id)
			if err != nil {
				return out, err
			}
			item.Kind, item.ChapterID, item.Name = "chapter", d.Document.ChapterID, d.Document.Title
			if !d.Ready {
				item.Reasons = append(item.Reasons, d.Reason)
			}
		case isNovelPlan(r.Kind):
			d, _, err := (&NovelPlanning{IP: ip}).draftTx(ctx, tx, a, book, id)
			if err != nil {
				return out, err
			}
			item.Kind, item.Name = "planning", d.Document.Title
			if !d.Ready {
				item.Reasons = append(item.Reasons, d.Reason)
			}
		default:
			if r.Policy != nil && r.Policy.Withdrawn {
				item.Status = "withdrawn"
				item.Reasons = append(item.Reasons, r.Policy.WithdrawalReason)
			}
			raw, err := resourceDraftTx(ctx, tx, a.ID, book.ProjectID, id)
			if err != nil {
				return out, err
			}
			var w researchWorkspace
			if json.Unmarshal([]byte(raw.Text), &w) == nil && w.Schema == "novel-research-v1" && w.ActorID == a.ID && w.BookID == bookID {
				item.Kind, item.Name = "research", w.Document.Title
				for _, pin := range w.Document.Sources {
					current, err := resourceTx(ctx, tx, book.ProjectID, pin.ID)
					if err != nil {
						return out, err
					}
					if current.Policy != nil && current.Policy.Withdrawn {
						item.Reasons = append(item.Reasons, pin.Name+"：已撤回使用，请刷新来源")
					} else if current.Head != pin.Revision || current.Hash != pin.Hash {
						item.Reasons = append(item.Reasons, pin.Name+"：已换版，请刷新来源")
					}
				}
				for _, card := range w.Document.Cards {
					if len(card.StaleSources) > 0 {
						item.Reasons = append(item.Reasons, "事实卡仍有旧版引文待核对")
						break
					}
				}
			} else if r.Dependencies != nil {
				for _, dep := range r.Dependencies.Inputs {
					if err := validateDependencySource(ctx, tx, book.ProjectID, dep); err != nil {
						item.Reasons = append(item.Reasons, "上游参考已变化或撤回，请从资料变更影响清单复核")
						break
					}
				}
			}
		}
		if strings.TrimSpace(item.Name) == "" {
			item.Name = r.Name
		}
		if len(item.Reasons) > 0 {
			out.Items = append(out.Items, item)
		}
	}
	return out, nil
}
