package integration

import (
	"context"
	"encoding/json"
	"errors"
	"novelstudio/internal/domain"
	"novelstudio/internal/novel"
	"novelstudio/internal/service"
	"os"
	"strings"
	"testing"
)

func assertNovelNaming(t *testing.T, b *browser, svc *service.NovelCharacters, transport *generationTransport, actor domain.Actor, book service.NovelBook, price string) string {
	t.Helper()
	ctx := context.Background()
	root := "/api/novels/" + book.ID + "/character-designs"
	pool := b.request("GET", root+"/name-materials?language=zh", nil, 200)
	if len(pool["records"].([]any)) != 100 {
		t.Fatal("missing seeded surname catalog")
	}
	d, err := svc.Open(ctx, actor.ID, book.ID, service.CharacterDesignCreate{OperationID: "name-workbench", Notes: "起名验收：朴素的边城守军", Genre: "武侠", Count: 2})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := svc.OpenNameCatalog(ctx, actor.ID, book.ID)
	if err != nil || catalog.Revision != 1 || len(catalog.Document.Records) != 500 {
		t.Fatal("initialize real name catalog", err)
	}
	preview, err := svc.PreviewNameImport(ctx, actor.ID, book.ID, `{"license":"author-original","records":[{"entity_id":"author-border-name","label":"边城守备","label_language":"zh","kind":"title","culture":"架空边城","era":"架空","evidence":"只作起名素材，不是历史事实"}]}`)
	if err != nil || len(preview.Document.Records) != 501 {
		t.Fatal("import preview", err)
	}
	unsaved, err := svc.NameCatalog(ctx, actor.ID, book.ID)
	if err != nil || len(unsaved.Document.Records) != 500 {
		t.Fatal("preview changed library", err)
	}
	catalog, err = svc.SaveNameCatalog(ctx, actor.ID, book.ID, preview)
	if err != nil {
		t.Fatal("save import", err)
	}
	if _, err = svc.SaveNameCatalog(ctx, actor.ID, book.ID, unsaved); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("catalog stale write", err)
	}
	matched, err := svc.NameMaterials(ctx, actor.ID, book.ID, "zh", "边城守备")
	if err != nil || len(matched.Records) != 1 || matched.Records[0].License != "author-original" {
		t.Fatal("query imported name", err)
	}
	kept := novel.NameCandidate{ID: "author-kept", Name: "杨守砚", Pronunciation: "Yang Shouyan", Origin: "invented", Confidence: "unverified", Locked: true, Tags: []string{"守军"}, Sources: []novel.NameSource{}, Conflicts: []string{}}
	d.Document.Naming = &novel.NamingBoard{Count: 3, Language: "zh", Culture: "边城", Era: "架空", Constraints: []string{"不要生僻字"}, NameProposal: novel.NameProposal{Candidates: []novel.NameCandidate{kept}, Gaps: []string{}, Checks: []string{}}}
	d.Document.Naming.Catalog = &novel.NameCatalogPin{ResourceID: catalog.ResourceID, Revision: catalog.Revision, Hash: catalog.Hash}
	d, err = svc.Save(ctx, actor.ID, book.ID, d.ResourceID, d)
	if err != nil {
		t.Fatal("save naming settings", err)
	}
	candidate := func(name string) novel.NameCandidate {
		return novel.NameCandidate{Name: name, Pronunciation: "待作者复核", Origin: "invented", Confidence: "unverified", Rationale: "便于同场对话区分的朴素名字", Tags: []string{"守军", "朴素"}, Sources: []novel.NameSource{}, Conflicts: []string{}}
	}
	result := novel.NameProposal{Candidates: []novel.NameCandidate{candidate("顾闻钟"), candidate("燕知白")}, Gaps: []string{}, Checks: []string{"本批读音待作者复核"}}
	body, _ := json.Marshal(result)
	response, _ := json.Marshal(map[string]any{"id": "names-local-only", "choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]string{"content": string(body)}}}, "usage": map[string]int{"prompt_tokens": 200, "completion_tokens": 100}})
	transport.response = string(response)
	in := service.NovelPlanningAssistInput{Revision: d.Revision, Hash: d.Hash, Notes: "保留杨守砚，其余名字自然朴素", PriceID: price, MaxOutputTokens: 4096, OperationID: "names-call"}
	task, err := svc.PrepareNames(ctx, actor.ID, book.ID, d.ResourceID, in)
	if err != nil {
		t.Fatal("prepare names", err)
	}
	replay, err := svc.PrepareNames(ctx, actor.ID, book.ID, d.ResourceID, in)
	if err != nil || replay.ID != task.ID {
		t.Fatal("naming replay", err)
	}
	budgets := &service.Budgets{Workspace: svc.Generations.IP.Workspace}
	budget, err := budgets.Read(ctx, actor.ID)
	if err != nil {
		t.Fatal(err)
	}
	budget.Enabled = true
	if _, err = budgets.Configure(ctx, actor.ID, budget); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Generations.Start(ctx, actor, book.ProjectID, task.ID); err != nil {
		t.Fatal("start names", err)
	}
	if state, e := svc.Generations.Execute(ctx, task.ID); e != nil || state != "SUCCEEDED" {
		t.Fatal("execute names", state, e)
	}
	for _, part := range []string{"角色起名与名字素材整理", "杨守砚", "source_id", "不要生僻字", "边城守备", "只作起名素材"} {
		if !strings.Contains(transport.payload, part) {
			t.Fatal("missing naming method/context", part)
		}
	}
	proposal, err := svc.NamingSuggestion(ctx, actor.ID, book.ID, d.ResourceID, task.ID)
	if err != nil {
		t.Fatal("naming suggestion", err)
	}
	if len(proposal.Draft.Document.Naming.Candidates) != 3 || proposal.Draft.Document.Naming.Candidates[0].Name != kept.Name || !proposal.Draft.Document.Naming.Candidates[0].Locked {
		t.Fatal("locked name overwritten")
	}
	d, err = svc.Save(ctx, actor.ID, book.ID, d.ResourceID, proposal.Draft)
	if err != nil {
		t.Fatal("save names", err)
	}
	in.Revision = d.Revision
	in.Hash = d.Hash
	in.OperationID = "stale-names"
	stale, err := svc.PrepareNames(ctx, actor.ID, book.ID, d.ResourceID, in)
	if err != nil {
		t.Fatal(err)
	}
	catalog.Document.Records[len(catalog.Document.Records)-1].Label = "边城军职"
	catalog, err = svc.SaveNameCatalog(ctx, actor.ID, book.ID, catalog)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Generations.Start(ctx, actor, book.ProjectID, stale.ID); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("changed catalog allowed stale request", err)
	}
	d.Document.Naming.Catalog = &novel.NameCatalogPin{ResourceID: catalog.ResourceID, Revision: catalog.Revision, Hash: catalog.Hash}
	d.Document.Naming.Culture = "作者修改后的西部边城"
	d, err = svc.Save(ctx, actor.ID, book.ID, d.ResourceID, d)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Generations.Start(ctx, actor, book.ProjectID, stale.ID); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("stale naming context started", err)
	}
	budget, err = budgets.Read(ctx, actor.ID)
	if err != nil {
		t.Fatal(err)
	}
	budget.Enabled = false
	if _, err = budgets.Configure(ctx, actor.ID, budget); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("STUDIO_NAMING_BROWSER_NODE") != "" {
		assertNovelNamingBrowser(t, svc.Generations.IP, book.Setup.Title)
	}
	d, err = svc.Read(ctx, actor.ID, book.ID, d.ResourceID)
	if err != nil {
		t.Fatal(err)
	}
	d.Document.Naming.Candidates[1].Favorite = true
	for i, e := range d.Document.Candidate.Entities {
		if e.ID == d.Document.IDs[0] {
			d.Document.Candidate.Entities[i].Name = "顾闻钟"
		}
	}
	d, err = svc.Save(ctx, actor.ID, book.ID, d.ResourceID, d)
	if err != nil {
		t.Fatal(err)
	}
	adopted, err := svc.Apply(ctx, actor.ID, book.ID, d.ResourceID, service.CharacterDesignApply{Revision: d.Revision, Hash: d.Hash, IPRevision: d.IPRevision, Selected: d.Document.IDs[:1], OperationID: "apply-chosen-name"})
	if err != nil {
		t.Fatal("apply name", err)
	}
	current, err := svc.Generations.IP.ReadDraft(ctx, actor, book.ProjectID, book.BranchID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range current.Snapshot.Entities {
		if e.ID == d.Document.IDs[0] && e.Name == "顾闻钟" {
			found = true
		}
	}
	if !found || adopted.Draft.Document.Naming.GenerationID != task.ID {
		t.Fatal("name identity/provenance lost")
	}
	t.Log("Naming: actual local model transport with method, sources and existing names; locked candidate preserved, stale request rejected, editable candidates adopted only into private character draft")
	return d.ResourceID
}
