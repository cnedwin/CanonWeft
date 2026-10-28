package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
	"novelstudio/internal/domain"
	"novelstudio/internal/novel"
)

// Nil services prove these routes cannot read/write a database or call a model.
func TestNovelPresetRoutesAreCompleteAndHaveNoServiceSideEffects(t *testing.T) {
	e := echo.New()
	registerNovelEntry(e.Group("/api"), nil, func(*echo.Context) domain.Actor {
		t.Fatal("static preview requested actor service")
		return domain.Actor{}
	})
	request := httptest.NewRequest(http.MethodGet, "/api/novel-setups/presets", nil)
	w := httptest.NewRecorder()
	e.ServeHTTP(w, request)
	var catalog struct {
		Items []novel.SetupPreset `json:"items"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &catalog) != nil || len(catalog.Items) != 8 {
		t.Fatalf("catalog status=%d: %s", w.Code, w.Body)
	}
	for _, item := range catalog.Items {
		w = httptest.NewRecorder()
		request = httptest.NewRequest(http.MethodPost, "/api/novel-setups/preset-preview", strings.NewReader(`{"preset_id":"`+item.ID+`","idea":"作者自己的想法"}`))
		request.Header.Set("Content-Type", "application/json")
		e.ServeHTTP(w, request)
		var p novel.SetupPresetPreview
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &p) != nil || p.Setup.Validate(true) != nil || p.Setup.Idea != "作者自己的想法" {
			t.Fatalf("preview status=%d: %s", w.Code, w.Body)
		}
	}
	for _, body := range []string{`{}`, `{"preset_id":"unknown","idea":""}`, `{"preset_id":"urban-career","idea":"x","confirm":true}`, `{"preset_id":"urban-career","idea":"x"} {}`, `{"preset_id":"urban-career","idea":"` + strings.Repeat("字", 16001) + `"}`, "{\"preset_id\":\"urban-career\",\"idea\":\"" + string([]byte{0xff}) + "\"}"} {
		w = httptest.NewRecorder()
		request = httptest.NewRequest(http.MethodPost, "/api/novel-setups/preset-preview", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		e.ServeHTTP(w, request)
		if w.Code != 400 {
			t.Fatalf("invalid preview accepted: status=%d body=%s", w.Code, w.Body)
		}
	}
}
