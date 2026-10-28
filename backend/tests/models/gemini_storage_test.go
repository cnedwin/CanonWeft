package models_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"novelstudio/internal/models"
)

func TestGeminiRequestsDisableProviderStorage(t *testing.T) {
	for _, mode := range []string{"generate", "stream"} {
		for _, format := range []string{"text", "structured"} {
			t.Run(mode+"/"+format, func(t *testing.T) {
				calls, authorized := 0, 0
				adapter := models.New(transportFunc(func(r *http.Request) (*http.Response, error) {
					calls++
					var body map[string]json.RawMessage
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Fatal(err)
					}
					// GenerateContentRequest.store overrides project logging; placing
					// this only in generationConfig would not express that policy.
					if string(body["store"]) != "false" {
						t.Errorf("request must explicitly disable provider storage, got %s", body["store"])
					}
					var generation map[string]json.RawMessage
					if err := json.Unmarshal(body["generationConfig"], &generation); err != nil {
						t.Fatal(err)
					}
					if _, exists := generation["store"]; exists {
						t.Error("storage policy is not a generation setting")
					}
					return nil, errors.New("local transport probe; no external request")
				}))
				input := request()
				if format == "text" {
					input.Schema = nil
				}
				authorize := func(context.Context) error { authorized++; return nil }
				var err error
				if mode == "stream" {
					_, err = adapter.GenerateStream(context.Background(), config("gemini"), []byte("fixture-only"), input, authorize, nil)
				} else {
					_, err = adapter.Generate(context.Background(), config("gemini"), []byte("fixture-only"), input, authorize)
				}
				if !errors.Is(err, models.ErrUnknown) || calls != 1 || authorized != 1 {
					t.Fatalf("probe must dispatch once after authorization: calls=%d authorized=%d err=%v", calls, authorized, err)
				}
			})
		}
	}
}
