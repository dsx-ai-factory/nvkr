// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package builtin

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/validationplan"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/validationplan/nvkequalify"
)

// shippedPlanners is the authored directory the repository ships, so this test
// runs the reviewed inputs through the same path the command takes.
const shippedPlanners = "../../../../inputs/testing/planners"

func digest(c string) string { return "sha256:" + strings.Repeat(c, 64) }

func release() validationplan.Release {
	names := []struct{ name, kind, c string }{
		{"nvke-controller", "oci", "a"},
		{"cluster-api-provider-provisioned", "oci", "b"},
		{"nvkectl", "executable", "c"},
		{"nvkectl-linux-arm64", "executable", "d"},
		{"nvke-node-image-amd64", "disk-image", "e"},
		{"nvke-node-image-arm64", "disk-image", "f"},
	}
	r := validationplan.Release{Reference: "registry.example.test/nvkr/releases@" + digest("1")}
	for _, n := range names {
		r.Outputs = append(r.Outputs, validationplan.Output{
			Name: n.name, Kind: n.kind,
			Reference: "registry.example.test/nvkr/" + n.name + "@" + digest(n.c), Digest: digest(n.c),
		})
	}
	return r
}

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

func TestPlannersListsTheNVKEPlanner(t *testing.T) {
	planners := Planners()
	if _, ok := planners[nvkequalify.Name]; !ok || len(planners) != 1 {
		t.Fatalf("planners = %v, want only %s", planners, nvkequalify.Name)
	}
}

// TestShippedInputsBuildAPlan runs the reviewed inputs and the built-in planners
// end to end over a synthetic release. It proves the shipped configuration, the
// planner and the builder agree. It does not prove anything about a real
// published release.
func TestShippedInputsBuildAPlan(t *testing.T) {
	build := func() *validationplan.Plan {
		plan, err := validationplan.Build(context.Background(), validationplan.Request{
			Release:     release(),
			PlannersDir: shippedPlanners,
			Vars: map[string]map[string]string{nvkequalify.Name: {
				nvkequalify.VarNVKERoot:  nvkeTree(t),
				nvkequalify.VarOverrides: "os-image-amd64=https://images.example.test/node-amd64.qcow2",
			}},
		}, Planners())
		if err != nil {
			t.Fatal(err)
		}
		return plan
	}
	plan := build()

	if plan.QualificationAuthority != validationplan.AuthorityNone {
		t.Fatalf("authority = %q", plan.QualificationAuthority)
	}
	if len(plan.Targets) != 2 || len(plan.Plans) != 1 {
		t.Fatalf("targets = %d, plans = %d, want 2 and 1", len(plan.Targets), len(plan.Plans))
	}
	for _, target := range plan.Targets {
		if target.Executor != validationplan.ExecutorNVKEE2EGoTest {
			t.Errorf("%s executor = %q", target.ID, target.Executor)
		}
		if target.Source.PlanDigest != plan.Plans[0].Digest {
			t.Errorf("%s does not point at the embedded plan", target.ID)
		}
	}

	first, err := plan.Encode()
	if err != nil {
		t.Fatal(err)
	}
	second, err := build().Encode()
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatal("two plans of the same release and inputs differ")
	}
}
