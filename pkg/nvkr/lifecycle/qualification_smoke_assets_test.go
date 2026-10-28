// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"cmp"
	"slices"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

func TestSmokeMixedArchiveScope(t *testing.T) {
	for _, mode := range []string{"valid", "missing rule", "wrong output", "wrong member", "duplicate rule", "unknown role", "tool overlap", "executable producer", "acquired archive", "wrong platform", "ambiguous producer", "executable archive", "missing archive", "extra member", "missing tool profile"} {
		t.Run(mode, func(t *testing.T) {
			plan, checks, bundles, policy := nativeSmokeScopeFixture()
			plan.LifecycleOutputs[0].Components = append(plan.LifecycleOutputs[0].Components, "assets")
			plan.Builds[0].Graph.Nodes = append(plan.Builds[0].Graph.Nodes, ir.Node{Kind: ir.KindBuild, Component: "assets", OutputType: "tarball", Platform: "linux/amd64"})
			bundle := bundles["cli"]
			bundle.Entries = append(bundle.Entries, delivery.BinaryEntry{Name: "assets", InstallPath: "/usr/share/assets.tar.gz", Mode: 0o644, SHA256: nativeSmokeTestDigest("6"), Size: 100})
			policy.Smoke.NonToolMembers = []QualificationNonToolMember{{Output: "cli", Member: "assets", Reason: "runtime-assets-archive"}}
			code := e.CodeReleaseIncomplete
			switch mode {
			case "missing rule":
				policy.Smoke.NonToolMembers = nil
				code = e.CodeGateUnmet
			case "wrong output":
				policy.Smoke.NonToolMembers[0].Output = "other"
				code = e.CodeGateUnmet
			case "wrong member":
				policy.Smoke.NonToolMembers[0].Member = "other"
				code = e.CodeGateUnmet
			case "duplicate rule":
				policy.Smoke.NonToolMembers = append(policy.Smoke.NonToolMembers, policy.Smoke.NonToolMembers[0])
				code = e.CodeTrustPolicyMissing
			case "unknown role":
				policy.Smoke.NonToolMembers[0].Reason = "skip"
				code = e.CodeTrustPolicyMissing
			case "tool overlap":
				policy.Smoke.Tools["assets"] = "helm-version"
				code = e.CodeTrustPolicyMissing
			case "executable producer":
				plan.Builds[0].Graph.Nodes[1].OutputType = "host-package"
				code = e.CodeGateUnmet
			case "acquired archive":
				plan.Builds[0].Graph.Nodes[1].Kind = ir.KindPull
				code = e.CodeGateUnmet
			case "wrong platform":
				plan.Builds[0].Graph.Nodes[1].Platform = "linux/arm64"
				code = e.CodeVerifyFailed
			case "ambiguous producer":
				plan.Builds[0].Graph.Nodes = append(plan.Builds[0].Graph.Nodes, plan.Builds[0].Graph.Nodes[1])
			case "executable archive":
				bundle.Entries[1].Mode = 0o755
				code = e.CodeGateUnmet
			case "missing archive":
				bundle.Entries = bundle.Entries[:1]
			case "extra member":
				entry := bundle.Entries[1]
				entry.Name, entry.InstallPath = "extra", "/usr/share/extra.tar.gz"
				bundle.Entries = append(bundle.Entries, entry)
			case "missing tool profile":
				delete(policy.Smoke.Tools, "tool")
				policy.Smoke.Tools["other"] = "helm-version"
				code = e.CodeGateUnmet
			}
			slices.SortFunc(bundle.Entries, func(a, b delivery.BinaryEntry) int { return cmp.Compare(a.Name, b.Name) })
			bundles["cli"] = bundle
			members, err := deriveQualificationSmokeMembers(&plan, checks, bundles, policy)
			if mode != "valid" {
				assertCode(t, err, code)
				return
			}
			if err != nil || len(members) != 1 || members[0].Member != "tool" {
				t.Fatalf("archive rule changed executable scope: %+v %v", members, err)
			}
		})
	}
}

func TestSmokeArchivePolicyIdentity(t *testing.T) {
	p := nativeSmokeTestPolicy()
	p.Smoke.NonToolMembers = []QualificationNonToolMember{{Output: "z", Member: "assets", Reason: "runtime-assets-archive"}, {Output: "a", Member: "assets", Reason: "runtime-assets-archive"}}
	want, err := p.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if p.Smoke.NonToolMembers[0].Output != "z" {
		t.Fatal("digest mutated policy")
	}
	slices.Reverse(p.Smoke.NonToolMembers)
	got, err := p.Digest()
	if err != nil || want != got {
		t.Fatalf("rule order changed identity: %v", err)
	}
	p.Smoke.NonToolMembers[0].Member = "different"
	got, err = p.Digest()
	if err != nil || want == got {
		t.Fatalf("member scope not bound: %v", err)
	}
}

func TestSmokeDescriptionMixedArchiveCoverage(t *testing.T) {
	_, _, policy, report := smokeAggregateFixture(t)
	policy.Smoke.NonToolMembers = []QualificationNonToolMember{{Output: report.Members[0].Output, Member: "assets", Reason: "runtime-assets-archive"}}
	policy.Cosign.Path = ""
	digest, err := policy.Digest()
	if err != nil {
		t.Fatal(err)
	}
	report.PolicyDigest = digest
	for i := range report.ArtifactChecks.Outputs {
		output := &report.ArtifactChecks.Outputs[i]
		if output.Name == report.Members[0].Output {
			output.BuildSubjects = append(output.BuildSubjects, delivery.BuildSubject{Component: "assets", Digest: nativeSmokeTestDigest("7")})
			slices.SortFunc(output.BuildSubjects, func(a, b delivery.BuildSubject) int {
				return cmp.Compare(a.Component, b.Component)
			})
		}
	}
	description := ToolSmokeDescription{ArtifactChecks: report.ArtifactChecks, Invocation: report.Invocation, PolicyDigest: digest, Policy: policy, Workers: []NativeToolSmokeReport{report}}
	if err := description.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"missing rule", "extra rule", "wrong output"} {
		t.Run(mode, func(t *testing.T) {
			changed := description
			p := *description.Policy.Smoke
			p.NonToolMembers = slices.Clone(p.NonToolMembers)
			changed.Policy.Smoke = &p
			switch mode {
			case "missing rule":
				p.NonToolMembers = nil
			case "extra rule":
				p.NonToolMembers = append(p.NonToolMembers, QualificationNonToolMember{Output: "other", Member: "assets", Reason: "runtime-assets-archive"})
			case "wrong output":
				p.NonToolMembers[0].Output = "other"
			}
			changed.PolicyDigest, err = changed.Policy.Digest()
			if err != nil {
				t.Fatal(err)
			}
			changed.Workers = slices.Clone(description.Workers)
			changed.Workers[0].PolicyDigest = changed.PolicyDigest
			assertCode(t, changed.Validate(), e.CodeReleaseIncomplete)
		})
	}
}
