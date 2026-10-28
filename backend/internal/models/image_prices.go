package models

import "novelstudio/internal/domain"

// ImageRates are integer millionths of currency per million tokens. Callers
// must pin the account's verified tariff, currency and expiry with the task.
// No default prices, currency conversion or model-specific quota is inferred.
type ImageRates struct {
	TextInput   int64 `json:"text_input_micros_per_million"`
	ImageInput  int64 `json:"image_input_micros_per_million"`
	TextOutput  int64 `json:"text_output_micros_per_million"`
	ImageOutput int64 `json:"image_output_micros_per_million"`
}

func (p ImageRates) Validate() error {
	if p.TextInput <= 0 || p.ImageInput <= 0 || p.ImageOutput <= 0 {
		return domain.ErrValidation
	}
	for _, r := range []int64{p.TextInput, p.ImageInput, p.TextOutput, p.ImageOutput} {
		if r < 0 || r > 1000000000 {
			return domain.ErrValidation
		}
	}
	return nil
}
func (p ImageRates) Charge(u *ImageUsage) (int64, error) {
	// A missing or incomplete receipt must retain its reservation for explicit
	// reconciliation, including when a provider returned a usable image.
	if p.Validate() != nil || u == nil || !u.Valid() || (u.TextOutput > 0 && p.TextOutput == 0) {
		return 0, domain.ErrValidation
	}
	total := u.TextInput*p.TextInput + u.ImageInput*p.ImageInput + u.TextOutput*p.TextOutput + u.ImageOutput*p.ImageOutput
	return (total + 999999) / 1000000, nil
}
