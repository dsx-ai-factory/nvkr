// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package mkosi owns the package assembly recipe and its offline invocation
// contract. A recipe is descriptive; it cannot authorize host execution or issue
// a Run-owned runtime-use receipt.
package mkosi

import (
	"crypto/sha256"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/material"
)

var recipeName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)
var contentPath = regexp.MustCompile(`^/[A-Za-z0-9_./+-]+$`)

// EvidenceDirectory is reserved for the original assembly reports and recipe.
const EvidenceDirectory = "/nvkr/mkosi"

// Spec is a transport-complete recipe. Configuration contains the exact verified
// mkosi.conf bytes. Roots are in dependency order; the last root is the OCI result.
// Distribution-specific package resolution belongs to the package-manager adapter.
type Spec struct {
	Configuration       string            `json:"configuration"`
	ConfigurationDigest string            `json:"configurationDigest"`
	Roots               []Root            `json:"roots"`
	Files               []File            `json:"files,omitempty"`
	Copies              []PackageFile     `json:"copies,omitempty"`
	Contents            []Content         `json:"contents,omitempty"`
	Measurements        []FileMeasurement `json:"measurements,omitempty"`
}

type Root struct {
	Name         string                 `json:"name"`
	Distribution string                 `json:"distribution"`
	Release      string                 `json:"release"`
	Inputs       material.PackageInputs `json:"inputs,omitzero"`
	// InputsDigest is a Plan transport reference. The factory must authenticate
	// and restore it before this recipe can be validated or executed.
	InputsDigest string  `json:"inputsDigest,omitempty"`
	Mounts       []Mount `json:"mounts,omitempty"`
}

// Mount nests a previously assembled root at a stable path in this root.
type Mount struct {
	Root string `json:"root"`
	Path string `json:"path"`
}

// File projects one authenticated regular-file material into the final root.
// Static tools keep their ordinary producer and acquisition/license closure.
type File struct {
	Material string `json:"material"`
	Path     string `json:"path"`
	Mode     uint32 `json:"mode"`
}

// PackageFile copies measured package bytes to a stable runtime-owned path.
// Original package files and metadata remain in the assembled root.
type PackageFile struct {
	Source string `json:"source"`
	Path   string `json:"path"`
	Digest string `json:"digest"`
	Mode   uint32 `json:"mode"`
}

// Content retains an exact pinned text file, such as a license or plugin hash.
// It is always staged with mode 0444; it grants no execution authority.
type Content struct {
	Path   string `json:"path"`
	Digest string `json:"digest"`
	Data   string `json:"data"`
}

// FileMeasurement binds expected firmware or executable bytes. It is a check on
// the assembled result, not an exemption from package or source provenance.
type FileMeasurement struct {
	Path   string `json:"path"`
	Digest string `json:"digest"`
}

func (spec *Spec) Validate(platform string, bindings []string) error {
	if spec == nil {
		return e.New(e.CodeInvalidCatalog, "mkosi producer requires its complete recipe")
	}
	if platform != "linux/amd64" && platform != "linux/arm64" {
		return e.New(e.CodeEngineCapability, "mkosi requires a native Linux target")
	}
	if spec.Configuration == "" || len(spec.Configuration) > 64<<10 || !utf8.ValidString(spec.Configuration) || strings.ContainsRune(spec.Configuration, 0) ||
		!canonical.IsDigest(spec.ConfigurationDigest) || fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(spec.Configuration))) != spec.ConfigurationDigest {
		return e.New(e.CodeUnpinnedInput, "mkosi configuration differs from its exact file pin")
	}
	if err := validateConfiguration(spec.Configuration); err != nil {
		return err
	}
	if len(spec.Roots) == 0 || len(spec.Roots) > 4 {
		return e.New(e.CodeInvalidCatalog, "mkosi requires one to four ordered roots")
	}
	seen := make(map[string]bool, len(spec.Roots))
	used := make(map[string]bool, len(spec.Roots))
	for _, root := range spec.Roots {
		if root.InputsDigest != "" {
			return e.New(e.CodeInvalidCatalog, "mkosi package inputs require Plan reference resolution")
		}
		if !recipeName.MatchString(root.Name) || seen[root.Name] || !recipeName.MatchString(root.Distribution) || !recipeName.MatchString(root.Release) {
			return e.New(e.CodeInvalidCatalog, "mkosi roots require unique names and explicit distribution releases")
		}
		if root.Distribution != "ubuntu" && root.Distribution != "debian" {
			return e.New(e.CodeEngineCapability, "mkosi currently admits APT distributions only")
		}
		if err := root.Inputs.AdmitAPT(); err != nil {
			return err
		}
		if root.Inputs.Architecture != strings.TrimPrefix(platform, "linux/") {
			return e.New(e.CodeEngineCapability, "mkosi root package inputs differ from the target platform")
		}
		paths := make([]string, 0, len(root.Mounts))
		if len(root.Mounts) > 3 {
			return e.New(e.CodeInvalidCatalog, "mkosi root mount count exceeds its bound")
		}
		previous := ""
		for _, mount := range root.Mounts {
			if !seen[mount.Root] || !projectionPath(mount.Path) || mount.Path <= previous {
				return e.New(e.CodeInvalidCatalog, "mkosi root mount requires a prior root and a private content path")
			}
			paths = append(paths, mount.Path)
			used[mount.Root] = true
			previous = mount.Path
		}
		if err := disjointPaths(paths); err != nil {
			return err
		}
		seen[root.Name] = true
	}
	for _, root := range spec.Roots[:len(spec.Roots)-1] {
		if !used[root.Name] {
			return e.New(e.CodeInvalidCatalog, "mkosi selects a root outside the final result closure")
		}
	}
	if len(spec.Files) > 64 || len(spec.Measurements) > 64 || len(spec.Copies) > 64 || len(spec.Contents) > 16 {
		return e.New(e.CodeInvalidCatalog, "mkosi file projection or measurement count exceeds its bound")
	}
	selected := make(map[string]bool, len(bindings))
	for _, binding := range bindings {
		if _, err := material.StagingPath(binding); err != nil {
			return err
		}
		if selected[binding] {
			return e.New(e.CodeInvalidCatalog, "mkosi repeats a material binding")
		}
		selected[binding] = true
	}
	paths := []string{EvidenceDirectory}
	previousFile := ""
	for _, file := range spec.Files {
		if !selected[file.Material] || !projectionPath(file.Path) || file.Path <= previousFile || (file.Mode != 0o444 && file.Mode != 0o555) {
			return e.New(e.CodeInvalidCatalog, "mkosi file requires one selected material, content path and read-only mode")
		}
		delete(selected, file.Material)
		paths = append(paths, file.Path)
		previousFile = file.Path
	}
	if len(selected) != 0 {
		return e.New(e.CodeInvalidCatalog, "mkosi has unused material bindings")
	}
	for _, mount := range spec.Roots[len(spec.Roots)-1].Mounts {
		paths = append(paths, mount.Path)
	}
	previous := ""
	for _, copied := range spec.Copies {
		if !projectionPath(copied.Source) || !projectionPath(copied.Path) || !strings.HasPrefix(copied.Path, "/nvkr/") ||
			copied.Path <= previous || copied.Source == copied.Path || !canonical.IsDigest(copied.Digest) || (copied.Mode != 0o444 && copied.Mode != 0o555) {
			return e.New(e.CodeInvalidCatalog, "mkosi package copies require measured bytes and sorted runtime-owned destinations")
		}
		paths = append(paths, copied.Path)
		previous = copied.Path
	}
	previous = ""
	for _, content := range spec.Contents {
		if !projectionPath(content.Path) || content.Path <= previous || len(content.Data) == 0 || len(content.Data) > 64<<10 ||
			!utf8.ValidString(content.Data) || strings.ContainsRune(content.Data, 0) || !canonical.IsDigest(content.Digest) ||
			fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(content.Data))) != content.Digest {
			return e.New(e.CodeUnpinnedInput, "mkosi text content requires sorted unique paths and exact bounded file pins")
		}
		paths = append(paths, content.Path)
		previous = content.Path
	}
	if err := disjointPaths(paths); err != nil {
		return err
	}
	previous = ""
	for _, measurement := range spec.Measurements {
		if !projectionPath(measurement.Path) || measurement.Path <= previous || !canonical.IsDigest(measurement.Digest) {
			return e.New(e.CodeInvalidCatalog, "mkosi measurements require sorted unique content paths and exact blob pins")
		}
		previous = measurement.Path
	}
	return nil
}

func projectionPath(value string) bool {
	if len(value) > 4096 || !contentPath.MatchString(value) || path.Clean(value) != value {
		return false
	}
	// These prefixes contain package and NVKR-owned runtime data. The launcher
	// separately owns /dev, /proc, /sys, scratch and input/output projections.
	return strings.HasPrefix(value, "/usr/") || strings.HasPrefix(value, "/opt/") || strings.HasPrefix(value, "/nvkr/")
}

func disjointPaths(paths []string) error {
	for index, candidate := range paths {
		for _, earlier := range paths[:index] {
			if candidate == earlier || strings.HasPrefix(candidate, earlier+"/") || strings.HasPrefix(earlier, candidate+"/") {
				return e.New(e.CodeInvalidCatalog, "mkosi content projections overlap")
			}
		}
	}
	return nil
}

// Clone prevents caller-owned slices from changing a compiled recipe.
func Clone(spec *Spec) *Spec {
	if spec == nil {
		return nil
	}
	clone := *spec
	clone.Roots = slices.Clone(spec.Roots)
	clone.Files = slices.Clone(spec.Files)
	clone.Copies = slices.Clone(spec.Copies)
	clone.Contents = slices.Clone(spec.Contents)
	clone.Measurements = slices.Clone(spec.Measurements)
	for index := range clone.Roots {
		root := &clone.Roots[index]
		root.Mounts = slices.Clone(root.Mounts)
		root.Inputs.Repositories = slices.Clone(root.Inputs.Repositories)
		root.Inputs.Packages = slices.Clone(root.Inputs.Packages)
	}
	return &clone
}
