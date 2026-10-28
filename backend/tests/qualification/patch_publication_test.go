package qualification_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"novelstudio/internal/domain"
	"novelstudio/internal/service"
	"testing"
)

func TestReviewedPatchDescendantPublishesAtomicallyAndLaterEditsRemainPossible(t *testing.T) {
	f, _, binding, _, price := semanticReviewFixture(t)
	ctx := context.Background()
	ip := f.svc.IP
	reviews := &service.SemanticReviews{Generations: f.svc.Generations}
	b, err := ip.ReadBranch(ctx, f.actor, f.project, f.branch)
	if err != nil {
		t.Fatal(err)
	}
	resources := []domain.Resource{}
	baseInput := domain.ReleaseInput{BranchID: f.branch, ExpectedIPHead: b.Head, OperationID: service.NewID()}
	for _, name := range []string{"场景", "相关记录"} {
		r, e := ip.CreateResource(ctx, f.actor, f.project, service.ResourceInput{BranchID: f.branch, Name: name, Kind: "note", OperationID: service.NewID()})
		if e != nil {
			t.Fatal(e)
		}
		resources = append(resources, r)
		baseInput.Updates = append(baseInput.Updates, domain.ResourceUpdate{ResourceID: r.ID, Text: "旧城"})
	}
	base, err := ip.PrepareRelease(ctx, f.actor, f.project, baseInput)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ip.AdoptRelease(ctx, f.actor, f.project, base.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = ip.PublishRelease(ctx, f.actor, f.project, base.ID); err != nil {
		t.Fatal(err)
	}
	hash := func(value string) string { sum := sha256.Sum256([]byte(value)); return hex.EncodeToString(sum[:]) }
	group := domain.TextPatchGroup{ID: service.NewID()}
	for _, r := range resources {
		group.Members = append(group.Members, r.ID)
		group.Patches = append(group.Patches, domain.TextPatch{ID: service.NewID(), GroupID: group.ID, ResourceID: r.ID, BaseRevision: base.ID, BaseHash: hash("旧城"), StartRune: 0, EndRune: 2, ExpectedSliceHash: hash("旧城"), Replacement: "新城", Reason: "同步地点名称"})
	}
	patch, err := ip.PreparePatchRelease(ctx, f.actor, f.project, service.PatchInput{BranchID: f.branch, ExpectedIPHead: b.Head, OperationID: service.NewID(), Group: group})
	if err != nil {
		t.Fatal(err)
	}
	editedInput := domain.ReleaseInput{BranchID: f.branch, ExpectedIPHead: b.Head, OperationID: service.NewID()}
	for _, r := range resources {
		editedInput.Updates = append(editedInput.Updates, domain.ResourceUpdate{ResourceID: r.ID, ExpectedHead: base.ID, Text: "新城已经入冬。", Origin: &domain.ArtifactOriginInput{ParentReleaseID: patch.ID}})
	}
	edited, err := ip.PrepareRelease(ctx, f.actor, f.project, editedInput)
	if err != nil {
		t.Fatal(err)
	}
	if len(edited.Manifest.PatchParents) != 1 || edited.Manifest.PatchParents[0] != patch.ID {
		t.Fatal("patch root lost on manual edit")
	}
	if _, err = ip.AdoptRelease(ctx, f.actor, f.project, edited.ID); !errors.Is(err, domain.ErrNotReady) {
		t.Fatal("edit reused missing review", err)
	}
	report := completeSemanticReview(t, f, edited, binding, price)
	if _, err = reviews.Select(ctx, f.actor, f.project, report.ID, 0); err != nil {
		t.Fatal(err)
	}
	if _, err = ip.AdoptRelease(ctx, f.actor, f.project, edited.ID); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err = ip.PublishRelease(ctx, f.actor, f.project, edited.ID); err != nil {
			t.Fatal(err)
		}
	}
	for _, r := range resources {
		current, e := ip.ReadResource(ctx, f.actor, f.project, r.ID)
		if e != nil || current.Head != edited.ID || current.Text != "新城已经入冬。" {
			t.Fatal("group did not publish actual revised text", current, e)
		}
	}
	if retry, e := ip.PrepareRelease(ctx, f.actor, f.project, editedInput); e != nil || retry.ID != edited.ID {
		t.Fatal("published edit retry failed", retry.ID, e)
	}
	// Publishing a complete descendant resolves the original pending group.
	// It must not permanently lock later unrelated work into the old group.
	later, err := ip.PrepareRelease(ctx, f.actor, f.project, domain.ReleaseInput{BranchID: f.branch, ExpectedIPHead: b.Head, OperationID: service.NewID(), Updates: []domain.ResourceUpdate{{ResourceID: resources[0].ID, ExpectedHead: edited.ID, Text: "新城已经入冬，河上结冰。"}}})
	if err != nil {
		t.Fatal("completed group permanently blocked later single-resource edit", err)
	}
	if _, err = ip.AdoptRelease(ctx, f.actor, f.project, later.ID); !errors.Is(err, domain.ErrNotReady) {
		t.Fatal("later edit reused old review", err)
	}
	var events int
	if err = f.db.QueryRow("SELECT COUNT(*) FROM outbox WHERE kind='release.published' AND resource_id=?", edited.ID).Scan(&events); err != nil || events != 1 {
		t.Fatal("publication retry duplicated event", events, err)
	}
}

func TestReviewedMixedPatchKeepsNewMemberThroughEditAndPublication(t *testing.T) {
	f, _, binding, _, price := semanticReviewFixture(t)
	ctx := context.Background()
	ip := f.svc.IP
	b, err := ip.ReadBranch(ctx, f.actor, f.project, f.branch)
	if err != nil {
		t.Fatal(err)
	}
	old, err := ip.CreateResource(ctx, f.actor, f.project, service.ResourceInput{BranchID: f.branch, Name: "已有场景", Kind: "note", OperationID: service.NewID()})
	if err != nil {
		t.Fatal(err)
	}
	added, err := ip.CreateResource(ctx, f.actor, f.project, service.ResourceInput{BranchID: f.branch, Name: "新增资料", Kind: "note", OperationID: service.NewID()})
	if err != nil {
		t.Fatal(err)
	}
	base, err := ip.PrepareRelease(ctx, f.actor, f.project, domain.ReleaseInput{BranchID: f.branch, ExpectedIPHead: b.Head, OperationID: service.NewID(), Updates: []domain.ResourceUpdate{{ResourceID: old.ID, Text: "旧城"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ip.AdoptRelease(ctx, f.actor, f.project, base.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = ip.PublishRelease(ctx, f.actor, f.project, base.ID); err != nil {
		t.Fatal(err)
	}
	hash := func(value string) string { sum := sha256.Sum256([]byte(value)); return hex.EncodeToString(sum[:]) }
	group := domain.TextPatchGroup{ID: service.NewID(), Members: []string{old.ID}}
	group.Patches = []domain.TextPatch{{ID: service.NewID(), GroupID: group.ID, ResourceID: old.ID, BaseRevision: base.ID, BaseHash: hash("旧城"), StartRune: 0, EndRune: 2, ExpectedSliceHash: hash("旧城"), Replacement: "新城", Reason: "合并地点修订"}}
	mixed, err := ip.PrepareRelease(ctx, f.actor, f.project, domain.ReleaseInput{BranchID: f.branch, ExpectedIPHead: b.Head, OperationID: service.NewID(), PatchGroup: &group, Updates: []domain.ResourceUpdate{{ResourceID: old.ID, ExpectedHead: base.ID, Text: "新城"}, {ResourceID: added.ID, Text: "作者新增资料"}}})
	if err != nil {
		t.Fatal(err)
	}
	edits := []domain.ResourceUpdate{{ResourceID: old.ID, ExpectedHead: base.ID, Text: "新城已经入冬。", Origin: &domain.ArtifactOriginInput{ParentReleaseID: mixed.ID}}}
	if _, err = ip.PrepareRelease(ctx, f.actor, f.project, domain.ReleaseInput{BranchID: f.branch, ExpectedIPHead: b.Head, OperationID: service.NewID(), Updates: edits}); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("new member dropped", err)
	}
	edits = append(edits, domain.ResourceUpdate{ResourceID: added.ID, Text: "作者新增资料，经过补充。", Origin: &domain.ArtifactOriginInput{ParentReleaseID: mixed.ID}})
	edited, err := ip.PrepareRelease(ctx, f.actor, f.project, domain.ReleaseInput{BranchID: f.branch, ExpectedIPHead: b.Head, OperationID: service.NewID(), Updates: edits})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ip.AdoptRelease(ctx, f.actor, f.project, edited.ID); !errors.Is(err, domain.ErrNotReady) {
		t.Fatal("unreviewed mixed edit accepted", err)
	}
	report := completeSemanticReview(t, f, edited, binding, price)
	reviews := &service.SemanticReviews{Generations: f.svc.Generations}
	if _, err = reviews.Select(ctx, f.actor, f.project, report.ID, 0); err != nil {
		t.Fatal(err)
	}
	if _, err = ip.AdoptRelease(ctx, f.actor, f.project, edited.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = ip.PublishRelease(ctx, f.actor, f.project, edited.ID); err != nil {
		t.Fatal(err)
	}
	for _, edit := range edits {
		actual, e := ip.ReadResource(ctx, f.actor, f.project, edit.ResourceID)
		if e != nil || actual.Head != edited.ID || actual.Text != edit.Text {
			t.Fatal("mixed publication incomplete", actual, e)
		}
	}
	// Once this joint revision is published, a later edit need not repeat the
	// whole group; it still receives its own independent review requirement.
	if _, err = ip.PrepareRelease(ctx, f.actor, f.project, domain.ReleaseInput{BranchID: f.branch, ExpectedIPHead: b.Head, OperationID: service.NewID(), Updates: []domain.ResourceUpdate{{ResourceID: added.ID, ExpectedHead: edited.ID, Text: "后续独立补充"}}}); err != nil {
		t.Fatal("published group blocked later work", err)
	}
}
