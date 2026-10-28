package qualification_test

import (
	"context"
	"errors"
	"novelstudio/internal/domain"
	"novelstudio/internal/service"
	"strings"
	"testing"
)

func TestSemanticNegativeCannotBeWashedByNewProposalIdentityOrRecordedTime(t *testing.T) {
	f, source, binding, protocol, price := semanticReviewFixture(t)
	ctx := context.Background()
	reviews := &service.SemanticReviews{Generations: f.svc.Generations}
	unknown := func(input service.SemanticReviewInput) service.SemanticReview {
		t.Helper()
		protocol.transform = func(r *domain.SemanticReviewReport) {
			r.Verdict = "UNKNOWN"
			r.Issues = []domain.SemanticReviewIssue{{Category: "unsupported", Severity: "unknown", TargetKey: r.Coverage[0], AnchorKeys: []string{r.Coverage[0]}, Explanation: "当前证据不足，不能确认"}}
		}
		defer func() { protocol.transform = nil }()
		return completeTargetReviewVerdict(t, f, input, binding, price, "UNKNOWN")
	}
	t.Run("release-id-is-not-content", func(t *testing.T) {
		resource, err := f.svc.IP.CreateResource(ctx, f.actor, f.project, service.ResourceInput{BranchID: f.branch, Kind: "note", Name: "人工语义资料", OperationID: service.NewID()})
		if err != nil {
			t.Fatal(err)
		}
		prepare := func(text string) domain.Release {
			t.Helper()
			r, e := f.svc.IP.PrepareRelease(ctx, f.actor, f.project, domain.ReleaseInput{BranchID: f.branch, OperationID: service.NewID(), Updates: []domain.ResourceUpdate{{ResourceID: resource.ID, Text: text}}})
			if e != nil {
				t.Fatal(e)
			}
			return r
		}
		first := prepare("未确定的人工资料")
		input := service.SemanticReviewInput{TargetKind: "artifact_release", TargetID: first.ID, TargetHash: first.Hash, BranchID: f.branch}
		negative := unknown(input)
		if _, err = f.svc.IP.AdoptRelease(ctx, f.actor, f.project, first.ID); !errors.Is(err, domain.ErrNotReady) {
			t.Fatal("original UNKNOWN adopted", err)
		}
		copied := prepare("未确定的人工资料")
		if copied.Hash == first.Hash {
			t.Fatal("fixture did not create a new manifest identity")
		}
		// Older live runs did not persist the equivalence fingerprint. They must
		// derive it from verified frozen input, rather than lose the known issue.
		if _, err = f.db.Exec("UPDATE semantic_review_runs SET frozen=JSON_REMOVE(frozen,'$.content_scope_hash') WHERE id=?", negative.ID); err != nil {
			t.Fatal(err)
		}
		if read, e := f.svc.IP.ReadRelease(ctx, f.actor, f.project, copied.ID); e != nil || !read.RequiresSemanticReview {
			t.Fatal("copied draft hid inherited review requirement", e)
		}
		if _, err = f.svc.IP.AdoptRelease(ctx, f.actor, f.project, copied.ID); !errors.Is(err, domain.ErrNotReady) {
			t.Fatal("same content washed negative through new release ID", err)
		}
		changed := prepare("已经补上明确依据的新资料")
		if _, err = f.db.Exec("UPDATE semantic_review_runs SET frozen=JSON_SET(frozen,'$.input_hash',?) WHERE id=?", strings.Repeat("0", 64), negative.ID); err != nil {
			t.Fatal(err)
		}
		if _, err = f.svc.IP.AdoptRelease(ctx, f.actor, f.project, changed.ID); !errors.Is(err, domain.ErrNotReady) {
			t.Fatal("unverifiable legacy material silently skipped", err)
		}
		if _, err = f.db.Exec("UPDATE semantic_review_runs SET frozen=JSON_SET(frozen,'$.input_hash',?) WHERE id=?", negative.Frozen.InputHash, negative.ID); err != nil {
			t.Fatal(err)
		}
		if _, err = f.svc.IP.AdoptRelease(ctx, f.actor, f.project, changed.ID); err != nil {
			t.Fatal("new content inherited unrelated negative", err)
		}
		oldPass := completeTargetReview(t, f, input, binding, price)
		if _, err = reviews.Select(ctx, f.actor, f.project, oldPass.ID, 0); err != nil {
			t.Fatal(err)
		}
		if _, err = f.svc.IP.AdoptRelease(ctx, f.actor, f.project, copied.ID); !errors.Is(err, domain.ErrNotReady) {
			t.Fatal("new proposal borrowed another target's PASS", err)
		}
		currentPass := completeSemanticReview(t, f, copied, binding, price)
		if _, err = reviews.Select(ctx, f.actor, f.project, currentPass.ID, 0); err != nil {
			t.Fatal(err)
		}
		if _, err = f.svc.IP.AdoptRelease(ctx, f.actor, f.project, copied.ID); err != nil {
			t.Fatal("current target PASS did not authorize adoption", err)
		}
		unknown(input)
		if _, err = f.svc.IP.AdoptRelease(ctx, f.actor, f.project, copied.ID); !errors.Is(err, domain.ErrConflict) {
			t.Fatal("old selected PASS ignored newer equivalent UNKNOWN", err)
		}
		if read, e := reviews.Read(ctx, f.actor, f.project, currentPass.ID); e != nil || read.SelectionReady {
			t.Fatal("superseded PASS still offered authority", e)
		}
		latest := completeSemanticReview(t, f, copied, binding, price)
		if _, err = reviews.Select(ctx, f.actor, f.project, latest.ID, 1); err != nil {
			t.Fatal(err)
		}
		if _, err = f.svc.IP.AdoptRelease(ctx, f.actor, f.project, copied.ID); err != nil {
			t.Fatal("newer exact PASS could not resolve negative", err)
		}
	})
	t.Run("qualified-execution-identity-is-not-new-content", func(t *testing.T) {
		resource, err := f.svc.IP.CreateResource(ctx, f.actor, f.project, service.ResourceInput{BranchID: f.branch, Kind: "note", Name: "相同内容换来源", OperationID: service.NewID()})
		if err != nil {
			t.Fatal(err)
		}
		input := domain.ReleaseInput{BranchID: f.branch, OperationID: service.NewID(), Updates: []domain.ResourceUpdate{{ResourceID: resource.ID, Text: source.Output.Text}}}
		manual, err := f.svc.IP.PrepareRelease(ctx, f.actor, f.project, input)
		if err != nil {
			t.Fatal(err)
		}
		unknown(service.SemanticReviewInput{TargetKind: "artifact_release", TargetID: manual.ID, TargetHash: manual.Hash, BranchID: f.branch})
		input.OperationID = service.NewID()
		input.Updates[0].Origin = &domain.ArtifactOriginInput{GenerationID: source.ID}
		qualified, err := f.svc.IP.PrepareRelease(ctx, f.actor, f.project, input)
		if err != nil {
			t.Fatal(err)
		}
		if qualified.Manifest.Artifacts[0].Origin.ReviewRequired {
			t.Fatal("fixture unexpectedly tests derived gate")
		}
		if _, err = f.svc.IP.AdoptRelease(ctx, f.actor, f.project, qualified.ID); !errors.Is(err, domain.ErrNotReady) {
			t.Fatal("qualified execution identity washed same-material UNKNOWN", err)
		}
	})
	t.Run("system-time-and-object-order-are-not-content", func(t *testing.T) {
		snapshot := domain.Snapshot{Entities: []domain.Entity{{ID: "a", Kind: "character", Name: "甲"}, {ID: "b", Kind: "character", Name: "乙"}}, Assertions: []domain.Assertion{{ID: "fact", Subject: "a", Predicate: "职业", Value: "医师", Kind: "fact", Source: "人工设定", Visibility: "public"}}}
		first, err := f.svc.IP.Propose(ctx, f.actor, f.project, f.branch, "", service.NewID(), snapshot)
		if err != nil {
			t.Fatal(err)
		}
		unknown(service.SemanticReviewInput{TargetKind: "ip_candidate", TargetID: first.ID, TargetHash: first.Hash, BranchID: f.branch})
		snapshot = first.Snapshot
		snapshot.Entities = append([]domain.Entity{}, first.Snapshot.Entities...)
		snapshot.Assertions = append([]domain.Assertion{}, first.Snapshot.Assertions...)
		snapshot.Entities[0], snapshot.Entities[1] = snapshot.Entities[1], snapshot.Entities[0]
		copied, err := f.svc.IP.Propose(ctx, f.actor, f.project, f.branch, "", service.NewID(), snapshot)
		if err != nil {
			t.Fatal(err)
		}
		if first.Hash == copied.Hash || first.Snapshot.Assertions[0].RecordedAt == copied.Snapshot.Assertions[0].RecordedAt {
			t.Fatal("fixture did not restamp the new candidate")
		}
		if _, err = f.svc.IP.Adopt(ctx, f.actor, f.project, f.branch, copied.ID); !errors.Is(err, domain.ErrNotReady) {
			t.Fatal("system timestamp/order erased semantic negative", err)
		}
		snapshot.Assertions[0].Value = "有明确来源的药师"
		changed, err := f.svc.IP.Propose(ctx, f.actor, f.project, f.branch, "", service.NewID(), snapshot)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.svc.IP.Adopt(ctx, f.actor, f.project, f.branch, changed.ID); err != nil {
			t.Fatal("real assertion edit kept old negative", err)
		}
	})
}
