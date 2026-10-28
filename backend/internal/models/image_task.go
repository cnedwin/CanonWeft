package models

import (
	"encoding/json"
	"novelstudio/internal/domain"
)

type NovelImageTarget struct {
	BookID       string   `json:"book_id"`
	Kind         string   `json:"kind"`
	Name         string   `json:"name"`
	SourceIDs    []string `json:"source_ids"`
	CharacterIDs []string `json:"character_ids"`
}
type NovelImageBasis struct {
	SettingsRevision int64                  `json:"settings_revision"`
	Setup            json.RawMessage        `json:"setup"`
	Characters       []json.RawMessage      `json:"characters"`
	Required         []domain.RequiredInput `json:"required"`
}

// Immutable references only. Credentials and binary media never enter task JSON.
type ImageReference struct {
	ID   string `json:"id"`
	Hash string `json:"hash"`
}
type CharacterImageTask struct {
	Novel              *NovelImageTarget `json:"novel,omitempty"`
	Region             *ImageRegion      `json:"region,omitempty"`
	VisualGenerationID string            `json:"visual_generation_id,omitempty"`
	CharacterID        string            `json:"character_id"`
	Look               string            `json:"look"`
	CharacterHash      string            `json:"character_hash"`
	Basis              string            `json:"basis"`
	Size               string            `json:"size"`
	Quality            string            `json:"quality"`
	References         []ImageReference  `json:"references"`
	Reservation        int64             `json:"reservation_micros"`
}
type CharacterImageOutput struct {
	ImportID      string `json:"import_id"`
	Hash          string `json:"hash"`
	MIME          string `json:"mime"`
	Width         int    `json:"width"`
	Height        int    `json:"height"`
	RevisedPrompt string `json:"revised_prompt,omitempty"`
}
