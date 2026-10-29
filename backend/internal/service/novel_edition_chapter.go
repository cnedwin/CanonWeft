package service

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"novelstudio/internal/domain"
	"novelstudio/internal/novel"
)

type NovelEditionChapter struct {
	BookID      string             `json:"book_id"`
	EditionID   string             `json:"edition_id"`
	EditionHash string             `json:"edition_hash"`
	Chapter     NovelExportChapter `json:"chapter"`
	Text        string             `json:"text"`
	TextHash    string             `json:"text_hash"`
}

// EditionChapter reads one immutable author chapter without opening a working
// chapter, creating drafts, or restoring any historical execution authority.
func (s *NovelExport) EditionChapter(ctx context.Context, actor, bookID, editionID, chapterID string) (NovelEditionChapter, error) {
	var out NovelEditionChapter
	if !lowerHex32(editionID) || !lowerHex32(chapterID) {
		return out, domain.ErrValidation
	}
	tx, a, book, err := (&NovelPlanning{IP: s.IP}).begin(ctx, actor, bookID, true)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	item, source, err := editionSourceTx(ctx, tx, a, book, editionID)
	if err != nil {
		return out, err
	}
	if !item.Published {
		return out, fmt.Errorf("%w：该成书版本尚未保存完成", domain.ErrConflict)
	}
	preview, manuscript, err := s.readEditionArchive(ctx, book, source)
	if err != nil {
		return out, err
	}
	chapter, text, err := editionChapterContent(preview, manuscript, chapterID)
	if err != nil {
		return out, err
	}
	return NovelEditionChapter{BookID: book.ID, EditionID: source.ID, EditionHash: source.Hash, Chapter: chapter, Text: text, TextHash: digest(text)}, nil
}

// Archives store stable IDs in the complete preview, while book.json stores
// only included prose in that order. Validate the complete mapping before
// returning a selection: an early matching ID cannot hide later corruption.
func editionChapterContent(preview NovelExportPreview, manuscript novel.ExportBook, chapterID string) (NovelExportChapter, string, error) {
	var selected NovelExportChapter
	selectedText := ""
	seen, resources := map[string]bool{}, map[string]bool{}
	volumes := map[string]string{}
	included, missing, characters := 0, 0, 0
	if preview.Title != manuscript.Title {
		return selected, "", domain.ErrNotReady
	}
	for _, chapter := range preview.Chapters {
		if !lowerHex32(chapter.ID) || !lowerHex32(chapter.VolumeID) || seen[chapter.ID] || chapter.Characters < 0 {
			return selected, "", domain.ErrNotReady
		}
		seen[chapter.ID] = true
		if chapter.ResourceID != "" {
			if !lowerHex32(chapter.ResourceID) || resources[chapter.ResourceID] {
				return selected, "", domain.ErrNotReady
			}
			resources[chapter.ResourceID] = true
		}
		if previous, exists := volumes[chapter.VolumeID]; exists && previous != chapter.Volume {
			return selected, "", domain.ErrNotReady
		}
		volumes[chapter.VolumeID] = chapter.Volume
		if !chapter.Included {
			if chapter.Characters != 0 {
				return selected, "", domain.ErrNotReady
			}
			missing++
			continue
		}
		if included >= len(manuscript.Chapters) || chapter.ResourceID == "" || !lowerHex32(chapter.Head) || chapter.Revision < 1 || len(chapter.Hash) != 64 || strings.Trim(chapter.Hash, "0123456789abcdef") != "" {
			return selected, "", domain.ErrNotReady
		}
		prose := manuscript.Chapters[included]
		if prose.Title != chapter.Title || prose.Volume != chapter.Volume || utf8.RuneCountInString(prose.Text) != chapter.Characters || strings.TrimSpace(prose.Text) == "" {
			return selected, "", domain.ErrNotReady
		}
		included++
		characters += chapter.Characters
		if chapter.ID == chapterID {
			selected, selectedText = chapter, prose.Text
		}
	}
	if included != len(manuscript.Chapters) || included != preview.Included || missing != preview.Missing || characters != preview.Characters {
		return selected, "", domain.ErrNotReady
	}
	if selected.ID == "" {
		return selected, "", fmt.Errorf("%w：所选成书稿本未收录此章正文，请选择其他稿本", domain.ErrConflict)
	}
	return selected, selectedText, nil
}
