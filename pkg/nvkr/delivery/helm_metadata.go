// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func publishedHelmChartConfig(ctx context.Context, root string, object PublishedObject) (helmChartConfig, error) {
	manifest, err := metadataManifest(ctx, root, object)
	if err != nil {
		return helmChartConfig{}, err
	}
	if object.Output == nil || object.Output.Format != outputFormatHelm || len(manifest.Layers) != 1 ||
		manifest.Layers[0].Digest != object.BlobDigest || object.Output.TransportPath != "" {
		return helmChartConfig{}, e.New(e.CodeVerifyFailed, "Helm chart metadata differs from its release binding")
	}
	return readHelmChartConfig(ctx, root, manifest)
}

func readHelmChartConfig(ctx context.Context, root string, manifest ociManifest) (helmChartConfig, error) {
	if manifest.Config.MediaType != MediaHelmConfig || len(manifest.Layers) != 1 ||
		manifest.Layers[0].MediaType != MediaHelmChartLayer || !isDigest(manifest.Layers[0].Digest) ||
		manifest.Layers[0].Size < 0 || manifest.Layers[0].Size > maxDeliveryFileBytes {
		return helmChartConfig{}, e.New(e.CodeVerifyFailed, "chart metadata requires the Helm config and one chart archive")
	}
	raw, err := readBlob(ctx, root, manifest.Config, 8<<20)
	if err != nil {
		return helmChartConfig{}, err
	}
	if err := canonical.ValidateJSONDocument(raw); err != nil {
		return helmChartConfig{}, err
	}
	// Configs follow Helm's external JSON spelling and need not use NVKR's
	// canonical encoding. Reject encoding/json's case-folded field aliases.
	if err := validateImageSchemaNames(raw, reflect.TypeFor[helmChartConfig]()); err != nil {
		return helmChartConfig{}, e.Wrap(e.CodeVerifyFailed, err, "checking Helm config field names")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var config helmChartConfig
	if err := decoder.Decode(&config); err != nil {
		return helmChartConfig{}, e.Wrap(e.CodeVerifyFailed, err, "decoding Helm chart config")
	}
	if err := validateHelmChartMetadata(config); err != nil {
		return helmChartConfig{}, err
	}
	return config, nil
}

func matchHelmChartConfig(raw []byte, config helmChartConfig) error {
	archive, err := helmChartMetadata(raw)
	if err != nil {
		return err
	}
	// Match metadata meaning while preserving the exact original external bytes.
	// Re-encoding gives omitted and empty collections the same interpretation.
	configProjection, err := json.Marshal(config)
	if err != nil {
		return e.Wrap(e.CodeInternal, err, "encoding chart config projection")
	}
	archiveProjection, err := json.Marshal(archive)
	if err != nil {
		return e.Wrap(e.CodeInternal, err, "encoding chart archive projection")
	}
	if !bytes.Equal(configProjection, archiveProjection) {
		return e.New(e.CodeVerifyFailed, "chart config and archive metadata differ")
	}
	return nil
}
