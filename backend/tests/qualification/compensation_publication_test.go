package qualification_test

import (
	"context"
	"novelstudio/internal/domain"
	"novelstudio/internal/service"
	"testing"
)

func TestReviewedCompensationCreatesNewVersionPreservingLaterText(t *testing.T) {
	f, _, binding, _, price := semanticReviewFixture(t)
	ctx := context.Background()
	ip := f.svc.IP
	b, err := ip.ReadBranch(ctx, f.actor, f.project, f.branch)
	if err != nil {
		t.Fatal(err)
	}
	r, err := ip.CreateResource(ctx, f.actor, f.project, service.ResourceInput{BranchID: f.branch, Name: "撤回场景", Kind: "note", OperationID: service.NewID()})
	if err != nil {
		t.Fatal(err)
	}
	publish := func(head, text string) domain.Release {
		v, err := ip.PrepareRelease(ctx, f.actor, f.project, domain.ReleaseInput{BranchID: f.branch, ExpectedIPHead: b.Head, OperationID: service.NewID(), Updates: []domain.ResourceUpdate{{ResourceID: r.ID, ExpectedHead: head, Text: text}}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = ip.AdoptRelease(ctx, f.actor, f.project, v.ID); err != nil {
			t.Fatal(err)
		}
		if _, err = ip.PublishRelease(ctx, f.actor, f.project, v.ID); err != nil {
			t.Fatal(err)
		}
		return v
	}
	base := publish("", "旧城。\n原来的结尾。")
	changed := publish(base.ID, "新城。\n原来的结尾。")
	later := publish(changed.ID, "新城。\n作者后来重写的结尾。")
	preview, err := ip.PreviewCompensation(ctx, f.actor, f.project, service.CompensationInput{BranchID: f.branch, ExpectedIPHead: b.Head, ReleaseID: changed.ID, Reason: "撤回地名修改"})
	if err != nil || preview.Group == nil {
		t.Fatal(preview, err)
	}
	input := service.PatchInput{BranchID: f.branch, ExpectedIPHead: b.Head, OperationID: service.NewID(), Group: *preview.Group}
	candidate, err := ip.PreparePatchRelease(ctx, f.actor, f.project, input)
	if err != nil {
		t.Fatal(err)
	}
	report := completeSemanticReview(t, f, candidate, binding, price)
	reviews := &service.SemanticReviews{Generations: f.svc.Generations}
	if _, err = reviews.Select(ctx, f.actor, f.project, report.ID, 0); err != nil {
		t.Fatal(err)
	}
	if _, err = ip.AdoptRelease(ctx, f.actor, f.project, candidate.ID); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err = ip.PublishRelease(ctx, f.actor, f.project, candidate.ID); err != nil {
			t.Fatal(err)
		}
	}
	current, err := ip.ReadResource(ctx, f.actor, f.project, r.ID)
	if err != nil || current.Head != candidate.ID || current.Head == base.ID || current.Text != "旧城。\n作者后来重写的结尾。" {
		t.Fatal(current, err)
	}
	for id, want := range map[string]string{changed.ID: "新城。\n原来的结尾。", later.ID: "新城。\n作者后来重写的结尾。"} {
		history, err := ip.ReadRelease(ctx, f.actor, f.project, id)
		if err != nil || len(history.Contents) != 1 || history.Contents[0].Text != want {
			t.Fatal("historical publication changed", history, err)
		}
	}
	if retry, err := ip.PreparePatchRelease(ctx, f.actor, f.project, input); err != nil || retry.ID != candidate.ID {
		t.Fatal("compensation retry failed", retry, err)
	}
}
