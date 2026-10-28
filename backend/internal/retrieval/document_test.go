package retrieval

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDocumentRuntimeNativeAndOCR(t *testing.T) {
	if os.Getenv("STUDIO_TEST_INGEST") != "1" {
		t.Skip("Run scripts/verify-ingest.ps1 for the real local runtime")
	}
	fixtures := filepath.Join("..", "..", "..", "tools", "ingest", "tests", "fixtures")
	for _, name := range []string{"native.pdf", "scan.pdf", "mixed.pdf", "text.png", "text.jpg", "text.webp"} {
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join(fixtures, name))
			if err != nil {
				t.Fatal(err)
			}
			report, err := Extract(context.Background(), name, data)
			if err != nil || !report.Complete || !strings.Contains(report.Text, "星河") || !strings.Contains(report.Text, "River") {
				t.Fatalf("report=%+v err=%v", report, err)
			}
			if strings.HasSuffix(name, ".pdf") && len(report.Units) != 2 {
				t.Fatalf("missing page coverage: %+v", report)
			}
		})
	}
	t.Run("disabled OCR retains native page", func(t *testing.T) {
		t.Setenv("STUDIO_INGEST_OCR", "disabled")
		data, _ := os.ReadFile(filepath.Join(fixtures, "mixed.pdf"))
		report, err := Extract(context.Background(), "mixed.pdf", data)
		if err != nil || report.Complete || report.Extracted != 1 || report.Pending != 1 || report.Units[1].Locator != "page:2" {
			t.Fatalf("%+v %v", report, err)
		}
	})
	t.Run("corrupt is error", func(t *testing.T) {
		_, err := Extract(context.Background(), "corrupt.pdf", []byte("%PDF-broken"))
		if !errors.Is(err, ErrImport) {
			t.Fatalf("expected safe validation error: %v", err)
		}
	})
}

func TestDocumentCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Extract(ctx, "a.pdf", []byte("%PDF"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("must preserve cancellation, got %v", err)
	}
}

func TestDocumentRealScanCancellation(t *testing.T) {
	if os.Getenv("STUDIO_TEST_INGEST") != "1" {
		t.Skip("requires dedicated local runtime")
	}
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "tools", "ingest", "tests", "fixtures", "scan.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 600*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err = Extract(ctx, "scan.pdf", data)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("real scan cancellation lost: %v", err)
	}
	if time.Since(started) > 3*time.Second {
		t.Fatal("cancelled OCR continued running")
	}
}

func TestDocumentQueueHonorsCancellation(t *testing.T) {
	documentSlots <- struct{}{}
	defer func() { <-documentSlots }()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := Extract(ctx, "a.pdf", []byte("%PDF"))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("queue ignored cancellation: %v", err)
	}
}
