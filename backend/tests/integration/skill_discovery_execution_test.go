package integration

import (
	"context"
	"errors"
	"novelstudio/internal/domain"
	"novelstudio/internal/service"
	"novelstudio/internal/skills"
	"novelstudio/internal/storage"
	"os"
	"strings"
	"testing"
)

type discoveryFixture struct {
	commit      string
	beforeFetch func()
	fail        bool
}

func (f *discoveryFixture) Discover(context.Context, skills.RemoteRequest, string, int) (skills.DiscoveryInventory, error) {
	return skills.DiscoveryInventory{Commit: f.commit, Paths: []string{"skills/method"}, Total: 1}, nil
}
func (f *discoveryFixture) Fetch(_ context.Context, input skills.RemoteRequest) (skills.Bundle, skills.RemoteSource, error) {
	if f.beforeFetch != nil {
		f.beforeFetch()
	}
	if f.fail {
		return skills.Bundle{}, skills.RemoteSource{}, skills.ErrFetch
	}
	bundle := skills.Bundle{Files: []skills.File{{Path: "SKILL.md", Data: []byte("---\nname: method\ndescription: Evidence method.\nlicense: MIT\n---\nUse source anchors. " + input.Ref)}, {Path: "examples/sample.json", Data: []byte(`{"source":"example"}`)}, {Path: "scripts/untrusted.py", Data: []byte("raise Exception('must never execute')")}, {Path: "LICENSE", Data: []byte("MIT fixture license")}}}
	return bundle, skills.RemoteSource{Repository: input.Repository, Commit: input.Ref, Path: input.Path, URL: "https://github.com/" + input.Repository + "/tree/" + input.Ref + "/" + input.Path}, nil
}
func TestDiscoveryCompleteReceiptsPreserveLocalEditsAndBlockLateResults(t *testing.T) {
	b, db := setup(t)
	project := createProject(b, "发现候选", "discover-execute")
	me := b.request("GET", "/api/me", nil, 200)
	actor := domain.Actor{ID: me["id"].(string)}
	ip := &service.IP{Workspace: &service.Workspace{DB: db}, Blobs: storage.LocalBlobs{Root: os.Getenv("STUDIO_BLOB_ROOT")}}
	remote := &discoveryFixture{commit: strings.Repeat("a", 40)}
	svc := &service.SkillDiscovery{IP: ip, Remote: remote}
	ctx := context.Background()
	source, err := svc.SaveSource(ctx, actor, project, "", service.DiscoverySourceInput{OperationID: "source", Repository: "fixture/repository", Ref: "main", Path: "skills", Limit: 1, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	run, err := svc.Start(ctx, actor, project, source.ID, "start")
	if err != nil {
		t.Fatal(err)
	}
	state, err := svc.Execute(ctx, run.ID)
	if err != nil || state != "COMPLETED" {
		t.Fatal(state, err)
	}
	state, err = svc.Execute(ctx, run.ID)
	if err != nil || state != "COMPLETED" {
		t.Fatal("replay", state, err)
	}
	overview, err := svc.Overview(ctx, actor, project)
	if err != nil || len(overview.Candidates) != 1 {
		t.Fatal(overview, err)
	}
	firstID := overview.Candidates[0].PackageID
	packages := &service.Skills{IP: ip}
	first, err := packages.Read(ctx, actor, project, firstID)
	if err != nil || len(first.Report.Files) != 4 || first.Report.Qualification != "STATIC_ONLY" {
		t.Fatal(first, err)
	}
	local, err := packages.Edit(ctx, actor, project, first.ID, service.SkillEdit{OperationID: "local", ExpectedHash: first.Report.Hash, Changes: []service.SkillFileChange{{Path: "notes.txt", Text: "Keep local changes"}}})
	if err != nil {
		t.Fatal(err)
	}
	same, err := svc.Start(ctx, actor, project, source.ID, "same-commit")
	if err != nil {
		t.Fatal(err)
	}
	if state, err = svc.Execute(ctx, same.ID); err != nil || state != "COMPLETED" {
		t.Fatal(state, err)
	}
	overview, _ = svc.Overview(ctx, actor, project)
	if len(overview.Candidates) != 1 {
		t.Fatal("same pinned source duplicated")
	}
	remote.commit = strings.Repeat("b", 40)
	next, err := svc.Start(ctx, actor, project, source.ID, "new-commit")
	if err != nil {
		t.Fatal(err)
	}
	if state, err = svc.Execute(ctx, next.ID); err != nil || state != "COMPLETED" {
		t.Fatal(state, err)
	}
	overview, _ = svc.Overview(ctx, actor, project)
	if len(overview.Candidates) != 2 || overview.Candidates[0].PreviousID != firstID {
		t.Fatal("source lineage missing", overview)
	}
	file, err := packages.File(ctx, actor, project, local.ID, "notes.txt")
	if err != nil || file.Text != "Keep local changes" {
		t.Fatal("local edits lost", err)
	}
	remote.commit = strings.Repeat("c", 40)
	late, err := svc.Start(ctx, actor, project, source.ID, "late")
	if err != nil {
		t.Fatal(err)
	}
	remote.beforeFetch = func() {
		if _, e := svc.Control(ctx, actor, project, late.ID, "cancel"); e != nil {
			t.Fatal(e)
		}
	}
	if state, err = svc.Execute(ctx, late.ID); err != nil || state != "CANCELLED" {
		t.Fatal("late result committed", state, err)
	}
	overview, _ = svc.Overview(ctx, actor, project)
	if len(overview.Candidates) != 2 {
		t.Fatal("cancelled candidate saved")
	}
	remote.beforeFetch = nil
	denied, err := svc.Start(ctx, actor, project, source.ID, "permission")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Control(ctx, actor, project, denied.ID, "pause"); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("UPDATE users SET role='reader' WHERE id=?", actor.ID); err != nil {
		t.Fatal(err)
	}
	if state, err = svc.Execute(ctx, denied.ID); err != nil || state != "BLOCKED" {
		t.Fatal("revoked actor executed", state, err)
	}
	if _, err = svc.Start(ctx, actor, project, source.ID, "forbidden"); !errors.Is(err, domain.ErrForbidden) {
		t.Fatal("reader started discovery", err)
	}
}

func TestDiscoveryDailyReservationAndSourceChangeFence(t *testing.T) {
	b, db := setup(t)
	project := createProject(b, "发现额度", "discovery-quota")
	me := b.request("GET", "/api/me", nil, 200)
	actor := domain.Actor{ID: me["id"].(string)}
	remote := &discoveryFixture{commit: strings.Repeat("e", 40)}
	svc := &service.SkillDiscovery{IP: &service.IP{Workspace: &service.Workspace{DB: db}, Blobs: storage.LocalBlobs{Root: os.Getenv("STUDIO_BLOB_ROOT")}}, Remote: remote}
	ctx := context.Background()
	input := service.DiscoverySourceInput{OperationID: "source", Repository: "fixture/repository", Ref: "main", Path: "skills", Limit: 5, Enabled: true}
	source, err := svc.SaveSource(ctx, actor, project, "", input)
	if err != nil {
		t.Fatal(err)
	}
	for _, op := range []string{"one", "two", "three"} {
		run, e := svc.Start(ctx, actor, project, source.ID, op)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = svc.Control(ctx, actor, project, run.ID, "cancel"); e != nil {
			t.Fatal(e)
		}
	}
	run, err := svc.Start(ctx, actor, project, source.ID, "four")
	if err != nil {
		t.Fatal(err)
	}
	remote.beforeFetch = func() {
		input.OperationID = "disable"
		input.ExpectedRevision = source.Revision
		input.Enabled = false
		if _, e := svc.SaveSource(ctx, actor, project, source.ID, input); e != nil {
			t.Fatal(e)
		}
	}
	if state, e := svc.Execute(ctx, run.ID); e != nil || state != "BLOCKED" {
		t.Fatal("source change did not fence fetched result", state, e)
	}
	overview, err := svc.Overview(ctx, actor, project)
	if err != nil || len(overview.Candidates) != 0 {
		t.Fatal("disabled source produced candidate", overview, err)
	}
	input.OperationID = "enable"
	input.ExpectedRevision = 2
	input.Enabled = true
	if _, err = svc.SaveSource(ctx, actor, project, source.ID, input); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Start(ctx, actor, project, source.ID, "five"); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("daily quota did not count cancelled and failed reservations", err)
	}
}
