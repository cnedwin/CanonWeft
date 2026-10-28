package storage

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRecoveryPreparedInventoryAndExactCleanup(t *testing.T) {
	ctx := context.Background()
	for _, phase := range []string{"empty", "partial", "complete", "linked"} {
		t.Run(phase, func(t *testing.T) {
			root := t.TempDir()
			store, err := NewLocalRetentionStore(root)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			project := strings.Repeat("b", 32)
			body := []byte("interrupted recovered author text")
			ref := BlobRef{Hash: contentHash(body), Size: int64(len(body))}
			if err := os.Mkdir(filepath.Join(root, project), 0700); err != nil {
				t.Fatal(err)
			}
			prepared := filepath.Join(root, project, "prepared-recovery-"+ref.Hash)
			data := body
			if phase == "empty" {
				data = nil
			}
			if phase == "partial" {
				data = body[:5]
			}
			if err := os.WriteFile(prepared, data, 0600); err != nil {
				t.Fatal(err)
			}
			if phase == "linked" {
				if err := os.Link(prepared, filepath.Join(root, project, ref.Hash)); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := store.ScanObjects(ctx, project); err == nil {
				t.Fatal("ordinary inventory accepted incomplete recovery")
			}
			objects, err := store.ScanRecoveryObjects(ctx, project, []BlobRef{ref})
			if err != nil || len(objects) != 1 || objects[0] != ref {
				t.Fatal("journal-bound inventory", objects, err)
			}
			if err := store.DeleteRecoveryPrepared(ctx, project, ref); err != nil {
				t.Fatal(err)
			}
			if err := store.DeleteRecoveryPrepared(ctx, project, ref); err != nil {
				t.Fatal("retry", err)
			}
			if _, err := os.Stat(prepared); !os.IsNotExist(err) {
				t.Fatal("temporary file remains", err)
			}
			if phase == "linked" {
				got, err := store.ReadObject(ctx, project, ref.Hash)
				if err != nil || string(got) != string(body) {
					t.Fatal("temporary cleanup changed immutable file", err)
				}
			}
		})
	}
}

func TestRecoveryPreparedRejectsUnregisteredAndChangedFiles(t *testing.T) {
	for _, scenario := range []string{"unknown", "legacy", "oversize", "directory", "corrupt-complete"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			store, err := NewLocalRetentionStore(root)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			project := strings.Repeat("b", 32)
			ref := BlobRef{Hash: contentHash([]byte("data")), Size: 4}
			if err := os.Mkdir(filepath.Join(root, project), 0700); err != nil {
				t.Fatal(err)
			}
			name, data := "prepared-recovery-"+ref.Hash, []byte("da")
			if scenario == "unknown" {
				name = "prepared-recovery-" + strings.Repeat("e", 64)
			}
			if scenario == "legacy" {
				name = "prepared-" + strings.Repeat("e", 32)
			}
			if scenario == "oversize" {
				data = []byte("extra data")
			}
			if scenario == "corrupt-complete" {
				data = []byte("evil")
			}
			path := filepath.Join(root, project, name)
			if scenario == "directory" {
				err = os.Mkdir(path, 0700)
			} else {
				err = os.WriteFile(path, data, 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.ScanRecoveryObjects(context.Background(), project, []BlobRef{ref}); err == nil {
				t.Fatal("unsafe inventory accepted")
			}
			if _, err := os.Stat(path); err != nil {
				t.Fatal("inspection deleted unknown data", err)
			}
		})
	}
}
