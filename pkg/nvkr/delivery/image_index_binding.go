// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"slices"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

// ImageIndexBinding describes the required native build vector and observed
// image children. It is not a receipt; publication verifies the held OCI bytes,
// and the release consumer independently authenticates build/evidence membership.
type ImageIndexBinding struct {
	RequiredPlatforms []string                 `json:"requiredPlatforms"`
	Children          []ImageIndexChildBinding `json:"children"`
}

// ImageIndexChildBinding keeps the native manifest identity separate from the
// logical Build and the exact OCI-export archive scanned to produce its SBOM.
type ImageIndexChildBinding struct {
	Platform       string `json:"platform"`
	ManifestDigest string `json:"manifestDigest"`
	Size           int64  `json:"size"`
	BuildDigest    string `json:"buildDigest"`
	Component      string `json:"component"`
	BuildSubject   string `json:"buildSubject"`
}

func (binding *ImageIndexBinding) Clone() *ImageIndexBinding {
	if binding == nil {
		return nil
	}
	return &ImageIndexBinding{RequiredPlatforms: slices.Clone(binding.RequiredPlatforms), Children: slices.Clone(binding.Children)}
}

// Validate checks descriptive shape only, never runtime or signature authority.
func (binding *ImageIndexBinding) Validate() error {
	if binding == nil || len(binding.RequiredPlatforms) == 0 || len(binding.RequiredPlatforms) > 2 ||
		len(binding.Children) != len(binding.RequiredPlatforms) {
		return e.New(e.CodeVerifyFailed, "image index binding must cover its explicit required platform set")
	}
	previous, component := "", ""
	manifests := map[string]bool{}
	for i, platform := range binding.RequiredPlatforms {
		child := binding.Children[i]
		if (platform != "linux/amd64" && platform != "linux/arm64") || platform <= previous || child.Platform != platform ||
			!isDigest(child.ManifestDigest) || manifests[child.ManifestDigest] || child.Size <= 0 || child.Size > 8<<20 ||
			!isDigest(child.BuildDigest) || !isDigest(child.BuildSubject) || !outputNamePattern.MatchString(child.Component) ||
			(component != "" && child.Component != component) {
			return e.New(e.CodeVerifyFailed, "image index children must be sorted, unique, exact native build and payload bindings")
		}
		previous, component = platform, child.Component
		manifests[child.ManifestDigest] = true
	}
	return nil
}

func validateImageIndexObject(object PublishedObject) error {
	if err := validateImageIndexMetadata(object.Kind, object.ArtifactType, object.OS, object.Arch, object.Output); err != nil {
		return err
	}
	if !isDigest(object.DescriptorDigest) || object.Size <= 0 || object.Size > 8<<20 ||
		object.BlobDigest != object.DescriptorDigest || object.SemanticDigest != object.DescriptorDigest {
		return e.New(e.CodeVerifyFailed, "image index publication metadata has inconsistent descriptor identities")
	}
	return nil
}

func validateImageIndexMetadata(kind, media, osName, arch string, output *OutputBinding) error {
	if output == nil || output.Format != "oci" || kind != "container-image" || media != MediaOCIIndex || osName != "" || arch != "" ||
		output.TransportPath != "" || len(output.BuildSubjects) != 0 || len(output.Components) != 1 || !isDigest(output.BuildDigest) {
		return e.New(e.CodeVerifyFailed, "aggregate image requires an exact OCI index binding without a single platform or build subject")
	}
	binding := output.ImageIndex
	if err := binding.validateBuildDigest(output.BuildDigest); err != nil {
		return err
	}
	if binding.Children[0].Component != output.Components[0] {
		return e.New(e.CodeVerifyFailed, "image index publication metadata has an inconsistent component")
	}
	return nil
}

func admitImageIndexInput(input ObjectInput) error {
	if err := validateImageIndexMetadata(input.Kind, input.ArtifactType, input.OS, input.Arch, input.Output); err != nil {
		return err
	}
	if !input.Layout || !isDigest(input.BlobDigest) || input.SemanticDigest != input.BlobDigest || input.Evidence != nil {
		return e.New(e.CodeVerifyFailed, "image index publication requires an exact runnable layout pin")
	}
	return nil
}

func validatePublishedImageIndex(object PublishedObject, description ImageIndexDescription) error {
	if err := validateImageIndexObject(object); err != nil {
		return err
	}
	binding := object.Output.ImageIndex
	if description.Digest != object.DescriptorDigest || description.Size != object.Size ||
		len(description.Children) != len(binding.Children) {
		return e.New(e.CodeVerifyFailed, "runnable index differs from its exact publication descriptor")
	}
	for i, child := range description.Children {
		want := binding.Children[i]
		if child.Platform != want.Platform || child.ManifestDigest != want.ManifestDigest || child.Size != want.Size ||
			want.Component != object.Output.Components[0] {
			return e.New(e.CodeVerifyFailed, "runnable index child differs from its required platform or native manifest binding")
		}
	}
	return nil
}

// validateBuildDigest checks the sole logical identity projection. It does not
// establish that any described native build executed or was authenticated.
func (binding *ImageIndexBinding) validateBuildDigest(expected string) error {
	if err := binding.Validate(); err != nil {
		return err
	}
	children := make([]ir.ImageIndexBuildChild, len(binding.Children))
	for i, child := range binding.Children {
		children[i] = ir.ImageIndexBuildChild{Platform: child.Platform, BuildDigest: child.BuildDigest, Component: child.Component}
	}
	digest, err := ir.ImageIndexBuildDigest(binding.RequiredPlatforms, children)
	if err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "invalid image index logical build vector")
	}
	if digest != expected {
		return e.New(e.CodeVerifyFailed, "image index aggregate Build Digest differs from its native build vector")
	}
	return nil
}
