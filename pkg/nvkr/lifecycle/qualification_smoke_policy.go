// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"regexp"
	"sort"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/google/go-containerregistry/pkg/name"
)

// QualificationSmokePolicy selects a separate, same-repository reusable worker
// and immutable native execution images. Profiles select fixed version commands;
// arbitrary commands, arguments, mounts and environment are not configurable.
type QualificationSmokePolicy struct {
	Workflow       string                       `json:"workflow"`
	Images         map[string]string            `json:"images"`
	Tools          map[string]string            `json:"tools"`
	NonToolOutputs []QualificationNonToolOutput `json:"nonToolOutputs,omitempty"`
}

// QualificationNonToolOutput explicitly scopes one exact OCI/APT output outside
// delivered loose-tool smoke. Deployment images may contain tools; this rule
// makes no assertion about their internal content or operational acceptance.
type QualificationNonToolOutput struct {
	Output     string   `json:"output"`
	Kind       string   `json:"kind"`
	Components []string `json:"components"`
	Reason     string   `json:"reason"`
}

var qualificationSmokeName = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9._-]{0,126}[a-z0-9])?$`)

var qualificationCosignVersion = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)

func (policy QualificationCosignPolicy) validatePins() error {
	if policy.Version != "" && !qualificationCosignVersion.MatchString(policy.Version) {
		return e.New(e.CodeUnpinnedInput, "qualification cosign version must be an exact stable vX.Y.Z release locator")
	}
	if policy.Digest != "" {
		if !canonical.IsDigest(policy.Digest) || len(policy.PlatformDigests) != 0 {
			return e.New(e.CodeUnpinnedInput, "qualification cosign requires one exact digest or native platform pins, never both")
		}
		return nil
	}
	if len(policy.PlatformDigests) == 0 || len(policy.PlatformDigests) > 2 {
		return e.New(e.CodeUnpinnedInput, "qualification policy requires an independent exact cosign executable digest")
	}
	for platform, digest := range policy.PlatformDigests {
		if !qualificationNativePlatform(platform) || !canonical.IsDigest(digest) {
			return e.New(e.CodeUnpinnedInput, "qualification cosign pins require exact native Linux platforms and byte digests")
		}
	}
	return nil
}

// ExecutableDigest selects only a validated native-platform pin. The executing
// owner still authenticates the selected executable's exact bytes and ELF.
func (policy QualificationCosignPolicy) ExecutableDigest(platform string) (string, error) {
	if err := policy.validatePins(); err != nil {
		return "", err
	}
	if !qualificationNativePlatform(platform) {
		return "", e.New(e.CodeEngineCapability, "qualification supports native Linux AMD64 and ARM64")
	}
	if policy.Digest != "" {
		return policy.Digest, nil
	}
	digest, exists := policy.PlatformDigests[platform]
	if !exists {
		return "", e.New(e.CodeUnpinnedInput, "qualification policy has no cosign pin for platform %q", platform)
	}
	return digest, nil
}

func qualificationNativePlatform(platform string) bool {
	return platform == "linux/amd64" || platform == "linux/arm64"
}

func (smoke QualificationSmokePolicy) validate(policy QualificationPolicy) error {
	worker := policy.Source
	worker.Workflow = smoke.Workflow
	if err := worker.Validate(); err != nil {
		return err
	}
	if policy.QualificationWorkflow == "" || smoke.Workflow == policy.Source.Workflow || smoke.Workflow == policy.QualificationWorkflow {
		return e.New(e.CodeTrustPolicyMissing, "smoke requires a same-repository workflow distinct from builder and qualification signer")
	}
	if len(smoke.Images) == 0 || len(smoke.Images) > 2 {
		return e.New(e.CodeTrustPolicyMissing, "smoke requires independently pinned native execution images")
	}
	for platform, image := range smoke.Images {
		if !qualificationNativePlatform(platform) || strings.Contains(image, "://") {
			return e.New(e.CodeUnpinnedInput, "smoke image requires a supported native platform and exact OCI digest reference")
		}
		ref, err := name.NewDigest(image, name.StrictValidation)
		if err != nil || ref.Name() != image || !canonical.IsDigest(ref.DigestStr()) {
			return e.New(e.CodeUnpinnedInput, "smoke image requires a canonical registry reference pinned by digest")
		}
		if _, err := policy.Cosign.ExecutableDigest(platform); err != nil {
			return err
		}
	}
	if len(smoke.Images) > 1 && len(policy.Cosign.PlatformDigests) == 0 {
		return e.New(e.CodeUnpinnedInput, "multi-platform smoke requires native cosign executable pins for every platform")
	}
	if len(smoke.Tools) == 0 || len(smoke.Tools) > 1024 || len(smoke.NonToolOutputs) > 1024 {
		return e.New(e.CodeTrustPolicyMissing, "smoke tool and output policy exceeds its supported bounds or has no tools")
	}
	for component, profile := range smoke.Tools {
		if !qualificationSmokeName.MatchString(component) {
			return e.New(e.CodeTrustPolicyMissing, "smoke component names must be exact canonical names")
		}
		switch profile {
		case "nvkectl-version-json", "helm-version", "kubectl-client-version-json":
		default:
			return e.New(e.CodeEngineCapability, "smoke profile %q is unsupported", profile)
		}
	}
	seen := make(map[string]bool, len(smoke.NonToolOutputs))
	for _, output := range smoke.NonToolOutputs {
		if !qualificationSmokeName.MatchString(output.Output) || seen[output.Output] || len(output.Components) == 0 || len(output.Components) > 1024 {
			return e.New(e.CodeTrustPolicyMissing, "non-tool smoke scope requires unique exact outputs and nonempty bounded component membership")
		}
		seen[output.Output] = true
		if (output.Kind != OutputOCI || output.Reason != "deployment-image") && (output.Kind != OutputAPT || output.Reason != "package-repository") {
			return e.New(e.CodeTrustPolicyMissing, "non-tool smoke scope requires an explicit deployment-image or package-repository role")
		}
		components := make(map[string]bool, len(output.Components))
		for _, component := range output.Components {
			if !qualificationSmokeName.MatchString(component) || components[component] {
				return e.New(e.CodeTrustPolicyMissing, "non-tool output component scope must be exact and unique")
			}
			components[component] = true
		}
	}
	return nil
}

// Digest binds the complete independent policy, including every architecture's
// pins. Local executable path selection is excluded: the pin, not a filesystem
// locator, authenticates code. Normalization never mutates the caller's policy.
func (policy QualificationPolicy) Digest() (string, error) {
	if err := policy.Validate(); err != nil {
		return "", err
	}
	raw, err := canonical.Marshal(policy)
	if err != nil {
		return "", err
	}
	var frozen QualificationPolicy
	if err := canonical.UnmarshalExact(raw, &frozen); err != nil {
		return "", e.Wrap(e.CodeTrustPolicyMissing, err, "freezing qualification policy")
	}
	frozen.Cosign.Path = ""
	sort.Strings(frozen.OutsideMVPSuites)
	sort.Strings(frozen.RequiredSuites)
	if frozen.Smoke != nil {
		sort.Slice(frozen.Smoke.NonToolOutputs, func(i, j int) bool {
			return frozen.Smoke.NonToolOutputs[i].Output < frozen.Smoke.NonToolOutputs[j].Output
		})
		for i := range frozen.Smoke.NonToolOutputs {
			sort.Strings(frozen.Smoke.NonToolOutputs[i].Components)
		}
	}
	return canonical.Digest(canonical.KindQualificationPolicy, frozen)
}
