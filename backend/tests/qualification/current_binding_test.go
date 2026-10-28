package qualification_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"novelstudio/internal/domain"
	"novelstudio/internal/models"
	"novelstudio/internal/service"
	"novelstudio/internal/storage"
	"testing"
)

type beforeBlobCommit struct {
	storage.BlobStore
	before func()
}

func (b *beforeBlobCommit) PutObject(ctx context.Context, project string, input io.Reader) (storage.BlobRef, error) {
	if b.before != nil {
		fn := b.before
		b.before = nil
		fn()
	}
	return b.BlobStore.PutObject(ctx, project, input)
}

func TestNewQualifiedOperationMustUseCurrentHeadButPreparedTaskKeepsBinding(t *testing.T) {
	f, p, input := modelFixture(t)
	ctx := context.Background()
	r, err := f.svc.Create(ctx, f.actor, f.project, input)
	if err != nil {
		t.Fatal(err)
	}
	r = drive(t, f, r.ID)
	b1, err := f.svc.Activate(ctx, f.actor, f.project, service.BindingInput{OperationID: service.NewID(), QualificationID: r.QualificationID, TaskKind: "shared.contract"})
	if err != nil {
		t.Fatal(err)
	}
	request := service.GenerationInput{BindingID: b1.ID, TaskKind: "shared.contract", BranchID: f.branch, Role: "researcher", PriceID: input.PriceID, OperationID: service.NewID(), AgentInput: json.RawMessage(`{"source":"pinned before owner switch"}`), Request: models.Request{Prompt: "candidate", MaxOutputTokens: 128}}
	prepared, err := f.svc.Generations.Prepare(ctx, f.actor, f.project, request)
	if err != nil {
		t.Fatal(err)
	}
	b2, err := f.svc.Activate(ctx, f.actor, f.project, service.BindingInput{OperationID: service.NewID(), QualificationID: r.QualificationID, TaskKind: "shared.contract", ExpectedBinding: b1.ID, Rollback: true})
	if err != nil {
		t.Fatal(err)
	}
	if replay, e := f.svc.Generations.Prepare(ctx, f.actor, f.project, request); e != nil || replay.ID != prepared.ID {
		t.Fatal("existing operation replay lost its frozen binding", e)
	}
	request.OperationID = service.NewID()
	if _, err = f.svc.Generations.Prepare(ctx, f.actor, f.project, request); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("new historical-binding operation bypassed current head", err)
	}
	// Switch after the initial binding check, while the request blob is persisted.
	blobs := f.svc.IP.Blobs
	f.svc.IP.Blobs = &beforeBlobCommit{BlobStore: blobs, before: func() {
		if _, e := f.svc.Activate(ctx, f.actor, f.project, service.BindingInput{OperationID: service.NewID(), QualificationID: r.QualificationID, TaskKind: "shared.contract", ExpectedBinding: b2.ID, Rollback: true}); e != nil {
			t.Fatal(e)
		}
	}}
	request.BindingID = b2.ID
	request.OperationID = service.NewID()
	if _, e := f.svc.Generations.Prepare(ctx, f.actor, f.project, request); !errors.Is(e, domain.ErrConflict) {
		t.Fatal("binding changed during preparation escaped final transaction check", e)
	}
	f.svc.IP.Blobs = blobs
	if _, err = f.svc.Generations.Start(ctx, f.actor, f.project, prepared.ID); err != nil {
		t.Fatal("existing pin invalidated", err)
	}
	if state, err := f.svc.Generations.Execute(ctx, prepared.ID); err != nil || state != "SUCCEEDED" || p.calls.Load() != 19 {
		t.Fatal("old prepared pin did not finish once", state, p.calls.Load(), err)
	}
}
