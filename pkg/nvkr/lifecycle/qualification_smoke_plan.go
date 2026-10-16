// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

// ValidateQualificationSmokePlan checks whether the exact planned outputs can
// satisfy the independently selected smoke policy. It performs no I/O, mutates
// neither input, and issues no receipt. The caller still validates the complete
// Plan; execution must authenticate artifact subjects, member bytes and runtime
// observations before a successful qualification can be issued.
func ValidateQualificationSmokePlan(plan *factory.Plan, policy QualificationPolicy) error {
	_, err := qualificationSmokePlanScope(plan, policy)
	return err
}

// These values describe planned scope only. They intentionally contain no
// artifact subject, payload digest, observation or pass status.
type plannedSmokeOutput struct {
	name, kind, buildDigest, platform string
	members                           map[string]plannedSmokeMember
}
type plannedSmokeMember struct {
	profile, version, commit string
	nonToolReason            string
}

// qualificationSmokePlanScope is shared by generation admission and runtime
// scope derivation. Every static constraint is checked before runtime consumers
// request any delivered bundle. All returned maps and values are newly owned.
func qualificationSmokePlanScope(plan *factory.Plan, policy QualificationPolicy) ([]plannedSmokeOutput, error) {
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	if policy.Smoke == nil {
		return nil, e.New(e.CodeTrustPolicyMissing, "tool smoke policy is required")
	}
	return toolSmokePlanScope(plan, *policy.Smoke)
}

// toolSmokePlanScope owns exact membership independently of the execution
// provider. The hosted caller separately admits its workflow and signer policy.
func toolSmokePlanScope(plan *factory.Plan, smoke QualificationSmokePolicy) ([]plannedSmokeOutput, error) {
	if err := smoke.validateExecution(); err != nil {
		return nil, err
	}
	if plan == nil || len(plan.LifecycleOutputs) == 0 || len(plan.LifecycleOutputs) > 1024 {
		return nil, e.New(e.CodeReleaseIncomplete, "smoke requires the complete planned output set")
	}
	exemptions := make(map[string]QualificationNonToolOutput, len(smoke.NonToolOutputs))
	for _, rule := range smoke.NonToolOutputs {
		exemptions[rule.Output] = rule
	}
	memberExemptions := make(map[string]QualificationNonToolMember, len(smoke.NonToolMembers))
	for _, rule := range smoke.NonToolMembers {
		memberExemptions[rule.key()] = rule
	}
	seenOutputs := map[string]bool{}
	usedTools := map[string]bool{}
	platforms := map[string]bool{}
	memberCount := 0
	planned := make([]plannedSmokeOutput, 0, len(plan.LifecycleOutputs))
	for _, output := range plan.LifecycleOutputs {
		if seenOutputs[output.Name] {
			return nil, e.New(e.CodeReleaseIncomplete, "duplicate planned smoke output")
		}
		if !qualificationSmokeName.MatchString(output.Name) || !canonical.IsDigest(output.BuildDigest) {
			return nil, e.New(e.CodeVerifyFailed, "planned smoke output has no exact name or build identity")
		}
		seenOutputs[output.Name] = true
		scope := plannedSmokeOutput{name: output.Name, kind: output.Kind, buildDigest: output.BuildDigest}
		switch output.Kind {
		case OutputDiskImage, OutputHelm:
			// These exact output kinds carry no loose executable bundle in the MVP.
		case OutputOCI, OutputAPT:
			rule, exists := exemptions[output.Name]
			if !exists || rule.Kind != output.Kind || !sameSmokeComponents(rule.Components, output.Components) {
				return nil, e.New(e.CodeGateUnmet, "output %q has no exact non-tool smoke rule", output.Name)
			}
			delete(exemptions, output.Name)
		case OutputExecutable:
			build, err := smokeOutputBuild(plan, output.Name)
			if err != nil {
				return nil, err
			}
			if build.BuildDigest != output.BuildDigest || !qualificationNativePlatform(build.Platform) {
				return nil, e.New(e.CodeVerifyFailed, "executable output has inconsistent native build platform")
			}
			if len(output.Components) == 0 || len(output.Components) > 1024 {
				return nil, e.New(e.CodeReleaseIncomplete, "executable smoke requires nonempty bounded component membership")
			}
			scope.platform = build.Platform
			scope.members = make(map[string]plannedSmokeMember, len(output.Components))
			toolCount := 0
			for _, component := range output.Components {
				if _, exists := scope.members[component]; exists || !qualificationSmokeName.MatchString(component) {
					return nil, e.New(e.CodeReleaseIncomplete, "executable smoke component names must be exact and unique")
				}
				key := output.Name + "\x00" + component
				if rule, exists := memberExemptions[key]; exists {
					node, err := smokeProducerNode(build, component)
					if err != nil {
						return nil, err
					}
					if node.Kind != ir.KindBuild || node.OutputType != "tarball" {
						return nil, e.New(e.CodeGateUnmet, "non-tool member requires a source-built tarball producer")
					}
					scope.members[component] = plannedSmokeMember{nonToolReason: rule.Reason}
					delete(memberExemptions, key)
					continue
				}
				profile, exists := smoke.Tools[component]
				if !exists {
					return nil, e.New(e.CodeGateUnmet, "member %q has no fixed smoke profile", component)
				}
				node, err := smokeComponentNode(build, component)
				if err != nil {
					return nil, err
				}
				version, commit, err := expectedSmokeVersion(node, profile)
				if err != nil {
					return nil, err
				}
				memberCount++
				if memberCount > 4096 {
					return nil, e.New(e.CodeEngineCapability, "complete tool smoke member set exceeds supported bound")
				}
				scope.members[component] = plannedSmokeMember{profile: profile, version: version, commit: commit}
				usedTools[component] = true
				toolCount++
			}
			if toolCount == 0 {
				return nil, e.New(e.CodeGateUnmet, "executable smoke output requires at least one delivered tool")
			}
			platforms[build.Platform] = true
		default:
			return nil, e.New(e.CodeEngineCapability, "output kind %q has no smoke applicability rule", output.Kind)
		}
		planned = append(planned, scope)
	}
	if len(exemptions) != 0 || len(memberExemptions) != 0 || len(usedTools) != len(smoke.Tools) || len(platforms) != len(smoke.Images) {
		return nil, e.New(e.CodeReleaseIncomplete, "smoke policy has missing or extra tool, image or output scope")
	}
	for platform := range platforms {
		if _, exists := smoke.Images[platform]; !exists {
			return nil, e.New(e.CodeTrustPolicyMissing, "smoke policy omits a required native image")
		}
	}
	return planned, nil
}
