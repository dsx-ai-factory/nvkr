// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/types"
)

// The release pull owner supplies an object from its independently checked
// metadata closure. This helper acquires exactly that object's image closure;
// it neither fetches siblings nor confers signature or release authority.
func pullImageOutput(ctx context.Context, reference, out string, object PublishedObject, options RegistryOptions) error {
	if err := deliveryContext(ctx); err != nil {
		return err
	}
	if err := options.Validate(); err != nil {
		return err
	}
	if err := ValidatePublicationReference(reference); err != nil {
		return err
	}
	if object.Output == nil || object.Output.Format != "oci" || object.Output.TransportPath != "" || len(object.Output.Components) != 1 {
		return e.New(e.CodeVerifyFailed, "image pull requires one exact OCI output binding")
	}
	parse := []name.Option{name.StrictValidation}
	if options.PlainHTTP {
		parse = append(parse, name.Insecure)
	}
	ref, err := name.NewDigest(reference, parse...)
	if err != nil || ref.DigestStr() != object.DescriptorDigest {
		return e.New(e.CodeVerifyFailed, "image reference differs from its selected output descriptor")
	}
	transport := registryOptions(ctx, options)
	if object.Output.ImageIndex != nil {
		if err := validateImageIndexObject(object); err != nil {
			return err
		}
		index, err := fetchRegistryImageIndex(ctx, ref, object.Size, object.Output.ImageIndex.RequiredPlatforms, transport)
		if err != nil {
			return err
		}
		defer func() { _ = index.Close() }()
		description, err := index.Description()
		if err != nil {
			return err
		}
		if err := validatePublishedImageIndex(object, description); err != nil {
			return err
		}
		return imagePullWriteResult(index.WriteLayout(ctx, out))
	}
	closure, err := capturePulledNativeImage(ctx, ref, object, transport)
	if err != nil {
		return err
	}
	defer closure.close()
	return imagePullWriteResult(writeImageIndexLayout(ctx, out, closure))
}

func capturePulledNativeImage(ctx context.Context, ref name.Digest, object PublishedObject, options []remote.Option) (*imageIndexClosure, error) {
	if object.OS != "linux" || (object.Arch != "amd64" && object.Arch != "arm64") ||
		(object.ArtifactType != string(types.OCIManifestSchema1) && object.ArtifactType != string(types.DockerManifestSchema2)) ||
		object.BlobDigest != object.DescriptorDigest || object.SemanticDigest != object.DescriptorDigest {
		return nil, e.New(e.CodeVerifyFailed, "image pull requires an exact native manifest and platform binding")
	}
	descriptor := Descriptor{MediaType: object.ArtifactType, Digest: object.DescriptorDigest, Size: object.Size}
	raw, err := registryManifest(ctx, ref, descriptor, options)
	if err != nil {
		return nil, err
	}
	if err := canonical.ValidateJSONDocument(raw); err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "validating pulled image manifest")
	}
	if err := validateImageSchemaNames(raw, reflect.TypeFor[v1.Manifest]()); err != nil {
		return nil, err
	}
	var manifest ociManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "decoding pulled image manifest")
	}
	if manifest.MediaType != descriptor.MediaType || len(manifest.Layers) > maxDeliveryObjects {
		return nil, e.New(e.CodeVerifyFailed, "pulled image manifest differs from its media type or layer bound")
	}
	closure := &imageIndexClosure{blobs: make(map[string]*heldfile.File)}
	complete := false
	defer func() {
		if !complete {
			closure.close()
		}
	}()
	held, err := heldfile.FromBytesContext(ctx, raw, heldfile.Options{Label: "pulled native image manifest", MaxBytes: 8 << 20, ExpectedDigest: descriptor.Digest})
	if err != nil {
		return nil, classifyContext(ctx, e.Wrap(e.CodeVerifyFailed, err, "holding native image manifest"))
	}
	closure.blobs[descriptor.Digest], closure.bytes = held, held.Size()
	if err := captureRegistryBlob(ctx, closure, ref.Context(), manifest.Config, 8<<20, options); err != nil {
		return nil, err
	}
	config, err := inspectImageMetadataFrom(ctx, manifest, closure.open)
	if err != nil {
		return nil, err
	}
	if config.OS != object.OS || config.Architecture != object.Arch {
		return nil, e.New(e.CodeVerifyFailed, "pulled image platform differs from its release binding")
	}
	for _, layer := range manifest.Layers {
		if err := captureRegistryBlob(ctx, closure, ref.Context(), layer, maxDeliveryFileBytes, options); err != nil {
			return nil, err
		}
	}
	visited := map[string]bool{descriptor.Digest: true}
	if _, err := verifyImageManifestFrom(ctx, manifest, visited, closure.open); err != nil {
		return nil, err
	}
	if len(visited) != len(closure.blobs) {
		return nil, e.New(e.CodeVerifyFailed, "pulled image has unreachable closure members")
	}
	// A native manifest needs local OCI-layout scaffolding. This wrapper is not
	// the published image identity; all native manifest/config/layer bytes remain
	// unchanged, including the native Docker media types admitted by inspection.
	indexRaw, err := canonical.Marshal(ociIndex{SchemaVersion: 2, MediaType: MediaOCIIndex, Manifests: []Descriptor{descriptor}})
	if err != nil {
		return nil, e.Wrap(e.CodeInternal, err, "encoding pulled image layout index")
	}
	closure.index, err = heldfile.FromBytesContext(ctx, indexRaw, heldfile.Options{Label: "pulled native image layout index", MaxBytes: 8 << 20})
	if err != nil {
		return nil, classifyContext(ctx, e.Wrap(e.CodeVerifyFailed, err, "holding pulled image layout index"))
	}
	complete = true
	return closure, nil
}

func imagePullWriteResult(err error) error {
	if errors.Is(err, os.ErrExist) {
		return e.Wrap(e.CodeImmutableViolation, err, "release pull refuses to replace an existing image destination")
	}
	return err
}
