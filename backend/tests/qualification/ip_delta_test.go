package qualification_test

import (
	"context"
	"errors"
	"novelstudio/internal/domain"
	"novelstudio/internal/service"
	"testing"
)

func TestIPDeltaPersistsReasonsAndDraftWithoutModel(t *testing.T) {
	f := isolated(t)
	ctx := context.Background()
	snapshot := domain.Snapshot{Entities: []domain.Entity{{ID: "hero", Kind: "character", Name: "林青"}}}
	if _, err := f.svc.IP.ProposeChanges(ctx, f.actor, f.project, f.branch, "", service.NewID(), snapshot, nil); !errors.Is(err, domain.ErrValidation) {
		t.Fatal("new change API silently omitted reasons", err)
	}
	reasons := map[string]string{"entity:hero:": "确立主要角色"}
	actions := &service.IPBranchActionDraft{Merge: &service.MergeInput{BaseHead: "old-base", UpstreamHead: "old-upstream", Resolutions: map[string]string{"entity:hero": "local"}, ChangeReasons: reasons}, Promotion: &service.PromotionInput{SourceBranch: "saved-source", SourceHead: "saved-head", Reason: "选择性回流", Selection: domain.PromotionSelection{Entities: []string{"hero"}}, ChangeReasons: reasons}}
	draft, err := f.svc.IP.SaveDraft(ctx, f.actor, f.project, f.branch, service.IPDraft{Snapshot: snapshot, ChangeReasons: reasons, BranchActions: actions})
	if err != nil {
		t.Fatal(err)
	}
	read, err := f.svc.IP.ReadDraft(ctx, f.actor, f.project, f.branch)
	if err != nil || read.ChangeReasons["entity:hero:"] != reasons["entity:hero:"] || read.Revision != draft.Revision {
		t.Fatal("draft reasons lost", read, err)
	}
	if read.BranchActions == nil || read.BranchActions.Merge.Resolutions["entity:hero"] != "local" || read.BranchActions.Promotion.SourceHead != "saved-head" || read.BranchActions.Promotion.ChangeReasons["entity:hero:"] != reasons["entity:hero:"] {
		t.Fatal("branch action draft lost", read)
	}
	stale := read
	read.ChangeReasons = map[string]string{"entity:hero:": "更新后的人工理由"}
	if _, err = f.svc.IP.SaveDraft(ctx, f.actor, f.project, f.branch, read); err != nil {
		t.Fatal(err)
	}
	stale.BranchActions = nil
	if _, err = f.svc.IP.SaveDraft(ctx, f.actor, f.project, f.branch, stale); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("stale branch editor overwrote draft", err)
	}
	c, err := f.svc.IP.ProposeChanges(ctx, f.actor, f.project, f.branch, "", service.NewID(), snapshot, reasons)
	if err != nil || c.Delta == nil || len(c.Delta.Operations) != 1 || c.Delta.Operations[0].Reason != reasons["entity:hero:"] {
		t.Fatal("candidate delta missing", c, err)
	}
	items, err := f.svc.IP.Candidates(ctx, f.actor, f.project, f.branch)
	if err != nil || len(items) != 1 || items[0].Delta == nil {
		t.Fatal("candidate delta not durable", err)
	}
	applied, err := domain.ApplyBranchDelta(domain.Snapshot{Entities: []domain.Entity{}, Assertions: []domain.Assertion{}}, *items[0].Delta)
	if err != nil || applied.Snapshot.Entities[0].Name != "林青" {
		t.Fatal("stored delta cannot reproduce effective snapshot", err)
	}
	if _, err = f.svc.IP.Adopt(ctx, f.actor, f.project, f.branch, c.ID); err != nil {
		t.Fatal("manual delta acquired fee gate", err)
	}
}
