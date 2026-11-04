// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// Parts are synchronous and sequential, so cleanup never races a part writer.
// SDK Standard owns per-request retries, including embedded completion errors;
// only a 409 can start a bounded fresh upload after reconciliation and cleanup.
func (store *s3ObjectStore) putMultipart(ctx context.Context, object Object, body io.ReadSeeker) (stored StoredObject, readback *objectReadback, returnErr error) {
	uploadID := ""
	defer func() {
		if uploadID != "" {
			returnErr = errors.Join(returnErr, store.abortOwned(ctx, object.Key, uploadID))
		}
		if returnErr != nil {
			stored = StoredObject{}
			readback = nil
		}
	}()
	reader := &objectReaderAt{body: body}
	partSize := store.partSize(object.Size)
	for attempt := 0; attempt < store.options.MaxUploadAttempts; attempt++ {
		started, err := store.api.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{Bucket: aws.String(store.options.Bucket), ExpectedBucketOwner: aws.String(store.options.ExpectedBucketOwner),
			Key: aws.String(store.physicalKey(object.Key)), ContentType: aws.String(object.MediaType), ChecksumAlgorithm: types.ChecksumAlgorithmCrc32, ChecksumType: types.ChecksumTypeComposite},
			// Retrying an initiation with a lost response would strand an unknown ID.
			func(o *s3.Options) { o.RetryMaxAttempts = 1 })
		if err != nil {
			return stored, nil, objectFailure(ctx, err, "multipart initiation failed; unknown IDs require provider expiry")
		}
		if started == nil || aws.ToString(started.UploadId) == "" {
			return stored, nil, objectFailure(ctx, nil, "multipart initiation returned no ID; provider expiry required")
		}
		uploadID = *started.UploadId
		parts := make([]types.CompletedPart, 0, (object.Size-1)/partSize+1)
		for offset := int64(0); offset < object.Size; offset += partSize {
			length := min(partSize, object.Size-offset)
			number := int32(len(parts) + 1)
			part, err := store.api.UploadPart(ctx, &s3.UploadPartInput{Bucket: aws.String(store.options.Bucket), ExpectedBucketOwner: aws.String(store.options.ExpectedBucketOwner), Key: aws.String(store.physicalKey(object.Key)), UploadId: aws.String(uploadID),
				PartNumber: aws.Int32(number), Body: io.NewSectionReader(reader, offset, length), ContentLength: aws.Int64(length), ChecksumAlgorithm: types.ChecksumAlgorithmCrc32})
			if err != nil {
				return stored, nil, objectFailure(ctx, err, "multipart part failed")
			}
			if part == nil || aws.ToString(part.ETag) == "" || aws.ToString(part.ChecksumCRC32) == "" {
				return stored, nil, e.New(e.CodeVerifyFailed, "multipart part has no completion/checksum binding")
			}
			parts = append(parts, types.CompletedPart{PartNumber: aws.Int32(number), ETag: part.ETag, ChecksumCRC32: part.ChecksumCRC32})
		}
		completed, err := store.api.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{Bucket: aws.String(store.options.Bucket), ExpectedBucketOwner: aws.String(store.options.ExpectedBucketOwner), Key: aws.String(store.physicalKey(object.Key)), UploadId: aws.String(uploadID),
			MultipartUpload: &types.CompletedMultipartUpload{Parts: parts}, IfNoneMatch: aws.String("*"), ChecksumType: types.ChecksumTypeComposite})
		if err == nil {
			if completed == nil {
				return stored, nil, objectFailure(ctx, nil, "empty multipart completion response")
			}
			uploadID = ""
			return StoredObject{Object: object, VersionID: aws.ToString(completed.VersionId), ETag: aws.ToString(completed.ETag)}, nil, nil
		}
		if objectCode(err) == "NoSuchUpload" {
			uploadID = ""
		}
		if !objectMayHaveCommitted(err) {
			return stored, nil, objectFailure(ctx, err, "multipart completion failed")
		}
		observed, proof, reconcileErr := store.reconcile(ctx, object)
		if reconcileErr == nil {
			return observed, proof, nil
		}
		if objectConflict(reconcileErr) || objectStatus(err) != 409 {
			return stored, nil, reconcileErr
		}
		if err := store.abortOwned(ctx, object.Key, uploadID); err != nil {
			uploadID = ""
			return stored, nil, err
		}
		uploadID = ""
		if err := deliveryContext(ctx); err != nil {
			return stored, nil, err
		}
	}
	return stored, nil, objectFailure(ctx, nil, "multipart restart budget exhausted; remote outcome remains unknown")
}

// Only the known upload ID is cleaned up. WithoutCancel retains metadata while
// a fresh finite deadline permits cleanup after caller cancellation/store Close.
// No AfterFunc closes over a reassigned context and no detached worker is used.
func (store *s3ObjectStore) abortOwned(ctx context.Context, key, id string) error {
	if id == "" {
		return nil
	}
	deadline, _ := store.lifetime.Deadline()
	deadline = deadline.Add(store.options.CleanupTimeout)
	if fromNow := time.Now().Add(store.options.CleanupTimeout); fromNow.Before(deadline) {
		deadline = fromNow
	}
	cleanup, cancel := context.WithDeadline(context.WithoutCancel(ctx), deadline)
	defer cancel()
	for attempt := 0; attempt < store.options.MaxUploadAttempts; attempt++ {
		_, err := store.api.AbortMultipartUpload(cleanup, &s3.AbortMultipartUploadInput{Bucket: aws.String(store.options.Bucket), ExpectedBucketOwner: aws.String(store.options.ExpectedBucketOwner), Key: aws.String(store.physicalKey(key)), UploadId: aws.String(id)})
		if objectCode(err) == "NoSuchUpload" {
			return nil
		}
		if err != nil {
			return objectFailure(cleanup, err, "owned multipart cleanup failed; provider expiry required")
		}
		_, err = store.api.ListParts(cleanup, &s3.ListPartsInput{Bucket: aws.String(store.options.Bucket), ExpectedBucketOwner: aws.String(store.options.ExpectedBucketOwner), Key: aws.String(store.physicalKey(key)), UploadId: aws.String(id)})
		if objectCode(err) == "NoSuchUpload" {
			return nil
		}
		if err != nil {
			return objectFailure(cleanup, err, "owned multipart cleanup could not be confirmed")
		}
	}
	return e.New(e.CodeUnavailable, "owned multipart cleanup remains unconfirmed; provider expiry required")
}

type objectReaderAt struct {
	mu   sync.Mutex
	body io.ReadSeeker
}

func (reader *objectReaderAt) ReadAt(buffer []byte, offset int64) (int, error) {
	reader.mu.Lock()
	defer reader.mu.Unlock()
	if _, err := reader.body.Seek(offset, io.SeekStart); err != nil {
		return 0, err
	}
	return io.ReadFull(reader.body, buffer)
}
