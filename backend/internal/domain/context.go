package domain

import (
	"novelstudio/internal/memory"
	"novelstudio/internal/retrieval"
)

type SearchInput struct {
	Structured            *StructuredScope    `json:"structured_scope,omitempty"`
	Memory                *MemoryCompileInput `json:"memory,omitempty"`
	IncludePrivateWorking bool                `json:"include_private_working,omitempty"`
	WorkingRefs           []WorkingRef        `json:"working_refs,omitempty"`
	RequiredResourceIDs   []string            `json:"required_resource_ids,omitempty"`
	IncludeWorking        bool                `json:"include_working,omitempty"`
	IncludeMethods        bool                `json:"include_methods,omitempty"`
	TokenBudget           int                 `json:"token_budget,omitempty"`
	Query                 string              `json:"query"`
	Purpose               string              `json:"purpose"`
	Perspective           string              `json:"perspective"`
	StoryTime             int64               `json:"story_time"`
	Limit                 int                 `json:"limit"`
}

type ContextPackage struct {
	SessionSource    *ContextSessionSource `json:"session_source,omitempty"`
	Structured       *StructuredSelection  `json:"structured_selection,omitempty"`
	Memory           *memory.Pack          `json:"memory,omitempty"`
	PrivateIndexID   string                `json:"private_index_id,omitempty"`
	PrivateHits      []PrivateHit          `json:"private_hits,omitempty"`
	Unconfirmed      []WorkingInput        `json:"unconfirmed_inputs,omitempty"`
	Required         []RequiredInput       `json:"required_inputs,omitempty"`
	Working          []retrieval.Chunk     `json:"working_materials,omitempty"`
	Methods          []retrieval.Chunk     `json:"methods,omitempty"`
	Research         []retrieval.Chunk     `json:"research_candidates,omitempty"`
	TokenBudget      int                   `json:"token_budget,omitempty"`
	EstimatedTokens  int                   `json:"estimated_tokens,omitempty"`
	ExpandedQuery    string                `json:"expanded_query,omitempty"`
	MatchedEntities  []string              `json:"matched_entities,omitempty"`
	ID               string                `json:"id"`
	IndexID          string                `json:"index_id"`
	Input            SearchInput           `json:"input"`
	IPHead           string                `json:"ip_head"`
	RetrievalVersion string                `json:"retrieval_version"`
	Facts            []Assertion           `json:"facts"`
	Hidden           []Assertion           `json:"hidden_constraints"`
	Entities         []Entity              `json:"entities"`
	Rules            []WorldRule           `json:"rules"`
	Evidence         []retrieval.Chunk     `json:"evidence"`
	Omissions        []string              `json:"omissions"`
}

// Selection provenance only; private session notes are not copied into prompts.
type ContextSessionSource struct {
	ID       string `json:"id"`
	Revision string `json:"revision"`
	Hash     string `json:"hash"`
}

type WorkingPart struct {
	Name   string `json:"name"`
	Format string `json:"format"`
	Text   string `json:"text"`
}

type WorkingInput struct {
	Ref      WorkingRef    `json:"ref"`
	State    string        `json:"state"`
	BaseHead string        `json:"base_head"`
	Parts    []WorkingPart `json:"parts"`
}

type RequiredInput struct {
	Dependency Dependency `json:"dependency"`
	Name       string     `json:"name"`
	Space      string     `json:"space"`
	Text       string     `json:"text"`
	Hidden     bool       `json:"hidden,omitempty"`
}

type PrivateHit struct {
	retrieval.Chunk
	Ref   WorkingRef `json:"ref"`
	Part  int        `json:"part"`
	State string     `json:"state"`
}

type MemoryCompileInput struct {
	Refresh       *memory.RefreshScope `json:"refresh,omitempty"`
	Purpose       string               `json:"purpose"`
	Perspective   string               `json:"perspective"`
	StoryTime     int64                `json:"story_time"`
	NarrativeTime int64                `json:"narrative_time"`
	Session       string               `json:"session"`
	Timeline      string               `json:"timeline"`
	Save          string               `json:"save,omitempty"`
	RequiredIDs   []string             `json:"required_ids,omitempty"`
	Query         string               `json:"query"`
	Budget        int                  `json:"budget"`
}
