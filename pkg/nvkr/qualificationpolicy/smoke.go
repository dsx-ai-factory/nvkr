// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package qualificationpolicy owns provider-independent delivered-tool policy.
package qualificationpolicy

import (
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/google/go-containerregistry/pkg/name"
)

// Smoke selects immutable native execution images and exact output membership.
// Workflow is interpreted only by hosted workflow admission. Profiles select fixed version commands;
// arbitrary commands, arguments, mounts and environment are not configurable.
type Smoke struct {
	Workflow       string            `json:"workflow"`
	Images         map[string]string `json:"images"`
	Tools          map[string]string `json:"tools"`
	NonToolOutputs []NonToolOutput   `json:"nonToolOutputs,omitempty"`
	NonToolMembers []NonToolMember   `json:"nonToolMembers,omitempty"`
}

// NonToolMember selects one data archive in a mixed executable
// bundle. The Plan must declare a tarball producer and the delivered member must
// be non-executable. This does not exempt sibling executables or archive checks.
type NonToolMember struct {
	Output string `json:"output"`
	Member string `json:"member"`
	Reason string `json:"reason"`
}

func (rule NonToolMember) Key() string { return rule.Output + "\x00" + rule.Member }

// NonToolOutput explicitly scopes one exact OCI, APT or bundle output outside
// delivered loose-tool smoke. Deployment images may contain tools; this rule
// makes no assertion about their internal content or operational acceptance.
type NonToolOutput struct {
	Output     string   `json:"output"`
	Kind       string   `json:"kind"`
	Components []string `json:"components"`
	Reason     string   `json:"reason"`
}

const NamePattern = `^[a-z0-9](?:[a-z0-9._-]{0,126}[a-z0-9])?$`

var smokeName = regexp.MustCompile(NamePattern)

// ValidateExecution admits only execution scope. Hosted identity and signing
// admission remain in validate; local rehearsal must not invent GitHub facts.
func (smoke Smoke) ValidateExecution() error {
	if len(smoke.Images) == 0 || len(smoke.Images) > 2 {
		return e.New(e.CodeTrustPolicyMissing, "smoke requires independently pinned native execution images")
	}
	for platform, image := range smoke.Images {
		if !(platform == "linux/amd64" || platform == "linux/arm64") || strings.Contains(image, "://") {
			return e.New(e.CodeUnpinnedInput, "smoke image requires a supported native platform and exact OCI digest reference")
		}
		ref, err := name.NewDigest(image, name.StrictValidation)
		if err != nil || ref.Name() != image || !canonical.IsDigest(ref.DigestStr()) {
			return e.New(e.CodeUnpinnedInput, "smoke image requires a canonical registry reference pinned by digest")
		}
	}
	if len(smoke.Tools) == 0 || len(smoke.Tools) > 1024 || len(smoke.NonToolOutputs) > 1024 || len(smoke.NonToolMembers) > 4096 {
		return e.New(e.CodeTrustPolicyMissing, "smoke tool and output policy exceeds its supported bounds or has no tools")
	}
	for component, profile := range smoke.Tools {
		if !smokeName.MatchString(component) {
			return e.New(e.CodeTrustPolicyMissing, "smoke component names must be exact canonical names")
		}
		if _, err := Profile(profile); err != nil {
			return err
		}
	}
	seenMembers := make(map[string]bool, len(smoke.NonToolMembers))
	for _, rule := range smoke.NonToolMembers {
		_, tool := smoke.Tools[rule.Member]
		if !smokeName.MatchString(rule.Output) || !smokeName.MatchString(rule.Member) || rule.Reason != "runtime-assets-archive" || seenMembers[rule.Key()] || tool {
			return e.New(e.CodeTrustPolicyMissing, "non-tool members require unique exact output/member archive roles disjoint from tools")
		}
		seenMembers[rule.Key()] = true
	}
	seen := make(map[string]bool, len(smoke.NonToolOutputs))
	for _, output := range smoke.NonToolOutputs {
		if !smokeName.MatchString(output.Output) || seen[output.Output] || len(output.Components) == 0 || len(output.Components) > 1024 {
			return e.New(e.CodeTrustPolicyMissing, "non-tool smoke scope requires unique exact outputs and nonempty bounded component membership")
		}
		seen[output.Output] = true
		if (output.Kind != "oci" || output.Reason != "deployment-image") && (output.Kind != "apt-repository" || output.Reason != "package-repository") && (output.Kind != "bundle-oci" || output.Reason != "bundle-archive") {
			return e.New(e.CodeTrustPolicyMissing, "non-tool smoke scope requires an explicit deployment-image, package-repository or bundle-archive role")
		}
		components := make(map[string]bool, len(output.Components))
		for _, component := range output.Components {
			if !smokeName.MatchString(component) || components[component] {
				return e.New(e.CodeTrustPolicyMissing, "non-tool output component scope must be exact and unique")
			}
			components[component] = true
		}
	}
	return nil
}

// Clone freezes all mutable policy membership.
func (smoke Smoke) Clone() Smoke {
	smoke.Images = maps.Clone(smoke.Images)
	smoke.Tools = maps.Clone(smoke.Tools)
	smoke.NonToolMembers = slices.Clone(smoke.NonToolMembers)
	smoke.NonToolOutputs = slices.Clone(smoke.NonToolOutputs)
	for i := range smoke.NonToolOutputs {
		smoke.NonToolOutputs[i].Components = slices.Clone(smoke.NonToolOutputs[i].Components)
	}
	return smoke
}

// Profile selects the shared fixed native execution profile, never arbitrary argv.
func Profile(profile string) (string, error) {
	switch profile {
	case "nvkectl-version-json":
		return "nvkectl", nil
	case "helm-version":
		return "helm", nil
	case "kubectl-client-version-json":
		return "kubectl", nil
	case "kind-version":
		return "kind", nil
	case "clusterctl-version-json":
		return "clusterctl", nil
	case "oras-version":
		return "oras", nil
	case "nicosim-version-json", "nicosim-patched-version-json":
		return "nicosim", nil
	default:
		return "", e.New(e.CodeEngineCapability, "unsupported tool smoke profile %q", profile)
	}
}
