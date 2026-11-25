// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkequalify

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvke/release/pkg/qualification"
	"github.com/dsx-ai-factory/nvke/release/pkg/resolve"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/validationplan"
)

// authoredConfig is the planner configuration the repository ships, so these
// tests exercise the inputs that are reviewed and not a copy of them.
const authoredConfig = "../../../../inputs/testing/planners/nvke-qualify/planner.json"

const releaseReference = "registry.example.test/nvkr/releases@sha256:" +
	"1111111111111111111111111111111111111111111111111111111111111111"

func digest(c string) string { return "sha256:" + strings.Repeat(c, 64) }

// release holds the outputs the shipped registry names, each with its own
// digest, so a swapped output changes the plan.
func release() validationplan.Release {
	outputs := []struct{ name, kind, platform, c string }{
		{"nvke-controller", "oci", "", "a"},
		{"cluster-api-provider-provisioned", "oci", "", "b"},
		{"nvkectl", "executable", "linux/amd64", "c"},
		{"nvkectl-linux-arm64", "executable", "linux/arm64", "d"},
		{"nvke-node-image-amd64", "disk-image", "linux/amd64", "e"},
		{"nvke-node-image-arm64", "disk-image", "linux/arm64", "f"},
	}
	r := validationplan.Release{Reference: releaseReference}
	for _, o := range outputs {
		r.Outputs = append(r.Outputs, validationplan.Output{
			Name: o.name, Kind: o.kind, Platform: o.platform,
			Reference: "registry.example.test/nvkr/" + o.name + "@" + digest(o.c),
			Digest:    digest(o.c),
		})
	}
	return r
}

// nvkeTree is the part of an NVKE tree the built-in strategies read.
func nvkeTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "make"), 0o755); err != nil {
		t.Fatal(err)
	}
	pins := "KIND_VERSION := v0.31.0\nCAPI_VERSION := v1.11.0\nNERDCTL_VERSION := 2.2.0\n"
	if err := os.WriteFile(filepath.Join(root, "make", "tool-versions.mk"), []byte(pins), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func input(t *testing.T, rel validationplan.Release, set map[string]string) validationplan.Input {
	t.Helper()
	if set == nil {
		set = map[string]string{}
	}
	if _, ok := set[VarNVKERoot]; !ok {
		set[VarNVKERoot] = nvkeTree(t)
	}
	return validationplan.Input{Release: rel, Config: authoredConfig, Vars: set}
}

func codeOf(t *testing.T, err error) e.Code {
	t.Helper()
	var coded *e.Error
	if !errors.As(err, &coded) {
		t.Fatalf("error %v carries no code", err)
	}
	return coded.Code
}

func decodePlan(t *testing.T, pp validationplan.PlannerPlan) qualification.Plan {
	t.Helper()
	var plan qualification.Plan
	if err := json.Unmarshal(pp.Plan, &plan); err != nil {
		t.Fatal(err)
	}
	return plan
}

func find(t *testing.T, plan qualification.Plan, name string) qualification.ComponentVersion {
	t.Helper()
	c := plan.Combinations[0]
	for _, list := range [][]qualification.ComponentVersion{c.Components, c.Dependencies} {
		for _, v := range list {
			if v.Name == name {
				return v
			}
		}
	}
	t.Fatalf("plan has no %q", name)
	return qualification.ComponentVersion{}
}

func TestPlanMakesOneTargetPerStream(t *testing.T) {
	result, err := Planner{}.Plan(context.Background(), input(t, release(), nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Plans) != 1 || result.Plans[0].Planner != Name {
		t.Fatalf("plans = %+v, want one embedded %s plan", result.Plans, Name)
	}
	var ids []string
	for _, target := range result.Targets {
		ids = append(ids, target.ID)
		if target.Source.Planner != Name || target.Source.PlanDigest != result.Plans[0].Digest {
			t.Errorf("target %s source = %+v", target.ID, target.Source)
		}
		if target.Inputs.Validator != "nvke/e2e" || target.Inputs.TestEnv == "" {
			t.Errorf("target %s inputs = %+v", target.ID, target.Inputs)
		}
		if target.Inputs.Outputs["controller"] != "nvke-controller" || target.Inputs.Outputs["cli-arm64"] != "nvkectl-linux-arm64" {
			t.Errorf("target %s outputs = %v", target.ID, target.Inputs.Outputs)
		}
	}
	want := []string{"e2e/forge-gb200-amd64-ubuntu-26.04-kube-1.36", "e2e/forge-gb200-arm64-ubuntu-26.04-kube-1.36"}
	if strings.Join(ids, ",") != strings.Join(want, ",") {
		t.Fatalf("target ids = %v, want %v", ids, want)
	}
}

func TestPlanResolvesReleaseComponentsToTheirDigests(t *testing.T) {
	result, err := Planner{}.Plan(context.Background(), input(t, release(), nil))
	if err != nil {
		t.Fatal(err)
	}
	plan := decodePlan(t, result.Plans[0])
	if plan.Subject != "nvkr" || plan.Version != digest("1") {
		t.Errorf("subject/version = %s/%s, want nvkr and the release digest", plan.Subject, plan.Version)
	}
	for name, c := range map[string]string{"controller": "a", "capi-provisioned": "b", "cli-amd64": "c", "cli-arm64": "d"} {
		got := find(t, plan, name)
		if got.Version != digest(c) {
			t.Errorf("%s version = %q, want %q", name, got.Version, digest(c))
		}
		if !strings.HasSuffix(got.URL, "@"+digest(c)) {
			t.Errorf("%s url = %q, want the output reference", name, got.URL)
		}
	}
	for name, c := range map[string]string{"os-image-amd64": "e", "os-image-arm64": "f"} {
		got := find(t, plan, name)
		if got.Version != "" || !strings.HasSuffix(got.URL, "@"+digest(c)) {
			t.Errorf("%s = %+v, want a location carrying the digest and no version", name, got)
		}
	}
	if got := find(t, plan, "kind"); got.Version != "v0.31.0" || got.VersionStrategy != "makeVar" {
		t.Errorf("kind = %+v, want the NVKE pin through makeVar", got)
	}
}

func TestPlanRecordsTheExactDeclaredKubeAndOVNVersions(t *testing.T) {
	result, err := Planner{}.Plan(context.Background(), input(t, release(), nil))
	if err != nil {
		t.Fatal(err)
	}
	plan := decodePlan(t, result.Plans[0])
	if got := find(t, plan, "cni-ovn"); got.Version != "2.4.2" || got.VersionStrategy != StrategyDeclared {
		t.Errorf("cni-ovn = %+v, want the declared 2.4.2 through %s", got, StrategyDeclared)
	}
	streams := plan.Combinations[0].Streams
	if len(streams) != 2 {
		t.Fatalf("streams = %d, want 2", len(streams))
	}
	for _, stream := range streams {
		var kube *qualification.ComponentVersion
		for i := range stream.Dependencies {
			if stream.Dependencies[i].Name == "kube" {
				kube = &stream.Dependencies[i]
			}
		}
		if kube == nil || kube.Version != "1.36.3" || kube.VersionStrategy != StrategyDeclared || stream.Capabilities["kube"] != "1.36" {
			t.Errorf("%s kube = %+v, want 1.36.3 for the stream's minor %q", stream.Name, kube, stream.Capabilities["kube"])
		}
	}
}

func TestPlanEmbedsADigestBoundPlan(t *testing.T) {
	result, err := Planner{}.Plan(context.Background(), input(t, release(), nil))
	if err != nil {
		t.Fatal(err)
	}
	pp := result.Plans[0]
	plan := decodePlan(t, pp)
	want, err := canonical.Digest(canonical.KindPlannerPlan, plan)
	if err != nil {
		t.Fatal(err)
	}
	if pp.Digest != want {
		t.Fatalf("digest = %s, want %s", pp.Digest, want)
	}
	raw, err := canonical.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != string(pp.Plan) {
		t.Fatal("the embedded plan is not in canonical encoding")
	}
}

func TestPlanIsDeterministic(t *testing.T) {
	tree := nvkeTree(t)
	set := func() map[string]string { return map[string]string{VarNVKERoot: tree} }
	first, err := Planner{}.Plan(context.Background(), input(t, release(), set()))
	if err != nil {
		t.Fatal(err)
	}
	second, err := Planner{}.Plan(context.Background(), input(t, release(), set()))
	if err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(first)
	b, _ := json.Marshal(second)
	if string(a) != string(b) {
		t.Fatal("two plans of the same release differ")
	}
}

func TestPlanDifferentReleasesGiveDifferentDigests(t *testing.T) {
	other := release()
	other.Reference = "registry.example.test/nvkr/releases@" + digest("9")
	other.Outputs[0].Digest = digest("9")
	other.Outputs[0].Reference = "registry.example.test/nvkr/nvke-controller@" + digest("9")
	a, err := Planner{}.Plan(context.Background(), input(t, release(), nil))
	if err != nil {
		t.Fatal(err)
	}
	b, err := Planner{}.Plan(context.Background(), input(t, other, nil))
	if err != nil {
		t.Fatal(err)
	}
	if a.Plans[0].Digest == b.Plans[0].Digest {
		t.Fatal("a swapped output left the plan digest unchanged")
	}
}

func TestPlanServedImageOverridesTheLocation(t *testing.T) {
	served := "https://images.example.test/nvkr/node-amd64.qcow2"
	result, err := Planner{}.Plan(context.Background(), input(t, release(), map[string]string{VarOverrides: "os-image-amd64=" + served}))
	if err != nil {
		t.Fatal(err)
	}
	plan := decodePlan(t, result.Plans[0])
	got := find(t, plan, "os-image-amd64")
	if got.URL != served {
		t.Errorf("url = %q, want the served URL", got.URL)
	}
	if got.Version != "" {
		t.Errorf("version = %q, want none for a location-only entry", got.Version)
	}
	if len(plan.Overrides) != 1 || plan.Overrides[0] != "os-image-amd64" {
		t.Errorf("overrides = %v, want os-image-amd64 recorded", plan.Overrides)
	}
}

func TestPlanFailsClosed(t *testing.T) {
	missingOutput := release()
	missingOutput.Outputs = missingOutput.Outputs[1:]
	duplicate := release()
	duplicate.Outputs = append(duplicate.Outputs, duplicate.Outputs[0])

	tests := []struct {
		name string
		rel  validationplan.Release
		set  map[string]string
		want e.Code
	}{
		{"no NVKE tree", release(), map[string]string{VarNVKERoot: ""}, e.CodeUsage},
		{"NVKE tree is not a directory", release(), map[string]string{VarNVKERoot: "/nonexistent/nvke"}, e.CodeUsage},
		{"a variable the planner does not take", release(), map[string]string{"nvke-rot": "./tree"}, e.CodeUsage},
		{"an override NVKE's subject does not list", release(), map[string]string{VarOverrides: "not-a-component=x"}, e.CodeUsage},
		{"an override of a version the release supplies", release(), map[string]string{VarOverrides: "controller=v1.0.0"}, e.CodeUsage},
		{"a served image that is not https", release(), map[string]string{VarOverrides: "os-image-amd64=http://images.example.test/a.qcow2"}, e.CodeUsage},
		{"an override that is not name=value", release(), map[string]string{VarOverrides: "os-image-amd64"}, e.CodeUsage},
		{"an override named twice", release(), map[string]string{VarOverrides: "kind=v1,kind=v2"}, e.CodeUsage},
		{"an output the registry names is absent", missingOutput, nil, e.CodeUsage},
		{"two outputs share a name", duplicate, nil, e.CodeUsage},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			set := tc.set
			if set == nil {
				set = map[string]string{}
			}
			if _, ok := set[VarNVKERoot]; !ok {
				set[VarNVKERoot] = nvkeTree(t)
			}
			result, err := Planner{}.Plan(context.Background(), validationplan.Input{Release: tc.rel, Config: authoredConfig, Vars: set})
			if err == nil {
				t.Fatalf("a plan was made: %+v", result)
			}
			if got := codeOf(t, err); got != tc.want {
				t.Fatalf("code = %s, want %s (%v)", got, tc.want, err)
			}
			if len(result.Targets) != 0 || len(result.Plans) != 0 {
				t.Fatalf("a failure returned partial output: %+v", result)
			}
		})
	}
}

func TestPlanRefusesACanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Planner{}.Plan(ctx, input(t, release(), nil))
	if err == nil || codeOf(t, err) != e.CodeTimeout {
		t.Fatalf("err = %v, want a timeout", err)
	}
}

func TestDeclaredStrategy(t *testing.T) {
	factory := strategies(release()).Version[StrategyDeclared]
	r, err := factory(nil, "")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		inputs map[string]string
		want   string
		fail   bool
	}{
		{"a constraint", map[string]string{"constraint": "1.36"}, "1.36", false},
		{"a value", map[string]string{"value": "2.4.2"}, "2.4.2", false},
		{"an override replaces the declaration", map[string]string{"constraint": "1.36", "override": "1.37"}, "1.37", false},
		{"nothing declared", map[string]string{}, "", true},
		{"a constraint and a value", map[string]string{"constraint": "1", "value": "2"}, "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := r.Resolve(tc.inputs)
			if tc.fail {
				if err == nil {
					t.Fatalf("resolved %v", got)
				}
				return
			}
			if err != nil || got["version"] != tc.want {
				t.Fatalf("got %v, %v, want %q", got, err, tc.want)
			}
		})
	}
	t.Run("validation refuses what it cannot answer", func(t *testing.T) {
		v, ok := r.(resolve.Validator)
		if !ok {
			t.Fatal("the declared resolver does not validate its declarations")
		}
		if err := v.Validate(map[string]string{}); err == nil {
			t.Error("an empty declaration was accepted")
		}
		if err := v.Validate(map[string]string{"releases": "1,2", "constraint": "1"}); err == nil {
			t.Error("a releases list was accepted")
		}
		if err := v.Validate(map[string]string{"constraint": "1.36"}); err != nil {
			t.Errorf("a constraint was refused: %v", err)
		}
	})
	t.Run("a key that is not a version prefix is refused", func(t *testing.T) {
		if _, err := factory(map[string]string{"output": "x"}, ""); err == nil {
			t.Fatal("a with key was accepted")
		}
	})
}

func TestDeclaredStrategyTable(t *testing.T) {
	factory := strategies(release()).Version[StrategyDeclared]

	t.Run("a minor resolves to its declared patch", func(t *testing.T) {
		r, err := factory(map[string]string{"1.36": "1.36.3", "1.35": "1.35.3"}, "")
		if err != nil {
			t.Fatal(err)
		}
		got, err := r.Resolve(map[string]string{"constraint": "1.35"})
		if err != nil || got["version"] != "1.35.3" {
			t.Fatalf("got %v, %v, want 1.35.3", got, err)
		}
	})

	r, err := factory(map[string]string{"1.36": "1.36.3"}, "")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		inputs map[string]string
	}{
		{"a minor the table does not list", map[string]string{"constraint": "1.37"}},
		{"no constraint", map[string]string{}},
		{"a value in place of a constraint", map[string]string{"value": "1.36.3"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got, err := r.Resolve(tc.inputs); err == nil {
				t.Fatalf("resolved %v", got)
			}
		})
	}
	t.Run("an override still replaces the declaration", func(t *testing.T) {
		got, err := r.Resolve(map[string]string{"constraint": "1.36", "override": "1.36.9"})
		if err != nil || got["version"] != "1.36.9" {
			t.Fatalf("got %v, %v", got, err)
		}
	})

	refused := []struct {
		name  string
		table map[string]string
	}{
		{"a key that is not a version prefix", map[string]string{"latest": "1.36.3"}},
		{"a value with a leading v", map[string]string{"1.36": "v1.36.3"}},
		{"a value that is not exact", map[string]string{"1.36": "1.36"}},
		{"a value under another key", map[string]string{"1.36": "1.35.3"}},
		{"a value that only shares digits with its key", map[string]string{"1.3": "1.36.3"}},
	}
	for _, tc := range refused {
		t.Run("a table with "+tc.name, func(t *testing.T) {
			if _, err := factory(tc.table, ""); err == nil {
				t.Fatalf("table %v was accepted", tc.table)
			}
		})
	}
}

func TestReleaseStrategies(t *testing.T) {
	s := strategies(release())
	version := s.Version[StrategyVersion]
	location := s.Location[StrategyLocation]

	t.Run("version of the release itself", func(t *testing.T) {
		r, err := version(nil, "")
		if err != nil {
			t.Fatal(err)
		}
		got, err := r.Resolve(nil)
		if err != nil || got["version"] != digest("1") {
			t.Fatalf("got %v, %v, want the release digest", got, err)
		}
	})
	t.Run("a key the strategies do not take", func(t *testing.T) {
		if _, err := version(map[string]string{"outptu": "nvkectl"}, ""); err == nil {
			t.Fatal("a misspelled key was accepted")
		}
		if _, err := location(map[string]string{"outptu": "nvkectl"}, resolve.Options{}); err == nil {
			t.Fatal("a misspelled key was accepted")
		}
	})
	t.Run("a location names an output", func(t *testing.T) {
		if _, err := location(nil, resolve.Options{}); err == nil {
			t.Fatal("a location with no output was accepted")
		}
	})
	t.Run("a version override is refused", func(t *testing.T) {
		r, err := version(map[string]string{"output": "nvkectl"}, "")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := r.Resolve(map[string]string{"override": "v1"}); err == nil {
			t.Fatal("an override replaced an authenticated version")
		}
	})
}
