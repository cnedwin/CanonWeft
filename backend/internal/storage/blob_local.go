package storage

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
)

type LocalBlobs struct{ Root string }

func contentHash(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
func (s LocalBlobs) openRoot(create bool) (*os.Root, error) {
	if s.Root == "" {
		return nil, errors.New("blob store root missing")
	}
	if create {
		if err := os.MkdirAll(s.Root, 0700); err != nil {
			return nil, err
		}
	}
	return os.OpenRoot(s.Root)
}
func (s LocalBlobs) PutObject(ctx context.Context, project string, input io.Reader) (BlobRef, error) {
	return s.putObject(ctx, project, input, false)
}

func (s LocalBlobs) putObject(ctx context.Context, project string, input io.Reader, recovery bool) (BlobRef, error) {
	if !validBlobKey(project, "") {
		return BlobRef{}, errors.New("invalid project reference")
	}
	data, err := readBounded(ctx, input)
	if err != nil {
		return BlobRef{}, err
	}
	root, err := s.openRoot(true)
	if err != nil {
		return BlobRef{}, err
	}
	defer root.Close()
	if err = root.MkdirAll(project, 0700); err != nil {
		return BlobRef{}, err
	}
	ref := BlobRef{Hash: contentHash(data), Size: int64(len(data))}
	path := filepath.Join(project, ref.Hash)
	if existing, readErr := root.ReadFile(path); readErr == nil {
		if contentHash(existing) != ref.Hash {
			return BlobRef{}, errors.New("existing blob is corrupt")
		}
		return ref, nil
	} else if !os.IsNotExist(readErr) {
		return BlobRef{}, readErr
	}
	name := recoveryPreparedName(ref.Hash)
	if !recovery {
		random := make([]byte, 16)
		if _, err = rand.Read(random); err != nil {
			return BlobRef{}, err
		}
		name = "prepared-" + hex.EncodeToString(random)
	}
	temporary := filepath.Join(project, name)
	file, err := root.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return BlobRef{}, err
	}
	defer root.Remove(temporary)
	if _, err = file.Write(data); err != nil {
		file.Close()
		return BlobRef{}, err
	}
	if err = file.Sync(); err != nil {
		file.Close()
		return BlobRef{}, err
	}
	if err = file.Close(); err != nil {
		return BlobRef{}, err
	}
	if err = ctx.Err(); err != nil {
		return BlobRef{}, err
	}
	// Root confines links and paths even if a directory is swapped for a junction.
	// Link is create-only, so an existing immutable object is never overwritten.
	if err = root.Link(temporary, path); err != nil {
		current, readErr := root.ReadFile(path)
		if readErr != nil || contentHash(current) != ref.Hash {
			return BlobRef{}, err
		}
	}
	// A recovery may commit only after its journal-bound temporary link is gone.
	// On failure, compensation and later reconciliation still own both names.
	if recovery {
		if err := root.Remove(temporary); err != nil {
			return BlobRef{}, err
		}
	}
	return ref, nil
}
func (s LocalBlobs) ReadObject(ctx context.Context, project, hash string) ([]byte, error) {
	if !validBlobKey(project, hash) || hash == "" {
		return nil, errors.New("invalid blob reference")
	}
	root, err := s.openRoot(false)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	file, err := root.Open(filepath.Join(project, hash))
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := readBounded(ctx, file)
	if err != nil {
		return nil, err
	}
	if contentHash(data) != hash {
		return nil, errors.New("blob checksum mismatch")
	}
	return data, nil
}
