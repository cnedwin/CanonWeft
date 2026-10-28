package backup

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestProtectionReopensAfterCrashBeforeIdentityPublication(t *testing.T) {
	now := time.Now().UTC()
	_, source := protectionFixture(t, now)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".writer.lock"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	store, err := OpenProtection(root, source)
	if err != nil {
		t.Fatal("OS released lock residue must not prevent initialization retry", err)
	}
	defer store.Close()
	if _, err := store.root.Stat("protection.json"); err != nil {
		t.Fatal("identity not durably published", err)
	}
}

type crashIntentReader struct {
	signal  string
	started bool
}

func (r *crashIntentReader) Read(p []byte) (int, error) {
	if !r.started {
		r.started = true
		return copy(p, []byte(`{"id":"partial`)), nil
	}
	if err := os.WriteFile(r.signal, []byte("partial write reached"), 0600); err != nil {
		return 0, err
	}
	time.Sleep(time.Hour)
	return 0, io.EOF
}

func TestProtectionCrashWriterChild(t *testing.T) {
	root := os.Getenv("STUDIO_TEST_CRASH_PROTECTION_ROOT")
	if root == "" {
		return
	}
	var source Source
	if err := json.Unmarshal([]byte(os.Getenv("STUDIO_TEST_CRASH_PROTECTION_SOURCE")), &source); err != nil {
		t.Fatal(err)
	}
	store, err := OpenProtection(root, source)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.writeNew(strings.Repeat("c", 32)+".pending", &crashIntentReader{signal: os.Getenv("STUDIO_TEST_CRASH_SIGNAL")}); err != nil {
		t.Fatal(err)
	}
}

func TestProtectionProcessCrashCannotPublishPartialIntent(t *testing.T) {
	now := time.Now().UTC()
	_, source := protectionFixture(t, now)
	base := t.TempDir()
	root, signal := filepath.Join(base, "backups"), filepath.Join(base, "child-ready")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(source)
	child := exec.Command(executable, "-test.run=^TestProtectionCrashWriterChild$", "-test.timeout=2m")
	child.Env = append(os.Environ(), "STUDIO_TEST_CRASH_PROTECTION_ROOT="+root, "STUDIO_TEST_CRASH_PROTECTION_SOURCE="+string(encoded), "STUDIO_TEST_CRASH_SIGNAL="+signal)
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if child.ProcessState == nil {
			child.Process.Kill()
			child.Wait()
		}
	})
	deadline := time.Now().Add(15 * time.Second)
	for {
		if _, err := os.Stat(signal); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("child did not reach actual partial intent write")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := child.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	child.Wait()
	store, err := OpenProtection(root, source)
	if err != nil {
		t.Fatal("process exit did not release repository ownership", err)
	}
	defer store.Close()
	id := strings.Repeat("c", 32)
	if _, err := store.root.Stat(id + ".pending"); !os.IsNotExist(err) {
		t.Fatal("partial intent was published under its committed filename")
	}
	if _, err := store.root.Stat(id + ".pending.tmp"); err != nil {
		t.Fatal("test did not leave actual partial metadata", err)
	}
	if n, err := store.CleanupInterrupted(context.Background(), time.Now().Add(25*time.Hour)); err != nil || n != 1 {
		t.Fatalf("crashed metadata not cleaned: %d %v", n, err)
	}
}

func TestProtectionTruncatedIntentDoesNotBlockSubsequentCheckpoints(t *testing.T) {
	now := time.Now().UTC()
	p, source := protectionFixture(t, now)
	for _, suffix := range []string{".pending", ".capture.intent"} {
		t.Run(suffix, func(t *testing.T) {
			store, err := OpenProtection(filepath.Join(t.TempDir(), "backups"), source)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			id := strings.Repeat("b", 32)
			name := id + suffix
			payload := id + ".zip"
			if suffix == ".capture.intent" {
				payload = id + ".capture"
			}
			if err := store.root.WriteFile(name, []byte(`{"id":"`), 0600); err != nil {
				t.Fatal(err)
			}
			if err := store.root.WriteFile(payload, []byte("private incomplete content"), 0600); err != nil {
				t.Fatal(err)
			}
			if n, err := store.CleanupInterrupted(context.Background(), now); err != nil || n != 0 {
				t.Fatalf("recent crash residue blocks normal protection: %d %v", n, err)
			}
			if _, err := store.Import(context.Background(), p, now, "baseline"); err != nil {
				t.Fatal("new valid checkpoint blocked by crash residue", err)
			}
			old := now.Add(-25 * time.Hour)
			if err := os.Chtimes(filepath.Join(store.Path, name), old, old); err != nil {
				t.Fatal(err)
			}
			if n, err := store.CleanupInterrupted(context.Background(), now); err != nil || n != 1 {
				t.Fatalf("expired corrupt intent did not clean its owned payload: %d %v", n, err)
			}
			if _, err := store.root.Stat(payload); !os.IsNotExist(err) {
				t.Fatal("expired private content retained")
			}
		})
	}
}
