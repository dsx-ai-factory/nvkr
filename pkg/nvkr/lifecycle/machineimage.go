// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"encoding/base32"
	"encoding/hex"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/material"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
	"go.yaml.in/yaml/v3"
)

// MachineImageMetadata describes a proposed versioned metadata binding. It is
// not producer evidence or an authorization to attach these values to an output.
type MachineImageMetadata struct {
	Output    string `json:"output"`
	Family    string `json:"family"`
	Version   string `json:"version"`
	Variant   string `json:"variant,omitempty"`
	BuildDate string `json:"buildDate"`
}

// MachineImageRenderRequest describes the future lifecycle join. Neither a
// profile string nor caller-supplied metadata can admit the disabled capability.
// This experimental request shape may change before production admission.
type MachineImageRenderRequest struct {
	Inputs       *BootInputExpectation
	Prepared     []*PreparedBootAssets
	Metadata     []MachineImageMetadata
	Profile      string
	TrustProfile string
}

// MachineImageManifest is exact descriptive content, not a signature or receipt.
type MachineImageManifest struct {
	Name              string
	YAML              []byte
	DependencyBinding []byte
}

// RenderMachineImages is deliberately unavailable. The strict imager profile,
// source pin, metadata/Plan joins and amd64/arm64 boot evidence must be admitted
// before this entry point can consume live receipts and release artifact bytes.
// It performs no receipt inspection, filesystem, network or publication work.
func (consumer *Consumer) RenderMachineImages(_ context.Context, _ MachineImageRenderRequest) ([]MachineImageManifest, error) {
	return nil, e.New(e.CodeEngineCapability, "MachineImage rendering requires an admitted strict imager profile, source pin and amd64/arm64 boot evidence")
}

// Everything below is a private formatting candidate, exercised by contracts
// only. It does not authenticate descriptions, restore receipts, authorize
// metadata, sign content or assert membership in a future aggregate.
const machineImageSchemaCommit = "28b9e338c1d08a4a0427b7c6d79c8174358ad991"
const machineImageCandidateVersion = "nvkr.dev/machineimage-candidate/v1"
const machineImageMaxScriptBytes = 16384

type machineImageCandidate struct {
	Metadata      MachineImageMetadata      `json:"metadata"`
	Disk          delivery.BootAssetBinding `json:"disk"`
	Imager        delivery.BootAssetBinding `json:"imager"`
	Destination   string                    `json:"destination"`
	ServingDigest string                    `json:"servingDigest"`
	DiskURL       string                    `json:"diskURL"`
	ImagerURL     string                    `json:"imagerURL"`
	// Synthetic profile/source descriptions are permitted only in these private
	// contract fixtures; no production profile is admitted by this serializer.
	Profile      string `json:"profile"`
	TrustProfile string `json:"trustProfile"`
	SourceCommit string `json:"sourceCommit"`
}

type machineImageSpec struct {
	OperatingSystem machineImageOS   `json:"operatingSystem" yaml:"operatingSystem"`
	Architecture    string           `json:"architecture" yaml:"architecture"`
	Variant         string           `json:"variant,omitempty" yaml:"variant,omitempty"`
	BuildDate       string           `json:"buildDate" yaml:"buildDate"`
	NICo            machineImageNICo `json:"nico" yaml:"nico"`
}
type machineImageOS struct {
	Family  string `json:"family" yaml:"family"`
	Version string `json:"version" yaml:"version"`
}
type machineImageNICo struct {
	IPXEScript string `json:"ipxeScript" yaml:"ipxeScript"`
}
type machineImageDocument struct {
	APIVersion string `yaml:"apiVersion"`
	Kind       string `yaml:"kind"`
	Metadata   struct {
		Name string `yaml:"name"`
	} `yaml:"metadata"`
	Spec machineImageSpec `yaml:"spec"`
}

var machineImageLabel = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
var machineImageVariant = regexp.MustCompile(`^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$`)
var machineImageVersion = regexp.MustCompile(`^[0-9]+(\.[0-9]+)*$`)
var machineImageSource = regexp.MustCompile(`^[0-9a-f]{40}$`)

func renderMachineImageCandidates(candidates []machineImageCandidate) ([]MachineImageManifest, error) {
	if len(candidates) == 0 || len(candidates) > 256 {
		return nil, e.New(e.CodeVerifyFailed, "MachineImage candidate set must contain 1..256 pairs")
	}
	candidates = slices.Clone(candidates)
	slices.SortFunc(candidates, func(a, b machineImageCandidate) int {
		return strings.Compare(machineImagePair(a), machineImagePair(b))
	})
	result := make([]MachineImageManifest, 0, len(candidates))
	names := make(map[string]bool, len(candidates))
	for index, candidate := range candidates {
		if index > 0 && machineImagePair(candidate) == machineImagePair(candidates[index-1]) {
			return nil, e.New(e.CodeVerifyFailed, "duplicate MachineImage disk/platform/destination pair")
		}
		manifest, err := renderMachineImageCandidate(candidate)
		if err != nil {
			return nil, err
		}
		if names[manifest.Name] {
			return nil, e.New(e.CodeVerifyFailed, "MachineImage name collision")
		}
		names[manifest.Name] = true
		result = append(result, manifest)
	}
	return result, nil
}

func machineImagePair(candidate machineImageCandidate) string {
	return candidate.Disk.Output + "\x00" + candidate.Disk.Platform + "\x00" + candidate.Destination
}

func renderMachineImageCandidate(candidate machineImageCandidate) (MachineImageManifest, error) {
	if err := validateMachineImageCandidate(candidate); err != nil {
		return MachineImageManifest{}, err
	}
	arch := strings.TrimPrefix(candidate.Disk.Platform, "linux/")
	console, kernel := "console=tty0 console=ttyS1,115200", "pci=realloc=off reboot=pci loglevel=1 iommu=pt intel_iommu=on"
	if arch == "arm64" {
		console, kernel = "console=tty0 console=ttyAMA0,115200", "loglevel=1 iommu.passthrough=1"
	}
	// The downstream strategy and cloud-init variables remain literal. URLs use
	// a deliberately narrow literal grammar; no shell/template escaping fallback.
	script := fmt.Sprintf("#!ipxe\nchain %s %s %s image_url=%s image_sha=%s disk_strategy={{ .DiskSelectionStrategy }} ds=nocloud-net;s=${cloudinit-url} update_grub_template=yes update_grub_cfg=yes\nboot\n",
		candidate.ImagerURL, console, kernel, candidate.DiskURL, strings.TrimPrefix(candidate.Disk.SHA256, "sha256:"))
	if len(script) > machineImageMaxScriptBytes {
		return MachineImageManifest{}, e.New(e.CodeVerifyFailed, "MachineImage script exceeds pinned schema bound")
	}
	spec := machineImageSpec{OperatingSystem: machineImageOS{Family: candidate.Metadata.Family, Version: candidate.Metadata.Version},
		Architecture: arch, Variant: candidate.Metadata.Variant, BuildDate: candidate.Metadata.BuildDate, NICo: machineImageNICo{IPXEScript: script}}
	// This identifies proposed build material, not a compiled Build or Release.
	candidateRecord := struct {
		Schema       string                `json:"schema"`
		SchemaCommit string                `json:"schemaCommit"`
		Candidate    machineImageCandidate `json:"candidate"`
		Spec         machineImageSpec      `json:"spec"`
	}{machineImageCandidateVersion, machineImageSchemaCommit, candidate, spec}
	candidateBytes, err := canonical.Marshal(candidateRecord)
	if err != nil {
		return MachineImageManifest{}, err
	}
	inputs, err := material.NewManifest([]material.Observation{{Binding: "machineimage-candidate", Component: "machineimage",
		Kind: "machineimage-candidate-v1", Architecture: arch, Digest: pipeline.FileDigest(candidateBytes)}})
	if err != nil {
		return MachineImageManifest{}, err
	}
	digest, err := inputs.Digest()
	if err != nil {
		return MachineImageManifest{}, err
	}
	hexDigest, err := hex.DecodeString(strings.TrimPrefix(digest, "sha256:"))
	if err != nil {
		return MachineImageManifest{}, e.Wrap(e.CodeInternal, err, "encoding MachineImage material identity")
	}
	name := "mi-" + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(hexDigest))
	document := machineImageDocument{APIVersion: "internal.nvke.nvidia.com/v1", Kind: "MachineImage", Spec: spec}
	document.Metadata.Name = name
	raw, err := yaml.Marshal(document)
	if err != nil {
		return MachineImageManifest{}, e.Wrap(e.CodeInternal, err, "encoding MachineImage candidate")
	}
	binding, err := canonical.Marshal(struct {
		Material       any                `json:"material"`
		Inputs         *material.Manifest `json:"inputs"`
		MaterialDigest string             `json:"materialDigest"`
		ManifestSHA256 string             `json:"manifestSHA256"`
	}{candidateRecord, inputs, digest, pipeline.FileDigest(raw)})
	if err != nil {
		return MachineImageManifest{}, err
	}
	return MachineImageManifest{Name: name, YAML: raw, DependencyBinding: binding}, nil
}

func validateMachineImageCandidate(candidate machineImageCandidate) error {
	invalid := func() error {
		return e.New(e.CodeVerifyFailed, "invalid MachineImage candidate metadata or dependency binding")
	}
	if candidate.TrustProfile != "importer-verification-tls-candidate-v1" {
		return e.New(e.CodeEngineCapability, "MachineImage candidate has no loader or firmware enforcement profile")
	}
	metadata := candidate.Metadata
	if metadata.Output != candidate.Disk.Output || len(metadata.Family) > 63 || !machineImageLabel.MatchString(metadata.Family) ||
		len(metadata.Version) > 32 || !machineImageVersion.MatchString(metadata.Version) || len(metadata.Variant) > 63 ||
		(metadata.Variant != "" && !machineImageVariant.MatchString(metadata.Variant)) {
		return invalid()
	}
	date, err := time.Parse("2006-01-02", metadata.BuildDate)
	if err != nil || date.Format("2006-01-02") != metadata.BuildDate {
		return invalid()
	}
	if candidate.Disk.Platform != "linux/amd64" && candidate.Disk.Platform != "linux/arm64" {
		return invalid()
	}
	if candidate.Imager.Platform != candidate.Disk.Platform || candidate.Disk.Output == candidate.Imager.Output ||
		(len(candidate.Destination) > 63 || !machineImageLabel.MatchString(candidate.Destination)) || !canonical.IsDigest(candidate.ServingDigest) ||
		candidate.Profile == "" || !machineImageSource.MatchString(candidate.SourceCommit) || candidate.Profile != candidate.Imager.Profile {
		return invalid()
	}
	if candidate.Disk.Kind != "disk-image" || candidate.Disk.Member != "" || candidate.Disk.Profile != "" || candidate.Imager.Kind != "executable" {
		return invalid()
	}
	selection := definition.BootSelection{SchemaVersion: definition.BootSelectionSchemaVersion,
		Disks:   []definition.BootDisk{{Output: candidate.Disk.Output, Platform: candidate.Disk.Platform}},
		Imagers: []definition.BootImager{{Output: candidate.Imager.Output, Member: candidate.Imager.Member, Platform: candidate.Imager.Platform, Profile: candidate.Imager.Profile}}}
	if err := selection.Validate(); err != nil {
		return invalid()
	}
	for _, binding := range []delivery.BootAssetBinding{candidate.Disk, candidate.Imager} {
		if binding.Output == "" || !canonical.IsDigest(binding.SHA256) || binding.Size <= 0 ||
			!canonical.IsDigest(binding.BuildDigest) || !canonical.IsDigest(binding.DescriptorDigest) {
			return invalid()
		}
		if err := delivery.ValidatePublicationReference(binding.Publication); err != nil {
			return invalid()
		}
	}
	if candidate.DiskURL == candidate.ImagerURL {
		return invalid()
	}
	for _, location := range []string{candidate.DiskURL, candidate.ImagerURL} {
		if err := validateMachineImageURL(location); err != nil {
			return err
		}
	}
	return nil
}

func validateMachineImageURL(location string) error {
	parsed, err := url.Parse(location)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" ||
		parsed.ForceQuery || parsed.Fragment != "" || parsed.RawPath != "" || parsed.Opaque != "" || parsed.Path == "" || path.Clean(parsed.Path) != parsed.Path {
		return e.New(e.CodeVerifyFailed, "MachineImage candidate requires a literal HTTPS URL")
	}
	if port := parsed.Port(); port != "" {
		number, err := strconv.Atoi(port)
		if err != nil || number < 1 || number > 65535 {
			return e.New(e.CodeVerifyFailed, "MachineImage URL has an invalid port")
		}
	}
	for _, character := range location {
		if !(character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || strings.ContainsRune("-._~:/[]", character)) {
			return e.New(e.CodeVerifyFailed, "MachineImage URL is unsafe in the pinned script grammar")
		}
	}
	return nil
}
