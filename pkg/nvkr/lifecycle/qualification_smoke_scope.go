// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"slices"
	"sort"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
)

type smokeBundleDescriptions interface {
	ExecutableBundle(string) (delivery.BinaryBundle, error)
}

// qualificationSmokeMembers recomputes complete coverage from A's private held
// publication and frozen Plan. Returned fields are defensive diagnostics only.
// Consumers register beginOperation before using these descriptions for work.
func qualificationSmokeMembers(checks *ArtifactCheckReceipt, policy QualificationPolicy) ([]ToolSmokeMember, error) {
	description, err := checks.Description()
	if err != nil {
		return nil, err
	}
	state := checks.lifetime
	state.mu.RLock()
	defer state.mu.RUnlock()
	if state.closed || state.plan == nil || state.verified == nil {
		return nil, e.New(e.CodeGateUnmet, "smoke scope requires live retained artifact checks")
	}
	return deriveQualificationSmokeMembers(state.plan, description, state.verified.publication, policy)
}

func deriveQualificationSmokeMembers(plan *factory.Plan, description ArtifactCheckDescription, bundles smokeBundleDescriptions, policy QualificationPolicy) ([]ToolSmokeMember, error) {
	planned, err := qualificationSmokePlanScope(plan, policy)
	if err != nil {
		return nil, err
	}
	if len(description.Outputs) != len(planned) {
		return nil, e.New(e.CodeReleaseIncomplete, "smoke requires the complete planned output set")
	}
	outputs := make(map[string]ArtifactCheckOutput, len(description.Outputs))
	for _, output := range description.Outputs {
		if _, exists := outputs[output.Name]; exists || output.Overall != "pass" {
			return nil, e.New(e.CodeGateUnmet, "smoke requires unique successful artifact checks")
		}
		outputs[output.Name] = output
	}
	members := []ToolSmokeMember{}
	for _, output := range planned {
		checked, exists := outputs[output.name]
		if !exists || checked.Kind != output.kind || checked.BuildDigest != output.buildDigest {
			return nil, e.New(e.CodeReleaseIncomplete, "smoke output differs from frozen Plan")
		}
		if output.kind != OutputExecutable {
			continue
		}
		if checked.Platform != output.platform {
			return nil, e.New(e.CodeVerifyFailed, "executable output has inconsistent native build platform")
		}
		bundle, err := bundles.ExecutableBundle(output.name)
		if err != nil {
			return nil, err
		}
		if err := bundle.Validate(); err != nil {
			return nil, err
		}
		if bundle.OS+"/"+bundle.Arch != output.platform || len(bundle.Entries) != len(output.members) {
			return nil, e.New(e.CodeReleaseIncomplete, "executable bundle differs from exact selected members or platform")
		}
		for _, entry := range bundle.Entries {
			member, exists := output.members[entry.Name]
			if !exists || entry.Mode&0o111 == 0 || entry.Size <= 0 || entry.Size > pipeline.MaxToolSmokeExecutableBytes {
				return nil, e.New(e.CodeEngineCapability, "output %q has an unselected, non-executable or unsupported-size member", output.name)
			}
			if !canonical.IsDigest(checked.Subject) {
				return nil, e.New(e.CodeVerifyFailed, "smoke output has no exact subject")
			}
			members = append(members, ToolSmokeMember{Output: output.name, Member: entry.Name, Subject: checked.Subject, BuildDigest: output.buildDigest, Platform: output.platform, PayloadDigest: entry.SHA256, Size: entry.Size, Profile: member.profile, ExpectedVersion: member.version, ExpectedCommit: member.commit})
		}
	}
	sort.Slice(members, func(i, j int) bool {
		if members[i].Output != members[j].Output {
			return members[i].Output < members[j].Output
		}
		return members[i].Member < members[j].Member
	})
	return members, nil
}

func sameSmokeComponents(left, right []string) bool {
	left, right = slices.Clone(left), slices.Clone(right)
	sort.Strings(left)
	sort.Strings(right)
	return slices.Equal(left, right)
}
func smokeOutputBuild(plan *factory.Plan, name string) (factory.BuildPlan, error) {
	var found *factory.BuildPlan
	for i := range plan.Builds {
		if plan.Builds[i].Name == name {
			if found != nil {
				return factory.BuildPlan{}, e.New(e.CodeReleaseIncomplete, "ambiguous smoke output build")
			}
			found = &plan.Builds[i]
		}
	}
	if found == nil || found.Graph == nil {
		return factory.BuildPlan{}, e.New(e.CodeReleaseIncomplete, "smoke output has no frozen execution graph")
	}
	return *found, nil
}
func smokeComponentNode(build factory.BuildPlan, component string) (ir.Node, error) {
	var found *ir.Node
	for i := range build.Graph.Nodes {
		node := &build.Graph.Nodes[i]
		if node.Component == component && (node.Kind == ir.KindBuild || node.Kind == ir.KindPull) {
			if found != nil {
				return ir.Node{}, e.New(e.CodeReleaseIncomplete, "ambiguous smoke component producer")
			}
			found = node
		}
	}
	if found == nil || found.OutputType != "host-package" || found.Platform != build.Platform {
		return ir.Node{}, e.New(e.CodeVerifyFailed, "smoke component has no matching executable producer")
	}
	return *found, nil
}
func expectedSmokeVersion(node ir.Node, profile string) (string, string, error) {
	if _, err := nativeSmokeProfile(profile); err != nil {
		return "", "", err
	}
	if node.Version == "" || len(node.Version) > 255 || strings.ContainsAny(node.Version, " \t\r\n\x00") || strings.HasPrefix(node.Version, "v") {
		return "", "", e.New(e.CodeVerifyFailed, "smoke producer version is not the normalized pinned version")
	}
	version := "v" + node.Version
	if profile != "nvkectl-version-json" {
		return version, "", nil
	}
	stamps := map[string]string{}
	for _, item := range node.Env {
		if _, exists := stamps[item.Name]; exists {
			return "", "", e.New(e.CodeVerifyFailed, "duplicate smoke version stamp")
		}
		stamps[item.Name] = item.Value
	}
	prefix := "NKECTL_"
	_, currentVersion := stamps["NVKECTL_VERSION"]
	_, currentCommit := stamps["NVKECTL_COMMIT"]
	if currentVersion || currentCommit {
		if _, legacy := stamps["NKECTL_VERSION"]; legacy {
			return "", "", e.New(e.CodeVerifyFailed, "ambiguous nvkectl version stamp family")
		}
		if _, legacy := stamps["NKECTL_COMMIT"]; legacy {
			return "", "", e.New(e.CodeVerifyFailed, "ambiguous nvkectl commit stamp family")
		}
		prefix = "NVKECTL_"
	}
	commit := stamps[prefix+"COMMIT"]
	if node.Kind != ir.KindBuild || stamps[prefix+"VERSION"] != version || len(node.SourceCommit) != 40 || !smokeHexCommit(node.SourceCommit) || !smokeHexCommit(commit) || !strings.HasPrefix(node.SourceCommit, commit) {
		return "", "", e.New(e.CodeVerifyFailed, "nvkectl stamps differ from frozen producer version or commit")
	}
	return version, commit, nil
}
func nativeSmokeProfile(profile string) (pipeline.ToolSmokeProfile, error) {
	switch profile {
	case "nvkectl-version-json":
		return pipeline.ToolSmokeNVKECTL, nil
	case "helm-version":
		return pipeline.ToolSmokeHelm, nil
	case "kubectl-client-version-json":
		return pipeline.ToolSmokeKubectl, nil
	default:
		return "", e.New(e.CodeEngineCapability, "unsupported tool smoke profile %q", profile)
	}
}
