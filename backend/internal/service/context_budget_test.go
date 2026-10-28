package service

import (
	"errors"
	"fmt"
	"novelstudio/internal/domain"
	"novelstudio/internal/memory"
	"novelstudio/internal/retrieval"
	"strings"
	"testing"
)

func TestMemoryBudgetReservesMandatoryOuterContext(t *testing.T) {
	p := ContextPackage{Facts: []domain.Assertion{{ID: "canon", Value: strings.Repeat("不能裁剪的正史。", 80)}}, Evidence: []retrieval.Chunk{{ID: "rag", Text: strings.Repeat("可选检索材料。", 500)}}}
	budget, err := availableMemoryBudget(p, 16000)
	if err != nil {
		t.Fatal(err)
	}
	items := make([]memory.Item, 24)
	for i := range items {
		items[i] = memory.Item{ID: fmt.Sprintf("memory-%d", i), Revision: "r1", Project: "p", Branch: "b", State: "published", Kind: "summary", Visibility: "public", Text: strings.Repeat("旧章节摘要与旁支细节。", 100), Sources: []memory.Source{{Resource: "source", Revision: "r1", Hash: strings.Repeat("a", 64), Start: 0, End: 10}}}
	}
	items[0].Text = "核心主题"
	items[0].Pinned = true
	r := memory.Request{Scope: memory.Scope{Project: "p", Branch: "b", Canon: "canon", Actor: "author", PermissionEpoch: "1", Purpose: "author", Timeline: "main"}, Budget: budget, SourceEpoch: "1", CompilerVersion: memory.Version}
	pack, err := memory.Compile(r, items)
	if err != nil {
		t.Fatal(err)
	}
	p.Memory = &pack
	if err = fitContext(&p, 16000); err != nil {
		t.Fatal("optional history blocked a valid task", err)
	}
	if p.EstimatedTokens > 16000 || len(p.Facts) != 1 || len(pack.Items) == 0 || pack.Items[0].Item.ID != items[0].ID || pack.OmittedCount == 0 {
		t.Fatal("core or budget lost")
	}
}

func TestContextBudgetNeverTruncatesMandatoryFacts(t *testing.T) {
	p := ContextPackage{Facts: []domain.Assertion{{ID: "locked", Value: strings.Repeat("秘密", 800)}}, Evidence: []retrieval.Chunk{{ID: "optional", Text: strings.Repeat("资料", 1000)}}}
	original := p.Facts[0].Value
	if err := fitContext(&p, 20000); err != nil {
		t.Fatal(err)
	}
	if p.Facts[0].Value != original || len(p.Evidence) != 0 || len(p.Omissions) == 0 || p.EstimatedTokens > 20000 {
		t.Fatalf("bad budget behavior: %+v", p)
	}
	if err := fitContext(&p, 4096); !errors.Is(err, domain.ErrContextBudget) {
		t.Fatalf("mandatory overflow must block, got %v", err)
	}
	if p.Facts[0].Value != original {
		t.Fatal("mandatory facts clipped")
	}
}

func TestPrivateSemanticBudgetPreservesCompleteSelectedInputs(t *testing.T) {
	text := strings.Repeat("必须保留的完整草稿。", 50)
	p := ContextPackage{Unconfirmed: []WorkingInput{{Ref: WorkingRef{Kind: "resource_draft", ID: "private", Version: "1"}, Parts: []WorkingPart{{Name: "draft", Format: "text", Text: text}}}}, PrivateHits: []PrivateHit{{Chunk: retrieval.Chunk{ID: "optional-private", Text: strings.Repeat("可省略的语义线索", 1000)}}}}
	if err := fitContext(&p, 12000); err != nil {
		t.Fatal(err)
	}
	if len(p.PrivateHits) != 0 || len(p.Unconfirmed) != 1 || p.Unconfirmed[0].Parts[0].Text != text || len(p.Omissions) == 0 {
		t.Fatal("semantic budget changed mandatory input")
	}
	if err := fitContext(&p, 4096); !errors.Is(err, domain.ErrContextBudget) {
		t.Fatal("mandatory overflow did not block", err)
	}
	if p.Unconfirmed[0].Parts[0].Text != text {
		t.Fatal("overflow truncated selected input")
	}
}
