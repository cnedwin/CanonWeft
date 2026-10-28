package qualification_test

import (
	"context"
	"errors"
	"novelstudio/internal/domain"
	"novelstudio/internal/service"
	"testing"
)

func TestCompositionPublishesOnlyAfterItsOwnIndependentReview(t *testing.T) {
	f, _, binding, _, price := semanticReviewFixture(t)
	ctx := context.Background()
	ip := f.svc.IP
	input := service.ReleaseCompositionInput{OperationID: service.NewID(), RebindFollowing: true}
	ids := []string{}
	for _, text := range []string{"前章：收到来信。", "后章：依照约定赴会。"} {
		r, err := ip.CreateResource(ctx, f.actor, f.project, service.ResourceInput{BranchID: f.branch, Name: text, Kind: "note", OperationID: service.NewID()})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, r.ID)
		p, err := ip.PrepareRelease(ctx, f.actor, f.project, domain.ReleaseInput{BranchID: f.branch, OperationID: service.NewID(), Updates: []domain.ResourceUpdate{{ResourceID: r.ID, Text: text}}})
		if err != nil {
			t.Fatal(err)
		}
		input.Releases = append(input.Releases, service.JointReleasePin{ReleaseID: p.ID, ManifestHash: p.Hash})
	}
	composed, err := ip.PrepareReleaseComposition(ctx, f.actor, f.project, input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ip.AdoptRelease(ctx, f.actor, f.project, composed.ID); !errors.Is(err, domain.ErrNotReady) {
		t.Fatal("composition bypassed independent review", err)
	}
	review := completeSemanticReview(t, f, composed, binding, price)
	reviews := &service.SemanticReviews{Generations: f.svc.Generations}
	if _, err = reviews.Select(ctx, f.actor, f.project, review.ID, 0); err != nil {
		t.Fatal(err)
	}
	if _, err = ip.AdoptRelease(ctx, f.actor, f.project, composed.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = ip.PublishRelease(ctx, f.actor, f.project, composed.ID); err != nil {
		t.Fatal(err)
	}
	replay, err := ip.PrepareReleaseComposition(ctx, f.actor, f.project, input)
	if err != nil || replay.ID != composed.ID {
		t.Fatal("lost reply could not replay after publication", err)
	}
	for _, id := range ids {
		r, err := ip.ReadResource(ctx, f.actor, f.project, id)
		if err != nil || r.Head != composed.ID {
			t.Fatal("composition not published atomically", err)
		}
	}
}
