package storage

import (
	"context"
	"sort"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

func (s *S3Blobs) ScanObjects(ctx context.Context, project string) ([]BlobRef, error) {
	if !gcProject.MatchString(project) {
		return nil, ErrInventoryInvalidKey
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s == nil || s.client == nil || s.bucket == "" {
		return nil, ErrInventoryUnavailable
	}
	if err := s.inventoryUnversioned(ctx); err != nil {
		return nil, err
	}
	prefix := project + "/"
	input := &s3.ListObjectsV2Input{Bucket: aws.String(s.bucket), Prefix: aws.String(prefix), MaxKeys: aws.Int32(1000)}
	objects := []BlobRef{}
	seen, tokens := map[string]bool{}, map[string]bool{}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		page, err := s.client.ListObjectsV2(ctx, input)
		if err != nil {
			return nil, inventoryS3Error(ctx)
		}
		if page.IsTruncated == nil || len(page.CommonPrefixes) != 0 || len(page.Contents) > 1000 || page.EncodingType != "" {
			return nil, ErrInventoryConflict
		}
		for _, object := range page.Contents {
			if len(objects) == maxInventoryObjects {
				return nil, ErrInventoryLimit
			}
			key := aws.ToString(object.Key)
			hash := strings.TrimPrefix(key, prefix)
			if !strings.HasPrefix(key, prefix) || !gcHash.MatchString(hash) || seen[hash] || object.Size == nil || *object.Size < 0 || *object.Size > MaxBlobBytes {
				return nil, ErrInventoryConflict
			}
			// Neither ETag nor a filename proves immutable contents. ReadObject
			// verifies SHA-256; list size must still match the returned bytes.
			body, err := s.ReadObject(ctx, project, hash)
			if err != nil {
				return nil, inventoryS3Error(ctx)
			}
			if int64(len(body)) != *object.Size {
				return nil, ErrInventoryConflict
			}
			seen[hash] = true
			objects = append(objects, BlobRef{Hash: hash, Size: *object.Size})
		}
		token := aws.ToString(page.NextContinuationToken)
		if !aws.ToBool(page.IsTruncated) {
			if token != "" {
				return nil, ErrInventoryConflict
			}
			break
		}
		if token == "" || len(token) > 16384 || tokens[token] || len(page.Contents) == 0 {
			return nil, ErrInventoryConflict
		}
		tokens[token] = true
		input.ContinuationToken = aws.String(token)
	}
	if err := s.inventoryUnversioned(ctx); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	sort.Slice(objects, func(i, j int) bool { return objects[i].Hash < objects[j].Hash })
	return objects, nil
}

func inventoryS3Error(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return ErrInventoryUnavailable
}

func (s *S3Blobs) inventoryUnversioned(ctx context.Context) error {
	result, err := s.client.GetBucketVersioning(ctx, &s3.GetBucketVersioningInput{Bucket: aws.String(s.bucket)})
	if err != nil {
		return inventoryS3Error(ctx)
	}
	// Current-key listing cannot establish the absence of old versions. No
	// version enumeration, deletion or governance bypass belongs to this API.
	if result.Status != "" {
		return ErrGCVersioned
	}
	return nil
}
