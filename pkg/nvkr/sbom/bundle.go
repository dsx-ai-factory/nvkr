// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package sbom

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/dsx-ai-factory/nvkr/internal/bundlearchive"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// BundleFilesMethod inventories only regular files actually present in a bundle.
// Referenced images, charts and deployment dependencies are not fetched or scanned.
const BundleFilesMethod = "bundle-files/v1"

func inspectBundleArchive(ctx context.Context, source io.Reader) (DependencyInventory, error) {
	entries, err := bundlearchive.Inspect(ctx, source)
	if err != nil {
		return DependencyInventory{}, err
	}
	components := make([]Component, 0, len(entries))
	for _, entry := range entries {
		if entry.Directory {
			continue
		}
		components = append(components, Component{
			Type: "file", Name: entry.Path,
			Hashes: []Hash{{Alg: "SHA-256", Content: strings.TrimPrefix(entry.Digest, "sha256:")}},
			Properties: []Property{
				{Name: "nvkr:bundleFileMode", Value: fmt.Sprintf("%04o", entry.Mode)},
				{Name: "nvkr:bundleFileSize", Value: strconv.FormatInt(entry.Size, 10)},
			},
		})
	}
	return DependencyInventory{Components: components, Method: BundleFilesMethod}, nil
}

func validateBundleFileComponent(component Component) error {
	name := component.Name
	if component.Type != "file" || component.Version != "" || component.PURL != "" ||
		!bundlearchive.ValidPath(name) || len(component.Properties) != 2 ||
		component.Properties[0].Name != "nvkr:bundleFileMode" || component.Properties[1].Name != "nvkr:bundleFileSize" {
		return e.New(e.CodeVerifyFailed, "bundle inventory requires exact relative regular-file bindings")
	}
	modeText, sizeText := component.Properties[0].Value, component.Properties[1].Value
	mode, modeErr := strconv.ParseInt(modeText, 8, 64)
	size, sizeErr := strconv.ParseInt(sizeText, 10, 64)
	if modeErr != nil || mode < 0 || mode > 0o777 || fmt.Sprintf("%04o", mode) != modeText ||
		sizeErr != nil || size < 0 || size > bundlearchive.MaxExpandedBytes || strconv.FormatInt(size, 10) != sizeText {
		return e.New(e.CodeVerifyFailed, "bundle inventory has invalid file mode or size")
	}
	return nil
}
