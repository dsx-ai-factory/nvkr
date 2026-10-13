// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"sort"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// Validate checks the complete inspectable shape. Only hosted authentication can
// issue authority; a structurally valid document is still just a claim.
func (description ToolSmokeDescription) Validate() error {
	policy := description.Policy
	if err := policy.Validate(); err != nil {
		return err
	}
	if policy.Smoke == nil || policy.Cosign.Path != "" || len(policy.RequiredSuites) > 0 {
		return e.New(e.CodeVerifyFailed, "smoke description requires complete independent policy and no local locator")
	}
	digest, err := policy.Digest()
	if err != nil {
		return err
	}
	if description.PolicyDigest != digest {
		return e.New(e.CodeVerifyFailed, "smoke description policy identity differs")
	}
	if err := matchNativeSmokeOrigins(description.ArtifactChecks.Origin, description.Invocation, policy); err != nil {
		return err
	}
	if len(description.Workers) != len(policy.Smoke.Images) {
		return e.New(e.CodeReleaseIncomplete, "smoke evidence omits or adds native workers")
	}
	members := map[string]map[string]ToolSmokeMember{}
	usedTools := map[string]bool{}
	previous := ""
	totalMembers := 0
	for _, worker := range description.Workers {
		if err := worker.Validate(); err != nil {
			return err
		}
		if worker.Platform <= previous || worker.Image != policy.Smoke.Images[worker.Platform] || worker.PolicyDigest != digest || worker.Invocation != description.Invocation || !sameSmokeChecks(worker.ArtifactChecks, description.ArtifactChecks) {
			return e.New(e.CodeGateUnmet, "native worker does not bind the exact smoke scope")
		}
		previous = worker.Platform
		totalMembers += len(worker.Members)
		if totalMembers > 4096 {
			return e.New(e.CodeEngineCapability, "complete smoke membership exceeds its admitted bound")
		}
		for _, member := range worker.Members {
			if policy.Smoke.Tools[member.Member] != member.Profile {
				return e.New(e.CodeGateUnmet, "smoke member profile differs from independent policy")
			}
			if members[member.Output] == nil {
				members[member.Output] = map[string]ToolSmokeMember{}
			}
			if _, exists := members[member.Output][member.Member]; exists {
				return e.New(e.CodeReleaseIncomplete, "duplicate smoke output member")
			}
			members[member.Output][member.Member] = member
			usedTools[member.Member] = true
		}
	}
	if len(usedTools) != len(policy.Smoke.Tools) {
		return e.New(e.CodeReleaseIncomplete, "smoke evidence does not use the complete tool policy")
	}
	exemptions := map[string]QualificationNonToolOutput{}
	for _, rule := range policy.Smoke.NonToolOutputs {
		exemptions[rule.Output] = rule
	}
	for _, output := range description.ArtifactChecks.Outputs {
		switch output.Kind {
		case OutputExecutable:
			if len(members[output.Name]) != len(output.BuildSubjects) || len(output.BuildSubjects) == 0 {
				return e.New(e.CodeReleaseIncomplete, "smoke does not cover the complete executable component set")
			}
			for _, subject := range output.BuildSubjects {
				if _, ok := members[output.Name][subject.Component]; !ok {
					return e.New(e.CodeReleaseIncomplete, "smoke omits an executable component")
				}
			}
			delete(members, output.Name)
		case OutputOCI, OutputAPT:
			rule, ok := exemptions[output.Name]
			components := make([]string, 0, len(output.BuildSubjects))
			if output.ImageIndex != nil {
				for _, child := range output.ImageIndex.Children {
					components = append(components, child.Component)
				}
			} else {
				for _, subject := range output.BuildSubjects {
					components = append(components, subject.Component)
				}
			}
			sort.Strings(components)
			unique := components[:0]
			for _, component := range components {
				if len(unique) == 0 || unique[len(unique)-1] != component {
					unique = append(unique, component)
				}
			}
			if !ok || rule.Kind != output.Kind || !sameSmokeComponents(rule.Components, unique) {
				return e.New(e.CodeGateUnmet, "smoke non-tool applicability differs from exact selected output")
			}
			delete(exemptions, output.Name)
		case OutputDiskImage, OutputHelm:
		default:
			return e.New(e.CodeEngineCapability, "unsupported smoke artifact kind")
		}
	}
	if len(members) != 0 || len(exemptions) != 0 {
		return e.New(e.CodeReleaseIncomplete, "smoke evidence contains extra output scope")
	}
	return nil
}
