package service

import (
	"strings"
	"testing"
)

func TestPreviewBufferBoundedUTF8AndImmutableSnapshot(t *testing.T) {
	b := &previewBuffer{}
	if err := b.append("前文🌧️"); err != nil {
		t.Fatal(err)
	}
	first := b.snapshot()
	if err := b.append("\n后文"); err != nil {
		t.Fatal(err)
	}
	if first.Text != "前文🌧️" || first.Sequence != 1 || !first.Provisional {
		t.Fatal("prior snapshot mutated", first)
	}
	second := b.snapshot()
	for _, text := range []string{string([]byte{0xff}), strings.Repeat("x", 4*1024*1024)} {
		if err := b.append(text); err == nil {
			t.Fatal("invalid or oversized preview accepted")
		}
		if b.snapshot() != second {
			t.Fatal("rejected append modified preview")
		}
	}
}
