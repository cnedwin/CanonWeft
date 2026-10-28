//go:build windows

package backup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
	"novelstudio/internal/storage"
)

// A real Windows handle denies deletion just as an antivirus/reader can. The
// cancellation boundary observes actual file creation, not a call counter.
type heldPayloadContext struct {
	context.Context
	directory string
	suffix    string
	handle    windows.Handle
	path      string
	lockErr   error
}

func (c *heldPayloadContext) Err() error {
	if c.handle != 0 || c.lockErr != nil {
		return context.Canceled
	}
	entries, err := os.ReadDir(c.directory)
	if err != nil {
		c.lockErr = err
		return context.Canceled
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), c.suffix) {
			continue
		}
		c.path = filepath.Join(c.directory, entry.Name())
		name, err := windows.UTF16PtrFromString(c.path)
		if err != nil {
			c.lockErr = err
			return context.Canceled
		}
		c.handle, c.lockErr = windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
		return context.Canceled
	}
	return c.Context.Err()
}

func assertRetriableCleanup(t *testing.T, store *ProtectionStore, blocked *heldPayloadContext, now time.Time, intentSuffix string) {
	t.Helper()
	if blocked.lockErr != nil || blocked.handle == 0 || blocked.handle == windows.InvalidHandle {
		t.Fatalf("actual sharing violation fixture not established: %v", blocked.lockErr)
	}
	defer func() {
		if blocked.handle != 0 {
			windows.CloseHandle(blocked.handle)
		}
	}()
	id := strings.TrimSuffix(filepath.Base(blocked.path), blocked.suffix)
	if _, err := store.root.Stat(id + intentSuffix); err != nil {
		t.Fatal("failed payload removal discarded its ownership intent", err)
	}
	if _, err := os.Stat(blocked.path); err != nil {
		t.Fatal("expected actual Windows sharing failure was not exercised", err)
	}
	if err := windows.CloseHandle(blocked.handle); err != nil {
		t.Fatal(err)
	}
	blocked.handle = 0
	if n, err := store.CleanupInterrupted(context.Background(), now.Add(25*time.Hour)); err != nil || n != 1 {
		t.Fatalf("retained ownership did not allow later cleanup: %d %v", n, err)
	}
	if _, err := os.Stat(blocked.path); !os.IsNotExist(err) {
		t.Fatal("private content was not removed after handle release")
	}
}

func TestProtectionImportCleanupRetainsIntentWhenWindowsDeniesDelete(t *testing.T) {
	now := time.Now().UTC()
	p, source := protectionFixture(t, now)
	store, err := OpenProtection(filepath.Join(t.TempDir(), "backups"), source)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	blocked := &heldPayloadContext{Context: context.Background(), directory: store.Path, suffix: ".zip"}
	if _, err := store.Import(blocked, p, now, "baseline"); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation after actual payload write: %v", err)
	}
	assertRetriableCleanup(t, store, blocked, now, ".pending")
}

func TestProtectionCaptureCleanupRetainsIntentWhenWindowsDeniesDelete(t *testing.T) {
	db, _, admin := testDatabase(t)
	provisionTestAuthority(t, db, admin)
	now := time.Now().UTC()
	runner, err := NewProtectionRunner(context.Background(), db, storage.LocalBlobs{Root: filepath.Join(t.TempDir(), "blobs")}, filepath.Join(t.TempDir(), "backups"))
	if err != nil {
		t.Fatal(err)
	}
	defer runner.Close()
	blocked := &heldPayloadContext{Context: context.Background(), directory: runner.Store.Path, suffix: ".capture"}
	if _, err := runner.capture(blocked, now, "baseline"); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation after actual capture write: %v", err)
	}
	assertRetriableCleanup(t, runner.Store, blocked, now, ".capture.intent")
}
