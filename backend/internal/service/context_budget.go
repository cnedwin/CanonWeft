package service

import (
	"encoding/json"
	"fmt"
	"novelstudio/internal/domain"
	"novelstudio/internal/retrieval"
)

// Reserve the full non-trimmable envelope before selecting optional memories.
// Include worst-case RAG omission diagnostics, since those also occupy tokens.
func availableMemoryBudget(p ContextPackage, budget int) (int, error) {
	p.Memory = nil
	p.TokenBudget, p.EstimatedTokens = budget, budget
	p.Omissions = append([]string(nil), p.Omissions...)
	for _, hit := range p.PrivateHits {
		p.Omissions = append(p.Omissions, "容量限制未纳入未确认片段："+hit.ID)
	}
	for _, chunks := range [][]retrieval.Chunk{p.Research, p.Working, p.Methods, p.Evidence} {
		for _, chunk := range chunks {
			p.Omissions = append(p.Omissions, "容量限制未纳入原文片段："+chunk.ID)
		}
	}
	p.PrivateHits = nil
	p.Research = nil
	p.Working = nil
	p.Methods = nil
	p.Evidence = []retrieval.Chunk{}
	raw, err := json.Marshal(p)
	if err != nil {
		return 0, err
	}
	remaining := budget - (len(raw)*2 + 1024) - 128 // memory property and encoding margin
	if remaining < 1024 {
		return 0, domain.ErrContextBudget
	}
	return remaining, nil
}

// This is a deliberately conservative UTF-8 byte envelope, not a provider tokenizer.
// The model gateway still checks its own full request and output reservation.
func fitContext(p *ContextPackage, budget int) error {
	p.TokenBudget = budget
	for {
		p.EstimatedTokens = budget // reserves at least as many decimal digits as the final count
		raw, err := json.Marshal(p)
		if err != nil {
			return err
		}
		estimate := len(raw)*2 + 1024
		if estimate <= budget {
			p.EstimatedTokens = estimate
			return nil
		}
		var trimmed *retrieval.Chunk
		if len(p.PrivateHits) > 0 {
			last := p.PrivateHits[len(p.PrivateHits)-1]
			p.PrivateHits = p.PrivateHits[:len(p.PrivateHits)-1]
			p.Omissions = append(p.Omissions, "容量限制未纳入未确认片段："+last.ID)
			continue
		}
		for _, list := range []*[]retrieval.Chunk{&p.Research, &p.Working, &p.Methods, &p.Evidence} {
			if len(*list) > 0 {
				last := (*list)[len(*list)-1]
				trimmed = &last
				*list = (*list)[:len(*list)-1]
				break
			}
		}
		if trimmed == nil {
			return fmt.Errorf("必要设定与隐藏约束超过上下文容量，请缩小任务范围或提高容量；未截断必要事实: %w", domain.ErrContextBudget)
		}
		p.Omissions = append(p.Omissions, "容量限制未纳入原文片段："+trimmed.ID)
	}
}

func contextPurposeForRole(role string) string {
	switch role {
	case "researcher":
		return "research"
	case "writer", "style_editor":
		return "writer"
	case "critic", "bookkeeper", "continuity_editor":
		return "critic"
	default:
		return "author"
	}
}
