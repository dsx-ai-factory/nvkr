// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	"github.com/dsx-ai-factory/nvkr/internal/transportarchive"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"golang.org/x/sys/unix"
)

// PullExecutable checks the complete release metadata/evidence closure and then
// downloads only the exact named executable output. It verifies bundle membership,
// file bytes and modes before atomically creating out without replacement. The
// parent of out must exist. workspace is an optional parent for owned scratch;
// private scratch is removed on return. This operation establishes selected byte
// integrity and membership, not signature verification or policy acceptance.
func PullExecutable(ctx context.Context, reference, output, out, workspace string, options RegistryOptions) (BinaryBundle, error) {
	if err := deliveryContext(ctx); err != nil {
		return BinaryBundle{}, err
	}
	if err := ValidatePublicationReference(reference); err != nil {
		return BinaryBundle{}, err
	}
	if !canonicalToken(output) || strings.ContainsAny(output, "/\\") {
		return BinaryBundle{}, e.New(e.CodeUsage, "release pull requires an exact named output")
	}
	if strings.TrimSpace(out) == "" || strings.ContainsRune(out, '\x00') ||
		filepath.Base(filepath.Clean(out)) == "." || filepath.Base(filepath.Clean(out)) == ".." || filepath.Clean(out) == string(filepath.Separator) {
		return BinaryBundle{}, e.New(e.CodeUsage, "release pull destination must name a new directory")
	}
	if err := options.Validate(); err != nil {
		return BinaryBundle{}, err
	}
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		return BinaryBundle{}, e.New(e.CodeEngineCapability, "release pull requires atomic no-replace directory publication")
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	if workspace != "" {
		outAbsolute, err := resolvePullPath(ctx, out)
		if err != nil {
			return BinaryBundle{}, classifyContext(ctx, e.Wrap(e.CodeUsage, err, "resolving release pull destination"))
		}
		workspaceAbsolute, err := resolvePullPath(ctx, workspace)
		if err != nil {
			return BinaryBundle{}, classifyContext(ctx, e.Wrap(e.CodeUsage, err, "resolving release pull workspace"))
		}
		relative, err := filepath.Rel(outAbsolute, workspaceAbsolute)
		if err != nil || relative == "." || !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && relative != ".." {
			return BinaryBundle{}, e.New(e.CodeUsage, "release pull workspace must be outside the destination directory")
		}
		workspace = workspaceAbsolute
	}
	if workspace != "" {
		if err := os.MkdirAll(workspace, 0o700); err != nil {
			return BinaryBundle{}, e.Wrap(e.CodeImmutableViolation, err, "creating release pull workspace")
		}
	}
	stage, err := os.MkdirTemp(workspace, "nvkr-release-pull-*")
	if err != nil {
		return BinaryBundle{}, e.Wrap(e.CodeImmutableViolation, err, "creating private release pull workspace")
	}
	defer func() { _ = os.RemoveAll(stage) }()
	layout := filepath.Join(stage, "metadata")
	inspection, err := PullMetadata(ctx, reference, layout, options)
	if err != nil {
		return BinaryBundle{}, err
	}
	description, err := InspectMetadataDescription(ctx, layout, reference)
	if err != nil {
		return BinaryBundle{}, err
	}
	var selected *PublicationOutput
	for i := range description.Outputs {
		if description.Outputs[i].Name == output {
			selected = &description.Outputs[i]
			break
		}
	}
	if selected == nil {
		return BinaryBundle{}, e.New(e.CodeUsage, "release has no output named %q", output)
	}
	if selected.Kind != "executable" || selected.ArtifactType != MediaBinaryBundle {
		return BinaryBundle{}, e.New(e.CodeEngineCapability, "release pull supports executable outputs; %q has format %q", output, selected.Kind)
	}
	var object PublishedObject
	for _, candidate := range inspection.Root.Objects {
		if candidate.Name == output {
			object = candidate
			break
		}
	}
	manifestRaw, err := readBlob(ctx, layout, Descriptor{Digest: object.DescriptorDigest, Size: object.Size}, 8<<20)
	if err != nil {
		return BinaryBundle{}, err
	}
	manifest, bundle, err := readBinaryBundleConfig(ctx, layout, manifestRaw)
	if err != nil {
		return BinaryBundle{}, e.Wrap(e.CodeVerifyFailed, err, "reading selected executable metadata")
	}
	if err := validatePulledBundle(bundle, object); err != nil {
		return BinaryBundle{}, err
	}
	held, err := fetchExecutableLayer(ctx, selected.Reference, manifest.Layers[0], stage, options)
	if err != nil {
		return BinaryBundle{}, err
	}
	defer func() { _ = held.Close() }()
	if err := extractPulledExecutable(ctx, held, out, bundle); err != nil {
		return BinaryBundle{}, err
	}
	return bundle, nil
}

// Resolve the existing prefix even when the final directory does not exist yet.
// This catches ordinary aliases such as /tmp -> /private/tmp before workspace
// creation can accidentally create the caller's destination.
func resolvePullPath(ctx context.Context, path string) (string, error) {
	current, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	var suffix []string
	for {
		if err := deliveryContext(ctx); err != nil {
			return "", err
		}
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			for i := len(suffix) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, suffix[i])
			}
			return resolved, nil
		}
		if !errors.Is(err, os.ErrNotExist) || current == filepath.Dir(current) {
			return "", err
		}
		suffix = append(suffix, filepath.Base(current))
		current = filepath.Dir(current)
	}
}

func validatePulledBundle(bundle BinaryBundle, object PublishedObject) error {
	semantic, err := bundle.Digest()
	if err != nil {
		return err
	}
	if object.Output == nil || object.Output.TransportPath != "" || semantic != object.SemanticDigest || len(bundle.Entries) > maxDeliveryObjects {
		return e.New(e.CodeVerifyFailed, "executable config differs from its release binding or exceeds its member bound")
	}
	members := make([]string, 0, len(bundle.Entries))
	var total int64
	for _, entry := range bundle.Entries {
		if entry.Size > maxDeliveryFileBytes-total {
			return e.New(e.CodeVerifyFailed, "executable bundle exceeds its expanded byte bound")
		}
		total += entry.Size
		members = append(members, entry.Name)
	}
	if !slices.Equal(members, object.Output.Components) || object.OS != "" && (object.OS != bundle.OS || object.Arch != bundle.Arch) {
		return e.New(e.CodeVerifyFailed, "executable payload members or platform differ from the exact named output")
	}
	return nil
}

func fetchExecutableLayer(ctx context.Context, reference string, descriptor Descriptor, workspace string, options RegistryOptions) (*heldfile.File, error) {
	if !isDigest(descriptor.Digest) || descriptor.Size < 0 || descriptor.Size > maxDeliveryFileBytes || descriptor.MediaType != MediaBinaryBundleLayer {
		return nil, e.New(e.CodeVerifyFailed, "executable layer descriptor is malformed or exceeds its byte bound")
	}
	parse := []name.Option{name.StrictValidation}
	if options.PlainHTTP {
		parse = append(parse, name.Insecure)
	}
	ref, err := name.NewDigest(reference, parse...)
	if err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "opening exact executable repository")
	}
	layer, err := remote.Layer(ref.Context().Digest(descriptor.Digest), registryOptions(ctx, options)...)
	if err != nil {
		return nil, metadataFetchError(ctx, err, "fetch executable layer")
	}
	reader, err := layer.Compressed()
	if err != nil {
		return nil, metadataFetchError(ctx, err, "open executable layer")
	}
	defer func() { _ = reader.Close() }()
	path := filepath.Join(workspace, "executable-layer")
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, e.Wrap(e.CodeImmutableViolation, err, "creating private executable download")
	}
	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(file, hash), io.LimitReader(contextReader{ctx: ctx, reader: reader}, descriptor.Size+1))
	closeErr := file.Close()
	if err := deliveryContext(ctx); err != nil {
		return nil, err
	}
	got := "sha256:" + hex.EncodeToString(hash.Sum(nil))
	if written > descriptor.Size || written == descriptor.Size && got != descriptor.Digest {
		return nil, e.New(e.CodeVerifyFailed, "executable layer differs from its exact descriptor")
	}
	if copyErr != nil {
		return nil, metadataFetchError(ctx, copyErr, "read executable layer")
	}
	if closeErr != nil {
		return nil, e.Wrap(e.CodeImmutableViolation, closeErr, "closing private executable download")
	}
	if written != descriptor.Size || got != descriptor.Digest {
		return nil, e.New(e.CodeVerifyFailed, "executable layer differs from its exact descriptor")
	}
	held, err := heldfile.OpenContext(ctx, path, heldfile.Options{Label: "release executable layer", ExpectedDigest: descriptor.Digest, MaxBytes: descriptor.Size})
	if err != nil {
		return nil, classifyContext(ctx, e.Wrap(e.CodeVerifyFailed, err, "holding executable layer"))
	}
	return held, nil
}

func extractPulledExecutable(ctx context.Context, held *heldfile.File, out string, bundle BinaryBundle) error {
	if err := deliveryContext(ctx); err != nil {
		return err
	}
	out = filepath.Clean(out)
	parent, _, err := securepath.OpenDirectory(filepath.Dir(out))
	if err != nil {
		return e.Wrap(e.CodeImmutableViolation, err, "opening executable destination parent (it must already exist)")
	}
	defer func() { _ = parent.Close() }()
	stage, temporary, err := createImageIndexStage(parent)
	if err != nil {
		return e.Wrap(e.CodeImmutableViolation, err, "creating private executable destination")
	}
	defer func() { _ = stage.Close() }()
	committed := false
	defer func() {
		if !committed {
			for _, entry := range bundle.Entries {
				_ = unix.Unlinkat(int(stage.Fd()), entry.Name, 0)
			}
			_ = unix.Unlinkat(int(parent.Fd()), temporary, unix.AT_REMOVEDIR)
		}
	}()
	create := func(entry BinaryEntry) (*os.File, error) {
		fd, err := unix.Openat(int(stage.Fd()), entry.Name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, entry.Mode)
		if err != nil {
			return nil, err
		}
		return os.NewFile(uintptr(fd), entry.Name), nil
	}
	if err := extractBundleLayerWith(ctx, held, bundle, create); err != nil {
		return classifyContext(ctx, err)
	}
	if err := stage.Sync(); err != nil {
		return e.Wrap(e.CodeImmutableViolation, err, "persisting executable directory")
	}
	if err := deliveryContext(ctx); err != nil {
		return err
	}
	if err := held.AssertUnchangedContext(ctx); err != nil {
		return classifyContext(ctx, e.Wrap(e.CodeVerifyFailed, err, "rechecking executable layer before commit"))
	}
	if err := transportarchive.PublishAtNoReplace(int(parent.Fd()), temporary, int(parent.Fd()), filepath.Base(out)); err != nil {
		return e.Wrap(e.CodeImmutableViolation, err, "committing executable directory without replacement")
	}
	committed = true
	if err := parent.Sync(); err != nil {
		return classifyContext(ctx, e.Wrap(e.CodeImmutableViolation, err, "persisting executable directory commit"))
	}
	return nil
}
