// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"hash"
	"io"
	"os"
	"sync"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/partial"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/types"
)

// A disk upload borrows the existing checked inode instead of copying a QCOW2
// into another OCI layout. The bounded control bytes are detached; every upload
// body is a fresh, independently hashed section of the same no-follow descriptor.
// This is transport integrity, not a restored producer or release capability.
func pushRegistryDisk(ctx context.Context, root string, object PublishedObject, ref name.Digest, options RegistryOptions) (resultErr error) {
	if ref.DigestStr() != object.DescriptorDigest {
		return e.New(e.CodeVerifyFailed, "disk publication destination differs from its exact manifest")
	}
	image, owner, err := registryDiskImage(ctx, root, object)
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, owner.Close()) }()
	if err := remote.Write(ref, image, registryOptions(ctx, options)...); err != nil {
		// Preserve a local integrity rejection even when HTTP wraps the body
		// failure or the registry answers after receiving only part of it.
		if local := owner.failure(); local != nil {
			return local
		}
		return classifyContext(ctx, e.Wrap(e.CodeUnavailable, err, "publishing disk deliverable %q", object.Name))
	}
	// Registry deduplication may skip every Compressed call, including when the
	// entire manifest already exists. Rehash the held source regardless before
	// this output can advance the release-root publication.
	if _, err := ResolveDigest(ctx, ref.Name(), options); err != nil {
		return err
	}
	return owner.verify()
}

func registryDiskImage(ctx context.Context, root string, object PublishedObject) (image v1.Image, owner *registryDiskOwner, resultErr error) {
	if err := deliveryContext(ctx); err != nil {
		return nil, nil, err
	}
	if object.Kind != "disk-image" || object.Output == nil || object.Output.Format != "disk-image" || object.Output.Disk == nil {
		return nil, nil, e.New(e.CodeVerifyFailed, "disk publication requires an exact disk output binding")
	}
	descriptor := Descriptor{MediaType: MediaOCIManifest, Digest: object.DescriptorDigest, Size: object.Size, ArtifactType: object.ArtifactType}
	disk, err := openDiskManifest(ctx, root, descriptor, object.OS+"/"+object.Arch, maxDeliveryFileBytes)
	if err != nil {
		return nil, nil, err
	}
	complete := false
	defer func() {
		if !complete {
			resultErr = errors.Join(resultErr, disk.Close())
		}
	}()
	if err := verifyPublishedObjectBinding(ctx, root, object, descriptor,
		map[string]DiskArtifactInspection{descriptor.Digest: disk.Inspection()}); err != nil {
		return nil, nil, err
	}
	manifestRaw, err := readBlob(ctx, root, descriptor, 8<<20)
	if err != nil {
		return nil, nil, err
	}
	var manifest ociManifest // The exact bytes were checked by the disk owner.
	if err := json.Unmarshal(manifestRaw, &manifest); err != nil {
		return nil, nil, e.Wrap(e.CodeVerifyFailed, err, "decoding checked disk manifest")
	}
	configRaw, err := readBlob(ctx, root, manifest.Config, 8<<20)
	if err != nil {
		return nil, nil, err
	}
	info, err := disk.owner.file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != disk.inspection.Size {
		return nil, nil, e.New(e.CodeVerifyFailed, "held disk changed before upload admission")
	}
	operation, cancel := context.WithCancel(ctx)
	owner = &registryDiskOwner{ctx: operation, cancel: cancel, disk: disk, info: info}
	core := &registryDiskCore{owner: owner, manifest: manifestRaw, config: configRaw, layer: manifest.Layers[0]}
	image, err = partial.CompressedToImage(core)
	if err != nil {
		cancel()
		return nil, nil, e.Wrap(e.CodeVerifyFailed, err, "adapting checked disk for publication")
	}
	complete = true
	return image, owner, nil
}

// Reads share a descriptor but never an offset. Close cancels future operations,
// joins current file reads under mu and revokes all outstanding body adapters.
// No upload body owns a goroutine, pipe, file or payload-sized buffer.
type registryDiskOwner struct {
	mu     sync.RWMutex
	ctx    context.Context
	cancel context.CancelFunc
	disk   *DiskArtifact
	info   os.FileInfo
	closed bool
	errMu  sync.Mutex
	err    error
}

func (owner *registryDiskOwner) failure() error {
	owner.errMu.Lock()
	defer owner.errMu.Unlock()
	return owner.err
}

func (owner *registryDiskOwner) reject(err error) error {
	owner.errMu.Lock()
	defer owner.errMu.Unlock()
	if owner.err == nil {
		owner.err = err
	}
	return owner.err
}

// Caller holds at least a read lock. A remembered stream integrity failure is
// terminal even if a later HTTP retry would observe restored source bytes.
func (owner *registryDiskOwner) usable() error {
	if owner.closed || owner.disk.owner.file == nil {
		return e.New(e.CodeGateUnmet, "disk publication source is closed")
	}
	if err := deliveryContext(owner.ctx); err != nil {
		return err
	}
	return owner.failure()
}

func (owner *registryDiskOwner) unchanged() error {
	if err := owner.usable(); err != nil {
		return err
	}
	info, err := owner.disk.owner.file.Stat()
	if err != nil || !securepath.SameMetadata(owner.info, info) {
		return owner.reject(e.New(e.CodeVerifyFailed, "held disk changed during publication"))
	}
	return nil
}

func (owner *registryDiskOwner) check() error {
	owner.mu.RLock()
	defer owner.mu.RUnlock()
	return owner.unchanged()
}

func (owner *registryDiskOwner) body() (io.ReadCloser, error) {
	owner.mu.RLock()
	defer owner.mu.RUnlock()
	if err := owner.unchanged(); err != nil {
		return nil, err
	}
	return &registryDiskReader{owner: owner, hash: sha256.New()}, nil
}

func (owner *registryDiskOwner) verify() error {
	reader, err := owner.body()
	if err != nil {
		return err
	}
	_, readErr := io.CopyBuffer(io.Discard, reader, make([]byte, 128<<10))
	return errors.Join(readErr, reader.Close())
}

func (owner *registryDiskOwner) Close() error {
	owner.cancel()
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.closed {
		return nil
	}
	owner.closed = true
	return owner.disk.Close()
}

type registryDiskCore struct {
	owner            *registryDiskOwner
	manifest, config []byte
	layer            Descriptor
}

func (image *registryDiskCore) RawManifest() ([]byte, error) {
	return bytes.Clone(image.manifest), image.owner.check()
}
func (image *registryDiskCore) RawConfigFile() ([]byte, error) {
	return bytes.Clone(image.config), image.owner.check()
}
func (image *registryDiskCore) MediaType() (types.MediaType, error) {
	return types.MediaType(MediaOCIManifest), image.owner.check()
}
func (image *registryDiskCore) LayerByDigest(digest v1.Hash) (partial.CompressedLayer, error) {
	if err := image.owner.check(); err != nil {
		return nil, err
	}
	if digest.String() != image.layer.Digest {
		return nil, e.New(e.CodeVerifyFailed, "disk layer is absent from its checked manifest")
	}
	return &registryDiskLayer{owner: image.owner, descriptor: image.layer}, nil
}

type registryDiskLayer struct {
	owner      *registryDiskOwner
	descriptor Descriptor
}

func (layer *registryDiskLayer) Digest() (v1.Hash, error) {
	if err := layer.owner.check(); err != nil {
		return v1.Hash{}, err
	}
	return v1.NewHash(layer.descriptor.Digest)
}
func (layer *registryDiskLayer) Size() (int64, error) {
	return layer.descriptor.Size, layer.owner.check()
}
func (layer *registryDiskLayer) MediaType() (types.MediaType, error) {
	return types.MediaType(layer.descriptor.MediaType), layer.owner.check()
}
func (layer *registryDiskLayer) Compressed() (io.ReadCloser, error) { return layer.owner.body() }

type registryDiskReader struct {
	mu     sync.Mutex
	owner  *registryDiskOwner
	hash   hash.Hash
	offset int64
	closed bool
}

func (reader *registryDiskReader) Read(buffer []byte) (int, error) {
	reader.mu.Lock()
	defer reader.mu.Unlock()
	reader.owner.mu.RLock()
	defer reader.owner.mu.RUnlock()
	if reader.closed {
		return 0, e.New(e.CodeGateUnmet, "disk upload body is closed")
	}
	if err := reader.owner.usable(); err != nil {
		return 0, err
	}
	if len(buffer) == 0 {
		return 0, nil
	}
	size := reader.owner.disk.inspection.Size
	if reader.offset == size {
		return 0, io.EOF
	}
	buffer = buffer[:min(int64(len(buffer)), size-reader.offset)]
	n, readErr := reader.owner.disk.owner.file.ReadAt(buffer, reader.offset)
	_, _ = reader.hash.Write(buffer[:n])
	reader.offset += int64(n)
	if readErr != nil && readErr != io.EOF {
		return n, reader.owner.reject(classifyContext(reader.owner.ctx, e.Wrap(e.CodeVerifyFailed, readErr, "reading held disk upload")))
	}
	if err := reader.owner.usable(); err != nil {
		return n, err
	}
	if reader.offset == size {
		if err := reader.owner.unchanged(); err != nil {
			return n, err
		}
		if "sha256:"+hex.EncodeToString(reader.hash.Sum(nil)) != reader.owner.disk.inspection.PayloadDigest {
			return n, reader.owner.reject(e.New(e.CodeVerifyFailed, "disk upload stream differs from its exact payload digest"))
		}
		return n, io.EOF
	}
	if readErr != nil || n == 0 {
		return n, reader.owner.reject(e.New(e.CodeVerifyFailed, "disk upload stream ended before its exact size"))
	}
	return n, nil
}

func (reader *registryDiskReader) Close() error {
	reader.mu.Lock()
	defer reader.mu.Unlock()
	reader.closed = true
	return nil
}
