// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package sbom

import (
	"context"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/material"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
)

const MkosiInstalledMethod = "mkosi-installed/v1"

// WithRuntimeContents reads the original installed-package reports from the
// live produced runtime. The document records installed package identities and
// measured projected tools, without claiming package-to-package dependencies,
// license clearance, source compilation or qualification.
func WithRuntimeContents(ctx context.Context, bom *BOM, runtime *pipeline.ProducedRuntime, scratch string) (*BOM, error) {
	if err := bom.Validate(); err != nil {
		return nil, err
	}
	binding, err := runtime.BindingContext(ctx)
	if err != nil {
		return nil, err
	}
	if err := matchRuntimeSubject(bom, binding); err != nil {
		return nil, err
	}
	contents, err := runtime.InspectContents(ctx, scratch)
	if err != nil {
		return nil, err
	}
	return withRuntimeContents(bom, contents)
}

func matchRuntimeSubject(bom *BOM, binding pipeline.ProducedRuntimeBinding) error {
	build, artifact, err := metadataBindings(bom.Metadata.Props)
	if err != nil {
		return err
	}
	if build != binding.BuildDigest || artifact != binding.Component || bom.Metadata.Component.Type != "container" ||
		len(bom.Metadata.Component.Hashes) != 1 || "sha256:"+bom.Metadata.Component.Hashes[0].Content != binding.Digest {
		return e.New(e.CodeVerifyFailed, "runtime inventory differs from its exact produced subject")
	}
	return nil
}

func withRuntimeContents(bom *BOM, contents *pipeline.RuntimeContents) (*BOM, error) {
	if err := matchRuntimeSubject(bom, contents.Binding); err != nil {
		return nil, err
	}
	base := cloneBOM(bom)
	base.Metadata.Props = append(base.Metadata.Props,
		Property{Name: "nvkr:runtimeLayerDigest", Value: contents.Binding.LayerDigest},
		Property{Name: "nvkr:runtimeRecipeDigest", Value: contents.RecipeDigest})
	var components []Component
	for _, root := range contents.Roots {
		prefix := "nvkr:mkosi:" + root.Name + ":"
		base.Metadata.Props = append(base.Metadata.Props, Property{Name: prefix + "reportDigest", Value: root.ReportDigest})
		for _, item := range root.Packages {
			// Root-specific acquisition properties preserve both observations
			// when the same package appears in more than one assembled root.
			qualifiers := url.Values{"arch": {item.Architecture}, "distro": {root.Release}}
			components = append(components, Component{Type: "library", Name: item.Name, Version: item.Version,
				PURL: "pkg:deb/" + root.Distribution + "/" + url.PathEscape(item.Name) + "@" + url.PathEscape(item.Version) + "?" + qualifiers.Encode(),
				Properties: []Property{
					{Name: prefix + "archiveDigest", Value: item.Digest},
					{Name: prefix + "archiveSize", Value: strconv.FormatInt(item.Size, 10)},
					{Name: prefix + "reportDigest", Value: root.ReportDigest},
					{Name: prefix + "repositoryDigest", Value: item.RepositoryDigest},
					{Name: prefix + "snapshot", Value: root.Snapshot},
				}})
		}
	}
	for _, file := range contents.Files {
		components = append(components, Component{Type: "file", Name: file.Path,
			Hashes:     []Hash{{Alg: "SHA-256", Content: strings.TrimPrefix(file.Digest, "sha256:")}},
			Properties: []Property{{Name: "nvkr:material", Value: file.Material}, {Name: "nvkr:size", Value: strconv.FormatInt(file.Size, 10)}}})
	}
	sort.Slice(base.Metadata.Props, func(i, j int) bool { return base.Metadata.Props[i].Name < base.Metadata.Props[j].Name })
	return withDependencies(base, components, MkosiInstalledMethod, "", nil)
}

// This validates serialized evidence vocabulary, not producer authority. Only
// WithRuntimeContents can obtain inventory through the concrete producer.
func validateRuntimeInventory(bom *BOM, artifact string) error {
	if artifact == "" || bom.Metadata.Component.Type != "container" {
		return e.New(e.CodeVerifyFailed, "runtime inventory requires an exact container subject")
	}
	bindings := 0
	reports := map[string]string{}
	for _, property := range bom.Metadata.Props {
		switch {
		case property.Name == "nvkr:runtimeLayerDigest" || property.Name == "nvkr:runtimeRecipeDigest":
			if !canonical.IsDigest(property.Value) {
				return e.New(e.CodeVerifyFailed, "runtime inventory omits an exact content binding")
			}
			bindings++
		case strings.HasPrefix(property.Name, "nvkr:mkosi:") && strings.HasSuffix(property.Name, ":reportDigest"):
			if !canonical.IsDigest(property.Value) {
				return e.New(e.CodeVerifyFailed, "runtime inventory omits an original report binding")
			}
			root := strings.TrimSuffix(strings.TrimPrefix(property.Name, "nvkr:mkosi:"), ":reportDigest")
			if root == "" || strings.Contains(root, ":") {
				return e.New(e.CodeVerifyFailed, "runtime inventory has an invalid root binding")
			}
			reports[root] = property.Value
		case property.Name == "nvkr:dependencyScannerDigest":
			return e.New(e.CodeVerifyFailed, "runtime inventory reads embedded reports, not an external scanner")
		}
	}
	if bindings != 2 || len(reports) == 0 || len(reports) > 4 {
		return e.New(e.CodeProvenanceMissing, "runtime inventory lacks recipe, layer or original report bindings")
	}
	used := map[string]bool{}
	for _, component := range bom.Components {
		if component.Type == "file" {
			if !strings.HasPrefix(component.Name, "/") || path.Clean(component.Name) != component.Name || component.PURL != "" || component.Version != "" ||
				len(component.Properties) != 2 || component.Properties[0].Name != "nvkr:material" || component.Properties[1].Name != "nvkr:size" {
				return e.New(e.CodeVerifyFailed, "runtime file inventory lacks its material and measured size")
			}
			if _, err := material.StagingPath(component.Properties[0].Value); err != nil {
				return err
			}
			size, err := strconv.ParseInt(component.Properties[1].Value, 10, 64)
			if err != nil || size < 0 {
				return e.New(e.CodeVerifyFailed, "runtime file inventory has an invalid size")
			}
			continue
		}
		if component.Type != "library" || component.Version == "" || !strings.HasPrefix(component.PURL, "pkg:deb/") || len(component.Hashes) != 0 {
			return e.New(e.CodeVerifyFailed, "runtime package inventory requires installed identities without archive hashes as content")
		}
		counts := map[string]int{}
		for _, property := range component.Properties {
			value, ok := strings.CutPrefix(property.Name, "nvkr:mkosi:")
			root, field, separated := strings.Cut(value, ":")
			if !ok || !separated || reports[root] == "" {
				return e.New(e.CodeVerifyFailed, "runtime package lacks its selected root report")
			}
			switch field {
			case "archiveDigest", "repositoryDigest":
				if !canonical.IsDigest(property.Value) {
					return e.New(e.CodeVerifyFailed, "runtime package input is not exactly pinned")
				}
			case "reportDigest":
				if property.Value != reports[root] {
					return e.New(e.CodeVerifyFailed, "runtime package changed its root report")
				}
			case "archiveSize":
				size, err := strconv.ParseInt(property.Value, 10, 64)
				if err != nil || size <= 0 {
					return e.New(e.CodeVerifyFailed, "runtime package input has an invalid size")
				}
			case "snapshot":
				if _, err := time.Parse("20060102T150405Z", property.Value); err != nil {
					return e.New(e.CodeVerifyFailed, "runtime package input lacks its exact snapshot")
				}
			default:
				return e.New(e.CodeVerifyFailed, "runtime package has an unknown input binding")
			}
			counts[root]++
		}
		if len(counts) == 0 {
			return e.New(e.CodeProvenanceMissing, "runtime package lacks retained input bindings")
		}
		for root, count := range counts {
			if count != 5 {
				return e.New(e.CodeProvenanceMissing, "runtime package input binding is incomplete")
			}
			used[root] = true
		}
	}
	if len(used) != len(reports) {
		return e.New(e.CodeProvenanceMissing, "runtime inventory omits a selected root's installed packages")
	}
	return nil
}
