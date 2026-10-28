package integration

import (
	"context"
	"errors"
	"novelstudio/internal/domain"
	"novelstudio/internal/service"
	"os"
	"strings"
	"testing"
)

func assertResearchWithdrawal(t *testing.T, svc *service.NovelResearch, actor domain.Actor, book service.NovelBook, d service.ResearchDraft, source, head string, in service.NovelPlanningAssistInput) service.ResearchDraft {
	t.Helper()
	ctx := context.Background()
	ip := svc.Generations.IP
	in.Revision, in.Hash, in.OperationID = d.Revision, d.Hash, "research-before-withdrawal"
	pending, err := svc.Prepare(ctx, actor.ID, book.ID, d.ResourceID, in)
	if err != nil {
		t.Fatal("prepare before withdrawal", err)
	}
	sourceBefore, err := ip.ReadResource(ctx, actor, book.ProjectID, source)
	if err != nil {
		t.Fatal(err)
	}
	pinSource := func() domain.DependencyRef {
		catalog, err := ip.DependencyCatalog(ctx, actor, book.ProjectID)
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range catalog {
			if item.Ref.ID == source && item.Ref.Kind == "resource" {
				return item.Ref
			}
		}
		t.Fatal("source missing from dependency catalog")
		return domain.DependencyRef{}
	}
	beforePin := pinSource()
	policy := *sourceBefore.Policy
	policy.Withdrawn = true
	policy.WithdrawalReason = "来源结论已撤回，等待可靠的一手资料"
	if os.Getenv("STUDIO_RESEARCH_FACTS_BROWSER_NODE") != "" {
		assertResearchFactsBrowser(t, ip, book.Setup.Title, "withdraw-source")
		saved, err := ip.ReadResourceDraft(ctx, actor, book.ProjectID, source)
		if err != nil || saved.Policy == nil || !saved.Policy.Withdrawn || saved.Policy.WithdrawalReason != policy.WithdrawalReason {
			t.Fatal("withdrawal editor not persisted", err)
		}
		policy = *saved.Policy
	}
	release, err := ip.PrepareRelease(ctx, actor, book.ProjectID, domain.ReleaseInput{BranchID: book.BranchID, ExpectedIPHead: head, OperationID: "withdraw-research-source", Updates: []domain.ResourceUpdate{{ResourceID: source, ExpectedHead: sourceBefore.Head, Text: sourceBefore.Text, Policy: &policy}}})
	if err != nil {
		t.Fatal("withdrawal candidate", err)
	}
	if _, err = ip.AdoptRelease(ctx, actor, book.ProjectID, release.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = ip.PublishRelease(ctx, actor, book.ProjectID, release.ID); err != nil {
		t.Fatal(err)
	}
	withdrawnPin := pinSource()
	target, err := ip.CreateResource(ctx, actor, book.ProjectID, service.ResourceInput{BranchID: book.BranchID, Name: "撤回依赖测试参考", Kind: "note", OperationID: "withdrawal-dependency-target"})
	if err != nil {
		t.Fatal(err)
	}
	assertPinnedRejected := func(pin domain.DependencyRef, op string) {
		_, err := ip.PrepareRelease(ctx, actor, book.ProjectID, domain.ReleaseInput{BranchID: book.BranchID, ExpectedIPHead: head, OperationID: op, Updates: []domain.ResourceUpdate{{ResourceID: target.ID, Text: "不能把撤回来源作为新参考", Dependencies: &domain.DependencyPolicy{Inputs: []domain.Dependency{{Source: pin, Kind: "evidence", Reason: "固定旧版来源", Pinned: true}}}}}})
		if !errors.Is(err, domain.ErrConflict) {
			t.Fatal("pinned dependency bypassed withdrawal", err)
		}
	}
	assertPinnedRejected(beforePin, "pinned-before-withdrawal")
	if _, err = svc.Generations.Start(ctx, actor, book.ProjectID, pending.ID); !errors.Is(err, domain.ErrConflict) && !errors.Is(err, domain.ErrNotReady) {
		t.Fatal("withdrawn source allowed pending task", err)
	}
	in.OperationID = "research-after-withdrawal"
	if _, err = svc.Prepare(ctx, actor.ID, book.ID, d.ResourceID, in); !errors.Is(err, domain.ErrConflict) || !strings.Contains(err.Error(), "撤回") {
		t.Fatal("new research ignored source withdrawal", err)
	}
	if _, err = svc.Knowledge.Build(ctx, actor, book.ProjectID, book.BranchID); err != nil {
		t.Fatal(err)
	}
	pack, err := svc.Knowledge.Search(ctx, actor, book.ProjectID, book.BranchID, service.SearchInput{Purpose: "research", IncludeWorking: true, Query: "市舶 商舶 职责", Limit: 12, TokenBudget: 32768})
	if err != nil {
		t.Fatal("search after withdrawal", err)
	}
	for _, hit := range pack.Research {
		if hit.ResourceID == source {
			t.Fatal("withdrawn source remained retrievable")
		}
	}
	review, err := svc.SourceReview(ctx, actor.ID, book.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	sourceFound, notebookFound := false, false
	for _, item := range review.Items {
		sourceFound = sourceFound || (item.ResourceID == source && item.Status == "withdrawn")
		notebookFound = notebookFound || (item.ResourceID == d.ResourceID && strings.Contains(strings.Join(item.Reasons, " "), "已撤回"))
	}
	if !sourceFound || !notebookFound {
		t.Fatal("withdrawal missing from author review queue", review)
	}
	if _, err = svc.SourceReview(ctx, service.NewID(), book.ID, ""); err == nil {
		t.Fatal("unrelated actor read private review queue")
	}
	if os.Getenv("STUDIO_RESEARCH_FACTS_BROWSER_NODE") != "" {
		assertResearchFactsBrowser(t, ip, book.Setup.Title, "withdraw-review")
		d, err = svc.Read(ctx, actor.ID, book.ID, d.ResourceID)
	} else {
		d, err = svc.RefreshSources(ctx, actor.ID, book.ID, d.ResourceID, service.ResearchReleaseInput{Revision: d.Revision, Hash: d.Hash})
	}
	if err != nil || len(d.Document.SourceIDs) != 0 || len(d.Document.Sources) != 0 || len(d.Document.WithdrawnSources) != 1 || d.Document.WithdrawnSources[0].Source.Revision != sourceBefore.Head {
		t.Fatal("withdrawal refresh lost historical pin", err)
	}
	for _, card := range d.Document.Cards {
		if len(card.Sources) > 0 || card.Approved || len(card.StaleSources) == 0 {
			t.Fatal("withdrawn fact retained current evidence", card)
		}
	}
	same, err := svc.RefreshSources(ctx, actor.ID, book.ID, d.ResourceID, service.ResearchReleaseInput{Revision: d.Revision, Hash: d.Hash})
	if err != nil || same.Revision != d.Revision {
		t.Fatal("withdrawal refresh duplicated history", err)
	}
	d.Document.Question += "；撤回后继续完善研究问题"
	d.Document.WithdrawnSources = nil // Client cannot erase server-preserved withdrawal provenance.
	d, err = svc.Save(ctx, actor.ID, book.ID, d.ResourceID, d)
	if err != nil || len(d.Document.WithdrawnSources) != 1 {
		t.Fatal("post-withdrawal editing lost provenance", err)
	}
	// Restoring availability is a new explicit release. It neither approves old
	// fact cards nor makes the withdrawn historical revision usable as evidence.
	policy.Withdrawn, policy.WithdrawalReason = false, ""
	restored, err := ip.PrepareRelease(ctx, actor, book.ProjectID, domain.ReleaseInput{BranchID: book.BranchID, ExpectedIPHead: head, OperationID: "restore-research-source", Updates: []domain.ResourceUpdate{{ResourceID: source, ExpectedHead: release.ID, Text: sourceBefore.Text, Policy: &policy}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ip.AdoptRelease(ctx, actor, book.ProjectID, restored.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = ip.PublishRelease(ctx, actor, book.ProjectID, restored.ID); err != nil {
		t.Fatal(err)
	}
	assertPinnedRejected(withdrawnPin, "pinned-withdrawn-revision-after-restore")
	d.Document.SourceIDs = []string{source}
	d, err = svc.Save(ctx, actor.ID, book.ID, d.ResourceID, d)
	if err != nil || len(d.Document.Sources) != 1 || d.Document.Sources[0].Revision != restored.ID || len(d.Document.WithdrawnSources) != 1 {
		t.Fatal("explicit source restoration lost history", err)
	}
	for _, card := range d.Document.Cards {
		if card.Approved || len(card.StaleSources) == 0 {
			t.Fatal("restoration silently approved prior facts")
		}
	}
	return d
}
