// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	authored "github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
)

func TestInternalToolRunGraphExcludesDeliveryMembership(t *testing.T) {
	base, plan := internalToolPlanFixture(t)
	if len(plan.InternalTools) != 1 || !reflect.DeepEqual(plan.LifecycleOutputs, base.LifecycleOutputs) {
		t.Fatal("internal tool changed delivery membership or lacks required Run selection")
	}
	if !reflect.DeepEqual(plan.Routes, base.Routes) || !reflect.DeepEqual(plan.Targets, base.Targets) {
		t.Fatal("internal helper created a publication route or target")
	}
	nodes, _ := planIndex(plan)
	consumer := nodes["node-image/sbom"]
	for _, input := range plan.InternalTools[0].inputs() {
		if !slices.Contains(consumer.Inputs, input) {
			t.Fatalf("inspection lacks required tool evidence %q", input)
		}
	}
	if !slices.Equal(consumer.InternalTools, []string{"inspector"}) {
		t.Fatal("consumer lacks explicit tool use")
	}
	assertFactoryErrorCode(t, admitInternalToolExecution(plan), e.CodeEngineCapability)
	_, err := admitExecutionNode(t.Context(), plan, consumer.ID)
	assertFactoryErrorCode(t, err, e.CodeEngineCapability)
}

func TestInternalToolScannerIsNativePlanEvidence(t *testing.T) {
	_, base := internalToolPlanFixture(t)
	for _, tc := range []struct {
		name, platform string
		code           e.Code
	}{
		{"native", "linux/amd64", ""},
		{"wrong architecture", "linux/arm64", e.CodeEngineCapability},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := base.Definition.Clone()
			d.InternalTools[0].SBOMTool = "helper-scanner"
			pin := base.CatalogSnapshot.Tools["cosign"]
			pin.Platform = tc.platform
			base.CatalogSnapshot.Tools["helper-scanner"] = pin
			stack, err := catalog.ApplyStack(base.CatalogSnapshot)
			if err != nil {
				t.Fatal(err)
			}
			plan, err := Compile(t.Context(), d, base.Environment, stack, "test")
			if tc.code != "" {
				assertFactoryErrorCode(t, err, tc.code)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := plan.Validate(); err != nil {
				t.Fatal(err)
			}
			nodes, _ := planIndex(plan)
			if !slices.Contains(nodes["internal/inspector/sbom"].Inputs, "tool/helper-scanner") {
				t.Fatal("internal SBOM lacks selected native scanner")
			}
			if plan.InternalTools[0].BuildDigest != base.InternalTools[0].BuildDigest {
				t.Fatal("evidence scanner changed producer Build identity")
			}
		})
	}
}

func TestInternalInspectionHelperDoesNotChangeDiskBuild(t *testing.T) {
	_, original := internalToolPlanFixture(t)
	before, err := original.Digest()
	if err != nil {
		t.Fatal(err)
	}
	helper := original.CatalogSnapshot.Components["helper"]
	helper.BuildCommand = []string{"make", "updated-helper"}
	original.CatalogSnapshot.Components["helper"] = helper
	stack, err := catalog.ApplyStack(original.CatalogSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := Compile(t.Context(), original.Definition, original.Environment, stack, "test")
	if err != nil {
		t.Fatal(err)
	}
	after, err := updated.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if before == after || original.InternalTools[0].BuildDigest == updated.InternalTools[0].BuildDigest {
		t.Fatal("helper producer change did not change helper Build and Plan identities")
	}
	if !reflect.DeepEqual(original.LifecycleOutputs, updated.LifecycleOutputs) {
		t.Fatal("observation helper changed disk logical Build identity")
	}
}

func TestInternalToolScannerCatalogScope(t *testing.T) {
	for _, tc := range []struct {
		name     string
		explicit bool
		conflict bool
		missing  bool
		code     e.Code
	}{
		{name: "explicit scanner uses internal catalog", explicit: true},
		{name: "inherited scanner uses definition catalog"},
		{name: "explicit scanner cannot fall back", explicit: true, missing: true, code: e.CodeUnpinnedSource},
		{name: "one name cannot select conflicting pins", explicit: true, conflict: true, code: e.CodeUnpinnedSource},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, base := internalToolPlanFixture(t)
			d := base.Definition.Clone()
			d.InternalTools[0].Catalog = &authored.Catalog{Base: "helper-catalog"}
			pin := base.CatalogSnapshot.Tools["cosign"]
			base.CatalogSnapshot.Tools["scanner"] = pin
			d.SBOMTool = "scanner"
			stack, err := catalog.ApplyStack(base.CatalogSnapshot)
			if err != nil {
				t.Fatal(err)
			}
			internal, err := catalog.ApplyStack(base.CatalogSnapshot)
			if err != nil {
				t.Fatal(err)
			}
			delete(internal.Catalog.Tools, "scanner")
			if tc.explicit {
				d.InternalTools[0].SBOMTool = "helper-scanner"
				if tc.conflict {
					d.InternalTools[0].SBOMTool = "scanner"
					pin.ExecutableDigest = "sha256:" + strings.Repeat("a", 64)
				}
				if !tc.missing {
					internal.Catalog.Tools[d.InternalTools[0].SBOMTool] = pin
				}
				// A pin existing only at the top level must not satisfy an explicit override.
				if tc.missing {
					stack.Catalog.Tools["helper-scanner"] = pin
				}
			}
			internal, err = catalog.ApplyStack(internal.Catalog)
			if err != nil {
				t.Fatal(err)
			}
			stack, err = catalog.ApplyStack(stack.Catalog)
			if err != nil {
				t.Fatal(err)
			}
			plan, err := compileWithTargetStacks(t.Context(), d, base.Environment, stack,
				map[string]*catalog.Stack{"internal/inspector": internal}, "test", "", "")
			if tc.code != "" {
				assertFactoryErrorCode(t, err, tc.code)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := plan.Validate(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func internalToolPlanFixture(t *testing.T) (*Plan, *Plan) {
	t.Helper()
	base := diskPublicationPlanFixture(t, "linux/amd64")
	d := base.Definition.Clone()
	d.InternalTools = []authored.InternalTool{{Name: "inspector", Platform: "linux/amd64", Format: authored.OutputExecutable,
		Components: []string{"helper"}, Result: "helper"}}
	env := base.Environment.Clone()
	profile := env.DiskInspectionProfiles["linux/amd64"]
	profile.HelperTool = "inspector"
	profile.BootstrapInterpreterDigest, profile.BootstrapProgramDigest = testInspectionBootstrapPins()
	env.DiskInspectionProfiles["linux/amd64"] = profile
	c := base.CatalogSnapshot.Components["image"]
	c.BuilderType, c.OutputType, c.Disk, c.Packer = catalog.BuilderOCIHermetic, "host-package", nil, nil
	c.BuildCommand, c.ArtifactPath, c.WriteDir = []string{"make", "helper"}, "bin/helper", "bin"
	c.Materials = nil
	c.BuildRequirements = nil
	base.CatalogSnapshot.Components["helper"] = c
	stack, err := catalog.ApplyStack(base.CatalogSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := Compile(t.Context(), d, env, stack, "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := plan.Validate(); err != nil {
		t.Fatal(err)
	}
	return base, plan
}

func TestInternalToolRequiredUseTampering(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*Plan)
	}{
		{"payload", func(p *Plan) { p.InternalTools[0].ArtifactID = "artifact/node-image/image" }},
		{"missing requirement", func(p *Plan) { p.InternalTools = nil }},
		{"wrong architecture", func(p *Plan) { p.InternalTools[0].Platform = "linux/arm64" }},
		{"consumer use", func(p *Plan) {
			for i := range p.Graph.Nodes {
				if p.Graph.Nodes[i].ID == "node-image/sbom" {
					p.Graph.Nodes[i].InternalTools = nil
				}
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, plan := internalToolPlanFixture(t)
			tc.mutate(plan)
			assertFactoryErrorCode(t, plan.Validate(), e.CodePipelineDrift)
		})
	}
}

func TestInternalToolMissingAndUnusedSelection(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*authored.Definition, *authored.Environment)
		code   e.Code
	}{
		{"missing", func(d *authored.Definition, _ *authored.Environment) { d.InternalTools = nil }, e.CodeEngineCapability},
		{"unused", func(_ *authored.Definition, env *authored.Environment) {
			p := env.DiskInspectionProfiles["linux/amd64"]
			p.HelperTool = ""
			p.BootstrapInterpreterDigest, p.BootstrapProgramDigest = "", ""
			env.DiskInspectionProfiles["linux/amd64"] = p
		}, e.CodeInvalidCatalog},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, plan := internalToolPlanFixture(t)
			d, env := plan.Definition.Clone(), plan.Environment.Clone()
			tc.mutate(&d, &env)
			stack, err := catalog.ApplyStack(plan.CatalogSnapshot)
			if err != nil {
				t.Fatal(err)
			}
			_, err = Compile(t.Context(), d, env, stack, "test")
			assertFactoryErrorCode(t, err, tc.code)
		})
	}
}

func TestInternalToolRejectsNonExecutableProducerBeforeWork(t *testing.T) {
	for _, output := range []string{"host-package", "tarball", "oci-image"} {
		t.Run(output, func(t *testing.T) {
			_, base := internalToolPlanFixture(t)
			component := base.CatalogSnapshot.Components["helper"]
			component.OutputType = output
			base.CatalogSnapshot.Components["helper"] = component
			stack, err := catalog.ApplyStack(base.CatalogSnapshot)
			if err != nil {
				t.Fatal(err)
			}
			plan, err := Compile(t.Context(), base.Definition, base.Environment, stack, "test")
			if output == "host-package" {
				if err != nil {
					t.Fatal(err)
				}
				if err := plan.Validate(); err != nil {
					t.Fatal(err)
				}
			} else {
				assertFactoryErrorCode(t, err, e.CodeEngineCapability)
				if plan != nil {
					t.Fatal("unsupported producer returned an executable Plan")
				}
			}
		})
	}
}

func TestInternalToolPublicationTransportsProducerEvidence(t *testing.T) {
	_, plan := internalToolPlanFixture(t)
	tool := plan.InternalTools[0]
	checked := 0
	for _, node := range plan.Graph.Nodes {
		if node.Kind != NodePackage || !slices.Contains(node.Inputs, "sbom/node-image") {
			continue
		}
		checked++
		for _, id := range tool.inputs() {
			if !slices.Contains(node.Inputs, id) {
				t.Fatalf("%s lacks original helper evidence %s", node.ID, id)
			}
		}
	}
	if checked < 2 {
		t.Fatalf("checked %d packaging boundaries, want disk and publication", checked)
	}
	if err := plan.validatePublicationEvidence(); err != nil {
		t.Fatal(err)
	}
	for i := range plan.Graph.Nodes {
		node := &plan.Graph.Nodes[i]
		if node.Kind != NodePackage || node.PackageKind != PackageOCI {
			continue
		}
		node.Inputs = slices.DeleteFunc(node.Inputs, func(id string) bool { return id == tool.ObservationArtifactID })
	}
	assertFactoryErrorCode(t, plan.validatePublicationEvidence(), e.CodeProvenanceMissing)
}

func testInspectionBootstrapPins() (string, string) {
	return "sha256:" + strings.Repeat("a", 64), pipeline.InspectionBootstrapProgramDigest()
}
