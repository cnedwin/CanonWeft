package models

import (
	"net/url"
	"novelstudio/internal/domain"
	"time"
)

// Prices are integer millionths of currency per million tokens. An operator
// records the relevant account/region/tier source; no guessed price enables calls.
type PriceCard struct {
	Image              *ImageRates `json:"image,omitempty"`
	ID                 string      `json:"id"`
	ConnectionID       string      `json:"connection_id"`
	ConnectionRevision int64       `json:"connection_revision"`
	Currency           string      `json:"currency"`
	Input              int64       `json:"input_micros_per_million"`
	Output             int64       `json:"output_micros_per_million"`
	CacheRead          int64       `json:"cache_read_micros_per_million"`
	CacheWrite         int64       `json:"cache_write_micros_per_million"`
	Source             string      `json:"source"`
	ExpiresAt          string      `json:"expires_at"`
}

func (p PriceCard) Validate() error {
	u, err := url.Parse(p.Source)
	expires, dateErr := time.Parse(time.RFC3339, p.ExpiresAt)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || len(p.Source) > 1000 || dateErr != nil || !expires.After(time.Now()) || expires.After(time.Now().Add(31*24*time.Hour)) || (p.Currency != "USD" && p.Currency != "CNY") {
		return domain.ErrValidation
	}
	if p.Image != nil {
		if p.Input != 0 || p.Output != 0 || p.CacheRead != 0 || p.CacheWrite != 0 {
			return domain.ErrValidation
		}
		return p.Image.Validate()
	}
	if p.Input <= 0 || p.Output <= 0 {
		return domain.ErrValidation
	}
	for _, rate := range []int64{p.Input, p.Output, p.CacheRead, p.CacheWrite} {
		if rate < 0 || rate > 1000000000 {
			return domain.ErrValidation
		}
	}
	return nil
}
func (p PriceCard) Quote(inputBytes, outputTokens int) (int64, error) {
	if p.Image != nil {
		return 0, domain.ErrValidation
	}
	if inputBytes < 1 || inputBytes > 2*1024*1024 || outputTokens < 1 || outputTokens > 131072 {
		return 0, domain.ErrValidation
	}
	// UTF-8 byte upper estimate plus protocol/schema overhead, independent of
	// optimistic cache hits. Actual usage is accounted from the provider receipt.
	inputTokens := int64(inputBytes*2 + 8192)
	return (inputTokens*max(p.Input, p.CacheRead, p.CacheWrite) + int64(outputTokens)*p.Output + 999999) / 1000000, nil
}
func (p PriceCard) Charge(result Result) (int64, error) {
	if p.Image != nil {
		return p.Image.Charge(result.ImageUsage)
	}
	if result.ImageUsage != nil || result.Image != nil {
		return 0, domain.ErrValidation
	}
	uncached := result.InputTokens - result.CachedTokens - result.CacheWriteTokens
	if result.InputTokens <= 0 || uncached < 0 || result.CachedTokens < 0 || result.CacheWriteTokens < 0 || result.OutputTokens < 0 || result.InputTokens > 10000000 || result.OutputTokens > 10000000 {
		return 0, domain.ErrValidation
	}
	return (uncached*p.Input + result.CachedTokens*p.CacheRead + result.CacheWriteTokens*p.CacheWrite + result.OutputTokens*p.Output + 999999) / 1000000, nil
}
