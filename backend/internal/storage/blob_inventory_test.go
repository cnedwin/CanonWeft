package storage

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type inventoryReader interface {
	ScanObjects(context.Context, string) ([]BlobRef, error)
}

func TestLocalBlobInventoryIsCompleteScopedAndReadOnly(t *testing.T) {
	ctx := context.Background()
	store := LocalBlobs{Root: t.TempDir()}
	reader, ok := any(store).(inventoryReader)
	if !ok {
		t.Fatal("physical inventory is not implemented")
	}
	project, other := strings.Repeat("a", 32), strings.Repeat("b", 32)
	refs := map[string]int64{}
	for _, body := range []string{"first", "second", ""} {
		ref, err := store.PutObject(ctx, project, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		refs[ref.Hash] = ref.Size
	}
	// Cross the bounded ReadDir page size instead of qualifying only one page.
	for i := 0; i < 260; i++ {
		ref, err := store.PutObject(ctx, project, strings.NewReader(fmt.Sprintf("page object %d", i)))
		if err != nil {
			t.Fatal(err)
		}
		refs[ref.Hash] = ref.Size
	}
	if _, err := store.PutObject(ctx, other, strings.NewReader("other private project")); err != nil {
		t.Fatal(err)
	}
	objects, err := reader.ScanObjects(ctx, project)
	if err != nil || len(objects) != len(refs) {
		t.Fatal("incomplete physical inventory", objects, err)
	}
	for i, object := range objects {
		if size, exists := refs[object.Hash]; !exists || size != object.Size || (i > 0 && objects[i-1].Hash >= object.Hash) {
			t.Fatal("wrong or unordered object", object)
		}
		if _, err := store.ReadObject(ctx, project, object.Hash); err != nil {
			t.Fatal("scan changed content", err)
		}
	}
	missing, err := reader.ScanObjects(ctx, strings.Repeat("c", 32))
	if err != nil || len(missing) != 0 {
		t.Fatal("absent project should have no objects", err)
	}
	if _, err := os.Stat(filepath.Join(store.Root, strings.Repeat("c", 32))); !os.IsNotExist(err) {
		t.Fatal("scan created a directory")
	}
}

func TestLocalBlobInventoryRefusesPartialOrUnsafeResults(t *testing.T) {
	project := strings.Repeat("a", 32)
	for _, mode := range []string{"corrupt", "unknown file", "nested directory", "missing root", "cancelled", "unsafe key"} {
		t.Run(mode, func(t *testing.T) {
			store := LocalBlobs{Root: t.TempDir()}
			reader, ok := any(store).(inventoryReader)
			if !ok {
				t.Fatal("physical inventory is not implemented")
			}
			ctx := context.Background()
			ref, err := store.PutObject(ctx, project, strings.NewReader("original"))
			if err != nil {
				t.Fatal(err)
			}
			key := project
			switch mode {
			case "corrupt":
				err = os.WriteFile(filepath.Join(store.Root, project, ref.Hash), []byte("PRIVATE_CANARY"), 0600)
			case "unknown file":
				err = os.WriteFile(filepath.Join(store.Root, project, "prepared-interrupted"), []byte("PRIVATE_CANARY"), 0600)
			case "nested directory":
				err = os.Mkdir(filepath.Join(store.Root, project, strings.Repeat("d", 64)), 0700)
			case "missing root":
				store.Root = filepath.Join(store.Root, "missing")
				reader = any(store).(inventoryReader)
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "unsafe key":
				key = "../" + project
			}
			if err != nil {
				t.Fatal(err)
			}
			objects, err := reader.ScanObjects(ctx, key)
			if err == nil || objects != nil || strings.Contains(err.Error(), "PRIVATE_CANARY") {
				t.Fatal("unsafe/partial inventory accepted", err)
			}
			if mode == "cancelled" && !errors.Is(err, context.Canceled) {
				t.Fatal("lost cancellation", err)
			}
		})
	}
}
