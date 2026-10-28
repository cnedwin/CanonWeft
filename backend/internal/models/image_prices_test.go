package models

import "testing"

func TestImageRatesSeparateModalitiesAndRejectUnknownUsage(t *testing.T) {
	rates := ImageRates{TextInput: 5000000, ImageInput: 8000000, TextOutput: 10000000, ImageOutput: 30000000}
	usage := ImageUsage{TextInput: 10, ImageInput: 20, TextOutput: 2, ImageOutput: 40}
	amount, err := rates.Charge(&usage)
	if err != nil || amount != 1430 {
		t.Fatal(amount, err)
	}
	if _, err = rates.Charge(nil); err == nil {
		t.Fatal("missing usage treated as free")
	}
	usage.ImageInput = -1
	if _, err = rates.Charge(&usage); err == nil {
		t.Fatal("negative usage accepted")
	}
	usage.ImageInput = 20
	rates.ImageInput = 0
	if _, err = rates.Charge(&usage); err == nil {
		t.Fatal("missing image rate treated as text rate")
	}
	rates.ImageInput = 8000000
	rates.TextOutput = 0
	if _, err = rates.Charge(&usage); err == nil {
		t.Fatal("unknown text output rate treated as free")
	}
	usage.TextOutput = 0
	if _, err = rates.Charge(&usage); err != nil {
		t.Fatal("unused text output rate required", err)
	}
	rates.TextInput = 1
	rates.ImageInput = 1
	rates.ImageOutput = 1
	if amount, err = rates.Charge(&usage); err != nil || amount != 1 {
		t.Fatal("fractional micro-unit not rounded up", amount, err)
	}
}
