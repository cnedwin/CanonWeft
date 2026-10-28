package storage

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func (s LocalBlobs) ScanObjects(ctx context.Context, project string) ([]BlobRef, error) {
	return s.scanObjects(ctx, project, nil)
}

func (s LocalBlobs) scanObjects(ctx context.Context, project string, recovery map[string]BlobRef) ([]BlobRef, error) {
	if !gcProject.MatchString(project) {
		return nil, ErrInventoryInvalidKey
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !filepath.IsAbs(s.Root) {
		return nil, ErrInventoryUnavailable
	}
	clean := filepath.Clean(s.Root)
	resolved, err := filepath.EvalSymlinks(clean)
	if err != nil || !strings.EqualFold(clean, filepath.Clean(resolved)) {
		return nil, ErrInventoryUnavailable
	}
	rootInfo, err := os.Lstat(clean)
	if err != nil || !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return nil, ErrInventoryUnavailable
	}
	root, err := os.OpenRoot(clean)
	if err != nil {
		return nil, ErrInventoryUnavailable
	}
	defer root.Close()
	boundInfo, err := root.Stat(".")
	if err != nil || !os.SameFile(rootInfo, boundInfo) {
		return nil, ErrInventoryConflict
	}
	before, err := root.Lstat(project)
	if os.IsNotExist(err) {
		return []BlobRef{}, nil
	}
	if err != nil {
		return nil, ErrInventoryUnavailable
	}
	if !before.IsDir() || before.Mode()&os.ModeSymlink != 0 {
		return nil, ErrInventoryConflict
	}
	parent, err := root.OpenRoot(project)
	if err != nil {
		return nil, ErrInventoryUnavailable
	}
	defer parent.Close()
	opened, err := parent.Stat(".")
	if err != nil || !os.SameFile(before, opened) {
		return nil, ErrInventoryConflict
	}
	dir, err := parent.Open(".")
	if err != nil {
		return nil, ErrInventoryUnavailable
	}
	defer dir.Close()
	objects := []BlobRef{}
	seen := map[string]bool{}
	count := 0
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		entries, readErr := dir.ReadDir(256)
		if readErr != nil && readErr != io.EOF {
			return nil, ErrInventoryUnavailable
		}
		for _, entry := range entries {
			if count == maxInventoryObjects {
				return nil, ErrInventoryLimit
			}
			count++
			hash := entry.Name()
			var object BlobRef
			if ref, known := recovery[hash]; known {
				if _, err := scanRecoveryPrepared(ctx, parent, ref); err != nil {
					return nil, err
				}
				object = ref
			} else {
				if !gcHash.MatchString(hash) {
					return nil, ErrInventoryConflict
				}
				object, err = scanLocalInventoryObject(ctx, parent, hash)
				if err != nil {
					return nil, err
				}
				if recovery != nil {
					if expected, known := recovery[recoveryPreparedName(hash)]; !known || expected != object {
						return nil, ErrInventoryConflict
					}
				}
			}
			if !seen[object.Hash] {
				seen[object.Hash] = true
				objects = append(objects, object)
			}
		}
		if readErr == io.EOF {
			break
		}
	}
	current, err := root.Lstat(project)
	if err != nil || !os.SameFile(before, current) || !current.ModTime().Equal(before.ModTime()) {
		return nil, ErrInventoryConflict
	}
	currentRoot, err := os.Lstat(clean)
	if err != nil || !os.SameFile(rootInfo, currentRoot) || currentRoot.Mode()&os.ModeSymlink != 0 {
		return nil, ErrInventoryConflict
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	sort.Slice(objects, func(i, j int) bool { return objects[i].Hash < objects[j].Hash })
	return objects, nil
}

func scanLocalInventoryObject(ctx context.Context, parent *os.Root, hash string) (BlobRef, error) {
	before, err := parent.Lstat(hash)
	if err != nil || !before.Mode().IsRegular() || before.Size() > MaxBlobBytes {
		return BlobRef{}, ErrInventoryConflict
	}
	file, err := parent.Open(hash)
	if err != nil {
		return BlobRef{}, ErrInventoryUnavailable
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(before, opened) {
		return BlobRef{}, ErrInventoryConflict
	}
	data, err := readBounded(ctx, file)
	if ctx.Err() != nil {
		return BlobRef{}, ctx.Err()
	}
	if err != nil {
		return BlobRef{}, ErrInventoryUnavailable
	}
	current, err := parent.Lstat(hash)
	if err != nil || !current.Mode().IsRegular() || !os.SameFile(before, current) || current.Size() != before.Size() || !current.ModTime().Equal(before.ModTime()) || int64(len(data)) != before.Size() || contentHash(data) != hash {
		return BlobRef{}, ErrInventoryConflict
	}
	return BlobRef{Hash: hash, Size: int64(len(data))}, nil
}
