package models

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"novelstudio/internal/domain"
	"strings"
	"testing"
)

func TestImageRegionProtocolPinsMaskToFirstReferenceBeforeAuthorization(t *testing.T) {
	var source bytes.Buffer
	jpeg.Encode(&source, image.NewRGBA(image.Rect(0, 0, 3, 2)), nil)
	calls := 0
	a := New(imageTransport(func(req *http.Request) (*http.Response, error) {
		calls++
		var body struct {
			Images []struct {
				URL string `json:"image_url"`
			} `json:"images"`
			Mask struct {
				URL string `json:"image_url"`
			} `json:"mask"`
		}
		if json.NewDecoder(req.Body).Decode(&body) != nil || req.URL.Path != "/v1/images/edits" || len(body.Images) != 1 {
			t.Fatal("invalid edit request")
		}
		decode := func(url string) image.Image {
			t.Helper()
			if !strings.HasPrefix(url, "data:image/png;base64,") {
				t.Fatal("source/mask format mismatch")
			}
			b, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(url, "data:image/png;base64,"))
			if err != nil {
				t.Fatal(err)
			}
			i, err := png.Decode(bytes.NewReader(b))
			if err != nil {
				t.Fatal(err)
			}
			return i
		}
		original, mask := decode(body.Images[0].URL), decode(body.Mask.URL)
		if original.Bounds() != mask.Bounds() {
			t.Fatal("mask dimensions changed")
		}
		_, _, _, editable := mask.At(0, 0).RGBA()
		_, _, _, retained := mask.At(2, 1).RGBA()
		if editable != 0 || retained != 65535 {
			t.Fatal("mask direction inverted")
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(imageReply(t, sampleImage(t))))}, nil
	}))
	c := domain.ModelConfig{Provider: "openai", Name: "test", Model: "test-image-protocol", ImageCapabilities: &domain.ImageCapabilities{Input: true, Generate: true, Edit: true, RegionEdit: true}, BaseURL: "https://api.openai.com/v1", Enabled: true}
	in := ImageRequest{Prompt: "更换选区衣服", Size: "1536x1024", Quality: "medium", References: [][]byte{source.Bytes()}, Region: &ImageRegion{SourceID: "source", Width: 3, Height: 2, Preserve: "原脸型", Areas: []ImageRectangle{{Width: 3333, Height: 5000}}}}
	authorized := 0
	authorize := func(context.Context) error { authorized++; return nil }
	if _, err := a.GenerateImage(context.Background(), c, []byte("test"), in, authorize); err != nil {
		t.Fatal(err)
	}
	c.ImageCapabilities.RegionEdit = false
	if _, err := a.GenerateImage(context.Background(), c, []byte("test"), in, authorize); err == nil || authorized != 1 {
		t.Fatal("unsupported region edit dispatched", err)
	}
	c.ImageCapabilities.RegionEdit = true
	in.Region.Width = 4
	if _, err := a.GenerateImage(context.Background(), c, []byte("test"), in, authorize); err == nil || authorized != 1 || calls != 1 {
		t.Fatal("dimension mismatch sent to provider", err)
	}
	in.References = nil
	if _, err := a.GenerateImage(context.Background(), c, []byte("test"), in, authorize); err == nil || authorized != 1 {
		t.Fatal("mask without source dispatched", err)
	}
}

func TestImageRegionMaskUsesOriginalDimensionsAndTransparentSelection(t *testing.T) {
	r := ImageRegion{SourceID: "source", Width: 100, Height: 50, Preserve: "保留脸部", Areas: []ImageRectangle{{X: 2500, Y: 2000, Width: 5000, Height: 4000}}}
	data, err := r.MaskPNG()
	if err != nil {
		t.Fatal(err)
	}
	mask, err := png.Decode(bytes.NewReader(data))
	if err != nil || mask.Bounds() != image.Rect(0, 0, 100, 50) {
		t.Fatal("wrong mask dimensions", err)
	}
	for _, p := range []struct {
		x, y  int
		alpha uint32
	}{{0, 0, 65535}, {25, 10, 0}, {74, 29, 0}, {75, 30, 65535}} {
		_, _, _, a := mask.At(p.x, p.y).RGBA()
		if a != p.alpha {
			t.Fatalf("(%d,%d) alpha %d", p.x, p.y, a)
		}
	}
}

func TestImageRegionRejectsUnboundedOrEmptySelection(t *testing.T) {
	valid := ImageRegion{SourceID: "source", Width: 100, Height: 50, Preserve: "保留脸部", Areas: []ImageRectangle{{Width: 1000, Height: 1000}}}
	cases := []ImageRegion{valid, valid, valid, valid, valid, valid, valid}
	cases[0].Width = 0
	cases[1].Areas = nil
	cases[2].Areas = []ImageRectangle{{X: 9999, Width: 2, Height: 2}}
	cases[3].Areas = []ImageRectangle{{X: -1, Width: 2, Height: 2}}
	cases[4].Areas = []ImageRectangle{{Width: 0, Height: 2}}
	cases[5].Preserve = ""
	cases[6].Width = 1000000
	for i, r := range cases {
		if _, err := r.MaskPNG(); err == nil {
			t.Fatalf("invalid region %d accepted", i)
		}
	}
}
