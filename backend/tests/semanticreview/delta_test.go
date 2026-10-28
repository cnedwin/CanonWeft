package semanticreview

import (
	"encoding/json"
	"errors"
	"testing"

	"novelstudio/internal/domain"
)

func TestBranchDeltaRequiresEachReasonAndChecksPriorValue(t *testing.T) {
	base := domain.Snapshot{Entities: []domain.Entity{{ID: "hero", Kind: "character", Name: "林", Description: "旧描述"}}}
	next := base
	next.Entities = append([]domain.Entity(nil), base.Entities...)
	next.Entities[0].Description = "新描述"
	next.Entities[0].Status = "disabled"
	reasons := map[string]string{"entity:hero:/description": "更正人物经历", "entity:hero:/status": "退出当前故事"}
	if _, err := domain.BuildBranchDelta(base, next, nil); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("missing reasons accepted: %v", err)
	}
	delta, err := domain.BuildBranchDelta(base, next, reasons)
	if err != nil {
		t.Fatal(err)
	}
	if len(delta.Operations) != 2 || delta.Operations[0].Reason == "" || delta.Operations[1].Kind != "disable" {
		t.Fatalf("wrong operations: %+v", delta.Operations)
	}
	result, err := domain.ApplyBranchDelta(base, delta)
	if err != nil {
		t.Fatal(err)
	}
	if result.Snapshot.Entities[0].Description != "新描述" || result.Snapshot.Entities[0].Status != "disabled" {
		t.Fatal("delta not applied")
	}
	changed := base
	changed.Entities = append([]domain.Entity(nil), base.Entities...)
	changed.Entities[0].Description = "later edit"
	if _, err = domain.ApplyBranchDelta(changed, delta); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("stale delta accepted: %v", err)
	}
	delta.Operations[0].BeforeHash = "forged"
	if _, err = domain.ApplyBranchDelta(base, delta); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("forged prior value accepted: %v", err)
	}
}

func TestBranchDeltaPreservesExactTimeAndRejectsFakeChanges(t *testing.T) {
	base := domain.Snapshot{Entities: []domain.Entity{{ID: "hero", Kind: "character", Name: "林"}}, Assertions: []domain.Assertion{{ID: "fact", Subject: "hero", Predicate: "年龄", Value: "30", Kind: "fact", Source: "manual", Visibility: "public", EffectiveFrom: 9007199254740993}}}
	next := base
	next.Assertions = append([]domain.Assertion(nil), base.Assertions...)
	next.Assertions[0].EffectiveFrom++
	delta, err := domain.BuildBranchDelta(base, next, map[string]string{"assertion:fact:/effective_from": "调整事件发生时间"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := domain.ApplyBranchDelta(base, delta)
	if err != nil {
		t.Fatal(err)
	}
	if result.Snapshot.Assertions[0].EffectiveFrom != 9007199254740994 {
		t.Fatal("time rounded")
	}
	if string(delta.Operations[0].Before) != "9007199254740993" {
		t.Fatalf("prior time rounded: %s", delta.Operations[0].Before)
	}
	if _, err = domain.BuildBranchDelta(base, base, map[string]string{"entity:hero:/name": "fake"}); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("unused reason accepted: %v", err)
	}
	raw, _ := json.Marshal(delta)
	other, _ := domain.BuildBranchDelta(base, next, map[string]string{"assertion:fact:/effective_from": "调整事件发生时间"})
	again, _ := json.Marshal(other)
	if string(raw) != string(again) {
		t.Fatal("unstable delta")
	}
}

func TestBranchDeltaAddsAndRemovesStableObjects(t *testing.T) {
	base := domain.Snapshot{Entities: []domain.Entity{{ID: "old", Kind: "character", Name: "旧人"}}}
	next := domain.Snapshot{Entities: []domain.Entity{{ID: "new", Kind: "character", Name: "新人"}}}
	delta, err := domain.BuildBranchDelta(base, next, map[string]string{"entity:old:": "删除未采用角色", "entity:new:": "新增角色"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := domain.ApplyBranchDelta(base, delta)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Snapshot.Entities) != 1 || result.Snapshot.Entities[0].ID != "new" {
		t.Fatal("object identity not preserved")
	}
	for i := range delta.Operations {
		if delta.Operations[i].Kind == "add" {
			delta.Operations[i].ObjectID = "misleading-identity"
		}
	}
	if _, err = domain.ApplyBranchDelta(base, delta); !errors.Is(err, domain.ErrValidation) {
		t.Fatal("delta identity differs from actual inserted object", err)
	}
}

func TestBranchDeltaPreviewDoesNotInventReasonsOrGrantApply(t *testing.T) {
	base := domain.Snapshot{Entities: []domain.Entity{{ID: "hero", Kind: "character", Name: "林"}}}
	next := domain.Snapshot{Entities: []domain.Entity{{ID: "hero", Kind: "character", Name: "林青"}}}
	preview, err := domain.PreviewBranchDelta(base, next)
	if err != nil || len(preview.Operations) != 1 || preview.Operations[0].Reason != "" {
		t.Fatal("preview fabricated reason", preview, err)
	}
	if _, err = domain.ApplyBranchDelta(base, preview); !errors.Is(err, domain.ErrValidation) {
		t.Fatal("unreasoned preview became authority", err)
	}
}
