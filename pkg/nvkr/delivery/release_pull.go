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

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"golang.org/x/sys/unix"
)

// PulledOutput describes a materialized exact named output. It is diagnostic data,
// not a signature, consumer-policy or release-acceptance receipt.
type PulledOutput struct {
	Output PublicationOutput `json:"output"`
	Files  []PulledFile      `json:"files,omitempty"`
	bundle BinaryBundle
}

// PulledFile describes a top-level payload file, relative to the destination.
// OCI layouts and APT repositories are represented by their complete directory.
type PulledFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size,omitempty"`
}

// PullOutput validates the complete release metadata/evidence closure and
// downloads only the exact named output's payload closure. Each supported format
// is checked by its delivery owner before atomically creating out without
// replacement. The destination parent must exist; workspace is an optional parent
// for owned scratch, which is removed on return. This checks byte integrity and
// membership, not signatures or consumer policy.
func PullOutput(ctx context.Context, reference, output, out, workspace string, options RegistryOptions) (PulledOutput, error) {
	return pullOutput(ctx, reference, output, out, workspace, options, "")
}

// PullExecutable retains the executable-only API and rejects other formats before
// payload retrieval. Use PullOutput to retrieve any supported release output.
func PullExecutable(ctx context.Context, reference, output, out, workspace string, options RegistryOptions) (BinaryBundle, error) {
	result, err := pullOutput(ctx, reference, output, out, workspace, options, "executable")
	return result.bundle, err
}

func pullOutput(ctx context.Context, reference, output, out, workspace string, options RegistryOptions, requiredFormat string) (PulledOutput, error) {
	if err := deliveryContext(ctx); err != nil {
		return PulledOutput{}, err
	}
	if err := ValidatePublicationReference(reference); err != nil {
		return PulledOutput{}, err
	}
	if !canonicalToken(output) || strings.ContainsAny(output, "/\\") {
		return PulledOutput{}, e.New(e.CodeUsage, "release pull requires an exact named output")
	}
	if strings.TrimSpace(out) == "" || strings.ContainsRune(out, '\x00') ||
		filepath.Base(filepath.Clean(out)) == "." || filepath.Base(filepath.Clean(out)) == ".." || filepath.Clean(out) == string(filepath.Separator) {
		return PulledOutput{}, e.New(e.CodeUsage, "release pull destination must name a new directory")
	}
	if err := options.Validate(); err != nil {
		return PulledOutput{}, err
	}
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		return PulledOutput{}, e.New(e.CodeEngineCapability, "release pull requires atomic no-replace directory publication")
	}
	if _, bounded := ctx.Deadline(); !bounded {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 20*time.Minute)
		defer cancel()
	}
	if workspace != "" {
		outAbsolute, err := resolvePullPath(ctx, out)
		if err != nil {
			return PulledOutput{}, classifyContext(ctx, e.Wrap(e.CodeUsage, err, "resolving release pull destination"))
		}
		workspaceAbsolute, err := resolvePullPath(ctx, workspace)
		if err != nil {
			return PulledOutput{}, classifyContext(ctx, e.Wrap(e.CodeUsage, err, "resolving release pull workspace"))
		}
		relative, err := filepath.Rel(outAbsolute, workspaceAbsolute)
		if err != nil || relative == "." || !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && relative != ".." {
			return PulledOutput{}, e.New(e.CodeUsage, "release pull workspace must be outside the destination directory")
		}
		workspace = workspaceAbsolute
	}
	if workspace != "" {
		if err := os.MkdirAll(workspace, 0o700); err != nil {
			return PulledOutput{}, e.Wrap(e.CodeImmutableViolation, err, "creating release pull workspace")
		}
	}
	stage, err := os.MkdirTemp(workspace, "nvkr-release-pull-*")
	if err != nil {
		return PulledOutput{}, e.Wrap(e.CodeImmutableViolation, err, "creating private release pull workspace")
	}
	defer func() { _ = os.RemoveAll(stage) }()
	layout := filepath.Join(stage, "metadata")
	inspection, err := PullMetadata(ctx, reference, layout, options)
	if err != nil {
		return PulledOutput{}, err
	}
	description, err := InspectMetadataDescription(ctx, layout, reference)
	if err != nil {
		return PulledOutput{}, err
	}
	var selected *PublicationOutput
	for i := range description.Outputs {
		if description.Outputs[i].Name == output {
			selected = &description.Outputs[i]
			break
		}
	}
	if selected == nil {
		return PulledOutput{}, e.New(e.CodeUsage, "release has no output named %q", output)
	}
	if requiredFormat != "" && selected.Kind != requiredFormat {
		return PulledOutput{}, e.New(e.CodeEngineCapability, "this pull requires %s outputs; %q has format %q", requiredFormat, output, selected.Kind)
	}
	var object PublishedObject
	for _, candidate := range inspection.Root.Objects {
		if candidate.Name == output {
			object = candidate
			break
		}
	}
	result := PulledOutput{Output: *selected}
	switch selected.Kind {
	case "executable":
		bundle, err := pullExecutableOutput(ctx, selected.Reference, out, layout, stage, object, options)
		if err != nil {
			return PulledOutput{}, err
		}
		result.bundle = bundle
		for _, entry := range bundle.Entries {
			result.Files = append(result.Files, PulledFile{Path: entry.Name, SHA256: entry.SHA256, Size: entry.Size})
		}
	case "disk-image":
		if err := pullDiskOutput(ctx, selected.Reference, out, layout, stage, object, options); err != nil {
			return PulledOutput{}, err
		}
		result.Files = []PulledFile{{Path: output + ".qcow2", SHA256: object.BlobDigest, Size: selected.Disk.Size}}
	case outputFormatHelm:
		file, err := pullHelmOutput(ctx, selected.Reference, out, layout, stage, object, options)
		if err != nil {
			return PulledOutput{}, err
		}
		result.Files = []PulledFile{file}
	case "apt-repository":
		if err := pullAPTOutput(ctx, selected.Reference, out, layout, stage, object, options); err != nil {
			return PulledOutput{}, err
		}
	case "oci":
		if err := pullImageOutput(ctx, selected.Reference, out, object, options); err != nil {
			return PulledOutput{}, err
		}
	default:
		return PulledOutput{}, e.New(e.CodeEngineCapability, "release pull does not support format %q", selected.Kind)
	}
	return result, nil
}

func pullExecutableOutput(ctx context.Context, reference, out, layout, stage string, object PublishedObject, options RegistryOptions) (BinaryBundle, error) {
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
	held, err := fetchExecutableLayer(ctx, reference, manifest.Layers[0], stage, options)
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
	if descriptor.MediaType != MediaBinaryBundleLayer {
		return nil, e.New(e.CodeVerifyFailed, "executable requires a binary bundle layer")
	}
	file, err := fetchPullLayer(ctx, reference, descriptor, workspace, options)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	held, err := heldfile.FromReaderContext(ctx, file, heldfile.Options{Label: "release executable layer", ExpectedDigest: descriptor.Digest, MaxBytes: max(descriptor.Size, 1)})
	if err != nil {
		return nil, classifyContext(ctx, e.Wrap(e.CodeVerifyFailed, err, "holding executable layer"))
	}
	return held, nil
}

func fetchPullLayer(ctx context.Context, reference string, descriptor Descriptor, workspace string, options RegistryOptions) (*os.File, error) {
	if !isDigest(descriptor.Digest) || descriptor.Size < 0 || descriptor.Size > maxDeliveryFileBytes || !validMediaType(descriptor.MediaType) {
		return nil, e.New(e.CodeVerifyFailed, "output layer descriptor is malformed or exceeds its byte bound")
	}
	parse := []name.Option{name.StrictValidation}
	if options.PlainHTTP {
		parse = append(parse, name.Insecure)
	}
	ref, err := name.NewDigest(reference, parse...)
	if err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "opening exact output repository")
	}
	layer, err := remote.Layer(ref.Context().Digest(descriptor.Digest), registryOptions(ctx, options)...)
	if err != nil {
		return nil, metadataFetchError(ctx, err, "fetch output layer")
	}
	reader, err := layer.Compressed()
	if err != nil {
		return nil, metadataFetchError(ctx, err, "open output layer")
	}
	defer func() { _ = reader.Close() }()
	path := filepath.Join(workspace, "payload-"+strings.TrimPrefix(descriptor.Digest, "sha256:"))
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, e.Wrap(e.CodeImmutableViolation, err, "creating private output download")
	}
	complete := false
	defer func() {
		if !complete {
			_ = file.Close()
		}
	}()
	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(file, hash), io.LimitReader(contextReader{ctx: ctx, reader: reader}, descriptor.Size+1))
	if err := deliveryContext(ctx); err != nil {
		return nil, err
	}
	got := "sha256:" + hex.EncodeToString(hash.Sum(nil))
	if written > descriptor.Size || written == descriptor.Size && got != descriptor.Digest {
		return nil, e.New(e.CodeVerifyFailed, "output layer differs from its exact descriptor")
	}
	if copyErr != nil {
		return nil, metadataFetchError(ctx, copyErr, "read output layer")
	}
	if written != descriptor.Size || got != descriptor.Digest {
		return nil, e.New(e.CodeVerifyFailed, "output layer differs from its exact descriptor")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, e.Wrap(e.CodeImmutableViolation, err, "rewinding private output download")
	}
	complete = true
	return file, nil
}

func extractPulledExecutable(ctx context.Context, held *heldfile.File, out string, bundle BinaryBundle) error {
	return publishPullDirectory(ctx, out, func(stage *os.File) error {
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
		if err := held.AssertUnchangedContext(ctx); err != nil {
			return classifyContext(ctx, e.Wrap(e.CodeVerifyFailed, err, "rechecking executable layer before commit"))
		}
		return nil
	})
}
