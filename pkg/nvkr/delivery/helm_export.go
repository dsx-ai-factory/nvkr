// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"context"
	"errors"

	"github.com/dsx-ai-factory/nvkr/internal/helmarchive"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// HelmChartExport records exact transport and archive identities. It is a
// diagnostic observation, not authenticated Release or compatibility authority.
type HelmChartExport struct {
	ManifestDigest string `json:"manifestDigest"`
	ArchiveDigest  string `json:"archiveDigest"`
	Size           int64  `json:"size"`
	Name           string `json:"name"`
	Version        string `json:"version"`
}

// ExportHelmChart verifies a pinned single-chart OCI layout and materializes its
// unchanged archive with the existing no-replace writer. The caller's expected
// name/version select a chart; they do not prove its suitability for deployment.
// Helm itself remains responsible for authoritative loading/rendering.
func ExportHelmChart(ctx context.Context, root, manifestDigest, name, version, out string) (HelmChartExport, error) {
	if err := deliveryContext(ctx); err != nil {
		return HelmChartExport{}, err
	}
	if !isDigest(manifestDigest) || name == "" || version == "" || out == "" {
		return HelmChartExport{}, e.New(e.CodeUsage, "chart extraction requires an exact manifest digest, chart name/version and new destination")
	}
	result, raw, err := ReadHelmChart(ctx, root, manifestDigest, name, version)
	if err != nil {
		return HelmChartExport{}, err
	}
	if err := writeExtractedObject(ctx, out, raw); err != nil {
		var coded *e.Error
		if errors.As(err, &coded) {
			return HelmChartExport{}, err
		}
		return HelmChartExport{}, e.Wrap(e.CodeInternal, err, "materializing verified chart")
	}
	return result, nil
}

// ReadHelmChart performs the same exact transport, media, archive and metadata
// checks as ExportHelmChart without creating any files. Its bytes and description
// are diagnostic data, not a Release acceptance or deployment capability.
func ReadHelmChart(ctx context.Context, root, manifestDigest, name, version string) (HelmChartExport, []byte, error) {
	if err := deliveryContext(ctx); err != nil {
		return HelmChartExport{}, nil, err
	}
	if !isDigest(manifestDigest) || name == "" || version == "" {
		return HelmChartExport{}, nil, e.New(e.CodeUsage, "chart inspection requires an exact manifest digest and chart name/version")
	}
	descriptor, raw, err := ReadArtifact(ctx, root)
	if err != nil {
		return HelmChartExport{}, nil, err
	}
	if descriptor.Digest != manifestDigest {
		return HelmChartExport{}, nil, e.New(e.CodeVerifyFailed, "chart manifest differs from selected digest")
	}
	manifest, err := flatManifest(ctx, root, PublishedObject{DescriptorDigest: descriptor.Digest, Size: descriptor.Size})
	if err != nil {
		return HelmChartExport{}, nil, err
	}
	if manifest.Config.MediaType != MediaHelmConfig || len(manifest.Layers) != 1 || manifest.Layers[0].MediaType != MediaHelmChartLayer || digestBytes(raw) != manifest.Layers[0].Digest {
		return HelmChartExport{}, nil, e.New(e.CodeVerifyFailed, "selected artifact is not an exact single-chart Helm OCI payload")
	}
	// Reuse the acquired-chart owner for complete path/link/duplicate/padding
	// admission and gzip checksum verification, not just Chart.yaml discovery.
	identity, err := helmarchive.Inspect(ctx, raw)
	if err != nil {
		return HelmChartExport{}, nil, err
	}
	if identity.Name != name || identity.Version != version {
		return HelmChartExport{}, nil, e.New(e.CodeVerifyFailed, "chart archive differs from selected name/version")
	}
	config, err := readHelmChartConfig(ctx, root, manifest)
	if err != nil {
		return HelmChartExport{}, nil, err
	}
	if err := matchHelmChartConfig(raw, config); err != nil {
		return HelmChartExport{}, nil, err
	}
	return HelmChartExport{ManifestDigest: descriptor.Digest, ArchiveDigest: manifest.Layers[0].Digest,
		Size: int64(len(raw)), Name: config.Name, Version: config.Version}, raw, nil
}
