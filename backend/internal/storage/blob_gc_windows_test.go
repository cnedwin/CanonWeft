//go:build windows

package storage

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestLocalBlobGCWindowsOccupiedFileReturnsFailureThenRetries(t *testing.T) {
	root := t.TempDir()
	project := strings.Repeat("a", 32)
	store := LocalBlobs{Root: root}
	ref, err := store.PutObject(context.Background(), project, strings.NewReader("locked object"))
	if err != nil {
		t.Fatal(err)
	}
	name, _ := windows.UTF16PtrFromString(filepath.Join(root, project, ref.Hash))
	h, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if h != 0 {
			windows.CloseHandle(h)
		}
	}()
	gc, err := NewLocalBlobGC(root)
	if err != nil {
		t.Fatal(err)
	}
	defer gc.Close()
	if _, err := gc.DeleteObject(context.Background(), project, ref.Hash); !errors.Is(err, ErrGCUnavailable) {
		t.Fatal("occupied file counted as deleted", err)
	}
	if _, err := store.ReadObject(context.Background(), project, ref.Hash); err != nil {
		t.Fatal("denied delete lost object", err)
	}
	if err := windows.CloseHandle(h); err != nil {
		t.Fatal(err)
	}
	h = 0
	if result, err := gc.DeleteObject(context.Background(), project, ref.Hash); err != nil || result != GCRemoved {
		t.Fatal("delete did not retry after handle released", result, err)
	}
}

func TestLocalBlobGCWindowsRejectsJunctionRootAndProject(t *testing.T) {
	owned := t.TempDir()
	root, outside := filepath.Join(owned, "objects"), filepath.Join(owned, "outside")
	for _, path := range []string{root, outside} {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	project := strings.Repeat("a", 32)
	ref, err := (LocalBlobs{Root: outside}).PutObject(context.Background(), project, strings.NewReader("outside object"))
	if err != nil {
		t.Fatal(err)
	}
	// Both targets are new test-owned paths. No recursive shell deletion or
	// caller-controlled path is involved in constructing the junction fixture.
	makeJunction := func(link, target string) {
		t.Helper()
		if output, err := exec.Command("cmd", "/c", "mklink", "/J", link, target).CombinedOutput(); err != nil {
			t.Fatalf("junction fixture: %v %s", err, output)
		}
	}
	linkedRoot := filepath.Join(owned, "linked")
	makeJunction(linkedRoot, outside)
	if _, err := (LocalBlobs{Root: linkedRoot}).ScanObjects(context.Background(), project); err == nil {
		t.Fatal("inventory accepted junction root")
	}
	if gc, err := NewLocalBlobGC(linkedRoot); err == nil {
		gc.Close()
		t.Fatal("junction root accepted")
	}
	makeJunction(filepath.Join(root, project), filepath.Join(outside, project))
	if _, err := (LocalBlobs{Root: root}).ScanObjects(context.Background(), project); err == nil {
		t.Fatal("inventory traversed project junction")
	}
	gc, err := NewLocalBlobGC(root)
	if err != nil {
		t.Fatal(err)
	}
	defer gc.Close()
	if _, err := gc.DeleteObject(context.Background(), project, ref.Hash); err == nil {
		t.Fatal("project junction traversed")
	}
	if _, err := (LocalBlobs{Root: outside}).ReadObject(context.Background(), project, ref.Hash); err != nil {
		t.Fatal("outside object lost", err)
	}
}
