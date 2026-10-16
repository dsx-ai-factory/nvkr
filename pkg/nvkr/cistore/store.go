// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package cistore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	"github.com/dsx-ai-factory/nvkr/internal/transportarchive"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
	"github.com/google/go-containerregistry/pkg/v1/types"
	"golang.org/x/sys/unix"
)

// Upload checks package privacy before accessing payload files. A missing package
// is seeded with metadata alone, then checked again; no binary fallback exists.
func Upload(ctx context.Context, request Request, root string, paths []string, opts Options) (Locator, error) {
	return newBackend().upload(ctx, request, root, paths, opts)
}

func (b *backend) upload(ctx context.Context, request Request, root string, paths []string, opts Options) (Locator, error) {
	ctx, cancel, opts, err := admit(ctx, request, opts)
	if err != nil {
		return Locator{}, err
	}
	defer cancel()
	source, err := b.source(ctx, request.Repository, opts)
	if err != nil {
		return Locator{}, err
	}
	packageRepository, _ := name.NewRepository(request.packageName(), name.StrictValidation)
	if err := b.ensurePrivatePackage(ctx, packageRepository, source, opts); err != nil {
		return Locator{}, err
	}
	clean, prefix, err := uploadPaths(ctx, root, paths)
	if err != nil {
		return Locator{}, err
	}
	temporary, err := os.MkdirTemp(root, ".nvkr-ci-upload-*")
	if err != nil {
		return Locator{}, e.Wrap(e.CodeBuildFailed, err, "creating private CI carrier staging")
	}
	defer os.RemoveAll(temporary)
	archive := filepath.Join(temporary, "payload.tar")
	if err := transportarchive.Pack(ctx, archive, root, clean); err != nil {
		return Locator{}, err
	}
	file, info, err := securepath.OpenRegular(archive)
	if err != nil {
		return Locator{}, e.Wrap(e.CodeVerifyFailed, err, "holding packed CI carrier")
	}
	defer file.Close()
	digest, err := fileDigest(ctx, file, info.Size())
	if err != nil {
		return Locator{}, err
	}
	l := Locator{SchemaVersion: SchemaVersion, Request: request, ArchiveDigest: digest, ArchiveSize: info.Size(), Paths: clean, StripPrefix: prefix}
	layer, err := tarball.LayerFromOpener(func() (io.ReadCloser, error) {
		return io.NopCloser(contextReader{ctx, io.NewSectionReader(file, 0, info.Size())}), nil
	}, tarball.WithMediaType(types.OCILayer))
	if err != nil {
		return Locator{}, safeIOError(ctx, e.CodeBuildFailed, "constructing CI carrier filesystem layer")
	}
	if size, err := layer.Size(); err != nil || size <= 0 || size > transportarchive.MaxArchiveBytes {
		return Locator{}, safeIOError(ctx, e.CodeVerifyFailed, "CI carrier layer exceeds its byte bound")
	}
	image, err := carrierImage(layer, l)
	if err != nil {
		return Locator{}, err
	}
	imageDigest, err := image.Digest()
	if err != nil {
		return Locator{}, safeIOError(ctx, e.CodeVerifyFailed, "computing CI carrier image digest")
	}
	l.Reference = request.packageName() + "@" + imageDigest.String()
	if err := l.Validate(request); err != nil {
		return Locator{}, err
	}
	// Recheck after packing and immediately before the first payload network
	// write. This cannot lock an external package's future visibility settings.
	if ok, err := b.checkPrivacy(ctx, request, source, opts); err != nil {
		return Locator{}, err
	} else if !ok {
		return Locator{}, e.New(e.CodeTrustPolicyMissing, "CI storage package disappeared before payload upload")
	}
	ref, _ := name.NewDigest(l.Reference, name.StrictValidation)
	if err := remote.Write(ref, image, b.registryOptions(ctx, opts)...); err != nil {
		return Locator{}, safeIOError(ctx, e.CodeFetchFailed, "uploading private CI carrier")
	}
	current, err := file.Stat()
	if err != nil || !securepath.SameMetadata(info, current) {
		return Locator{}, e.New(e.CodeVerifyFailed, "held CI archive changed during upload")
	}
	return l, nil
}

func carrierImage(layer v1.Layer, l Locator) (v1.Image, error) {
	raw, err := l.bindingBytes()
	if err != nil {
		return nil, err
	}
	image, err := mutate.AppendLayers(empty.Image, layer)
	if err != nil {
		return nil, e.New(e.CodeInternal, "constructing CI carrier image")
	}
	image, err = mutate.Config(image, v1.Config{Labels: map[string]string{BindingLabel: string(raw)}, Cmd: []string{"/__nvkr_ci_carrier_not_executable__"}})
	if err != nil {
		return nil, e.New(e.CodeInternal, "binding CI carrier metadata")
	}
	config, err := image.ConfigFile()
	if err != nil {
		return nil, e.New(e.CodeInternal, "reading CI carrier config")
	}
	// This fixed transport platform is never a claim about payload architecture.
	// The carrier has no executable process and consumers only unpack its TAR.
	config.OS, config.Architecture = "linux", "amd64"
	image, err = mutate.ConfigFile(image, config)
	if err != nil {
		return nil, e.New(e.CodeInternal, "setting CI carrier transport platform")
	}
	return mutate.ConfigMediaType(mutate.MediaType(image, types.OCIManifestSchema1), types.OCIConfigJSON), nil
}

// Download verifies exact request, private access policy, image/config binding
// and archive bytes before restoring files. Existing files are never replaced.
func Download(ctx context.Context, request Request, locator Locator, destination string, opts Options) error {
	return newBackend().download(ctx, request, locator, destination, opts)
}

func (b *backend) download(ctx context.Context, request Request, l Locator, destination string, opts Options) error {
	ctx, cancel, opts, err := admit(ctx, request, opts)
	if err != nil {
		return err
	}
	defer cancel()
	if err := l.Validate(request); err != nil {
		return err
	}
	source, err := b.source(ctx, request.Repository, opts)
	if err != nil {
		return err
	}
	if ok, err := b.checkPrivacy(ctx, request, source, opts); err != nil {
		return err
	} else if !ok {
		return e.New(e.CodeTrustPolicyMissing, "CI storage package is missing")
	}
	ref, _ := name.NewDigest(l.Reference, name.StrictValidation)
	image, err := remote.Image(ref, b.registryOptions(ctx, opts)...)
	if err != nil {
		return safeIOError(ctx, e.CodeFetchFailed, "fetching private CI carrier manifest")
	}
	manifest, err := image.Manifest()
	if err != nil || manifest.MediaType != types.OCIManifestSchema1 || len(manifest.Layers) != 1 || manifest.Config.Size <= 0 || manifest.Config.Size > MaxLocatorBytes*2 || manifest.Config.MediaType != types.OCIConfigJSON || manifest.Layers[0].MediaType != types.OCILayer || manifest.Layers[0].Size <= 0 || manifest.Layers[0].Size > transportarchive.MaxArchiveBytes {
		return e.New(e.CodeVerifyFailed, "CI carrier requires one exact bounded filesystem layer and config")
	}
	actual, err := image.Digest()
	if err != nil || actual.String() != ref.DigestStr() {
		return e.New(e.CodeVerifyFailed, "CI carrier image digest mismatch")
	}
	config, err := image.ConfigFile()
	if err != nil {
		return safeIOError(ctx, e.CodeVerifyFailed, "reading CI carrier config")
	}
	want, err := l.bindingBytes()
	if err != nil {
		return err
	}
	if config.Config.Labels[BindingLabel] != string(want) || len(config.RootFS.DiffIDs) != 1 || config.RootFS.DiffIDs[0].String() != l.ArchiveDigest {
		return e.New(e.CodeVerifyFailed, "CI carrier config does not bind the exact locator")
	}
	layers, err := image.Layers()
	if err != nil || len(layers) != 1 {
		return e.New(e.CodeVerifyFailed, "reading CI carrier filesystem layer")
	}
	return restoreLayer(ctx, layers[0], l, destination)
}

func (b *backend) registryOptions(ctx context.Context, opts Options) []remote.Option {
	return []remote.Option{remote.WithContext(ctx), remote.WithAuth(authn.FromConfig(authn.AuthConfig{Username: opts.Username, Password: opts.Token})), remote.WithTransport(b.transport), remote.WithUserAgent("nvkr-ci-carrier")}
}

func uploadPaths(ctx context.Context, root string, paths []string) ([]string, string, error) {
	if len(paths) == 0 || len(paths) > 4096 {
		return nil, "", e.New(e.CodeUsage, "CI carrier requires bounded workspace paths")
	}
	clean := slices.Clone(paths)
	slices.Sort(clean)
	rootHandle, err := os.OpenRoot(root)
	if err != nil {
		return nil, "", e.Wrap(e.CodeBuildFailed, err, "opening CI carrier workspace")
	}
	defer func() { _ = rootHandle.Close() }()
	prefix := ""
	seen := map[string]bool{}
	for i, p := range clean {
		if err := ctx.Err(); err != nil {
			return nil, "", e.Wrap(e.CodeTimeout, err, "selecting CI carrier roots")
		}
		if !safePath(p) || seen[p] {
			return nil, "", e.New(e.CodeUsage, "CI carrier requires unique canonical workspace paths")
		}
		for ancestor := path.Dir(p); ancestor != "."; ancestor = path.Dir(ancestor) {
			if seen[ancestor] {
				return nil, "", e.New(e.CodeUsage, "CI carrier workspace roots overlap")
			}
		}
		seen[p] = true
		info, err := rootHandle.Lstat(filepath.FromSlash(p))
		if err != nil {
			return nil, "", e.Wrap(e.CodeBuildFailed, err, "checking CI carrier root")
		}
		base := path.Dir(p)
		if info.IsDir() {
			base = p
		} else if !info.Mode().IsRegular() {
			return nil, "", e.New(e.CodeVerifyFailed, "CI carrier roots must be regular files or directories")
		}
		if base == "." {
			base = ""
		}
		if i == 0 {
			prefix = base
		} else {
			for prefix != "" && base != prefix && !strings.HasPrefix(base, prefix+"/") {
				prefix = path.Dir(prefix)
				if prefix == "." {
					prefix = ""
				}
			}
		}
	}
	return clean, prefix, nil
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

func fileDigest(ctx context.Context, file *os.File, size int64) (string, error) {
	if size <= 0 || size > transportarchive.MaxArchiveBytes {
		return "", e.New(e.CodeVerifyFailed, "CI carrier archive exceeds its byte bound")
	}
	hash := sha256.New()
	n, err := io.Copy(hash, contextReader{ctx, io.NewSectionReader(file, 0, size)})
	if err != nil || n != size {
		return "", safeIOError(ctx, e.CodeVerifyFailed, "hashing held CI archive")
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

func restoreLayer(ctx context.Context, layer v1.Layer, l Locator, destination string) error {
	if err := os.MkdirAll(destination, 0o700); err != nil {
		return e.Wrap(e.CodeBuildFailed, err, "creating private CI destination")
	}
	root, _, err := securepath.OpenDirectory(destination)
	if err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "holding CI destination directory")
	}
	defer root.Close()
	temporary, err := os.MkdirTemp(destination, ".nvkr-ci-download-*")
	if err != nil {
		return e.Wrap(e.CodeBuildFailed, err, "creating private CI download staging")
	}
	defer os.RemoveAll(temporary)
	file, err := os.OpenFile(filepath.Join(temporary, "payload.tar"), os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return e.Wrap(e.CodeBuildFailed, err, "creating CI download archive")
	}
	defer file.Close()
	reader, err := layer.Uncompressed()
	if err != nil {
		return safeIOError(ctx, e.CodeFetchFailed, "opening private CI payload")
	}
	hash := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(file, hash), contextReader{ctx, io.LimitReader(reader, l.ArchiveSize+1)})
	closeErr := reader.Close()
	if copyErr != nil || closeErr != nil {
		return safeIOError(ctx, e.CodeFetchFailed, "reading private CI payload")
	}
	if n != l.ArchiveSize || "sha256:"+hex.EncodeToString(hash.Sum(nil)) != l.ArchiveDigest {
		return e.New(e.CodeVerifyFailed, "private CI payload size or digest mismatch")
	}
	restored := filepath.Join(temporary, "restored")
	if err := transportarchive.UnpackOpen(ctx, file, restored, l.Paths); err != nil {
		return err
	}
	if _, actualPrefix, err := uploadPaths(ctx, restored, l.Paths); err != nil {
		return err
	} else if actualPrefix != l.StripPrefix {
		return e.New(e.CodeVerifyFailed, "CI locator does not describe the actual archive least common ancestor")
	}
	projected := restored
	if l.StripPrefix != "" {
		projected = filepath.Join(restored, filepath.FromSlash(l.StripPrefix))
	}
	stage, _, err := securepath.OpenDirectory(projected)
	if err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "holding CI archive projection")
	}
	defer stage.Close()
	entries, err := stage.ReadDir(-1)
	if err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "reading CI archive projection")
	}
	slices.SortFunc(entries, func(a, b os.DirEntry) int { return strings.Compare(a.Name(), b.Name()) })
	for _, entry := range entries {
		var info unix.Stat_t
		if err := unix.Fstatat(int(root.Fd()), entry.Name(), &info, unix.AT_SYMLINK_NOFOLLOW); err == nil {
			return e.New(e.CodeImmutableViolation, "CI carrier destination already exists")
		} else if !errors.Is(err, unix.ENOENT) {
			return e.Wrap(e.CodeVerifyFailed, err, "checking CI carrier destination")
		}
	}
	moved := []string{}
	rollback := func(cause error) error {
		var rollbackErr error
		for _, member := range slices.Backward(moved) {
			rollbackErr = errors.Join(rollbackErr, transportarchive.PublishAtNoReplace(int(root.Fd()), member, int(stage.Fd()), member))
		}
		if rollbackErr != nil {
			return e.Wrap(e.CodeImmutableViolation, errors.Join(cause, rollbackErr), "CI carrier projection rollback failed")
		}
		return cause
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return rollback(e.Wrap(e.CodeTimeout, err, "restoring CI carrier canceled"))
		}
		if err := transportarchive.PublishAtNoReplace(int(stage.Fd()), entry.Name(), int(root.Fd()), entry.Name()); err != nil {
			return rollback(e.Wrap(e.CodeImmutableViolation, err, "publishing CI carrier projection"))
		}
		moved = append(moved, entry.Name())
	}
	if err := root.Sync(); err != nil {
		return rollback(e.Wrap(e.CodeImmutableViolation, err, "persisting CI carrier projection"))
	}
	return nil
}
