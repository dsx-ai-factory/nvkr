// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package material

import (
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

const PackageInputsSchema = "nvkr.dev/package-inputs/v1"

var (
	aptInputName    = regexp.MustCompile(`^[a-z0-9][a-z0-9+.-]+$`)
	aptInputVersion = regexp.MustCompile(`^[0-9][A-Za-z0-9.+:~\-]*$`)
)

// PackageRepository pins repository metadata, independently of its package
// manager's wire format. URI locates the exact metadata blob, not a moving suite.
// Acquisition checks the catalog's blob digest and size. This description does
// not authenticate repository signatures or membership in referenced indexes.
type PackageRepository struct {
	URI    string `json:"uri"`
	Digest string `json:"digest"`
	Size   int64  `json:"size"`
}

// PackageInput is a retained assembly input. RepositoryDigest binds the metadata
// from which the adapter resolved the package. These records describe bytes;
// they do not authenticate downloads, installed content, or a live Run.
type PackageInput struct {
	Name             string `json:"name"`
	Version          string `json:"version"`
	Architecture     string `json:"architecture"`
	Digest           string `json:"digest"`
	Size             int64  `json:"size"`
	URI              string `json:"uri"`
	RepositoryDigest string `json:"repositoryDigest"`
}

// PackageInputs is the complete resolved package selection for one root. The
// package-manager adapter owns repository syntax and resolution. The common
// record keeps Debian control fields and archive suffixes out of the contract.
type PackageInputs struct {
	SchemaVersion  string              `json:"schemaVersion"`
	PackageManager string              `json:"packageManager"`
	Architecture   string              `json:"architecture"`
	Snapshot       string              `json:"snapshot"`
	Repositories   []PackageRepository `json:"repositories"`
	Packages       []PackageInput      `json:"packages"`
}

// Validate checks a bounded, deterministic description. Adapter admission is a
// separate check: a well-formed record cannot enable an unsupported manager.
func (inputs PackageInputs) Validate() error {
	if inputs.SchemaVersion != PackageInputsSchema {
		return e.New(e.CodeInvalidCatalog, "unsupported package-input schema")
	}
	if !canonicalToken(inputs.PackageManager, 32) {
		return e.New(e.CodeInvalidCatalog, "invalid package manager")
	}
	if inputs.Architecture != "amd64" && inputs.Architecture != "arm64" {
		return e.New(e.CodeEngineCapability, "package inputs require a native Linux architecture")
	}
	if _, err := time.Parse("20060102T150405Z", inputs.Snapshot); err != nil {
		return e.New(e.CodeInvalidCatalog, "package inputs require an exact UTC snapshot timestamp")
	}
	if len(inputs.Repositories) == 0 || len(inputs.Repositories) > 32 {
		return e.New(e.CodeInvalidCatalog, "package inputs require bounded repository metadata")
	}
	repositories := make(map[string]bool, len(inputs.Repositories))
	previous := ""
	for _, repository := range inputs.Repositories {
		if !packageInputURI(repository.URI) || repository.URI <= previous ||
			!canonical.IsDigest(repository.Digest) || repositories[repository.Digest] || repository.Size <= 0 || repository.Size > 64<<20 {
			return e.New(e.CodeInvalidCatalog, "package repositories require sorted unique locations and exact bounded blob pins")
		}
		previous = repository.URI
		repositories[repository.Digest] = true
	}
	if len(inputs.Packages) == 0 || len(inputs.Packages) > maxAcquisitionPackages {
		return e.New(e.CodeInvalidCatalog, "package input count outside supported bounds")
	}
	previous = ""
	var totalBytes int64
	digests := make(map[string]bool, len(inputs.Packages))
	for _, item := range inputs.Packages {
		key := item.Name + "\x00" + item.Architecture
		if !canonicalToken(item.Name, maxMaterialNameBytes) || !canonicalToken(item.Version, maxMaterialNameBytes) ||
			!canonicalToken(item.Architecture, 32) || key <= previous {
			return e.New(e.CodeInvalidCatalog, "package inputs require sorted unique name and architecture pairs")
		}
		if !canonical.IsDigest(item.Digest) || digests[item.Digest] || item.Size <= 0 || item.Size > maxAcquisitionPackageBytes {
			return e.New(e.CodeInvalidCatalog, "package input requires a unique bounded archive pin")
		}
		if item.Size > MaxAcquisitionArchiveBytes-totalBytes {
			return e.New(e.CodeInvalidCatalog, "package input closure exceeds the retained archive byte limit")
		}
		totalBytes += item.Size
		if !packageInputURI(item.URI) || !repositories[item.RepositoryDigest] {
			return e.New(e.CodeInvalidCatalog, "package input requires a location and selected repository metadata")
		}
		previous = key
		digests[item.Digest] = true
	}
	return nil
}

// AdmitAPT admits only the implemented package-manager adapter. Future adapters
// can consume the same neutral description without relaxing this boundary.
func (inputs PackageInputs) AdmitAPT() error {
	if err := inputs.Validate(); err != nil {
		return err
	}
	if inputs.PackageManager != "apt" {
		return e.New(e.CodeEngineCapability, "package assembly currently requires the APT adapter")
	}
	for _, item := range inputs.Packages {
		if !aptInputName.MatchString(item.Name) || !aptInputVersion.MatchString(item.Version) {
			return e.New(e.CodeInvalidCatalog, "APT input has an invalid package name or exact version")
		}
		if item.Architecture != "all" && item.Architecture != inputs.Architecture {
			return e.New(e.CodeEngineCapability, "APT package architecture differs from the assembly target")
		}
	}
	return nil
}

// Marshal returns the canonical input document for inclusion in the producer's
// selected source/material closure. It grants no acquisition authority.
func (inputs PackageInputs) Marshal() ([]byte, error) {
	if err := inputs.Validate(); err != nil {
		return nil, err
	}
	return canonical.Marshal(inputs)
}

func packageInputURI(raw string) bool {
	if !canonicalToken(raw, maxAcquisitionRepositoryURIBytes) {
		return false
	}
	location, err := url.Parse(raw)
	return err == nil && location.Scheme == "https" && location.Hostname() != "" &&
		location.User == nil && location.RawQuery == "" && !location.ForceQuery &&
		location.Fragment == "" && !strings.Contains(raw, "#") && location.Opaque == ""
}
