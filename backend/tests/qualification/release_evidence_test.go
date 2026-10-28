package qualification_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"novelstudio/internal/domain"
	"novelstudio/internal/models"
	"novelstudio/internal/service"
)

func releaseGenerationFixture(t *testing.T) (*fixture, service.Generation, service.QualifiedBinding) {
	t.Helper()
	f, _, input := modelFixture(t)
	ctx := context.Background()
	run, err := f.svc.Create(ctx, f.actor, f.project, input)
	if err != nil {
		t.Fatal(err)
	}
	result := drive(t, f, run.ID)
	if result.State != "PASSED" {
		t.Fatal("protocol qualification failed", result.State)
	}
	binding, err := f.svc.Activate(ctx, f.actor, f.project, service.BindingInput{OperationID: service.NewID(), QualificationID: result.QualificationID, TaskKind: "shared.contract"})
	if err != nil {
		t.Fatal(err)
	}
	job, err := f.svc.Generations.Prepare(ctx, f.actor, f.project, service.GenerationInput{BindingID: binding.ID, TaskKind: "shared.contract", BranchID: f.branch, Role: "researcher", PriceID: input.PriceID, OperationID: service.NewID(), AgentInput: json.RawMessage(`{"source":"独立的资料候选"}`), Request: models.Request{Prompt: "检查资料", MaxOutputTokens: 128}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.Generations.Start(ctx, f.actor, f.project, job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.Generations.Execute(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	job, err = f.svc.Generations.Read(ctx, f.actor, f.project, job.ID)
	if err != nil || job.Output == nil {
		t.Fatal("source output missing", err)
	}
	return f, job, binding
}

func prepareGenerationRelease(t *testing.T, f *fixture, job service.Generation, text string) domain.Release {
	t.Helper()
	ctx := context.Background()
	r, err := f.svc.IP.CreateResource(ctx, f.actor, f.project, service.ResourceInput{BranchID: f.branch, Name: "来源资料", Kind: "note", OperationID: service.NewID()})
	if err != nil {
		t.Fatal(err)
	}
	release, err := f.svc.IP.PrepareRelease(ctx, f.actor, f.project, domain.ReleaseInput{OperationID: service.NewID(), BranchID: f.branch, Updates: []domain.ResourceUpdate{{ResourceID: r.ID, Text: text, Origin: &domain.ArtifactOriginInput{GenerationID: job.ID}}}})
	if err != nil {
		t.Fatal(err)
	}
	return release
}

func TestQualifiedReleasePreservesFrozenBindingAndBlocksDisabledEvidence(t *testing.T) {
	f, job, binding := releaseGenerationFixture(t)
	ctx := context.Background()
	release := prepareGenerationRelease(t, f, job, job.Output.Text)
	origin := release.Manifest.Artifacts[0].Origin
	if origin == nil || origin.Generation == nil || origin.Generation.QualificationID != binding.QualificationID || origin.Generation.BindingID != binding.ID || origin.ReviewRequired {
		t.Fatal("release did not freeze actual qualified output", origin)
	}
	// Switching the head does not rewrite a completed task's explicitly pinned binding.
	if _, err := f.svc.Activate(ctx, f.actor, f.project, service.BindingInput{OperationID: service.NewID(), QualificationID: binding.QualificationID, TaskKind: binding.TaskKind, ExpectedBinding: binding.ID, Rollback: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.IP.AdoptRelease(ctx, f.actor, f.project, release.ID); err != nil {
		t.Fatal("new binding head invalidated pinned evidence", err)
	}
	if err := f.svc.DisableBinding(ctx, f.actor, f.project, binding.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.IP.PublishRelease(ctx, f.actor, f.project, release.ID); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("disabled binding escaped publication preflight", err)
	}
	companion := jointManualCompanion(t, f)
	if _, err := f.svc.IP.PublishJoint(ctx, f.actor, f.project, jointInput(release, companion)); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("disabled binding escaped joint publication", err)
	}
	untouched, err := f.svc.IP.ReadResource(ctx, f.actor, f.project, companion.Manifest.Artifacts[0].ResourceID)
	if err != nil || untouched.Head != "" {
		t.Fatal("failed qualified member partially published companion", err)
	}
	var head string
	if err := f.db.QueryRow("SELECT head_revision FROM resources WHERE id=?", origin.ResourceID).Scan(&head); err != nil || head != "" {
		t.Fatal("blocked publication changed target", head, err)
	}
}

func TestQualifiedReleaseEditLosesEligibilityAndCannotRelabelManual(t *testing.T) {
	f, job, _ := releaseGenerationFixture(t)
	ctx := context.Background()
	source := prepareGenerationRelease(t, f, job, job.Output.Text)
	ref := source.Manifest.Artifacts[0]
	input := domain.ReleaseInput{OperationID: service.NewID(), BranchID: f.branch, Updates: []domain.ResourceUpdate{{ResourceID: ref.ResourceID, Text: "人工改变了实际生成内容"}}}
	edited, err := f.svc.IP.PrepareRelease(ctx, f.actor, f.project, input)
	if err != nil {
		t.Fatal(err)
	}
	if o := edited.Manifest.Artifacts[0].Origin; o == nil || o.Kind != "derived" || !o.ReviewRequired || o.Generation.GenerationID != job.ID {
		t.Fatal("edited content inherited old output eligibility", o)
	}
	if _, err = f.svc.IP.AdoptRelease(ctx, f.actor, f.project, edited.ID); !errors.Is(err, domain.ErrNotReady) {
		t.Fatal("edit reused original qualification", err)
	}
	read, err := f.svc.IP.ReadRelease(ctx, f.actor, f.project, edited.ID)
	if err != nil || len(read.ReviewTargets) != 1 {
		t.Fatal("missing independent review handoff", err)
	}
	target := read.ReviewTargets[0]
	if target.ManifestHash != edited.Hash || target.ContentHash != edited.Manifest.Artifacts[0].Hash || target.ResourceID != ref.ResourceID || target.SourceGenerationID != job.ID || target.ReleaseID != edited.ID {
		t.Fatal("independent review handoff not bound to exact current content", target)
	}
	// A different source target cannot be borrowed as this resource's parent.
	other := prepareGenerationRelease(t, f, job, job.Output.Text)
	input.OperationID = service.NewID()
	input.Updates[0].Origin = &domain.ArtifactOriginInput{ParentReleaseID: other.ID}
	if _, err = f.svc.IP.PrepareRelease(ctx, f.actor, f.project, input); !errors.Is(err, domain.ErrForbidden) {
		t.Fatal("source parent retargeted", err)
	}
}

func TestQualifiedReleaseRechecksExpiryAndRevocationBeforeAdoption(t *testing.T) {
	f, job, binding := releaseGenerationFixture(t)
	ctx := context.Background()
	release := prepareGenerationRelease(t, f, job, job.Output.Text)
	if _, err := f.db.Exec("UPDATE capability_qualifications SET expires_at=UTC_TIMESTAMP(6)-INTERVAL 1 SECOND WHERE id=?", binding.QualificationID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.IP.AdoptRelease(ctx, f.actor, f.project, release.ID); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("expired evidence adopted", err)
	}
	if _, err := f.db.Exec("UPDATE capability_qualifications SET expires_at=UTC_TIMESTAMP(6)+INTERVAL 1 HOUR WHERE id=?", binding.QualificationID); err != nil {
		t.Fatal(err)
	}
	if err := (&service.Capabilities{IP: f.svc.IP}).Revoke(ctx, f.actor, f.project, job.Frozen.Capability.AgentVersionID, "来源方法撤销"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.IP.AdoptRelease(ctx, f.actor, f.project, release.ID); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("revoked evidence adopted", err)
	}
}

func TestQualifiedReleaseRejectsMismatchedCallInput(t *testing.T) {
	f, job, _ := releaseGenerationFixture(t)
	ctx := context.Background()
	release := prepareGenerationRelease(t, f, job, job.Output.Text)
	if _, err := f.db.Exec("UPDATE model_calls SET input_hash=? WHERE id=?", strings.Repeat("a", 64), job.CallID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.IP.AdoptRelease(ctx, f.actor, f.project, release.ID); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("result from a different frozen input accepted as source evidence", err)
	}
}

func TestQualifiedReleasePublishesExactSourceAndRetainsHistory(t *testing.T) {
	f, job, _ := releaseGenerationFixture(t)
	ctx := context.Background()
	budgets := &service.Budgets{Workspace: f.svc.IP.Workspace}
	before, err := budgets.Read(ctx, f.actor.ID)
	if err != nil {
		t.Fatal(err)
	}
	before.Enabled = false
	if _, err = budgets.Configure(ctx, f.actor.ID, before); err != nil {
		t.Fatal(err)
	}
	release := prepareGenerationRelease(t, f, job, job.Output.Text)
	if _, err := f.svc.IP.AdoptRelease(ctx, f.actor, f.project, release.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.IP.PublishRelease(ctx, f.actor, f.project, release.ID); err != nil {
		t.Fatal("qualified reference publication", err)
	}
	if _, err := f.svc.IP.PublishRelease(ctx, f.actor, f.project, release.ID); err != nil {
		t.Fatal("publication replay", err)
	}
	resource, err := f.svc.IP.ReadResource(ctx, f.actor, f.project, release.Manifest.Artifacts[0].ResourceID)
	if err != nil || resource.Head != release.ID || resource.Text != job.Output.Text {
		t.Fatal("published content mismatch", err)
	}
	var events int
	if err = f.db.QueryRow("SELECT COUNT(*) FROM outbox WHERE kind='release.published' AND resource_id=?", release.ID).Scan(&events); err != nil || events != 1 {
		t.Fatal("publication was not atomic/idempotent", events, err)
	}
	after, err := budgets.Read(ctx, f.actor.ID)
	if err != nil || after.Enabled || after.Spent != before.Spent || after.Reserved != before.Reserved {
		t.Fatal("source validation changed budget", before, after, err)
	}
}

func TestReleaseManualImportAndLegacySourcePolicies(t *testing.T) {
	f := isolated(t)
	ctx := context.Background()
	r, err := f.svc.IP.CreateResource(ctx, f.actor, f.project, service.ResourceInput{BranchID: f.branch, Name: "人工整理", Kind: "note", OperationID: service.NewID()})
	if err != nil {
		t.Fatal(err)
	}
	input := domain.ReleaseInput{BranchID: f.branch, OperationID: service.NewID(), Updates: []domain.ResourceUpdate{{ResourceID: r.ID, Text: "人工参考记录"}}}
	manual, err := f.svc.IP.PrepareRelease(ctx, f.actor, f.project, input)
	if err != nil {
		t.Fatal(err)
	}
	o := manual.Manifest.Artifacts[0].Origin
	if o == nil || o.Kind != "manual" || o.Generation != nil || o.ReviewRequired || o.ActorID != f.actor.ID {
		t.Fatal("manual source invented AI review", o)
	}
	repeated, err := f.svc.IP.PrepareRelease(ctx, f.actor, f.project, input)
	if err != nil || repeated.ID != manual.ID {
		t.Fatal("source preparation is not idempotent", err)
	}
	if _, err = f.svc.IP.AdoptRelease(ctx, f.actor, f.project, manual.ID); err != nil {
		t.Fatal("manual reference adoption blocked", err)
	}
	source, err := f.svc.IP.ImportSource(ctx, f.actor, f.project, f.branch, "访谈.txt", service.NewID(), []byte("原始访谈记录"))
	if err != nil {
		t.Fatal(err)
	}
	imported, err := f.svc.IP.ImportCandidate(ctx, f.actor, f.project, f.branch, source.ID, service.ImportCandidateInput{Text: "原始访谈记录", OperationID: service.NewID()})
	if err != nil {
		t.Fatal(err)
	}
	o = imported.Manifest.Artifacts[0].Origin
	if o == nil || o.Kind != "import" || o.ImportID != source.ID || o.ImportHash != source.Hash || o.Generation != nil || o.ReviewRequired {
		t.Fatal("import source lost or misrepresented", o)
	}
	if _, err = f.svc.IP.AdoptRelease(ctx, f.actor, f.project, imported.ID); err != nil {
		t.Fatal("import reference adoption blocked", err)
	}
	if _, err = f.svc.IP.PublishRelease(ctx, f.actor, f.project, imported.ID); err != nil {
		t.Fatal("import reference publication blocked", err)
	}
	// Old immutable records retain their exact canonical bytes and read path.
	legacy := manual.Manifest
	legacy.Schema = 1
	legacy.Artifacts[0].Origin = nil
	raw, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(raw)
	if _, err = f.db.Exec("UPDATE artifact_releases SET manifest=?,content_hash=?,status='PREPARED' WHERE id=?", raw, hex.EncodeToString(hash[:]), manual.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.IP.ReadRelease(ctx, f.actor, f.project, manual.ID); err != nil {
		t.Fatal("legacy manifest unreadable", err)
	}
	if _, err = f.svc.IP.AdoptRelease(ctx, f.actor, f.project, manual.ID); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("legacy record silently acquired v2 authority", err)
	}
	f.request(t, "POST", "/releases", map[string]any{"branch_id": f.branch, "operation_id": service.NewID(), "updates": []any{map[string]any{"resource_id": r.ID, "text": "forged", "origin": map[string]any{"kind": "manual", "qualified": true}}}}, 400)
}
