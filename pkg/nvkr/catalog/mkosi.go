// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalog

import (
	"crypto/sha256"
	"fmt"
	"slices"
	"unicode/utf8"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/mkosi"
)

// Authoring uses pinned provider files; inline caller bytes cannot bypass the
// configuration or package-input file check. The resolved recipe travels in IR.
type mkosiDeclaration struct {
	Configuration patchDeclaration          `json:"configuration"`
	Roots         []mkosiRootDeclaration    `json:"roots"`
	Files         []mkosi.File              `json:"files,omitempty"`
	Copies        []mkosi.PackageFile       `json:"copies,omitempty"`
	Contents      []mkosiContentDeclaration `json:"contents,omitempty"`
	Measurements  []mkosi.FileMeasurement   `json:"measurements,omitempty"`
}

type mkosiContentDeclaration struct {
	Path   string           `json:"path"`
	Source patchDeclaration `json:"source"`
}

type mkosiRootDeclaration struct {
	Name         string           `json:"name"`
	Distribution string           `json:"distribution"`
	Release      string           `json:"release"`
	Inputs       patchDeclaration `json:"inputs"`
	Mounts       []mkosi.Mount    `json:"mounts,omitempty"`
}

func resolveMkosi(provider DataProvider, name string, component *Component, declaration *mkosiDeclaration) error {
	if declaration == nil {
		return nil
	}
	if component.BuilderType != BuilderMkosi || !component.FromSource {
		return e.New(e.CodeInvalidCatalog, "component %q: mkosi recipe requires its from-source builder", name)
	}
	if len(declaration.Roots) == 0 || len(declaration.Roots) > 4 {
		return e.New(e.CodeInvalidCatalog, "component %q: mkosi requires one to four roots", name)
	}
	raw, err := readMkosiPin(provider, declaration.Configuration, 64<<10)
	if err != nil {
		return err
	}
	spec := &mkosi.Spec{Configuration: string(raw), ConfigurationDigest: declaration.Configuration.SHA256,
		Files: slices.Clone(declaration.Files), Copies: slices.Clone(declaration.Copies), Measurements: slices.Clone(declaration.Measurements)}
	if len(declaration.Contents) > 16 {
		return e.New(e.CodeInvalidCatalog, "mkosi text content exceeds its file count bound")
	}
	for _, content := range declaration.Contents {
		raw, err := readMkosiPin(provider, content.Source, 64<<10)
		if err != nil {
			return err
		}
		spec.Contents = append(spec.Contents, mkosi.Content{Path: content.Path, Digest: content.Source.SHA256, Data: string(raw)})
	}
	for _, declared := range declaration.Roots {
		root := mkosi.Root{Name: declared.Name, Distribution: declared.Distribution, Release: declared.Release, Mounts: slices.Clone(declared.Mounts)}
		raw, err := readMkosiPin(provider, declared.Inputs, 8<<20)
		if err != nil {
			return err
		}
		if err := canonical.UnmarshalExact(raw, &root.Inputs); err != nil {
			return e.Wrap(e.CodeInvalidCatalog, err, "decoding mkosi package inputs for %q", name)
		}
		spec.Roots = append(spec.Roots, root)
	}
	component.Mkosi = spec
	return nil
}

func readMkosiPin(provider DataProvider, pin patchDeclaration, limit int) ([]byte, error) {
	if !validPatchPath(pin.Name) || !canonical.IsDigest(pin.SHA256) {
		return nil, e.New(e.CodeUnpinnedInput, "mkosi input requires a provider-relative path and exact blob pin")
	}
	raw, err := provider.ReadFile(pin.Name)
	if err != nil {
		return nil, e.Wrap(e.CodeInvalidCatalog, err, "reading pinned mkosi input")
	}
	if len(raw) == 0 || len(raw) > limit || !utf8.Valid(raw) {
		return nil, e.New(e.CodeInvalidCatalog, "mkosi input is empty, oversized or invalid UTF-8")
	}
	if fmt.Sprintf("sha256:%x", sha256.Sum256(raw)) != pin.SHA256 {
		return nil, e.New(e.CodeUnpinnedInput, "mkosi input differs from its exact blob pin")
	}
	return raw, nil
}

func validateMkosi(name string, component Component) error {
	if !component.FromSource || component.Prebuilt != nil || component.ExclusionReason != "" || component.OutputType != "oci-image" ||
		component.Mkosi == nil || len(component.Platforms) != 1 || component.ArtifactPath != "nvkr-output/runtime" {
		return e.New(e.CodeInvalidCatalog, "component %q: mkosi requires one native from-source OCI runtime result", name)
	}
	if component.Embeds != nil || component.Helm != nil || component.Flake != nil || component.CGO || component.Disk != nil || component.Packer != nil ||
		len(component.BuildCommand) != 0 || len(component.BuildCommandByPlatform) != 0 || component.WriteDir != "" || len(component.VendorCommand) != 0 ||
		len(component.Patches) != 0 || len(component.VersionStamp) != 0 || len(component.SourceMetadata) != 0 ||
		component.ToolchainImage != "" || len(component.ToolchainImages) != 0 || component.Tool != "" || component.ToolExecutable != nil || len(component.ToolArgs) != 0 || len(component.ToolEnv) != 0 ||
		(component.BuildNetwork != "" && component.BuildNetwork != "none") {
		return e.New(e.CodeInvalidCatalog, "component %q: mkosi recipe cannot inherit another builder, image or network route", name)
	}
	if err := checkSourceRepoScheme(name, component.Repo); err != nil {
		return err
	}
	if !fullSHA.MatchString(component.Commit) || !fullSHA.MatchString(component.TreeHash) || component.SourceDate <= 0 {
		return e.New(e.CodeUnpinnedSource, "component %q: mkosi requires an exact source commit, tree and commit timestamp", name)
	}
	if component.BuildRequirements != nil {
		if err := component.BuildRequirements.Validate(); err != nil {
			return err
		}
	}
	if err := validateMaterialBindings(name, component); err != nil {
		return err
	}
	return component.Mkosi.Validate(component.Platforms[0], sortedStringKeys(component.Materials))
}
