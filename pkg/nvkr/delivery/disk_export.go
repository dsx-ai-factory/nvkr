// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"context"
	"encoding/json"
	"io"
	"runtime"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// WriteLayout exports a standard disk wrapper and its held payload without
// reopening producer paths. It preserves both manifest and payload identities.
// The caller admits disk capacity; the existing destination parent must be a real
// directory. Export uses private staging and no-replace publication, and grants
// transport integrity only, never execution, composition or signing authority.
func (artifact *DiskArtifact) WriteLayout(ctx context.Context, out string) error {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		return e.New(e.CodeEngineCapability, "disk export requires no-replace filesystem support")
	}
	if err := artifact.RequireStandardProfile(ctx); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return classifyContext(ctx, ctx.Err())
	case <-artifact.owner.permit:
	}
	defer func() { artifact.owner.permit <- struct{}{} }()
	if artifact.owner.file == nil {
		return e.New(e.CodeGateUnmet, "disk artifact is closed")
	}
	observed := artifact.inspection
	platform, err := diskPlatform(observed.Platform)
	if err != nil {
		return err
	}
	configRaw, err := json.Marshal(platform)
	if err != nil {
		return e.Wrap(e.CodeInternal, err, "encoding exported disk platform")
	}
	config := Descriptor{MediaType: MediaImageConfig, Digest: digestBytes(configRaw), Size: int64(len(configRaw))}
	manifestRaw, err := json.Marshal(standardDiskManifest(config, observed.PayloadDigest, observed.Size, observed.ArtifactType, observed.Encryption))
	if err != nil {
		return e.Wrap(e.CodeInternal, err, "encoding exported disk manifest")
	}
	if digestBytes(manifestRaw) != observed.ManifestDigest {
		return e.New(e.CodeVerifyFailed, "exported disk manifest differs from captured identity")
	}
	indexRaw, err := json.Marshal(ociIndex{SchemaVersion: 2, MediaType: MediaOCIIndex, Manifests: []Descriptor{{MediaType: MediaOCIManifest, Digest: observed.ManifestDigest, Size: int64(len(manifestRaw)), ArtifactType: observed.ArtifactType}}})
	if err != nil {
		return e.Wrap(e.CodeInternal, err, "encoding exported disk index")
	}
	writeBytes := func(raw []byte) func(io.Writer) error {
		return func(writer io.Writer) error { _, err := writer.Write(raw); return err }
	}
	var payloadErr error
	err = writeHeldLayout(ctx, out, writeBytes(indexRaw), map[string]func(io.Writer) error{
		config.Digest:           writeBytes(configRaw),
		observed.ManifestDigest: writeBytes(manifestRaw),
		observed.PayloadDigest: func(writer io.Writer) error {
			_, err := streamDiskPayload(ctx, artifact.owner.file, writer, observed.PayloadDigest, observed.Size)
			payloadErr = err
			return err
		},
	})
	if payloadErr != nil {
		return payloadErr
	}
	return err
}
