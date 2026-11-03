// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package sbom

import (
	"archive/tar"
	"net/url"
	"slices"
	"sort"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// WithDependencies attaches the inspected inventory to the exact existing subject.
// Root edges describe inclusion in this artifact, not inferred package-to-package
// relationships. No timestamps, scanner locations or random scanner IDs survive.
func WithDependencies(bom *BOM, components []Component, method, scannerDigest string) (*BOM, error) {
	if err := bom.Validate(); err != nil {
		return nil, err
	}
	_, artifact, err := metadataBindings(bom.Metadata.Props)
	if err != nil || artifact == "" || len(bom.Metadata.Component.Hashes) != 1 {
		return nil, e.New(e.CodeProvenanceMissing, "dependency inventory requires a per-artifact subject")
	}
	if !supportedDependencyMethod(method) {
		return nil, e.New(e.CodeUnsupported, "unsupported dependency inspection method %q", method)
	}
	if dependencyMethodNeedsScanner(method) && !canonical.IsDigest(scannerDigest) {
		return nil, e.New(e.CodeUnverifiedRef, "dependency inventory requires the scanner executable digest")
	}
	if len(components) == 0 {
		return nil, e.New(e.CodeProvenanceMissing, "dependency inspection returned no components for %q", bom.Metadata.Component.Name)
	}
	result := cloneBOM(bom)
	if method == archiveContentMethod {
		result.Metadata.Component.Type = "file"
	}
	result.Components = nil
	byRef := map[string]Component{}
	for _, component := range components {
		component.BOMRef = dependencyRef(component)
		if err := validateSBOMComponent("dependency", component); err != nil {
			return nil, err
		}
		if previous, exists := byRef[component.BOMRef]; exists {
			// Catalogers may discover the same package at several paths. Refuse
			// conflicting metadata instead of letting traversal order pick a winner.
			if previous.Type != component.Type || previous.Name != component.Name || previous.Version != component.Version || previous.PURL != component.PURL || !slices.Equal(previous.Hashes, component.Hashes) {
				return nil, e.New(e.CodeVerifyFailed, "conflicting dependency identity %q", component.BOMRef)
			}
			properties := map[string]string{}
			for _, property := range previous.Properties {
				properties[property.Name] = property.Value
			}
			for _, property := range component.Properties {
				if value, exists := properties[property.Name]; exists && value != property.Value {
					return nil, e.New(e.CodeVerifyFailed, "conflicting dependency property %q", property.Name)
				}
				properties[property.Name] = property.Value
			}
			previous.Properties = nil
			for name, value := range properties {
				previous.Properties = append(previous.Properties, Property{Name: name, Value: value})
			}
			sort.Slice(previous.Properties, func(i, j int) bool { return previous.Properties[i].Name < previous.Properties[j].Name })
			byRef[component.BOMRef] = previous
			continue
		}
		byRef[component.BOMRef] = cloneComponent(component)
	}
	var refs []string
	for ref := range byRef {
		refs = append(refs, ref)
	}
	sort.Strings(refs)
	for _, ref := range refs {
		result.Components = append(result.Components, byRef[ref])
	}
	result.Metadata.Component.BOMRef = "artifact:" + result.Metadata.Component.Hashes[0].Content
	result.Dependencies = []Dependency{{Ref: result.Metadata.Component.BOMRef, DependsOn: refs}}
	result.Metadata.Props = append(result.Metadata.Props, Property{Name: "nvkr:dependencyInspection", Value: method})
	if scannerDigest != "" {
		result.Metadata.Props = append(result.Metadata.Props, Property{Name: "nvkr:dependencyScannerDigest", Value: scannerDigest})
	}
	sort.Slice(result.Metadata.Props, func(i, j int) bool { return result.Metadata.Props[i].Name < result.Metadata.Props[j].Name })
	if err := result.Validate(); err != nil {
		return nil, err
	}
	return result, nil
}

func dependencyRef(component Component) string {
	if component.PURL != "" {
		return component.PURL
	}
	return "urn:nvkr:dependency:" + url.PathEscape(component.Type+"/"+component.Name+"@"+component.Version)
}

// This is descriptive vocabulary validation, not evidence authentication. The
// original content/Run owners still issue receipts and authenticate subjects.
func validateInventorySemantics(bom *BOM, artifact string) error {
	method := ""
	for _, property := range bom.Metadata.Props {
		if property.Name == "nvkr:dependencyInspection" {
			method = property.Value
		}
	}
	diskInventory := method == "disk-content/v1" || method == "disk-reconciled/v1"
	archiveInventory := method == archiveContentMethod
	if archiveInventory {
		outputType := ""
		for _, property := range bom.Metadata.Component.Properties {
			if property.Name == "nvkr:outputType" {
				outputType = property.Value
			}
		}
		if artifact == "" || bom.Metadata.Component.Type != "file" || outputType != "tarball" {
			return e.New(e.CodeVerifyFailed, "archive content inventory requires an exact per-artifact tarball file subject")
		}
		for _, property := range bom.Metadata.Props {
			if property.Name == "nvkr:dependencyScannerDigest" {
				return e.New(e.CodeVerifyFailed, "archive content inventory cannot claim an external package scanner")
			}
		}
	}
	if diskInventory && artifact == "" {
		return e.New(e.CodeVerifyFailed, "disk inventory requires an exact per-artifact subject")
	}
	if method == "disk-reconciled/v1" && bom.Metadata.Component.Type != "operating-system" {
		return e.New(e.CodeVerifyFailed, "reconciled disk inventory requires an operating-system subject")
	}
	if method == BundleFilesMethod && artifact == "" {
		return e.New(e.CodeVerifyFailed, "bundle inventory requires an exact per-artifact subject")
	}
	archivePaths := map[string]byte{}
	for _, component := range bom.Components {
		if archiveInventory && (component.Type != "file" || !archiveMemberPath(component.Name) || component.Version != "" || component.PURL != "" || len(component.Properties) != 0) {
			return e.New(e.CodeVerifyFailed, "archive content inventory admits only member paths and exact file hashes")
		}
		if method == BundleFilesMethod {
			if err := validateBundleFileComponent(component); err != nil {
				return err
			}
		} else if component.Type == "file" && !diskInventory && !archiveInventory {
			return e.New(e.CodeVerifyFailed, "file inventory requires its descriptive disk-content or reconciled disk method")
		}
		if archiveInventory {
			archivePaths[component.Name] = tar.TypeReg
		}
	}
	if archiveInventory && archivePathConflict(archivePaths) {
		return e.New(e.CodeVerifyFailed, "archive content inventory has conflicting file and directory paths")
	}
	return nil
}

func validateDependencies(bom *BOM) error {
	method, scanner := "", ""
	for _, property := range bom.Metadata.Props {
		if property.Name == "nvkr:dependencyInspection" {
			method = property.Value
		}
		if property.Name == "nvkr:dependencyScannerDigest" {
			scanner = property.Value
		}
	}
	if len(bom.Components) == 0 {
		if len(bom.Dependencies) != 0 || method != "" {
			return e.New(e.CodeVerifyFailed, "SBOM has dependency edges without components")
		}
		return nil
	}
	if method == "" {
		return e.New(e.CodeProvenanceMissing, "dependency inventory omits its inspection method")
	}
	if !supportedDependencyMethod(method) {
		return e.New(e.CodeUnsupported, "unsupported dependency inspection method %q", method)
	}
	if dependencyMethodNeedsScanner(method) && !canonical.IsDigest(scanner) {
		return e.New(e.CodeUnverifiedRef, "dependency inventory has no scanner pin")
	}
	if method == BundleFilesMethod && scanner != "" {
		return e.New(e.CodeVerifyFailed, "bundle file inventory uses the internal archive reader, not an external scanner")
	}
	if len(bom.Metadata.Component.Hashes) != 1 || bom.Metadata.Component.BOMRef != "artifact:"+bom.Metadata.Component.Hashes[0].Content {
		return e.New(e.CodeUnverifiedRef, "dependency SBOM must bind its root to the exact artifact")
	}
	if len(bom.Dependencies) != 1 || bom.Dependencies[0].Ref != bom.Metadata.Component.BOMRef || len(bom.Dependencies[0].DependsOn) != len(bom.Components) {
		return e.New(e.CodeVerifyFailed, "dependency SBOM must cover every component with an artifact edge")
	}
	for i, component := range bom.Components {
		if component.BOMRef != dependencyRef(component) || component.BOMRef == bom.Metadata.Component.BOMRef || strings.TrimSpace(component.BOMRef) == "" || bom.Dependencies[0].DependsOn[i] != component.BOMRef {
			return e.New(e.CodeVerifyFailed, "SBOM dependency identity or edge is invalid")
		}
	}
	return nil
}

func supportedDependencyMethod(method string) bool {
	return method == "helm-manifests/v1" || method == "syft-cyclonedx/v1" || method == "disk-reconciled/v1" || method == "disk-content/v1" || method == archiveContentMethod || method == BundleFilesMethod
}

func dependencyMethodNeedsScanner(method string) bool {
	return method != "helm-manifests/v1" && method != archiveContentMethod && method != BundleFilesMethod
}
