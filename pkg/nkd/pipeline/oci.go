// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/catalog"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/types"
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
		remote.WithUserAgent("nkdctl/oci-acquire"),
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
	if ctx == nil {
		return 0, e.New(e.CodeUsage, "offline OCI verification requires a non-nil context")
	}
	if err := ctx.Err(); err != nil {
		return 0, e.Wrap(e.CodeTimeout, err, "offline OCI verification canceled")
	}
	if err := validateSHA256(want); err != nil {
		return 0, e.New(e.CodeVerifyFailed, "invalid expected OCI digest %q", want)
	}
	tree, err := snapshotRegularTreeContext(ctx, root)
	if err != nil {
		if ctx.Err() != nil {
			return 0, e.Wrap(e.CodeTimeout, ctx.Err(), "offline OCI verification canceled")
		}
		return 0, e.Wrap(e.CodeVerifyFailed, err, "holding OCI layout %s", root)
	}
	if err := verifyOCILayoutSource(ctx, tree, want); err != nil {
		return 0, err
	}
	var size int64
	for _, file := range tree.files {
		if err := ctx.Err(); err != nil {
			return 0, e.Wrap(e.CodeTimeout, err, "sizing held OCI layout")
		}
		if file.size > 0 && size > int64(^uint64(0)>>1)-file.size {
			return 0, e.New(e.CodeVerifyFailed, "OCI layout size overflows int64")
		}
		size += file.size
	}
	return size, nil
}

func verifyOCILayoutSource(ctx context.Context, source ociLayoutSource, want string) error {
	if ctx == nil {
		return e.New(e.CodeUsage, "offline OCI verification requires a non-nil context")
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "offline OCI verification canceled")
	}
	if source == nil {
		return e.New(e.CodeVerifyFailed, "OCI layout source is nil")
	}
	if err := validateSHA256(want); err != nil {
		return e.New(e.CodeVerifyFailed, "invalid expected OCI digest %q", want)
	}
	if err := verifyOCILayoutHeader(ctx, source); err != nil {
		if ctx.Err() != nil {
			return e.Wrap(e.CodeTimeout, ctx.Err(), "offline OCI verification canceled")
		}
		return err
	}
	indexRaw, err := source.readRegular(ctx, "index.json", maxOCIManifestBytes)
	if err != nil {
		if ctx.Err() != nil {
			return e.Wrap(e.CodeTimeout, ctx.Err(), "offline OCI verification canceled")
		}
		return e.Wrap(e.CodeVerifyFailed, err, "reading OCI root index")
	}
	var index ociIndexDocument
	if err := decodeOCIJSON(indexRaw, &index); err != nil || index.SchemaVersion != 2 {
		return e.New(e.CodeVerifyFailed, "OCI root index is not a schemaVersion 2 document")
	}

	actual, err := source.blobInventory(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return e.Wrap(e.CodeTimeout, ctx.Err(), "offline OCI verification canceled")
		}
		var coded *e.Error
		if errors.As(err, &coded) {
			return err
		}
		return e.Wrap(e.CodeVerifyFailed, err, "verifying OCI blob closure")
	}
	referenced := map[string]bool{}
	visiting := map[string]bool{}
	visited := map[string]bool{}
	visit := func(desc ociDescriptor) error { return nil }
	visit = recursiveOCIDescriptorVerifier(ctx, source, actual, referenced, visiting, visited, &visit)

	rootDigest := digestBytes(indexRaw)
	if rootDigest == want && isIndexMediaType(index.MediaType) {
		for _, desc := range index.Manifests {
			if err := visit(desc); err != nil {
				return err
			}
		}
	} else {
		if len(index.Manifests) != 1 || index.Manifests[0].Digest != want {
			return e.New(e.CodeVerifyFailed, "OCI layout root does not identify pinned digest %s", want)
		}
		if err := visit(index.Manifests[0]); err != nil {
			return err
		}
	}
	for digest := range actual {
		if !referenced[digest] {
			return e.New(e.CodeVerifyFailed, "OCI layout contains unreferenced blob %s", digest)
		}
	}
	return nil
}

func recursiveOCIDescriptorVerifier(
	ctx context.Context,
	source ociLayoutSource,
	actual map[string]int64,
	referenced, visiting, visited map[string]bool,
	self *func(ociDescriptor) error,
) func(ociDescriptor) error {
	return func(desc ociDescriptor) error {
		if err := ctx.Err(); err != nil {
			return e.Wrap(e.CodeTimeout, err, "offline OCI verification canceled")
		}
		if err := validateSHA256(desc.Digest); err != nil || desc.Size < 0 {
			return e.New(e.CodeVerifyFailed, "OCI descriptor has invalid digest/size %q/%d", desc.Digest, desc.Size)
		}
		size, ok := actual[desc.Digest]
		if !ok || size != desc.Size {
			return e.New(e.CodeVerifyFailed, "OCI blob %s is missing or has size %d, want %d", desc.Digest, size, desc.Size)
		}
		referenced[desc.Digest] = true
		if visited[desc.Digest] {
			return nil
		}
		if visiting[desc.Digest] {
			return e.New(e.CodeVerifyFailed, "OCI descriptor graph contains a cycle at %s", desc.Digest)
		}
		visiting[desc.Digest] = true
		defer delete(visiting, desc.Digest)
		media := types.MediaType(desc.MediaType)
		switch {
		case media.IsIndex():
			raw, err := readOCIBlob(ctx, source, desc.Digest, maxOCIManifestBytes)
			if err != nil {
				if ctx.Err() != nil {
					return e.Wrap(e.CodeTimeout, ctx.Err(), "offline OCI verification canceled")
				}
				return e.Wrap(e.CodeVerifyFailed, err, "reading OCI index blob %s", desc.Digest)
			}
			var index ociIndexDocument
			if err := decodeOCIJSON(raw, &index); err != nil || index.SchemaVersion != 2 || index.MediaType != desc.MediaType {
				return e.New(e.CodeVerifyFailed, "OCI index blob %s is invalid", desc.Digest)
			}
			for _, child := range index.Manifests {
				if err := (*self)(child); err != nil {
					return err
				}
			}
		case media.IsImage():
			raw, err := readOCIBlob(ctx, source, desc.Digest, maxOCIManifestBytes)
			if err != nil {
				if ctx.Err() != nil {
					return e.Wrap(e.CodeTimeout, ctx.Err(), "offline OCI verification canceled")
				}
				return e.Wrap(e.CodeVerifyFailed, err, "reading OCI image manifest blob %s", desc.Digest)
			}
			var manifest ociManifestDocument
			if err := decodeOCIJSON(raw, &manifest); err != nil || manifest.SchemaVersion != 2 || manifest.MediaType != desc.MediaType {
				return e.New(e.CodeVerifyFailed, "OCI image manifest blob %s is invalid", desc.Digest)
			}
			if err := (*self)(manifest.Config); err != nil {
				return err
			}
			for _, layer := range manifest.Layers {
				if err := (*self)(layer); err != nil {
					return err
				}
			}
		default:
			// Config and layer payloads terminate the graph. An unknown media type
			// could hide another descriptor graph from this verifier, so fail closed.
			if !media.IsConfig() && !media.IsLayer() {
				return e.New(e.CodeVerifyFailed, "OCI descriptor %s has unsupported media type %q", desc.Digest, desc.MediaType)
			}
		}
		visited[desc.Digest] = true
		return nil
	}
}

func verifyOCILayoutHeader(ctx context.Context, source ociLayoutSource) error {
	raw, err := source.readRegular(ctx, "oci-layout", 1024)
	if err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "reading oci-layout")
	}
	var header struct {
		ImageLayoutVersion string `json:"imageLayoutVersion"`
	}
	if err := decodeOCIJSON(raw, &header); err != nil || header.ImageLayoutVersion != "1.0.0" {
		return e.New(e.CodeVerifyFailed, "invalid oci-layout header")
	}
	return nil
}

func readOCIBlob(ctx context.Context, source ociLayoutSource, digest string, limit int64) ([]byte, error) {
	return source.readRegular(ctx, "blobs/sha256/"+strings.TrimPrefix(digest, "sha256:"), limit)
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

// decodeOCIJSON rejects duplicate object keys before decoding. OCI manifests are
// content-addressed but are consumed by multiple implementations; ambiguous JSON
// would let two conforming-looking parsers disagree about the referenced closure.
func decodeOCIJSON(raw []byte, out any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if err := rejectDuplicateJSONValue(dec); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return fmt.Errorf("trailing JSON")
	}
	return json.Unmarshal(raw, out)
}

func rejectDuplicateJSONValue(dec *json.Decoder) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	switch delim := tok.(type) {
	case json.Delim:
		switch delim {
		case '{':
			seen := map[string]bool{}
			for dec.More() {
				keyToken, err := dec.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok || seen[key] {
					return fmt.Errorf("duplicate or invalid JSON object key %q", key)
				}
				seen[key] = true
				if err := rejectDuplicateJSONValue(dec); err != nil {
					return err
				}
			}
			_, err := dec.Token()
			return err
		case '[':
			for dec.More() {
				if err := rejectDuplicateJSONValue(dec); err != nil {
					return err
				}
			}
			_, err := dec.Token()
			return err
		default:
			return fmt.Errorf("unexpected JSON delimiter %q", delim)
		}
	}
	return nil
}

func digestBytes(raw []byte) string {
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func isIndexMediaType(media string) bool {
	return types.MediaType(media).IsIndex()
}
