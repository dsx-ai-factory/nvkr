// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package releaseview

import (
	"encoding/json"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/dsx-ai-factory/nvkr/internal/versionpolicy"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// Output-only projection of NVIDIA/aicr@dc78e354f2a55985782cfe0d9d6b8f1591e75dd5
// pkg/artifact/{types,parse}.go. These compatibility bounds do not verify a release.
var inventoryHost = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9.-]*[a-z0-9])?(?::[0-9]+)?$`)
var inventoryRepository = regexp.MustCompile(`^[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*(?:/[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*)*$`)
var inventoryName = regexp.MustCompile(`^[a-z0-9]([a-z0-9._-]*[a-z0-9])?$`)

type inventoryIdentity struct {
	Name           string             `json:"name"`
	Type           string             `json:"type"`
	Reference      string             `json:"reference"`
	Version        string             `json:"version"`
	ManifestDigest string             `json:"manifestDigest"`
	ArchiveDigest  string             `json:"archiveDigest,omitempty"`
	Platform       *inventoryPlatform `json:"platform,omitempty"`
}
type inventoryPlatform struct {
	OS           string `json:"os"`
	Architecture string `json:"architecture"`
}

// ArtifactInventory projects inspected chart/image metadata into JSON-form YAML.
// Image versions are digest-derived labels, not claims that version tags exist.
// Like the input description, this grants no signature or consumer-policy trust.
func ArtifactInventory(description Description) ([]byte, error) {
	document := struct {
		APIVersion string              `json:"apiVersion"`
		Kind       string              `json:"kind"`
		Artifacts  []inventoryIdentity `json:"artifacts"`
	}{"aicr.run/v1beta1", "ArtifactInventory", []inventoryIdentity{}}
	names := make(map[string]bool)
	for _, output := range description.Outputs {
		if output.Kind != "helm-oci" && output.Kind != "oci" {
			continue
		}
		incompatible := func() ([]byte, error) {
			return nil, e.New(e.CodeEngineCapability, "output %q is not AICR inventory compatible", output.Name)
		}
		if len(output.Name) > 128 || !inventoryName.MatchString(output.Name) || names[output.Name] || !canonical.IsDigest(output.Digest) {
			return incompatible()
		}
		names[output.Name] = true
		repository, digest, pinned := strings.Cut(output.Reference, "@")
		if !pinned || digest != output.Digest {
			return incompatible()
		}
		reference := "oci://" + repository
		parsed, err := url.Parse(reference)
		if err != nil || parsed.Scheme != "oci" || parsed.User != nil || !inventoryHost.MatchString(parsed.Host) || (parsed.Host != "localhost" && !strings.ContainsAny(parsed.Host, ".:")) || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.RawPath != "" || strings.ContainsAny(reference, "@%?#") || !inventoryRepository.MatchString(strings.TrimPrefix(parsed.Path, "/")) {
			return incompatible()
		}
		item := inventoryIdentity{Name: output.Name, Type: "image", Reference: reference, ManifestDigest: output.Digest, Version: strings.ReplaceAll(output.Digest, ":", "-")}
		if output.Kind == "helm-oci" {
			if output.Helm == nil || !canonical.IsDigest(output.Helm.DefaultDigest) || len(output.ChartVersion) > 128 || !versionpolicy.PinSet(output.ChartVersion) {
				return incompatible()
			}
			item.Type, item.Version, item.ArchiveDigest = "chart", strings.ReplaceAll(output.ChartVersion, "+", "_"), output.Helm.DefaultDigest
		} else if output.ImageIndex == nil {
			if output.OS != "linux" || (output.Arch != "amd64" && output.Arch != "arm64") {
				return incompatible()
			}
			item.Platform = &inventoryPlatform{OS: output.OS, Architecture: output.Arch}
		}
		document.Artifacts = append(document.Artifacts, item)
	}
	if len(document.Artifacts) == 0 {
		return nil, e.New(e.CodeReleaseIncomplete, "release has no chart or image inventory entries")
	}
	if len(document.Artifacts) > 10000 {
		return nil, e.New(e.CodeEngineCapability, "inventory exceeds AICR entry limit")
	}
	slices.SortFunc(document.Artifacts, func(a, b inventoryIdentity) int { return strings.Compare(a.Name, b.Name) })
	raw, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return nil, e.Wrap(e.CodeInternal, err, "encoding AICR inventory")
	}
	if len(raw)+1 > 8<<20 {
		return nil, e.New(e.CodeEngineCapability, "inventory exceeds AICR document limit")
	}
	return append(raw, '\n'), nil
}
