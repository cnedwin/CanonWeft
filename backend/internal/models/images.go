package models

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
	"io"
	"net/http"
	"novelstudio/internal/domain"
	"novelstudio/internal/retrieval"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

// ImageRequest is an execution-only request. References must be resolved by the
// caller from authorized, version-pinned assets, never arbitrary remote URLs.
// Persist IDs and hashes in the task; do not put image bytes in workflow history.
type ImageRequest struct {
	Region     *ImageRegion
	Prompt     string
	Size       string
	Quality    string
	References [][]byte
}
type ImageUsage struct {
	TextInput   int64 `json:"text_input_tokens"`
	ImageInput  int64 `json:"image_input_tokens"`
	TextOutput  int64 `json:"text_output_tokens"`
	ImageOutput int64 `json:"image_output_tokens"`
}
type ImageResult struct {
	Data          []byte      `json:"-"`
	RequestID     string      `json:"request_id"`
	MIME          string      `json:"mime"`
	Width         int         `json:"width"`
	Height        int         `json:"height"`
	RevisedPrompt string      `json:"revised_prompt,omitempty"`
	Usage         *ImageUsage `json:"usage,omitempty"`
}

const maxImageResponseBytes = 24 * 1024 * 1024

func validImageSize(size string) bool {
	parts := strings.Split(size, "x")
	if len(parts) != 2 {
		return false
	}
	w, e := strconv.Atoi(parts[0])
	h, f := strconv.Atoi(parts[1])
	return e == nil && f == nil && strconv.Itoa(w)+"x"+strconv.Itoa(h) == size && w > 0 && h > 0 && w <= 3840 && h <= 3840 && w%16 == 0 && h%16 == 0 && w <= h*3 && h <= w*3 && w*h >= 655360 && w*h <= 8388608
}
func inspectImage(data []byte) (retrieval.CharacterImage, error) {
	report, err := retrieval.InspectCharacterImage(retrieval.CharacterImage{CharacterID: "protocol-reference", Look: "protocol-validation", View: "three_view", Source: "provider transport", Rights: "not evaluated by transport"}, data)
	if err != nil {
		return retrieval.CharacterImage{}, domain.ErrValidation
	}
	return *report.CharacterImage, nil
}

// GenerateImage implements the public OpenAI Images protocol, not a claim that
// an arbitrary configured model exists. The application must verify its chosen
// model and parameters before enabling paid execution. It never substitutes a model.
// authorize has the same durable dispatch contract as Generate; it is called
// only after all local validation, and an uncertain response is never retried.
func (a *Adapter) GenerateImage(ctx context.Context, c domain.ModelConfig, key []byte, in ImageRequest, authorize func(context.Context) error) (ImageResult, error) {
	var out ImageResult
	if authorize == nil || a.networkDenied {
		return out, ErrPaidClosed
	}
	if !domain.SupportsImageOperation(c, len(in.References) > 0, in.Region != nil) {
		return out, domain.ErrValidation
	}
	if domain.ValidateModelConfig(c) != nil || c.Provider != "openai" || !c.Enabled || len(key) == 0 || len(key) > 8192 || bytes.ContainsAny(key, "\r\n\x00") || !utf8.ValidString(in.Prompt) || strings.TrimSpace(in.Prompt) == "" || utf8.RuneCountInString(in.Prompt) > 32000 || !validImageSize(in.Size) || !slices.Contains([]string{"low", "medium", "high", "xhigh", "max"}, in.Quality) || len(in.References) > 16 {
		return out, domain.ErrValidation
	}
	payload := map[string]any{"model": c.Model, "prompt": in.Prompt, "n": 1, "size": in.Size, "quality": in.Quality, "output_format": "png", "background": "opaque", "moderation": "auto"}
	endpoint := strings.TrimRight(c.BaseURL, "/") + "/images/generations"
	if in.Region != nil && (in.Region.Validate() != nil || len(in.References) == 0) {
		return out, domain.ErrValidation
	}
	if len(in.References) > 0 {
		refs := []map[string]string{}
		total := 0
		for i, data := range in.References {
			total += len(data)
			// The JSON edit API limits each data URL to 20 MiB. Leave room for its prefix.
			if len(data) > 15*1024*1024-64 || total > 32*1024*1024 {
				return out, domain.ErrValidation
			}
			meta, err := inspectImage(data)
			if err != nil {
				return out, err
			}
			if i == 0 && in.Region != nil {
				if meta.Width != in.Region.Width || meta.Height != in.Region.Height {
					return out, domain.ErrValidation
				}
				mask, err := in.Region.MaskPNG()
				if err != nil {
					return out, err
				}
				payload["mask"] = map[string]string{"image_url": "data:image/png;base64," + base64.StdEncoding.EncodeToString(mask)}
				// Match PNG mask/source formats for JPEG/WebP without changing
				// its original immutable file. No resize or remote image fetch.
				if meta.MIME != "image/png" {
					decoded, _, err := image.Decode(bytes.NewReader(data))
					if err != nil {
						return out, domain.ErrValidation
					}
					var converted bytes.Buffer
					if err = png.Encode(&converted, decoded); err != nil {
						return out, domain.ErrValidation
					}
					if converted.Len() > 15*1024*1024-64 || total-len(data)+converted.Len() > 32*1024*1024 {
						return out, domain.ErrValidation
					}
					total += converted.Len() - len(data)
					data = converted.Bytes()
					meta.MIME = "image/png"
				}
			}
			refs = append(refs, map[string]string{"image_url": "data:" + meta.MIME + ";base64," + base64.StdEncoding.EncodeToString(data)})
		}
		payload["images"] = refs
		endpoint = strings.TrimRight(c.BaseURL, "/") + "/images/edits"
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return out, domain.ErrValidation
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return out, domain.ErrValidation
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+string(key))
	if err = ctx.Err(); err != nil {
		return out, err
	}
	if err = authorize(ctx); err != nil {
		return out, err
	}
	res, err := a.client.Do(req)
	if err != nil {
		return out, ErrUnknown
	}
	defer res.Body.Close()
	// A provider error may echo credentials, prompts or URLs: never propagate it.
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		if res.StatusCode >= 500 || res.StatusCode == 408 {
			return out, ErrUnknown
		}
		return out, ErrRejected
	}
	requestID := res.Header.Get("X-Request-Id")
	if len(requestID) <= 256 && !strings.ContainsAny(requestID, "\r\n\x00") {
		out.RequestID = requestID
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, maxImageResponseBytes+1))
	if err != nil || len(raw) > maxImageResponseBytes {
		return out, ErrUnknown
	}
	var response struct {
		Data []struct {
			Base64        string `json:"b64_json"`
			URL           string `json:"url"`
			RevisedPrompt string `json:"revised_prompt"`
		} `json:"data"`
		Usage *struct {
			Input        *int64 `json:"input_tokens"`
			Output       *int64 `json:"output_tokens"`
			Total        *int64 `json:"total_tokens"`
			InputDetails *struct {
				Text  *int64 `json:"text_tokens"`
				Image *int64 `json:"image_tokens"`
			} `json:"input_tokens_details"`
			OutputDetails *struct {
				Text  *int64 `json:"text_tokens"`
				Image *int64 `json:"image_tokens"`
			} `json:"output_tokens_details"`
		} `json:"usage"`
	}
	if json.Unmarshal(raw, &response) != nil {
		return out, ErrUnknown
	}
	u := response.Usage
	if u != nil && u.Input != nil && u.Output != nil && u.Total != nil && u.InputDetails != nil && u.InputDetails.Text != nil && u.InputDetails.Image != nil {
		usage := ImageUsage{TextInput: *u.InputDetails.Text, ImageInput: *u.InputDetails.Image, ImageOutput: *u.Output}
		complete := true
		if u.OutputDetails != nil {
			if u.OutputDetails.Text == nil || u.OutputDetails.Image == nil {
				complete = false
			} else {
				usage.TextOutput = *u.OutputDetails.Text
				usage.ImageOutput = *u.OutputDetails.Image
			}
		}
		if complete && usage.Valid() && *u.Input == usage.TextInput+usage.ImageInput && *u.Output == usage.TextOutput+usage.ImageOutput && *u.Total == *u.Input+*u.Output {
			out.Usage = &usage
		}
	}
	if len(response.Data) != 1 || response.Data[0].Base64 == "" || response.Data[0].URL != "" {
		return out, ErrInvalidOutput
	}
	item := response.Data[0]
	data, err := base64.StdEncoding.Strict().DecodeString(item.Base64)
	if err != nil {
		return out, ErrInvalidOutput
	}
	meta, err := inspectImage(data)
	if err != nil || meta.MIME != "image/png" {
		return out, ErrInvalidOutput
	}
	if utf8.RuneCountInString(item.RevisedPrompt) > 32000 {
		return out, ErrInvalidOutput
	}
	out.Data = data
	out.MIME = meta.MIME
	out.Width = meta.Width
	out.Height = meta.Height
	out.RevisedPrompt = item.RevisedPrompt
	if out.Usage == nil {
		return out, ErrUnknown
	}
	return out, nil
}

func (u ImageUsage) Valid() bool {
	for _, v := range []int64{u.TextInput, u.ImageInput, u.TextOutput, u.ImageOutput} {
		if v < 0 || v > 10000000 {
			return false
		}
	}
	return u.TextInput+u.ImageInput > 0 && u.ImageOutput > 0
}
