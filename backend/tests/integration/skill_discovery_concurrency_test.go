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
	"sync/atomic"
	"testing"
	"time"
)

type discoveryConcurrentRemote struct {
	firstCalls    atomic.Int32
	oldStarted    chan struct{}
	releaseOld    chan struct{}
	secondStarted chan struct{}
	releaseSecond chan struct{}
}

func (f *discoveryConcurrentRemote) Discover(context.Context, skills.RemoteRequest, string, int) (skills.DiscoveryInventory, error) {
	return skills.DiscoveryInventory{Commit: strings.Repeat("a", 40), Paths: []string{"skills/first", "skills/second"}, Total: 2}, nil
}
func (f *discoveryConcurrentRemote) Fetch(ctx context.Context, input skills.RemoteRequest) (skills.Bundle, skills.RemoteSource, error) {
	if input.Path == "skills/first" && f.firstCalls.Add(1) == 1 {
		close(f.oldStarted)
		select {
		case <-f.releaseOld:
		case <-ctx.Done():
			return skills.Bundle{}, skills.RemoteSource{}, ctx.Err()
		}
	}
	if input.Path == "skills/second" {
		close(f.secondStarted)
		select {
		case <-f.releaseSecond:
		case <-ctx.Done():
			return skills.Bundle{}, skills.RemoteSource{}, ctx.Err()
		}
	}
	return (&discoveryFixture{}).Fetch(ctx, input)
}
func TestDiscoverySupersededActivityDoesNotBlockReplacement(t *testing.T) {
	b, db := setup(t)
	project := createProject(b, "重叠恢复", "discovery-overlap")
	me := b.request("GET", "/api/me", nil, 200)
	actor := domain.Actor{ID: me["id"].(string)}
	remote := &discoveryConcurrentRemote{oldStarted: make(chan struct{}), releaseOld: make(chan struct{}), secondStarted: make(chan struct{}), releaseSecond: make(chan struct{})}
	svc := &service.SkillDiscovery{IP: &service.IP{Workspace: &service.Workspace{DB: db}, Blobs: storage.LocalBlobs{Root: os.Getenv("STUDIO_BLOB_ROOT")}}, Remote: remote}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	source, err := svc.SaveSource(ctx, actor, project, "", service.DiscoverySourceInput{OperationID: "source", Repository: "fixture/repository", Ref: "main", Path: "skills", Limit: 2, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	run, err := svc.Start(ctx, actor, project, source.ID, "run")
	if err != nil {
		t.Fatal(err)
	}
	type outcome struct {
		state string
		err   error
	}
	oldResult := make(chan outcome, 1)
	replacementResult := make(chan outcome, 1)
	go func() { state, e := svc.Execute(ctx, run.ID); oldResult <- outcome{state, e} }()
	select {
	case <-remote.oldStarted:
	case <-ctx.Done():
		t.Fatal("old activity never reached fetch")
	}
	go func() { state, e := svc.Execute(ctx, run.ID); replacementResult <- outcome{state, e} }()
	select {
	case <-remote.secondStarted:
	case <-ctx.Done():
		t.Fatal("replacement never persisted first candidate")
	}
	close(remote.releaseOld)
	old := <-oldResult
	current, err := svc.ReadRun(ctx, actor, project, run.ID)
	close(remote.releaseSecond)
	replacement := <-replacementResult
	if err != nil || current.State != "RUNNING" || current.Done != 1 {
		t.Fatalf("superseded result terminated shared task: %#v %v (old=%#v)", current, err, old)
	}
	if old.err == nil || errors.Is(old.err, domain.ErrConflict) {
		t.Fatal("superseded activity needs a distinct continuation error", old)
	}
	if replacement.err != nil || replacement.state != "COMPLETED" {
		t.Fatal("replacement did not finish", replacement)
	}
	overview, err := svc.Overview(ctx, actor, project)
	if err != nil || len(overview.Candidates) != 2 {
		t.Fatal("missing or duplicate candidates", overview, err)
	}
}

type discoveryInventoryRaceRemote struct {
	inventories             atomic.Int32
	oldInventoryStarted     chan struct{}
	releaseOldInventory     chan struct{}
	replacementFetchStarted chan struct{}
	releaseReplacementFetch chan struct{}
}

func (f *discoveryInventoryRaceRemote) Discover(ctx context.Context, _ skills.RemoteRequest, _ string, _ int) (skills.DiscoveryInventory, error) {
	commit := strings.Repeat("a", 40)
	if f.inventories.Add(1) == 1 {
		close(f.oldInventoryStarted)
		select {
		case <-f.releaseOldInventory:
		case <-ctx.Done():
			return skills.DiscoveryInventory{}, ctx.Err()
		}
		commit = strings.Repeat("b", 40)
	}
	return skills.DiscoveryInventory{Commit: commit, Paths: []string{"skills/method"}, Total: 1}, nil
}
func (f *discoveryInventoryRaceRemote) Fetch(ctx context.Context, input skills.RemoteRequest) (skills.Bundle, skills.RemoteSource, error) {
	if input.Ref == strings.Repeat("a", 40) {
		close(f.replacementFetchStarted)
		select {
		case <-f.releaseReplacementFetch:
		case <-ctx.Done():
			return skills.Bundle{}, skills.RemoteSource{}, ctx.Err()
		}
	}
	return (&discoveryFixture{}).Fetch(ctx, input)
}
func TestDiscoverySupersededInventoryCannotRepinCommit(t *testing.T) {
	b, db := setup(t)
	project := createProject(b, "固定发现提交", "discovery-inventory")
	me := b.request("GET", "/api/me", nil, 200)
	actor := domain.Actor{ID: me["id"].(string)}
	remote := &discoveryInventoryRaceRemote{oldInventoryStarted: make(chan struct{}), releaseOldInventory: make(chan struct{}), replacementFetchStarted: make(chan struct{}), releaseReplacementFetch: make(chan struct{})}
	svc := &service.SkillDiscovery{IP: &service.IP{Workspace: &service.Workspace{DB: db}, Blobs: storage.LocalBlobs{Root: os.Getenv("STUDIO_BLOB_ROOT")}}, Remote: remote}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	source, err := svc.SaveSource(ctx, actor, project, "", service.DiscoverySourceInput{OperationID: "source", Repository: "fixture/repository", Ref: "main", Path: "skills", Limit: 1, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	run, err := svc.Start(ctx, actor, project, source.ID, "run")
	if err != nil {
		t.Fatal(err)
	}
	oldResult := make(chan error, 1)
	replacementResult := make(chan error, 1)
	go func() { _, e := svc.Execute(ctx, run.ID); oldResult <- e }()
	select {
	case <-remote.oldInventoryStarted:
	case <-ctx.Done():
		t.Fatal("old inventory not started")
	}
	go func() { _, e := svc.Execute(ctx, run.ID); replacementResult <- e }()
	select {
	case <-remote.replacementFetchStarted:
	case <-ctx.Done():
		t.Fatal("replacement did not pin inventory")
	}
	close(remote.releaseOldInventory)
	oldErr := <-oldResult
	current, err := svc.ReadRun(ctx, actor, project, run.ID)
	close(remote.releaseReplacementFetch)
	replacementErr := <-replacementResult
	if err != nil || current.Inventory == nil || current.Inventory.Commit != strings.Repeat("a", 40) || current.State != "RUNNING" {
		t.Fatalf("old inventory replaced fixed commit: %#v %v", current, err)
	}
	if !errors.Is(oldErr, service.ErrDiscoverySuperseded) || replacementErr != nil {
		t.Fatal("inventory race did not continue safely", oldErr, replacementErr)
	}
}
