// This executable is a browser-test fixture. It refuses every database except
// the explicitly named studio_test and never contacts a provider network.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-sql-driver/mysql"
	"github.com/labstack/echo/v5"
	"novelstudio/internal/httpapi"
	"novelstudio/internal/models"
	"novelstudio/internal/service"
	"novelstudio/internal/storage"
	"novelstudio/internal/worker"
)

type protocolTransport struct{}

func (protocolTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Host != "api.deepseek.com" {
		return nil, errors.New("fixture denies unknown provider")
	}
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	var request struct {
		Messages []struct{ Role, Content string } `json:"messages"`
	}
	if json.Unmarshal(raw, &request) != nil || len(request.Messages) == 0 {
		return nil, errors.New("fixture invalid protocol")
	}
	prompt := request.Messages[len(request.Messages)-1].Content
	var input struct {
		Source string `json:"source"`
	}
	index := strings.Index(prompt, "Input JSON:\n")
	if index >= 0 {
		err = json.NewDecoder(strings.NewReader(prompt[index+len("Input JSON:\n"):])).Decode(&input)
	} else {
		marker := "<fixed-method-data>\n"
		index = strings.Index(prompt, marker)
		if index < 0 {
			marker = "<capability-trial-data>\n"
			index = strings.Index(prompt, marker)
		}
		if index < 0 {
			return nil, errors.New("fixture only supports contract tasks")
		}
		var payload struct {
			Input struct {
				Source string `json:"source"`
			} `json:"input"`
		}
		err = json.NewDecoder(strings.NewReader(prompt[index+len(marker):])).Decode(&payload)
		input.Source = payload.Input.Source
	}
	if err != nil {
		return nil, err
	}
	rejected := strings.Contains(prompt, "fixed-method-data") && (strings.Contains(input.Source, "密钥") || strings.Contains(input.Source, "收费") || strings.Contains(input.Source, "正史"))
	summary := input.Source
	if rejected {
		summary = ""
	}
	output, _ := json.Marshal(map[string]any{"summary": summary, "rejected": rejected})
	if semantic, ok := semanticFixtureOutput(prompt); ok {
		output = semantic
	}
	body, _ := json.Marshal(map[string]any{"id": "browser-fixture-receipt", "choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]any{"content": string(output)}}}, "usage": map[string]any{"prompt_tokens": 20, "completion_tokens": 10, "prompt_cache_hit_tokens": 0}})
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(body))), Header: http.Header{}}, nil
}

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	cfg, err := mysql.ParseDSN(os.Getenv("STUDIO_DSN"))
	if err != nil || cfg.User != "studio_test" || cfg.DBName != "studio_test" {
		return errors.New("browser fixture requires explicit studio_test account and database")
	}
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		return err
	}
	defer db.Close()
	if err = storage.RunMigrations(context.Background(), db); err != nil {
		return err
	}
	ip := &service.IP{Workspace: &service.Workspace{DB: db}, Blobs: storage.LocalBlobs{Root: os.Getenv("STUDIO_BLOB_ROOT")}}
	gen := &service.Generations{IP: ip, Adapter: models.New(protocolTransport{})}
	runtime, err := worker.StartRuntime(context.Background(), &service.Tasks{IP: ip}, os.Getenv("STUDIO_TEMPORAL"), "studio-qualification-browser", worker.RuntimeServices{Generations: gen})
	if err != nil {
		return err
	}
	defer runtime.Close()
	e := httpapi.New(db, "http://127.0.0.1:18865", runtime)
	web := os.Getenv("STUDIO_WEB_DIR")
	if !filepath.IsAbs(web) {
		return errors.New("absolute browser fixture web root required")
	}
	e.StaticFS("/assets/", os.DirFS(filepath.Join(web, "assets")))
	e.GET("/*", func(c *echo.Context) error {
		if strings.HasPrefix(c.Request().URL.Path, "/api/") {
			return c.JSON(404, map[string]string{"error": "接口不存在"})
		}
		return c.FileFS("index.html", os.DirFS(web))
	})
	return http.ListenAndServe("127.0.0.1:18865", e)
}
