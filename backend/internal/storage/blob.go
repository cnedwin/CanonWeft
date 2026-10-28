package storage

import (
	"context"
	"encoding/hex"
	"errors"
	"io"
)

const MaxBlobBytes int64 = 32 * 1024 * 1024

type BlobRef struct {
	Hash string `json:"hash"`
	Size int64  `json:"size"`
}
type BlobStore interface {
	PutObject(context.Context, string, io.Reader) (BlobRef, error)
	ReadObject(context.Context, string, string) ([]byte, error)
}

func validBlobKey(project, hash string) bool {
	if len(project) != 32 || (hash != "" && len(hash) != 64) {
		return false
	}
	_, err := hex.DecodeString(project + hash)
	return err == nil
}
func readBounded(ctx context.Context, input io.Reader) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(input, MaxBlobBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > MaxBlobBytes {
		return nil, errors.New("file exceeds 32 MiB limit")
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	return data, nil
}
