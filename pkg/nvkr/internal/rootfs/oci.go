// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package rootfs

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/ociclosure"
)

const (
	ociManifest        = "application/vnd.oci.image.manifest.v1+json"
	ociConfig          = "application/vnd.oci.image.config.v1+json"
	ociLayer           = "application/vnd.oci.image.layer.v1.tar"
	maxRuntimeDocument = 1 << 20
)

// CaptureLayer verifies the complete OCI closure and holds the single
// uncompressed layer emitted by the admitted mkosi recipe. This is byte
// authentication, not a production or runtime-use receipt. The caller owns Close.
// OCI process configuration is never used as a launcher instruction.
func CaptureLayer(ctx context.Context, layout, digest, platform string) (*Layer, error) {
	if ctx == nil {
		return nil, e.New(e.CodeUsage, "runtime layer capture requires context")
	}
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "runtime layer capture canceled")
	}
	if platform != "linux/amd64" && platform != "linux/arm64" {
		return nil, e.New(e.CodeEngineCapability, "runtime layer requires a supported Linux platform")
	}
	if !canonical.IsDigest(digest) {
		return nil, e.New(e.CodeVerifyFailed, "runtime OCI subject is not an exact digest")
	}
	tree, err := ociclosure.Snapshot(ctx, layout)
	if err != nil {
		return nil, stageError(ctx, err)
	}
	if err := ociclosure.VerifySource(ctx, tree, digest); err != nil {
		return nil, err
	}
	indexRaw, err := tree.ReadRegular(ctx, "index.json", maxRuntimeDocument)
	if err != nil {
		return nil, stageError(ctx, err)
	}
	var index ociclosure.IndexDocument
	if err := decodeRuntimeDocument(indexRaw, &index); err != nil || len(index.Manifests) != 1 ||
		index.Manifests[0].Digest != digest || index.Manifests[0].MediaType != ociManifest {
		return nil, e.New(e.CodeVerifyFailed, "runtime requires exactly one OCI image manifest")
	}
	raw, err := tree.ReadRegular(ctx, runtimeBlobPath(digest), maxRuntimeDocument)
	if err != nil {
		return nil, stageError(ctx, err)
	}
	var manifest struct {
		SchemaVersion int                     `json:"schemaVersion"`
		MediaType     string                  `json:"mediaType"`
		Config        ociclosure.Descriptor   `json:"config"`
		Layers        []ociclosure.Descriptor `json:"layers"`
		Annotations   map[string]string       `json:"annotations,omitempty"`
	}
	if err := decodeRuntimeDocument(raw, &manifest); err != nil || manifest.Config.MediaType != ociConfig ||
		len(manifest.Layers) != 1 || manifest.Layers[0].MediaType != ociLayer || manifest.Layers[0].Size <= 0 || manifest.Layers[0].Size > MaxLayerBytes {
		return nil, e.New(e.CodeVerifyFailed, "runtime requires one bounded uncompressed filesystem layer")
	}
	layer := manifest.Layers[0]
	configRaw, err := tree.ReadRegular(ctx, runtimeBlobPath(manifest.Config.Digest), maxRuntimeDocument)
	if err != nil {
		return nil, stageError(ctx, err)
	}
	var config struct {
		Created      string `json:"created"`
		Architecture string `json:"architecture"`
		OS           string `json:"os"`
		RootFS       struct {
			Type    string   `json:"type"`
			DiffIDs []string `json:"diff_ids"`
		} `json:"rootfs"`
		Config struct {
			Cmd []string `json:"Cmd"`
		} `json:"config"`
		History []struct {
			Created string `json:"created"`
			Comment string `json:"comment"`
		} `json:"history"`
	}
	if err := decodeRuntimeDocument(configRaw, &config); err != nil || config.OS+"/"+config.Architecture != platform ||
		config.RootFS.Type != "layers" || len(config.RootFS.DiffIDs) != 1 || config.RootFS.DiffIDs[0] != layer.Digest {
		return nil, e.New(e.CodeVerifyFailed, "runtime OCI configuration changes its platform or filesystem identity")
	}
	root, _, err := securepath.OpenDirectory(layout)
	if err != nil {
		return nil, stageError(ctx, err)
	}
	defer root.Close()
	blobs, _, err := securepath.OpenChild(root, "blobs", true)
	if err != nil {
		return nil, stageError(ctx, err)
	}
	defer blobs.Close()
	sha, _, err := securepath.OpenChild(blobs, "sha256", true)
	if err != nil {
		return nil, stageError(ctx, err)
	}
	defer sha.Close()
	file, info, err := securepath.OpenChild(sha, strings.TrimPrefix(layer.Digest, "sha256:"), false)
	if err != nil {
		return nil, stageError(ctx, err)
	}
	defer file.Close()
	if info.Size() != layer.Size {
		return nil, e.New(e.CodeVerifyFailed, "runtime layer changed during capture")
	}
	held, err := captureDiskLayer(ctx, file, layer.Digest, layer.Size)
	if err != nil {
		return nil, stageError(ctx, err)
	}
	if held.Size() != layer.Size {
		_ = held.Close()
		return nil, e.New(e.CodeVerifyFailed, "runtime layer size differs from its descriptor")
	}
	return held, nil
}

func runtimeBlobPath(digest string) string {
	return "blobs/sha256/" + strings.TrimPrefix(digest, "sha256:")
}

func decodeRuntimeDocument(raw []byte, target any) error {
	if err := canonical.ValidateFoldedJSONDocument(raw); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return e.New(e.CodeVerifyFailed, "runtime OCI document has trailing data")
	}
	return nil
}
