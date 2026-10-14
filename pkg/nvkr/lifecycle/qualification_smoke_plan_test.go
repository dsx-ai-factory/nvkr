// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

func TestQualificationSmokePlanAdmitsCompleteStaticScope(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*factory.Plan, *QualificationPolicy)
		code e.Code
	}{
		{"complete", func(*factory.Plan, *QualificationPolicy) {}, ""},
		{"missing tool", func(_ *factory.Plan, p *QualificationPolicy) {
			p.Smoke.Tools = map[string]string{"other": "helm-version"}
		}, e.CodeGateUnmet},
		{"extra tool", func(_ *factory.Plan, p *QualificationPolicy) { p.Smoke.Tools["other"] = "helm-version" }, e.CodeReleaseIncomplete},
		{"unsupported profile", func(_ *factory.Plan, p *QualificationPolicy) { p.Smoke.Tools["tool"] = "shell" }, e.CodeEngineCapability},
		{"no smoke policy", func(_ *factory.Plan, p *QualificationPolicy) { p.Smoke = nil }, e.CodeTrustPolicyMissing},
		{"no outputs", func(p *factory.Plan, _ *QualificationPolicy) { p.LifecycleOutputs = nil }, e.CodeReleaseIncomplete},
		{"duplicate output", func(p *factory.Plan, _ *QualificationPolicy) {
			p.LifecycleOutputs = append(p.LifecycleOutputs, p.LifecycleOutputs[0])
		}, e.CodeReleaseIncomplete},
		{"no components", func(p *factory.Plan, _ *QualificationPolicy) { p.LifecycleOutputs[0].Components = nil }, e.CodeReleaseIncomplete},
		{"duplicate component", func(p *factory.Plan, _ *QualificationPolicy) {
			p.LifecycleOutputs[0].Components = []string{"tool", "tool"}
		}, e.CodeReleaseIncomplete},
		{"unknown kind", func(p *factory.Plan, _ *QualificationPolicy) { p.LifecycleOutputs[0].Kind = "unknown" }, e.CodeEngineCapability},
		{"no build", func(p *factory.Plan, _ *QualificationPolicy) { p.Builds = nil }, e.CodeReleaseIncomplete},
		{"ambiguous build", func(p *factory.Plan, _ *QualificationPolicy) { p.Builds = append(p.Builds, p.Builds[0]) }, e.CodeReleaseIncomplete},
		{"no graph", func(p *factory.Plan, _ *QualificationPolicy) { p.Builds[0].Graph = nil }, e.CodeReleaseIncomplete},
		{"different build", func(p *factory.Plan, _ *QualificationPolicy) { p.Builds[0].BuildDigest = nativeSmokeTestDigest("9") }, e.CodeVerifyFailed},
		{"unsupported platform", func(p *factory.Plan, _ *QualificationPolicy) { p.Builds[0].Platform = "darwin/arm64" }, e.CodeVerifyFailed},
		{"wrong producer platform", func(p *factory.Plan, _ *QualificationPolicy) { p.Builds[0].Graph.Nodes[0].Platform = "linux/arm64" }, e.CodeVerifyFailed},
		{"wrong producer type", func(p *factory.Plan, _ *QualificationPolicy) { p.Builds[0].Graph.Nodes[0].OutputType = "oci" }, e.CodeVerifyFailed},
		{"no version", func(p *factory.Plan, _ *QualificationPolicy) { p.Builds[0].Graph.Nodes[0].Version = "" }, e.CodeVerifyFailed},
		{"prefixed version", func(p *factory.Plan, _ *QualificationPolicy) { p.Builds[0].Graph.Nodes[0].Version = "v1.2.3" }, e.CodeVerifyFailed},
		{"missing nvkectl stamps", func(_ *factory.Plan, p *QualificationPolicy) { p.Smoke.Tools["tool"] = "nvkectl-version-json" }, e.CodeVerifyFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan, _, _, policy := nativeSmokeScopeFixture()
			tc.edit(&plan, &policy)
			err := ValidateQualificationSmokePlan(&plan, policy)
			if tc.code != "" {
				assertCode(t, err, tc.code)
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
	_, _, _, policy := nativeSmokeScopeFixture()
	assertCode(t, ValidateQualificationSmokePlan(nil, policy), e.CodeReleaseIncomplete)
}

func TestQualificationSmokePlanExactNonToolExemptions(t *testing.T) {
	for _, kind := range []string{OutputOCI, OutputAPT} {
		for _, mode := range []string{"exact", "missing", "wrong components", "wrong kind", "extra output"} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				plan, _, _, policy := nativeSmokeScopeFixture()
				plan.LifecycleOutputs = append(plan.LifecycleOutputs, factory.LifecycleOutputPlan{Name: "non-tool", Kind: kind, BuildDigest: nativeSmokeTestDigest("5"), Components: []string{"second", "first"}})
				reason := "deployment-image"
				if kind == OutputAPT {
					reason = "package-repository"
				}
				policy.Smoke.NonToolOutputs = []QualificationNonToolOutput{{Output: "non-tool", Kind: kind, Components: []string{"first", "second"}, Reason: reason}}
				switch mode {
				case "missing":
					policy.Smoke.NonToolOutputs = nil
				case "wrong components":
					policy.Smoke.NonToolOutputs[0].Components = []string{"other"}
				case "wrong kind":
					if kind == OutputOCI {
						policy.Smoke.NonToolOutputs[0].Kind, policy.Smoke.NonToolOutputs[0].Reason = OutputAPT, "package-repository"
					} else {
						policy.Smoke.NonToolOutputs[0].Kind, policy.Smoke.NonToolOutputs[0].Reason = OutputOCI, "deployment-image"
					}
				case "extra output":
					extra := policy.Smoke.NonToolOutputs[0]
					extra.Output = "extra"
					policy.Smoke.NonToolOutputs = append(policy.Smoke.NonToolOutputs, extra)
				}
				beforePlan, _ := canonical.Marshal(plan)
				beforePolicy, _ := canonical.Marshal(policy)
				err := ValidateQualificationSmokePlan(&plan, policy)
				switch mode {
				case "exact":
					if err != nil {
						t.Fatal(err)
					}
				case "extra output":
					assertCode(t, err, e.CodeReleaseIncomplete)
				default:
					assertCode(t, err, e.CodeGateUnmet)
				}
				afterPlan, _ := canonical.Marshal(plan)
				afterPolicy, _ := canonical.Marshal(policy)
				if string(beforePlan) != string(afterPlan) || string(beforePolicy) != string(afterPolicy) {
					t.Fatal("static admission mutated caller inputs")
				}
			})
		}
	}
}

func TestQualificationSmokePlanExactNativePlatforms(t *testing.T) {
	for _, platforms := range [][]string{{"linux/amd64"}, {"linux/arm64"}, {"linux/amd64", "linux/arm64"}} {
		t.Run(strings.Join(platforms, "+"), func(t *testing.T) {
			plan, _, _, policy := nativeSmokeScopeFixture()
			output, build := plan.LifecycleOutputs[0], plan.Builds[0]
			plan.LifecycleOutputs, plan.Builds = nil, nil
			policy.Cosign.Digest = ""
			policy.Cosign.PlatformDigests = map[string]string{"linux/amd64": nativeSmokeTestDigest("1"), "linux/arm64": nativeSmokeTestDigest("2")}
			policy.Smoke.Images = map[string]string{}
			for _, platform := range platforms {
				selected, executable := output, build
				selected.Name = "tool-" + strings.TrimPrefix(platform, "linux/")
				executable.Name, executable.Platform = selected.Name, platform
				executable.Graph = &ir.Graph{Nodes: slices.Clone(build.Graph.Nodes)}
				executable.Graph.Nodes[0].Platform = platform
				plan.LifecycleOutputs = append(plan.LifecycleOutputs, selected)
				plan.Builds = append(plan.Builds, executable)
				policy.Smoke.Images[platform] = "registry.example.test/smoke@" + nativeSmokeTestDigest("1")
			}
			if err := ValidateQualificationSmokePlan(&plan, policy); err != nil {
				t.Fatal(err)
			}
			if len(platforms) == 1 {
				other := "linux/arm64"
				if platforms[0] == other {
					other = "linux/amd64"
				}
				policy.Smoke.Images[other] = policy.Smoke.Images[platforms[0]]
			} else {
				delete(policy.Smoke.Images, platforms[0])
			}
			assertCode(t, ValidateQualificationSmokePlan(&plan, policy), e.CodeReleaseIncomplete)
		})
	}
}

type smokePlanBundleReader func(string) (delivery.BinaryBundle, error)

func (read smokePlanBundleReader) ExecutableBundle(name string) (delivery.BinaryBundle, error) {
	return read(name)
}

func TestQualificationSmokePlanAdmissionPrecedesEveryBundle(t *testing.T) {
	plan, checks, _, policy := nativeSmokeScopeFixture()
	// The invalid output follows the executable: a sequential per-output gate
	// would already request a bundle before learning this policy can never pass.
	plan.LifecycleOutputs = append(plan.LifecycleOutputs, factory.LifecycleOutputPlan{Name: "late-image", Kind: OutputOCI, BuildDigest: nativeSmokeTestDigest("5"), Components: []string{"image"}})
	checks.Outputs = append(checks.Outputs, ArtifactCheckOutput{Name: "late-image", Kind: OutputOCI, BuildDigest: nativeSmokeTestDigest("5"), Overall: "pass"})
	calls := 0
	members, err := deriveQualificationSmokeMembers(&plan, checks, smokePlanBundleReader(func(string) (delivery.BinaryBundle, error) {
		calls++
		return delivery.BinaryBundle{}, nil
	}), policy)
	assertCode(t, err, e.CodeGateUnmet)
	if calls != 0 || members != nil {
		t.Fatal("impossible policy reached bundle acquisition or returned partial scope")
	}
}

func TestQualificationSmokePlanScopeOwnsDerivedExpectations(t *testing.T) {
	plan, checks, bundles, policy := nativeSmokeScopeFixture()
	expected, err := deriveQualificationSmokeMembers(&plan, checks, bundles, policy)
	if err != nil {
		t.Fatal(err)
	}
	actual, err := deriveQualificationSmokeMembers(&plan, checks, smokePlanBundleReader(func(name string) (delivery.BinaryBundle, error) {
		plan.LifecycleOutputs[0].Components[0] = "changed"
		plan.Builds[0].Graph.Nodes[0].Version = "9.9.9"
		policy.Smoke.Tools["tool"] = "kubectl-client-version-json"
		return bundles.ExecutableBundle(name)
	}), policy)
	if err != nil || !reflect.DeepEqual(actual, expected) {
		t.Fatalf("bundle callback changed planned expectations: %v %v", actual, err)
	}
}
