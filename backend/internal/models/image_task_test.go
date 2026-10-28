package models

import (
	"encoding/json"
	"testing"
	"time"
)

func TestImagePriceCardCannotUseTextEstimatesOrLoseUsage(t *testing.T) {
	p := PriceCard{Currency: "USD", Source: "https://developers.openai.com/api/docs/pricing", ExpiresAt: time.Now().Add(time.Hour).Format(time.RFC3339), Image: &ImageRates{TextInput: 1000000, ImageInput: 2000000, ImageOutput: 4000000}}
	if p.Validate() != nil {
		t.Fatal("image price rejected")
	}
	if _, err := p.Quote(100, 100); err == nil {
		t.Fatal("text estimate accepted for image")
	}
	if _, err := p.Charge(Result{InputTokens: 100, OutputTokens: 20}); err == nil {
		t.Fatal("unknown image usage silently charged as text")
	}
	charge, err := p.Charge(Result{ImageUsage: &ImageUsage{TextInput: 10, ImageInput: 20, ImageOutput: 30}})
	if err != nil || charge != 170 {
		t.Fatal(charge, err)
	}
	p.Image = nil
	p.Input = 1
	p.Output = 1
	raw, _ := json.Marshal(p)
	var fields map[string]any
	json.Unmarshal(raw, &fields)
	if _, ok := fields["image"]; ok {
		t.Fatal("changed historical text price encoding")
	}
	if _, err := p.Charge(Result{ImageUsage: &ImageUsage{TextInput: 10, ImageOutput: 30}}); err == nil {
		t.Fatal("image billed using text prices")
	}
}
