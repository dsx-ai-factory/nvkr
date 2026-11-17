// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/ociclosure"
	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

const (
	maxOCIManifestBytes = 8 << 20
	maxOCITreeEntries   = 100_000
)

// OCITransport is the narrow acquisition seam for the prebuilt OCI lane. Its
// implementation must publish a complete local OCI layout atomically and verify it
// without a container daemon, socket, or network access. The default implementation
// uses anonymous registry authentication; an operator can inject an explicit
// credential adapter without leaking ambient Docker credentials into build jobs.
type OCITransport interface {
	Acquire(ctx context.Context, digestRef, destination string) (digest string, size int64, err error)
	Verify(ctx context.Context, digestRef, layoutPath string) error
}

type registryOCITransport struct{}

// This private built-in path consumes the pin owner's original image. Legacy
// injected adapters remain compatible for native-only pulls, but cannot claim
// parent/native authority by implementing a descriptive optional interface.
func (registryOCITransport) acquireNative(ctx context.Context, parentRef, nativeRef, platform, destination string) (string, int64, error) {
	parent, _, err := parseDigestReference(parentRef)
	if err != nil {
		return "", 0, err
	}
	native, want, err := parseDigestReference(nativeRef)
	if err != nil {
		return "", 0, err
	}
	if parent.Context().Name() != native.Context().Name() {
		return "", 0, e.New(e.CodeVerifyFailed, "native acquisition cannot cross parent repositories")
	}
	var size int64
	err = catalog.WithNativeImage(ctx, parentRef, want, platform, func(ctx context.Context, selection *catalog.NativeImageSelection) error {
		return selection.Consume(ctx, func(ctx context.Context, image v1.Image) error {
			if _, err := os.Lstat(destination); err == nil {
				var verifyErr error
				size, verifyErr = verifyOCILayoutAndSize(ctx, destination, want)
				return verifyErr
			} else if !os.IsNotExist(err) {
				return e.Wrap(e.CodeFetchFailed, err, "inspecting native OCI destination")
			}
			parentPath := filepath.Dir(destination)
			if err := os.MkdirAll(parentPath, 0o755); err != nil {
				return e.Wrap(e.CodeFetchFailed, err, "creating native OCI transport parent")
			}
			stage, err := os.MkdirTemp(parentPath, "."+filepath.Base(destination)+"-")
			if err != nil {
				return e.Wrap(e.CodeFetchFailed, err, "creating private native OCI stage")
			}
			defer os.RemoveAll(stage)
			root, err := layout.Write(stage, empty.Index)
			if err == nil {
				err = root.AppendImage(image)
			}
			if err != nil {
				return classifyOCIReadError(ctx, err, "materializing authenticated native image")
			}
			size, err = verifyOCILayoutAndSize(ctx, stage, want)
			if err != nil {
				return err
			}
			if err := ctx.Err(); err != nil {
				return e.Wrap(e.CodeTimeout, err, "native OCI acquisition canceled before commit")
			}
			if err := os.Rename(stage, destination); err != nil {
				return e.Wrap(e.CodeFetchFailed, err, "committing native OCI transport")
			}
			return nil
		})
	})
	if err != nil {
		return "", 0, err
	}
	return want, size, nil
}

func (registryOCITransport) Acquire(ctx context.Context, digestRef, destination string) (string, int64, error) {
	ref, want, err := parseDigestReference(digestRef)
	if err != nil {
		return "", 0, err
	}
	if _, statErr := os.Lstat(destination); statErr == nil {
		size, err := verifyOCILayoutAndSize(ctx, destination, want)
		if err != nil {
			return "", 0, e.Wrap(e.CodeVerifyFailed, err, "existing OCI layout %s is not the pinned closure", destination)
		}
		return want, size, nil
	} else if !os.IsNotExist(statErr) {
		return "", 0, e.Wrap(e.CodeFetchFailed, statErr, "inspecting OCI destination %s", destination)
	}

	parent := filepath.Dir(destination)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return "", 0, e.Wrap(e.CodeFetchFailed, err, "creating OCI destination parent %s", parent)
	}
	tmp, err := os.MkdirTemp(parent, "."+filepath.Base(destination)+"-")
	if err != nil {
		return "", 0, e.Wrap(e.CodeFetchFailed, err, "creating private OCI staging directory")
	}
	defer os.RemoveAll(tmp)

	desc, err := remote.Get(ref,
		remote.WithContext(ctx),
		remote.WithAuth(authn.Anonymous),
		remote.WithUserAgent("nvkr/oci-acquire"),
	)
	if err != nil {
		return "", 0, classifyOCIReadError(ctx, err, "fetching OCI descriptor %s", digestRef)
	}
	if got := desc.Digest.String(); got != want {
		return "", 0, e.New(e.CodeVerifyFailed, "registry returned descriptor %s for %s, want %s", got, digestRef, want)
	}
	switch {
	case desc.MediaType.IsIndex():
		index, err := desc.ImageIndex()
		if err != nil {
			return "", 0, e.Wrap(e.CodeFetchFailed, err, "opening OCI index %s", digestRef)
		}
		if _, err := layout.Write(tmp, index); err != nil {
			return "", 0, classifyOCIReadError(ctx, err, "materializing OCI index %s", digestRef)
		}
	case desc.MediaType.IsImage():
		image, err := desc.Image()
		if err != nil {
			return "", 0, e.Wrap(e.CodeFetchFailed, err, "opening OCI image %s", digestRef)
		}
		lp, err := layout.Write(tmp, empty.Index)
		if err == nil {
			err = lp.AppendImage(image)
		}
		if err != nil {
			return "", 0, classifyOCIReadError(ctx, err, "materializing OCI image %s", digestRef)
		}
	default:
		return "", 0, e.New(e.CodeVerifyFailed, "descriptor %s has non-image media type %q", digestRef, desc.MediaType)
	}
	size, err := verifyOCILayoutAndSize(ctx, tmp, want)
	if err != nil {
		return "", 0, err
	}
	if err := os.Rename(tmp, destination); err != nil {
		return "", 0, e.Wrap(e.CodeFetchFailed, err, "atomically publishing OCI layout %s", destination)
	}
	return want, size, nil
}

func (registryOCITransport) Verify(ctx context.Context, digestRef, layoutPath string) error {
	_, want, err := parseDigestReference(digestRef)
	if err != nil {
		return err
	}
	return verifyOCILayout(ctx, layoutPath, want)
}

func parseDigestReference(ref string) (name.Digest, string, error) {
	if strings.Count(ref, "@") != 1 {
		return name.Digest{}, "", e.New(e.CodeUnpinnedSource, "OCI reference %q must contain exactly one digest", ref)
	}
	want := ref[strings.LastIndexByte(ref, '@')+1:]
	if err := validateSHA256(want); err != nil {
		return name.Digest{}, "", e.New(e.CodeUnpinnedSource, "OCI reference %q has invalid digest: %v", ref, err)
	}
	digest, err := name.NewDigest(ref, name.StrictValidation)
	if err != nil {
		return name.Digest{}, "", e.Wrap(e.CodeUnpinnedSource, err, "parsing digest-pinned OCI reference %q", ref)
	}
	return digest, want, nil
}

func classifyOCIReadError(ctx context.Context, err error, format string, args ...any) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return e.Wrap(e.CodeTimeout, ctxErr, format, args...)
	}
	return e.Wrap(e.CodeUnavailable, err, format, args...)
}

type ociDescriptor struct {
	MediaType string `json:"mediaType"`
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
}

type ociIndexDocument struct {
	SchemaVersion int             `json:"schemaVersion"`
	MediaType     string          `json:"mediaType,omitempty"`
	Manifests     []ociDescriptor `json:"manifests"`
}

type ociManifestDocument struct {
	SchemaVersion int             `json:"schemaVersion"`
	MediaType     string          `json:"mediaType,omitempty"`
	Config        ociDescriptor   `json:"config"`
	Layers        []ociDescriptor `json:"layers"`
}

// ociLayoutSource is the semantic input to complete-closure verification. The
// pathname adapter serves acquisition-time checks; terminal authority supplies an
// immutable held-file implementation, so the verifier itself never needs to reopen
// a producer-controlled or same-UID-swappable tree path.
type ociLayoutSource interface {
	readRegular(context.Context, string, int64) ([]byte, error)
	blobInventory(context.Context) (map[string]int64, error)
}

// verifyOCILayout proves the complete offline closure: every descriptor reachable
// from the pinned root exists with its declared size and sha256, and no unreferenced
// blob is smuggled into the transport. The root can be either a registry index stored
// directly as index.json or the standard one-entry layout index pointing at an image.
func verifyOCILayout(ctx context.Context, root, want string) error {
	_, err := verifyOCILayoutAndSize(ctx, root, want)
	return err
}

// VerifyOfflineOCILayout validates a complete local closure using the same
// offline owner as acquisition. It issues no execution or publication receipt.
func VerifyOfflineOCILayout(ctx context.Context, root, want string) error {
	return verifyOCILayout(ctx, root, want)
}

func verifyOCILayoutAndSize(ctx context.Context, root, want string) (int64, error) {
	return ociclosure.VerifyPathAndSize(ctx, root, want)
}

type ociSourceAdapter struct{ ociLayoutSource }

func (source ociSourceAdapter) ReadRegular(ctx context.Context, path string, limit int64) ([]byte, error) {
	return source.readRegular(ctx, path, limit)
}
func (source ociSourceAdapter) BlobInventory(ctx context.Context) (map[string]int64, error) {
	return source.blobInventory(ctx)
}
func verifyOCILayoutSource(ctx context.Context, source ociLayoutSource, want string) error {
	if source == nil {
		return ociclosure.VerifySource(ctx, nil, want)
	}
	return ociclosure.VerifySource(ctx, ociSourceAdapter{source}, want)
}

func readRegularLimitedContext(ctx context.Context, path string, limit int64) ([]byte, error) {
	return readRegularBeneathContext(ctx, filepath.Dir(path), filepath.Base(path), limit)
}

func readRegularBeneathContext(ctx context.Context, root, relative string, limit int64) ([]byte, error) {
	if ctx == nil {
		return nil, fmt.Errorf("reading a bounded regular file requires a non-nil context")
	}
	if limit < 0 {
		return nil, fmt.Errorf("negative bounded-file limit")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f, info, err := openRegularBeneath(root, relative)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if !info.Mode().IsRegular() || info.Size() > limit {
		return nil, fmt.Errorf("%s is not a regular file within the %d-byte limit", relative, limit)
	}
	raw, err := io.ReadAll(io.LimitReader(pipelineContextReader{ctx: ctx, source: f}, limit+1))
	if err != nil {
		return nil, fmt.Errorf("reading bounded file %s: %w", relative, err)
	}
	if int64(len(raw)) > limit {
		return nil, fmt.Errorf("file %s exceeds the %d-byte limit", relative, limit)
	}
	after, err := f.Stat()
	if err != nil || !os.SameFile(info, after) || after.Size() != int64(len(raw)) {
		return nil, fmt.Errorf("file %s changed while reading", relative)
	}
	return raw, nil
}

func validateSHA256(digest string) error {
	if !strings.HasPrefix(digest, "sha256:") || len(digest) != len("sha256:")+64 || strings.ToLower(digest) != digest {
		return fmt.Errorf("want sha256:<64-hex>")
	}
	_, err := hex.DecodeString(strings.TrimPrefix(digest, "sha256:"))
	return err
}

func decodeOCIJSON(raw []byte, out any) error { return ociclosure.DecodeJSON(raw, out) }

func digestBytes(raw []byte) string {
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}
