package models

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"novelstudio/internal/domain"
	"testing"
)

func TestImageReceiptMustReconcileAllTokenCounts(t *testing.T) {
	picture := sampleImage(t)
	config := domain.ModelConfig{Provider: "openai", Name: "test-only", Model: "test-image-protocol", ImageCapabilities: &domain.ImageCapabilities{Input: true, Generate: true, Edit: true, RegionEdit: true}, BaseURL: "https://api.openai.com/v1", Enabled: true}
	input := ImageRequest{Prompt: "三视图", Size: "1536x1024", Quality: "high"}
	for _, kind := range []string{"missing", "inconsistent-input", "negative", "partial-output", "text-output"} {
		t.Run(kind, func(t *testing.T) {
			var payload map[string]any
			_ = json.Unmarshal(imageReply(t, picture), &payload)
			usage := payload["usage"].(map[string]any)
			switch kind {
			case "missing":
				delete(payload, "usage")
			case "inconsistent-input":
				usage["input_tokens"] = 31
			case "negative":
				usage["input_tokens_details"].(map[string]any)["image_tokens"] = -20
			case "partial-output":
				usage["output_tokens_details"] = map[string]any{"image_tokens": 40}
			case "text-output":
				usage["output_tokens_details"] = map[string]any{"image_tokens": 38, "text_tokens": 2}
			}
			raw, _ := json.Marshal(payload)
			calls := 0
			a := New(imageTransport(func(*http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(raw))}, nil
			}))
			out, err := a.GenerateImage(context.Background(), config, []byte("test-only"), input, func(context.Context) error { return nil })
			if calls != 1 || !bytes.Equal(out.Data, picture) {
				t.Fatal("receipt lost returned image", calls, err)
			}
			if kind == "text-output" {
				if err != nil || out.Usage == nil || out.Usage.TextOutput != 2 || out.Usage.ImageOutput != 38 {
					t.Fatal(out.Usage, err)
				}
			} else if !errors.Is(err, ErrUnknown) || out.Usage != nil {
				t.Fatal("unknown fees accepted", err)
			}
		})
	}
}

func TestImageValidationAndCancellationPrecedeAuthorization(t *testing.T) {
	calls, authorized := 0, 0
	adapter := New(imageTransport(func(*http.Request) (*http.Response, error) { calls++; return nil, errors.New("must not dispatch") }))
	c := domain.ModelConfig{Provider: "openai", Name: "test-only", Model: "test-image-protocol", ImageCapabilities: &domain.ImageCapabilities{Input: true, Generate: true, Edit: true, RegionEdit: true}, BaseURL: "https://api.openai.com/v1", Enabled: true}
	input := ImageRequest{Prompt: "三视图", Size: "1536x1024", Quality: "high"}
	authorize := func(context.Context) error { authorized++; return nil }
	for _, size := range []string{"auto", "1536x1024junk", "1x1", "3840x3840", "1537x1024"} {
		bad := input
		bad.Size = size
		if _, err := adapter.GenerateImage(context.Background(), c, []byte("test-only"), bad, authorize); !errors.Is(err, domain.ErrValidation) {
			t.Fatal(size, err)
		}
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := adapter.GenerateImage(cancelled, c, []byte("test-only"), input, authorize); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := adapter.GenerateImage(context.Background(), c, []byte("test-only"), input, func(context.Context) error { return ErrPaidClosed }); !errors.Is(err, ErrPaidClosed) {
		t.Fatal(err)
	}
	if calls != 0 || authorized != 0 {
		t.Fatal("invalid/cancelled/denied input dispatched", calls, authorized)
	}
}

func TestImageRedirectCannotForwardCredentials(t *testing.T) {
	n := 0
	a := New(imageTransport(func(*http.Request) (*http.Response, error) {
		n++
		return &http.Response{StatusCode: 307, Header: http.Header{"Location": []string{"https://example.invalid/capture"}}, Body: io.NopCloser(bytes.NewReader(nil))}, nil
	}))
	c := domain.ModelConfig{Provider: "openai", Name: "test-only", Model: "test-image-protocol", ImageCapabilities: &domain.ImageCapabilities{Input: true, Generate: true, Edit: true, RegionEdit: true}, BaseURL: "https://api.openai.com/v1", Enabled: true}
	_, err := a.GenerateImage(context.Background(), c, []byte("test-only"), ImageRequest{Prompt: "三视图", Size: "1536x1024", Quality: "high"}, func(context.Context) error { return nil })
	if !errors.Is(err, ErrRejected) || n != 1 {
		t.Fatal("redirect followed", err, n)
	}
}
