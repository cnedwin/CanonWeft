package models

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"novelstudio/internal/domain"
	"strings"
	"testing"
)

type imageTransport func(*http.Request) (*http.Response, error)

func (f imageTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func sampleImage(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 3, 2))
	img.Set(1, 1, color.RGBA{10, 20, 30, 255})
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
func imageReply(t *testing.T, picture []byte) []byte {
	t.Helper()
	b, e := json.Marshal(map[string]any{"created": 1, "data": []any{map[string]string{"b64_json": base64.StdEncoding.EncodeToString(picture)}}, "usage": map[string]any{"input_tokens": 30, "input_tokens_details": map[string]int{"text_tokens": 10, "image_tokens": 20}, "output_tokens": 40, "total_tokens": 70}})
	if e != nil {
		t.Fatal(e)
	}
	return b
}
func TestImageGenerateAndEditProtocol(t *testing.T) {
	raster := sampleImage(t)
	ctx := context.Background()
	config := domain.ModelConfig{Provider: "openai", Name: "test-only", Model: "test-image-protocol", ImageCapabilities: &domain.ImageCapabilities{Input: true, Generate: true, Edit: true, RegionEdit: true}, BaseURL: "https://api.openai.com/v1", Enabled: true}
	calls, authorized := 0, 0
	adapter := New(imageTransport(func(req *http.Request) (*http.Response, error) {
		calls++
		if calls != authorized {
			t.Fatal("dispatch before durable authorization")
		}
		if req.Header.Get("Authorization") != "Bearer test-only" {
			t.Fatal("missing credential")
		}
		var payload map[string]any
		if json.NewDecoder(req.Body).Decode(&payload) != nil {
			t.Fatal("bad request")
		}
		if payload["model"] != config.Model || payload["n"] != float64(1) || payload["output_format"] != "png" || payload["moderation"] != "auto" {
			t.Fatal("unexpected protocol settings")
		}
		if calls == 1 {
			if req.URL.Path != "/v1/images/generations" {
				t.Fatal(req.URL.Path)
			}
		} else {
			if req.URL.Path != "/v1/images/edits" {
				t.Fatal(req.URL.Path)
			}
			refs := payload["images"].([]any)
			url := refs[0].(map[string]any)["image_url"].(string)
			if url != "data:image/png;base64,"+base64.StdEncoding.EncodeToString(raster) {
				t.Fatal("reference changed")
			}
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"X-Request-Id": []string{"image-test-receipt"}}, Body: io.NopCloser(bytes.NewReader(imageReply(t, raster)))}, nil
	}))
	input := ImageRequest{Prompt: "人物正侧背三视图", Size: "1536x1024", Quality: "high"}
	if _, e := adapter.GenerateImage(ctx, config, []byte("test-only"), input, nil); !errors.Is(e, ErrPaidClosed) || calls != 0 {
		t.Fatal("unapproved request", e)
	}
	authorize := func(context.Context) error { authorized++; return nil }
	result, e := adapter.GenerateImage(ctx, config, []byte("test-only"), input, authorize)
	if e != nil || !bytes.Equal(result.Data, raster) || result.Usage == nil || result.Usage.ImageInput != 20 || result.Usage.TextInput != 10 || result.Usage.ImageOutput != 40 || result.RequestID != "image-test-receipt" {
		t.Fatal("image result", e, result)
	}
	input.References = [][]byte{raster}
	if _, e = adapter.GenerateImage(ctx, config, []byte("test-only"), input, authorize); e != nil {
		t.Fatal(e)
	}
	input.References = [][]byte{[]byte("not an image")}
	if _, e = adapter.GenerateImage(ctx, config, []byte("test-only"), input, authorize); !errors.Is(e, domain.ErrValidation) || calls != 2 || authorized != 2 {
		t.Fatal("invalid reference dispatched", e)
	}
}

func TestImageUnknownAndInvalidReceiptsDoNotRetry(t *testing.T) {
	config := domain.ModelConfig{Provider: "openai", Name: "test-only", Model: "test-image-protocol", ImageCapabilities: &domain.ImageCapabilities{Input: true, Generate: true, Edit: true, RegionEdit: true}, BaseURL: "https://api.openai.com/v1", Enabled: true}
	input := ImageRequest{Prompt: "三视图", Size: "1536x1024", Quality: "high"}
	cases := []struct {
		name    string
		status  int
		body    []byte
		network bool
		want    error
		usage   bool
	}{
		{"network", 0, nil, true, ErrUnknown, false},
		{"server", 503, nil, false, ErrUnknown, false},
		{"rate limit", 429, nil, false, ErrRejected, false},
		{"broken JSON", 200, []byte("{"), false, ErrUnknown, false},
		{"remote URL is not fetched", 200, []byte(`{"data":[{"url":"https://example.invalid/secret"}]}`), false, ErrInvalidOutput, false},
		{"corrupt raster preserves receipt", 200, imageReply(t, []byte("bad png")), false, ErrInvalidOutput, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			n := 0
			a := New(imageTransport(func(*http.Request) (*http.Response, error) {
				n++
				if tc.network {
					return nil, errors.New("provider reflected key: should not escape")
				}
				return &http.Response{StatusCode: tc.status, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(tc.body))}, nil
			}))
			r, e := a.GenerateImage(context.Background(), config, []byte("test-only"), input, func(context.Context) error { return nil })
			if !errors.Is(e, tc.want) || n != 1 || (r.Usage != nil) != tc.usage {
				t.Fatal(e, n, r.Usage)
			}
		})
	}
}

func TestImageWebPReferencesAndRegion(t *testing.T) {
	data, _ := base64.StdEncoding.DecodeString("UklGRh4AAABXRUJQVlA4TBEAAAAvAkAAAAdQrYIUr/+BiOh/AAA=")
	for _, region := range []bool{false, true} {
		calls := 0
		a := New(imageTransport(func(r *http.Request) (*http.Response, error) {
			calls++
			var body struct {
				Images []struct {
					URL string `json:"image_url"`
				} `json:"images"`
				Mask any `json:"mask"`
			}
			if e := json.NewDecoder(r.Body).Decode(&body); e != nil {
				t.Fatal(e)
			}
			if len(body.Images) != 1 {
				t.Fatal("reference missing")
			}
			if !region && body.Images[0].URL != "data:image/webp;base64,"+base64.StdEncoding.EncodeToString(data) {
				t.Fatal("WebP original changed")
			}
			if region {
				const prefix = "data:image/png;base64,"
				if !strings.HasPrefix(body.Images[0].URL, prefix) || body.Mask == nil {
					t.Fatal("region source/mask not PNG")
				}
				raw, e := base64.StdEncoding.DecodeString(strings.TrimPrefix(body.Images[0].URL, prefix))
				if e != nil {
					t.Fatal(e)
				}
				img, e := png.Decode(bytes.NewReader(raw))
				if e != nil || img.Bounds() != image.Rect(0, 0, 3, 2) {
					t.Fatal("WebP region resized", e)
				}
			}
			return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(imageReply(t, sampleImage(t))))}, nil
		}))
		c := domain.ModelConfig{Provider: "openai", Name: "test", Model: "test-image-protocol", BaseURL: "https://api.openai.com/v1", Enabled: true, ImageCapabilities: &domain.ImageCapabilities{Input: true, Generate: true, Edit: true, RegionEdit: true}}
		in := ImageRequest{Prompt: "修订服装", Size: "1536x1024", Quality: "medium", References: [][]byte{data}}
		if region {
			in.Region = &ImageRegion{SourceID: "test", Width: 3, Height: 2, Preserve: "保留脸部", Areas: []ImageRectangle{{Width: 3333, Height: 5000}}}
		}
		if _, e := a.GenerateImage(context.Background(), c, []byte("local-only"), in, func(context.Context) error { return nil }); e != nil || calls != 1 {
			t.Fatal(e, calls)
		}
	}
}
