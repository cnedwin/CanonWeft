package models_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"novelstudio/internal/domain"
	"novelstudio/internal/models"
	"strings"
	"testing"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

var schema = json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"],"additionalProperties":false}`)

func permit(context.Context) error { return nil }

func TestTestServerNetworkKillSwitchDeniesBeforeAuthorization(t *testing.T) {
	t.Setenv("STUDIO_PROVIDER_NETWORK", "disabled")
	authorized := false
	_, err := models.New(nil).Generate(context.Background(), config("deepseek"), []byte("not-a-real-provider-key"), request(), func(context.Context) error { authorized = true; return nil })
	if !errors.Is(err, models.ErrPaidClosed) || authorized {
		t.Fatal("test runtime attempted external authorization", err)
	}
}
func config(provider string) domain.ModelConfig {
	for _, p := range domain.ProviderCatalog() {
		if p.ID == provider {
			return domain.ModelConfig{Provider: p.ID, Name: p.Name, BaseURL: p.BaseURL, Model: p.Model, APIVersion: "2026-10-21", Enabled: true}
		}
	}
	panic("unknown fixture")
}
func request() models.Request {
	return models.Request{System: "返回 JSON", Prompt: "设计人物", Schema: schema, MaxOutputTokens: 1024}
}

func TestFiveProviderProtocolsAndSchema(t *testing.T) {
	for _, provider := range []string{"openai", "anthropic", "deepseek", "qwen", "gemini"} {
		t.Run(provider, func(t *testing.T) {
			calls := 0
			adapter := models.New(transportFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				body, _ := io.ReadAll(r.Body)
				var input map[string]any
				if err := json.Unmarshal(body, &input); err != nil {
					t.Fatal(err)
				}
				response := ""
				switch provider {
				case "openai":
					if r.URL.Path != "/v1/responses" || input["store"] != false || input["text"] == nil {
						t.Fatal("Responses protocol missing")
					}
					response = `{"id":"r1","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"{\"text\":\"人物候选\"}"}]}],"usage":{"input_tokens":10,"output_tokens":20}}`
				case "anthropic":
					if r.URL.Path != "/v1/messages" || r.Header.Get("anthropic-version") == "" || input["output_config"] == nil {
						t.Fatal("Claude protocol missing")
					}
					response = `{"id":"m1","stop_reason":"end_turn","content":[{"type":"text","text":"{\"text\":\"人物候选\"}"}],"usage":{"input_tokens":10,"output_tokens":20}}`
				case "gemini":
					if !strings.HasSuffix(r.URL.Path, ":generateContent") || r.Header.Get("x-goog-api-key") == "" || input["generationConfig"] == nil {
						t.Fatal("Gemini protocol missing")
					}
					response = `{"responseId":"g1","candidates":[{"finishReason":"STOP","content":{"parts":[{"text":"{\"text\":\"人物候选\"}"}]}}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":20}}`
				default:
					if !strings.HasSuffix(r.URL.Path, "/chat/completions") || input["response_format"] == nil {
						t.Fatal("chat protocol missing")
					}
					response = `{"id":"c1","choices":[{"finish_reason":"stop","message":{"content":"{\"text\":\"人物候选\"}"}}],"usage":{"prompt_tokens":10,"completion_tokens":20}}`
				}
				if provider != "gemini" && r.Header.Get("Authorization") != "Bearer fixture-only" {
					t.Fatal("auth missing")
				}
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(response))}, nil
			}))
			result, err := adapter.Generate(context.Background(), config(provider), []byte("fixture-only"), request(), permit)
			if err != nil || result.OutputTokens != 20 || !strings.Contains(result.Text, "人物候选") || calls != 1 {
				t.Fatalf("calls=%d result=%+v err=%v", calls, result, err)
			}
		})
	}
}

func TestAdapterDefaultClosedAndUnknownNotRetried(t *testing.T) {
	calls := 0
	adapter := models.New(transportFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return nil, errors.New("network error contains fixture-secret")
	}))
	_, err := adapter.Generate(context.Background(), config("openai"), []byte("fixture-secret"), request(), nil)
	if !errors.Is(err, models.ErrPaidClosed) || calls != 0 {
		t.Fatal("default paid gate open")
	}
	_, err = adapter.Generate(context.Background(), config("openai"), []byte("fixture-secret"), request(), permit)
	if !errors.Is(err, models.ErrUnknown) || calls != 1 || strings.Contains(err.Error(), "fixture-secret") {
		t.Fatal("retry or leaked network error", err, calls)
	}
}

func TestAdapterRejectsSchemaMismatchAndExternalReferences(t *testing.T) {
	calls := 0
	adapter := models.New(transportFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"{\"wrong\":1}"}]}],"usage":{"input_tokens":10,"output_tokens":20}}`))}, nil
	}))
	result, err := adapter.Generate(context.Background(), config("openai"), []byte("fixture"), request(), permit)
	if !errors.Is(err, models.ErrInvalidOutput) || result.OutputTokens != 20 {
		t.Fatal("schema mismatch accepted or usage lost", err)
	}
	req := request()
	req.Schema = json.RawMessage(`{"$ref":"file:///private/secret.json"}`)
	_, err = adapter.Generate(context.Background(), config("openai"), []byte("fixture"), req, permit)
	if !errors.Is(err, domain.ErrValidation) || calls != 1 {
		t.Fatal("schema external load permitted", err, calls)
	}
}
