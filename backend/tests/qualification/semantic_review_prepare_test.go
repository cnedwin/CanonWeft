package qualification_test

import (
	"context"
	"errors"
	"novelstudio/internal/domain"
	"novelstudio/internal/service"
	"strings"
	"testing"
)

func TestSemanticReviewPrepareWithoutModelPreservesTargetAndCostsNothing(t *testing.T) {
	f := isolated(t)
	ctx := context.Background()
	c, err := f.svc.IP.Propose(ctx, f.actor, f.project, f.branch, "", service.NewID(), domain.Snapshot{Entities: []domain.Entity{{ID: "hero", Kind: "character", Name: "林"}}})
	if err != nil {
		t.Fatal(err)
	}
	svc := &service.SemanticReviews{Generations: &service.Generations{IP: f.svc.IP}}
	input := service.SemanticReviewInput{OperationID: service.NewID(), TargetKind: "ip_candidate", TargetID: c.ID, TargetHash: c.Hash, BranchID: f.branch, MaxOutputTokens: 1024}
	run, err := svc.Prepare(ctx, f.actor, f.project, input)
	if err != nil || run.State != "BLOCKED" || run.Reason == "" || run.Frozen.InputHash == "" {
		t.Fatal("no model must preserve blocked review", run, err)
	}
	same, err := svc.Prepare(ctx, f.actor, f.project, input)
	if err != nil || same.ID != run.ID {
		t.Fatal("prepare replay changed identity", err)
	}
	input.TargetHash = strings.Repeat("a", 64)
	if _, err = svc.Prepare(ctx, f.actor, f.project, input); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("operation retargeted", err)
	}
	var calls int
	if err = f.db.QueryRow("SELECT COUNT(*) FROM model_calls").Scan(&calls); err != nil || calls != 0 {
		t.Fatal("prepare charged model", calls, err)
	}
	read, err := svc.Read(ctx, f.actor, f.project, run.ID)
	if err != nil || read.TargetHash != c.Hash {
		t.Fatal("blocked request not readable", err)
	}
	// Manual adoption does not acquire a new paid semantic gate.
	if _, err = f.svc.IP.Adopt(ctx, f.actor, f.project, f.branch, c.ID); err != nil {
		t.Fatal("manual adoption acquired fee gate", err)
	}
}

func TestSemanticReviewRestoredRequestCannotStartOrSelectHistoricalPass(t *testing.T) {
	f := isolated(t)
	ctx := context.Background()
	c, err := f.svc.IP.Propose(ctx, f.actor, f.project, f.branch, "", service.NewID(), domain.Snapshot{Entities: []domain.Entity{{ID: "hero", Kind: "character", Name: "林"}}})
	if err != nil {
		t.Fatal(err)
	}
	svc := &service.SemanticReviews{Generations: &service.Generations{IP: f.svc.IP}}
	run, err := svc.Prepare(ctx, f.actor, f.project, service.SemanticReviewInput{OperationID: service.NewID(), TargetKind: "ip_candidate", TargetID: c.ID, TargetHash: c.Hash, BranchID: f.branch, MaxOutputTokens: 1024})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Exec("UPDATE semantic_review_runs SET restored_disabled=TRUE,state='COMPLETED',verdict='PASS' WHERE id=?", run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Start(ctx, f.actor, f.project, run.ID); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("restored run started", err)
	}
	if _, err = svc.Select(ctx, f.actor, f.project, run.ID, 0); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("restored report selected", err)
	}
	read, err := svc.Read(ctx, f.actor, f.project, run.ID)
	if err != nil || !read.RestoredDisabled || read.Verdict != "PASS" {
		t.Fatal("restored history lost", err)
	}
}
