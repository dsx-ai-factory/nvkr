// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"context"
	"io"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// Object describes exact blob bytes. Key is canonical and relative to the store's
// configured prefix; the provider joins that prefix exactly once. SHA256 uses "sha256:" followed by 64
// lowercase hexadecimal characters; it is blob integrity, not structured identity.
type Object struct {
	Key, SHA256, MediaType string
	Size                   int64
}

// StoredObject describes a provider observation, never signature or Run authority.
type StoredObject struct {
	Object
	VersionID, ETag string
}

// ObjectStore transports every artifact as bytes. The publication owner supplies
// immutable replayable sources, preflights its complete inventory, and verifies
// one complete OpenPinned read before issuing any delivery receipt. PutIfAbsent
// never overwrites an existing key. OpenPinned authenticates the requested
// version/strong validator and size; its caller still checks the complete SHA256.
// Close cancels and joins active work and closes outstanding response bodies.
// Do not call Close from an active source or token callback.
type ObjectStore interface {
	PutIfAbsent(context.Context, Object, io.ReadSeeker) (StoredObject, error)
	OpenPinned(context.Context, StoredObject) (io.ReadCloser, error)
	Close() error
}

// ObjectStoreOptions selects the provider; only S3 is implemented.
type ObjectStoreOptions struct {
	Provider string
	S3       S3ObjectStoreOptions
}

// NewObjectStore admits a byte transport, not publication or signing authority.
func NewObjectStore(ctx context.Context, options ObjectStoreOptions) (ObjectStore, error) {
	if err := deliveryContext(ctx); err != nil {
		return nil, err
	}
	if options.Provider != "s3" {
		return nil, objectCapability("unsupported object-store provider")
	}
	return newS3ObjectStore(ctx, options.S3)
}

// Only delivery-issued stores can offer this optimization. Generic providers
// retain the public byte API and the mirror performs its independent full read.
type objectStoreReadback interface {
	putIfAbsentWithReadback(context.Context, Object, io.ReadSeeker) (StoredObject, *objectReadback, error)
}

// An in-process observation of one exact completed read, never serialized or a
// caller boolean. It grants no signature or Run authority and holds no bytes.
type objectReadback struct {
	store  *s3ObjectStore
	stored StoredObject
}

func (receipt *objectReadback) require(ctx context.Context, store ObjectStore, stored StoredObject) error {
	if err := deliveryContext(ctx); err != nil {
		return err
	}
	owner, ok := store.(*s3ObjectStore)
	if receipt == nil || !ok || owner == nil || owner.lifetime == nil || receipt.store != owner || receipt.stored != stored {
		return e.New(e.CodeVerifyFailed, "object readback does not bind this store, object and exact pin")
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.closed || owner.lifetime.Err() != nil {
		return e.New(e.CodeTimeout, "object readback store lifetime ended")
	}
	return nil
}
