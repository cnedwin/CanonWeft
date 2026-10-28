package integration

import (
	"context"
	"net/http/httptest"
	"novelstudio/internal/httpapi"
	"novelstudio/internal/service"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNovelPlanningActualBrowserWorkflow(t *testing.T) {
	node := os.Getenv("STUDIO_NOVEL_BROWSER_NODE")
	if node == "" {
		t.Skip("explicit installed browser runtime required")
	}
	_, db, entry, actor := novelEntryFixture(t)
	setup := validNovelSetup()
	setup.Title = "规划验收小说"
	setup.VolumeCount = 1
	setup.TargetWords = 2000
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	draft, err := entry.SaveDraft(ctx, actor.ID, service.NovelSetupDraft{ID: service.NewID(), Step: 5, Setup: setup})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = entry.Confirm(ctx, actor.ID, draft.ID, service.NovelConfirmation{Revision: draft.Revision, OperationID: "planning-browser-book", AcceptSettings: true}); err != nil {
		t.Fatal(err)
	}
	web, err := filepath.Abs("../../../web")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(nil)
	origin := "http://" + server.Listener.Addr().String()
	app := httpapi.New(db, origin)
	app.Static("/", filepath.Join(web, "dist"))
	server.Config.Handler = app
	server.Start()
	defer server.Close()
	cmd := exec.CommandContext(ctx, node, filepath.Join(web, "tests", "novel-planning-browser.cjs"), origin)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("novel planning browser: %v\n%s", err, out)
	}
	t.Log(strings.TrimSpace(string(out)))
	var count int
	if err = db.QueryRow("SELECT COUNT(*) FROM resources WHERE kind LIKE 'novel_plan_%' AND head_revision<>''").Scan(&count); err != nil || count != 4 {
		t.Fatal("missing adopted planning documents", count, err)
	}
	if err = db.QueryRow("SELECT COUNT(*) FROM model_calls").Scan(&count); err != nil || count != 0 {
		t.Fatal("planning made model calls", err)
	}
}
