// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"regexp"
	"sort"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/qualificationpolicy"
)

// QualificationSmokePolicy adds hosted workflow admission to shared smoke scope.
type QualificationSmokePolicy qualificationpolicy.Smoke
type QualificationNonToolMember = qualificationpolicy.NonToolMember
type QualificationNonToolOutput = qualificationpolicy.NonToolOutput

var qualificationSmokeName = regexp.MustCompile(qualificationpolicy.NamePattern)

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
	if policy.Tekton != nil {
		if smoke.Workflow != "" || len(smoke.Images) != 1 || smoke.Images[policy.Tekton.Platform] == "" {
			return e.New(e.CodeTrustPolicyMissing, "Tekton smoke requires its exact native image and no hosted workflow")
		}
		if err := smoke.validateExecution(); err != nil {
			return err
		}
		_, err := policy.Cosign.ExecutableDigest(policy.Tekton.Platform)
		return err
	}
	worker := policy.Source
	worker.Workflow = smoke.Workflow
	if err := worker.Validate(); err != nil {
		return err
	}
	if policy.QualificationWorkflow == "" || smoke.Workflow == policy.Source.Workflow || smoke.Workflow == policy.QualificationWorkflow {
		return e.New(e.CodeTrustPolicyMissing, "smoke requires a same-repository workflow distinct from builder and qualification signer")
	}
	if err := smoke.validateExecution(); err != nil {
		return err
	}
	for platform := range smoke.Images {
		if _, err := policy.Cosign.ExecutableDigest(platform); err != nil {
			return err
		}
	}
	if len(smoke.Images) > 1 && len(policy.Cosign.PlatformDigests) == 0 {
		return e.New(e.CodeUnpinnedInput, "multi-platform smoke requires native cosign executable pins for every platform")
	}
	return nil
}

// validateExecution shares execution policy with every CI adapter.
func (smoke QualificationSmokePolicy) validateExecution() error {
	return qualificationpolicy.Smoke(smoke).ValidateExecution()
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
		sort.Slice(frozen.Smoke.NonToolMembers, func(i, j int) bool {
			return frozen.Smoke.NonToolMembers[i].Key() < frozen.Smoke.NonToolMembers[j].Key()
		})
		sort.Slice(frozen.Smoke.NonToolOutputs, func(i, j int) bool {
			return frozen.Smoke.NonToolOutputs[i].Output < frozen.Smoke.NonToolOutputs[j].Output
		})
		for i := range frozen.Smoke.NonToolOutputs {
			sort.Strings(frozen.Smoke.NonToolOutputs[i].Components)
		}
	}
	return canonical.Digest(canonical.KindQualificationPolicy, frozen)
}
