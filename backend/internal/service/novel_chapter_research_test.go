package service

import (
	"novelstudio/internal/domain"
	"novelstudio/internal/novel"
	"testing"
)

func TestChapterReferencesRespectKnowledgeBoundaries(t *testing.T) {
	d := novel.ChapterDocument{StoryTime: 4, Perspective: "hero", BodyID: "body"}
	for _, tt := range []struct {
		name     string
		resource domain.Resource
		allowed  bool
	}{
		{"adopted working author reference", domain.Resource{ID: "note", Head: "published", Kind: "note", Policy: &domain.KnowledgePolicy{Space: "working", Visibility: "author"}}, true},
		{"withdrawn working author reference", domain.Resource{ID: "note", Head: "published", Kind: "note", Policy: &domain.KnowledgePolicy{Space: "working", Visibility: "author", Withdrawn: true, WithdrawalReason: "来源撤回"}}, false},
		{"unpublished", domain.Resource{Kind: "note", Policy: &domain.KnowledgePolicy{Space: "working", Visibility: "author"}}, false},
		{"raw research", domain.Resource{Head: "published", Kind: "note", Policy: &domain.KnowledgePolicy{Space: "research", Visibility: "author"}}, false},
		{"own body", domain.Resource{ID: "body", Head: "published", Kind: "note", Policy: &domain.KnowledgePolicy{Space: "working", Visibility: "author"}}, false},
		{"other chapter", domain.Resource{Head: "published", Kind: "novel_chapter_control", Policy: &domain.KnowledgePolicy{Space: "working", Visibility: "author"}}, false},
		{"future fact", domain.Resource{Head: "published", Kind: "note", Policy: &domain.KnowledgePolicy{Space: "evidence", Visibility: "public", RevealAt: 5}}, false},
		{"unknown to character", domain.Resource{Head: "published", Kind: "note", Policy: &domain.KnowledgePolicy{Space: "evidence", Visibility: "restricted", KnownBy: []string{"other"}}}, false},
		{"known to character", domain.Resource{Head: "published", Kind: "note", Policy: &domain.KnowledgePolicy{Space: "evidence", Visibility: "restricted", KnownBy: []string{"hero"}}}, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := chapterReferenceAllowed(tt.resource, d); got != tt.allowed {
				t.Fatalf("allowed=%v, want=%v", got, tt.allowed)
			}
		})
	}
}
