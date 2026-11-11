// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package ociclosure

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/google/go-containerregistry/pkg/v1/types"
)

// Source supplies captured bytes and independently checked blob hashes/sizes.
// A caller implementation is descriptive; this interface issues no receipt.
type Source interface {
	ReadRegular(context.Context, string, int64) ([]byte, error)
	BlobInventory(context.Context) (map[string]int64, error)
}

type Descriptor struct {
	MediaType string `json:"mediaType"`
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
}

type IndexDocument struct {
	SchemaVersion int          `json:"schemaVersion"`
	MediaType     string       `json:"mediaType,omitempty"`
	Manifests     []Descriptor `json:"manifests"`
}

type ManifestDocument struct {
	SchemaVersion int          `json:"schemaVersion"`
	MediaType     string       `json:"mediaType,omitempty"`
	Config        Descriptor   `json:"config"`
	Layers        []Descriptor `json:"layers"`
}

func VerifySource(ctx context.Context, source Source, want string) error {
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
	indexRaw, err := source.ReadRegular(ctx, "index.json", maxOCIManifestBytes)
	if err != nil {
		if ctx.Err() != nil {
			return e.Wrap(e.CodeTimeout, ctx.Err(), "offline OCI verification canceled")
		}
		return e.Wrap(e.CodeVerifyFailed, err, "reading OCI root index")
	}
	var index IndexDocument
	if err := DecodeJSON(indexRaw, &index); err != nil || index.SchemaVersion != 2 {
		return e.New(e.CodeVerifyFailed, "OCI root index is not a schemaVersion 2 document")
	}

	actual, err := source.BlobInventory(ctx)
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
	visit := func(desc Descriptor) error { return nil }
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
	source Source,
	actual map[string]int64,
	referenced, visiting, visited map[string]bool,
	self *func(Descriptor) error,
) func(Descriptor) error {
	return func(desc Descriptor) error {
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
			var index IndexDocument
			if err := DecodeJSON(raw, &index); err != nil || index.SchemaVersion != 2 || index.MediaType != desc.MediaType {
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
			var manifest ManifestDocument
			if err := DecodeJSON(raw, &manifest); err != nil || manifest.SchemaVersion != 2 || manifest.MediaType != desc.MediaType {
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

func verifyOCILayoutHeader(ctx context.Context, source Source) error {
	raw, err := source.ReadRegular(ctx, "oci-layout", 1024)
	if err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "reading oci-layout")
	}
	var header struct {
		ImageLayoutVersion string `json:"imageLayoutVersion"`
	}
	if err := DecodeJSON(raw, &header); err != nil || header.ImageLayoutVersion != "1.0.0" {
		return e.New(e.CodeVerifyFailed, "invalid oci-layout header")
	}
	return nil
}

func readOCIBlob(ctx context.Context, source Source, digest string, limit int64) ([]byte, error) {
	return source.ReadRegular(ctx, "blobs/sha256/"+strings.TrimPrefix(digest, "sha256:"), limit)
}

func validateSHA256(digest string) error {
	if !strings.HasPrefix(digest, "sha256:") || len(digest) != len("sha256:")+64 || strings.ToLower(digest) != digest {
		return fmt.Errorf("want sha256:<64-hex>")
	}
	_, err := hex.DecodeString(strings.TrimPrefix(digest, "sha256:"))
	return err
}

// DecodeJSON rejects duplicate object keys before decoding. OCI manifests are
// content-addressed but are consumed by multiple implementations; ambiguous JSON
// would let two conforming-looking parsers disagree about the referenced closure.
func DecodeJSON(raw []byte, out any) error {
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

// VerifyPath captures the complete local tree before semantic validation.
func VerifyPath(ctx context.Context, root, want string) error {
	_, err := VerifyPathAndSize(ctx, root, want)
	return err
}
func VerifyPathAndSize(ctx context.Context, root, want string) (int64, error) {
	if ctx == nil {
		return 0, e.New(e.CodeUsage, "offline OCI verification requires a non-nil context")
	}
	if err := ctx.Err(); err != nil {
		return 0, e.Wrap(e.CodeTimeout, err, "offline OCI verification canceled")
	}
	if err := validateSHA256(want); err != nil {
		return 0, e.New(e.CodeVerifyFailed, "invalid expected OCI digest %q", want)
	}
	tree, err := Snapshot(ctx, root)
	if err != nil {
		if ctx.Err() != nil {
			return 0, e.Wrap(e.CodeTimeout, ctx.Err(), "offline OCI verification canceled")
		}
		return 0, e.Wrap(e.CodeVerifyFailed, err, "holding OCI layout %s", root)
	}
	if err := VerifySource(ctx, tree, want); err != nil {
		return 0, err
	}
	return tree.Size(ctx)
}
