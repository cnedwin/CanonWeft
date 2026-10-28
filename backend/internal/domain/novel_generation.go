package domain

import "encoding/json"

// NovelChapterSource pins the author's private chapter version. It confers no
// permission to publish or to treat planned scenes as already occurred facts.
type NovelChapterSource struct {
	BookID     string `json:"book_id"`
	ResourceID string `json:"resource_id"`
	Revision   int64  `json:"revision"`
	Hash       string `json:"hash"`
	BaseHead   string `json:"base_head"`
}

type NovelChapterRequest struct {
	Previous   json.RawMessage `json:"previous,omitempty"`
	Draft      json.RawMessage `json:"draft"`
	Plans      []RequiredInput `json:"plans"`
	Notes      string          `json:"notes"`
	Style      string          `json:"style"`
	Language   string          `json:"language"`
	Narration  string          `json:"narration"`
	ChapterMin int             `json:"chapter_min"`
	ChapterMax int             `json:"chapter_max"`
}
