package qualification_test

import (
	"context"
	"errors"
	"novelstudio/internal/domain"
	"novelstudio/internal/service"
	"testing"
	"time"
)

func TestSemanticReviewCrossAuthorSelectionRetriesWithoutLosingRevocation(t *testing.T) {
	f, source, binding, _, price := semanticReviewFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	owner := f.actor
	editor, err := f.svc.IP.Workspace.CreateMember(ctx, owner.ID, "independent-reviewer", "independent-review-password-123", "editor")
	if err != nil {
		t.Fatal(err)
	}
	if err = f.svc.IP.Workspace.SetMember(ctx, owner.ID, f.project, editor.ID, "editor", false); err != nil {
		t.Fatal(err)
	}
	release := prepareGenerationRelease(t, f, source, "另一成员独立审查这段改稿")
	f.actor = editor
	report := completeSemanticReview(t, f, release, binding, price)
	f.actor = owner
	svc := &service.SemanticReviews{Generations: f.svc.Generations}
	gate, err := f.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer gate.Rollback()
	var id string
	if err = gate.QueryRowContext(ctx, "SELECT id FROM semantic_review_runs WHERE id=? FOR UPDATE", report.ID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	selected := make(chan error, 1)
	go func() { _, e := svc.Select(ctx, owner, f.project, report.ID, 0); selected <- e }()
	waitReleaseLock(t, ctx, f, "semantic_review_runs")
	read := make(chan error, 1)
	go func() {
		_, e := f.svc.IP.ReadResource(ctx, editor, f.project, release.Manifest.Artifacts[0].ResourceID)
		read <- e
	}()
	waitReleaseLock(t, ctx, f, "projects")
	if err = gate.Commit(); err != nil {
		t.Fatal(err)
	}
	for name, result := range map[string]<-chan error{"select": selected, "reviewer ordinary read": read} {
		select {
		case e := <-result:
			if e != nil {
				t.Errorf("%s: %v", name, e)
			}
		case <-ctx.Done():
			t.Fatal("cross-author deadlock", ctx.Err())
		}
	}
	if t.Failed() {
		return
	}
	if err = f.svc.IP.Workspace.SetMember(ctx, owner.ID, f.project, editor.ID, "", true); err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.IP.AdoptRelease(ctx, owner, f.project, release.ID); !errors.Is(err, domain.ErrForbidden) {
		t.Fatal("removed reviewer still authorized", err)
	}
}
