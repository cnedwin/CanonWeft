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

func assertNovelNamingBrowser(t *testing.T, ip *service.IP, title string) {
	t.Helper()
	web, err := filepath.Abs("../../../web")
	if err != nil {
		t.Fatal(err)
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
	out, err := exec.CommandContext(ctx, os.Getenv("STUDIO_NAMING_BROWSER_NODE"), filepath.Join(web, "tests", "novel-naming-browser.cjs"), origin, title).CombinedOutput()
	if err != nil {
		t.Fatalf("naming browser: %v\n%s", err, out)
	}
	t.Log(strings.TrimSpace(string(out)))
}

func assertSetupAssistanceBrowser(t *testing.T, ip *service.IP, task string) {
	t.Helper()
	web, err := filepath.Abs("../../../web")
	if err != nil {
		t.Fatal(err)
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
	out, err := exec.CommandContext(ctx, os.Getenv("STUDIO_SETUP_ASSIST_BROWSER_NODE"), filepath.Join(web, "tests", "novel-setup-assistance-browser.cjs"), origin, task).CombinedOutput()
	if err != nil {
		t.Fatalf("setup assistant browser: %v\n%s", err, out)
	}
	t.Log(strings.TrimSpace(string(out)))
}

func assertNovelImagesBrowser(t *testing.T, ip *service.IP, title, book, imageTask, visualTask string) {
	t.Helper()
	web, err := filepath.Abs("../../../web")
	if err != nil {
		t.Fatal(err)
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
	out, err := exec.CommandContext(ctx, os.Getenv("STUDIO_NOVEL_IMAGES_BROWSER_NODE"), filepath.Join(web, "tests", "novel-images-browser.cjs"), origin, title, book, imageTask, visualTask).CombinedOutput()
	if err != nil {
		t.Fatalf("novel images browser: %v\n%s", err, out)
	}
	t.Log(strings.TrimSpace(string(out)))
}

func assertNovelCoverExportBrowser(t *testing.T, ip *service.IP, title, book, cover, other, scene string) {
	t.Helper()
	web, err := filepath.Abs("../../../web")
	if err != nil {
		t.Fatal(err)
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
	out, err := exec.CommandContext(ctx, os.Getenv("STUDIO_NOVEL_COVER_BROWSER_NODE"), filepath.Join(web, "tests", "novel-cover-export-browser.cjs"), origin, title, book, cover, other, scene).CombinedOutput()
	if err != nil {
		t.Fatalf("cover export browser: %v\n%s", err, out)
	}
	t.Log(strings.TrimSpace(string(out)))
}

func assertNovelEditionBrowser(t *testing.T, ip *service.IP, title, book, edition string) {
	t.Helper()
	web, err := filepath.Abs("../../../web")
	if err != nil {
		t.Fatal(err)
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
	out, err := exec.CommandContext(ctx, os.Getenv("STUDIO_NOVEL_EDITION_BROWSER_NODE"), filepath.Join(web, "tests", "novel-edition-browser.cjs"), origin, title, book, edition).CombinedOutput()
	if err != nil {
		t.Fatalf("edition browser: %v\n%s", err, out)
	}
	t.Log(strings.TrimSpace(string(out)))
}

func assertCharacterImageBrowser(t *testing.T, ip *service.IP, visualTask, imageTask string) {
	t.Helper()
	node := os.Getenv("STUDIO_CHARACTER_IMAGE_BROWSER_NODE")
	web, err := filepath.Abs("../../../web")
	if err != nil {
		t.Fatal(err)
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
	out, err := exec.CommandContext(ctx, node, filepath.Join(web, "tests", "character-image-generation-browser.cjs"), origin, visualTask, imageTask).CombinedOutput()
	if err != nil {
		t.Fatalf("image browser: %v\n%s", err, out)
	}
	t.Log(strings.TrimSpace(string(out)))
}

func assertCharacterSourcesBrowser(t *testing.T, ip *service.IP, title, design string) {
	t.Helper()
	node := os.Getenv("STUDIO_CHARACTER_SOURCES_BROWSER_NODE")
	web, err := filepath.Abs("../../../web")
	if err != nil {
		t.Fatal(err)
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
	out, err := exec.CommandContext(ctx, node, filepath.Join(web, "tests", "novel-character-sources-browser.cjs"), origin, title, design).CombinedOutput()
	if err != nil {
		t.Fatalf("character sources browser: %v\n%s", err, out)
	}
	t.Log(strings.TrimSpace(string(out)))
}

func assertCharacterDesignBrowser(t *testing.T, ip *service.IP, title, design, task string) {
	t.Helper()
	node := os.Getenv("STUDIO_CHARACTER_BROWSER_NODE")
	if node == "" {
		return
	}
	web, err := filepath.Abs("../../../web")
	if err != nil {
		t.Fatal(err)
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
	out, err := exec.CommandContext(ctx, node, filepath.Join(web, "tests", "novel-characters-browser.cjs"), origin, title, design, task).CombinedOutput()
	if err != nil {
		t.Fatalf("character browser: %v\n%s", err, out)
	}
	t.Log(strings.TrimSpace(string(out)))
}

func assertCharacterBatchBrowser(t *testing.T, ip *service.IP, title, design, run string) {
	t.Helper()
	node := os.Getenv("STUDIO_CHARACTER_BATCH_BROWSER_NODE")
	if node == "" {
		return
	}
	web, err := filepath.Abs("../../../web")
	if err != nil {
		t.Fatal(err)
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
	out, err := exec.CommandContext(ctx, node, filepath.Join(web, "tests", "novel-character-batch-browser.cjs"), origin, title, design, run).CombinedOutput()
	if err != nil {
		t.Fatalf("character batch browser: %v\n%s", err, out)
	}
	t.Log(strings.TrimSpace(string(out)))
}
