package storage

import (
	"context"
	"errors"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
)

type S3BlobGC struct{ store *S3Blobs }

func NewS3BlobGC(store *S3Blobs) (*S3BlobGC, error) {
	if store == nil || store.client == nil || store.bucket == "" {
		return nil, ErrGCUnavailable
	}
	return &S3BlobGC{store: store}, nil
}
func gcS3Error(ctx context.Context, err error) error {
	if canceled := ctx.Err(); canceled != nil {
		return canceled
	}
	var api smithy.APIError
	if errors.As(err, &api) && api.ErrorCode() == "PreconditionFailed" {
		return ErrGCConflict
	}
	return ErrGCUnavailable
}
func gcS3Missing(err error) bool {
	var api smithy.APIError
	return errors.As(err, &api) && api.ErrorCode() == "NoSuchKey"
}
func (s *S3BlobGC) requireUnversioned(ctx context.Context) error {
	result, err := s.store.client.GetBucketVersioning(ctx, &s3.GetBucketVersioningInput{Bucket: aws.String(s.store.bucket)})
	if err != nil {
		return gcS3Error(ctx, err)
	}
	// Suspended buckets can still contain old versions; an ordinary DELETE only
	// hides them. This narrow API never enumerates versions or bypasses locks.
	if result.Status != "" {
		return ErrGCVersioned
	}
	return nil
}
func (s *S3BlobGC) DeleteObject(ctx context.Context, project, hash string) (GCResult, error) {
	if !validGCKey(project, hash) {
		return "", ErrGCInvalidKey
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if s == nil || s.store == nil || s.store.client == nil {
		return "", ErrGCUnavailable
	}
	if err := s.requireUnversioned(ctx); err != nil {
		return "", err
	}
	input := &s3.GetObjectInput{Bucket: aws.String(s.store.bucket), Key: aws.String(project + "/" + hash)}
	object, err := s.store.client.GetObject(ctx, input)
	if err != nil {
		if !gcS3Missing(err) {
			return "", gcS3Error(ctx, err)
		}
		if err := s.requireUnversioned(ctx); err != nil {
			return "", err
		}
		return GCAbsent, nil
	}
	if object.Body == nil {
		return "", ErrGCConflict
	}
	if aws.ToString(object.ETag) == "" || (aws.ToString(object.VersionId) != "" && aws.ToString(object.VersionId) != "null") || object.ObjectLockMode != "" || object.ObjectLockLegalHoldStatus == "ON" {
		object.Body.Close()
		return "", ErrGCConflict
	}
	body, readErr := readBounded(ctx, object.Body)
	closeErr := object.Body.Close()
	if readErr != nil {
		return "", gcS3Error(ctx, readErr)
	}
	if closeErr != nil {
		return "", ErrGCUnavailable
	}
	if contentHash(body) != hash {
		return "", ErrGCConflict
	}
	_, err = s.store.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: input.Bucket, Key: input.Key, IfMatch: object.ETag})
	if err != nil {
		return "", gcS3Error(ctx, err)
	}
	remaining, err := s.store.client.GetObject(ctx, input)
	if err == nil {
		if remaining.Body != nil {
			remaining.Body.Close()
		}
		return "", ErrGCConflict
	}
	if !gcS3Missing(err) {
		return "", gcS3Error(ctx, err)
	}
	// Check again before acknowledging physical removal: a delete marker is not
	// proof that bytes disappeared if versioning changed during this operation.
	if err := s.requireUnversioned(ctx); err != nil {
		return "", err
	}
	return GCRemoved, nil
}
