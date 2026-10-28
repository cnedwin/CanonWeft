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

func assertBookReviewBrowser(t *testing.T, ip *service.IP, title, runID string) {
	t.Helper()
	node := os.Getenv("STUDIO_REVIEW_BROWSER_NODE")
	if node == "" {
		return
	}
	web, e := filepath.Abs("../../../web")
	if e != nil {
		t.Fatal(e)
	}
	server := httptest.NewUnstartedServer(nil)
	origin := "http://" + server.Listener.Addr().String()
	app := httpapi.New(ip.Workspace.DB, origin)
	app.Static("/", filepath.Join(web, "dist"))
	server.Config.Handler = app
	server.Start()
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	out, e := exec.CommandContext(ctx, node, filepath.Join(web, "tests", "novel-book-review-browser.cjs"), origin, title, runID).CombinedOutput()
	if e != nil {
		t.Fatalf("review browser: %v\n%s", e, out)
	}
	t.Log(strings.TrimSpace(string(out)))
}
