package qualification_test

import (
	"context"
	"novelstudio/internal/service"
	"strings"
	"testing"
)

func TestMissingBaselineIsNotRunAndCannotQualify(t *testing.T) {
	f, p, input := modelFixture(t)
	input.BaselineID = ""
	ctx := context.Background()
	q, err := f.svc.Quote(ctx, f.actor, f.project, input)
	if err != nil || q.Ready || len(q.Reasons) == 0 || !strings.Contains(q.Reasons[0], "NOT_RUN") {
		t.Fatal("missing baseline silently passed", q, err)
	}
	r, err := f.svc.Create(ctx, f.actor, f.project, input)
	if err != nil || r.State != "BLOCKED" || r.QualificationID != "" {
		t.Fatal(r.State, err)
	}
	if state, err := f.svc.Advance(ctx, r.ID); err != nil || state != "BLOCKED" || p.calls.Load() != 0 {
		t.Fatal("unavailable comparison dispatched", state, p.calls.Load(), err)
	}
}

func TestChangedBranchMakesFrozenEvaluationStaleBeforeDispatch(t *testing.T) {
	f, p, input := modelFixture(t)
	ctx := context.Background()
	r, err := f.svc.Create(ctx, f.actor, f.project, input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Exec("UPDATE branches SET head_revision=?,revision=revision+1 WHERE id=?", service.NewID(), f.branch); err != nil {
		t.Fatal(err)
	}
	if state, err := f.svc.Advance(ctx, r.ID); err != nil || state != "BLOCKED" || p.calls.Load() != 0 {
		t.Fatal("changed branch dispatched", state, p.calls.Load(), err)
	}
	read, err := f.svc.Read(ctx, f.actor, f.project, r.ID)
	if err != nil || !read.Stale {
		t.Fatal("changed branch not reported stale", err)
	}
}
