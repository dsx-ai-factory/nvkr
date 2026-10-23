// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

// PushDiskArtifact publishes only a standard disk transport by manifest digest.
// It creates no discovery tag and confers no release or qualification authority.
// The caller must admit space for one payload copy in scratch before calling.
func PushDiskArtifact(ctx context.Context, artifact *DiskArtifact, repository, scratch string, options RegistryOptions) (reference string, err error) {
	if err := deliveryContext(ctx); err != nil {
		return "", err
	}
	if _, bounded := ctx.Deadline(); !bounded {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 30*time.Minute)
		defer cancel()
	}
	if err := options.Validate(); err != nil {
		return "", err
	}
	parse := []name.Option{name.StrictValidation}
	if options.PlainHTTP {
		parse = append(parse, name.Insecure)
	}
	repo, err := name.NewRepository(repository, parse...)
	if err != nil || repo.Name() != repository || strings.ContainsAny(repository[strings.LastIndex(repository, "/")+1:], ":@") {
		return "", e.New(e.CodeUsage, "disk publication requires a canonical untagged repository")
	}
	if scratch == "" {
		return "", e.New(e.CodeUsage, "disk publication requires admitted scratch storage")
	}
	if err := artifact.RequireStandardProfile(ctx); err != nil {
		return "", err
	}
	stage, err := os.MkdirTemp(scratch, "nvkr-disk-push-*")
	if err != nil {
		return "", e.Wrap(e.CodeImmutableViolation, err, "creating disk upload scratch")
	}
	defer func() {
		if cleanupErr := os.RemoveAll(stage); cleanupErr != nil {
			err = errors.Join(err, e.Wrap(e.CodeImmutableViolation, cleanupErr, "removing disk upload scratch"))
		}
	}()
	root := filepath.Join(stage, "layout")
	if err := artifact.WriteLayout(ctx, root); err != nil {
		return "", err
	}
	view := artifact.Inspection()
	hash, err := v1.NewHash(view.ManifestDigest)
	if err != nil {
		return "", e.Wrap(e.CodeVerifyFailed, err, "reading disk manifest digest")
	}
	image, err := layout.Path(root).Image(hash)
	if err != nil {
		return "", e.Wrap(e.CodeVerifyFailed, err, "opening held disk upload")
	}
	ref := repo.Digest(view.ManifestDigest)
	if err := remote.Write(ref, image, registryOptions(ctx, options)...); err != nil {
		return ref.Name(), classifyContext(ctx, e.Wrap(e.CodeUnavailable, err, "publishing disk transport; destination may contain partial uploads"))
	}
	return ref.Name(), nil
}

// PullDiskArtifact materializes only an exact standard disk manifest and bounded
// payload. The caller admits capacity for download staging plus held export. No decoded metadata or
// registry location grants signing, provenance or qualification authority.
func PullDiskArtifact(ctx context.Context, reference, out, platform string, maxPayloadBytes int64, options RegistryOptions) (err error) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		return e.New(e.CodeEngineCapability, "disk download requires no-replace filesystem support")
	}
	if err := deliveryContext(ctx); err != nil {
		return err
	}
	if _, bounded := ctx.Deadline(); !bounded {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 30*time.Minute)
		defer cancel()
	}
	if err := options.Validate(); err != nil {
		return err
	}
	if _, err := diskPlatform(platform); err != nil {
		return err
	}
	if maxPayloadBytes <= 0 || maxPayloadBytes > maxDeliveryFileBytes {
		return e.New(e.CodeVerifyFailed, "disk download requires bounded payload capacity")
	}
	parse := []name.Option{name.StrictValidation}
	if options.PlainHTTP {
		parse = append(parse, name.Insecure)
	}
	ref, err := name.NewDigest(reference, parse...)
	if err != nil || ref.Name() != reference || !isDigest(ref.DigestStr()) {
		return e.New(e.CodeUnpinnedSource, "disk download requires a canonical digest reference")
	}
	remoteOptions := registryOptions(ctx, options)
	descriptor, err := remote.Get(ref, remoteOptions...)
	if err != nil {
		return classifyContext(ctx, e.Wrap(e.CodeUnavailable, err, "fetching disk manifest"))
	}
	if len(descriptor.Manifest) > 8<<20 || digestBytes(descriptor.Manifest) != ref.DigestStr() || string(descriptor.MediaType) != MediaOCIManifest {
		return e.New(e.CodeVerifyFailed, "registry disk manifest differs from requested identity")
	}
	image, err := descriptor.Image()
	if err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "opening registry disk manifest")
	}
	manifest, err := image.Manifest()
	if err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "reading registry disk descriptors")
	}
	if manifest.Config.Size <= 0 || manifest.Config.Size > 8<<20 || len(manifest.Layers) != 1 || manifest.Layers[0].Size <= 0 || manifest.Layers[0].Size > maxPayloadBytes {
		return e.New(e.CodeVerifyFailed, "registry disk descriptors exceed bounds")
	}
	configLayer, err := remote.Layer(ref.Context().Digest(manifest.Config.Digest.String()), remoteOptions...)
	if err != nil {
		return classifyContext(ctx, e.Wrap(e.CodeUnavailable, err, "fetching disk config"))
	}
	configStream, err := configLayer.Compressed()
	if err != nil {
		return classifyContext(ctx, e.Wrap(e.CodeUnavailable, err, "opening disk config stream"))
	}
	configRaw, readErr := io.ReadAll(io.LimitReader(configStream, manifest.Config.Size+1))
	if err := errors.Join(readErr, configStream.Close()); err != nil {
		return classifyContext(ctx, e.Wrap(e.CodeUnavailable, err, "reading disk config"))
	}
	if int64(len(configRaw)) != manifest.Config.Size || digestBytes(configRaw) != manifest.Config.Digest.String() {
		return e.New(e.CodeVerifyFailed, "disk config differs from its descriptor")
	}
	stage, _, cleanup, err := stageLayout(out)
	if err != nil {
		return err
	}
	defer cleanup()
	writer := layoutWriter{root: stage}
	if _, err := writer.addBlobBytes(configRaw, MediaImageConfig); err != nil {
		return err
	}
	md, err := writer.addBlobBytes(descriptor.Manifest, MediaOCIManifest)
	if err != nil {
		return err
	}
	checked, _, err := inspectDiskMetadata(ctx, stage, md, platform, maxPayloadBytes)
	if err != nil {
		return err
	}
	standard, err := json.Marshal(standardDiskManifest(Descriptor{MediaType: MediaImageConfig, Digest: checked.Config.Digest.String(), Size: checked.Config.Size}, checked.Layers[0].Digest.String(), checked.Layers[0].Size, checked.ArtifactType, checked.Annotations[diskEncryptionAnnotation]))
	if err != nil {
		return e.Wrap(e.CodeInternal, err, "encoding downloaded disk profile")
	}
	if !supportedDiskEncryption(checked.Annotations[diskEncryptionAnnotation]) || digestBytes(standard) != ref.DigestStr() {
		return e.New(e.CodeVerifyFailed, "registry disk differs from standard transport profile")
	}
	md.ArtifactType = checked.ArtifactType
	if _, err := writer.writeIndex(ociIndex{SchemaVersion: 2, MediaType: MediaOCIIndex, Manifests: []Descriptor{md}}); err != nil {
		return err
	}
	layer, err := remote.Layer(ref.Context().Digest(checked.Layers[0].Digest.String()), remoteOptions...)
	if err != nil {
		return classifyContext(ctx, e.Wrap(e.CodeUnavailable, err, "fetching disk payload"))
	}
	stream, err := layer.Compressed()
	if err != nil {
		return classifyContext(ctx, e.Wrap(e.CodeUnavailable, err, "opening disk payload stream"))
	}
	defer stream.Close()
	path := filepath.Join(stage, "blobs", "sha256", checked.Layers[0].Digest.Encoded())
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return e.Wrap(e.CodeImmutableViolation, err, "staging disk download")
	}
	size, copyErr := io.Copy(file, io.LimitReader(stream, checked.Layers[0].Size+1))
	if copyErr == nil {
		copyErr = file.Sync()
	}
	if err := errors.Join(copyErr, file.Close(), stream.Close()); err != nil {
		return classifyContext(ctx, e.Wrap(e.CodeUnavailable, err, "writing disk download"))
	}
	if size != checked.Layers[0].Size {
		return e.New(e.CodeVerifyFailed, "disk download size differs from descriptor")
	}
	owner, err := OpenDiskArtifact(ctx, stage, ref.DigestStr(), platform, maxPayloadBytes)
	if err != nil {
		return err
	}
	// Export from the authenticated held payload through the descriptor-relative
	// no-replace owner. The temporary download is never renamed over a caller's
	// destination, even if that destination appears while the stream is active.
	return errors.Join(owner.WriteLayout(ctx, out), owner.Close())
}
