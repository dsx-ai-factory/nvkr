// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	authored "github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/execution"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

// The existing image fixture supplies actual validated OCI bytes and a producer
// record. The unrelated VM remains descriptive: this test never executes it.
func mixedLocalUtilityPlan(t *testing.T) *Plan {
	t.Helper()
	imagePlan, _, _ := acquiredImagePlanFixture(t, "linux/amd64")
	d, env, vm := vmFactoryInputs(t, "linux/amd64", execution.CapabilityPreferred)
	components := map[string]catalog.Component{"worker": imagePlan.CatalogSnapshot.Components["worker"]}
	image := vm.Catalog.Components["image"]
	image.BuildCommand = nil
	image.Packer = &catalog.PackerConfig{
		ProjectPath: "os-image/packer", PackerVersion: "1.14.2", BuildName: "image.qemu.node",
		ArchitectureVariable: "architecture", AcceleratorVariable: "accelerator",
		MaterialManifestVariable: "material_manifest", OutputDirectoryVariable: "output_directory",
	}
	components["image"] = image
	stack, err := catalog.ApplyStack(&catalog.Catalog{Components: components})
	if err != nil {
		t.Fatal(err)
	}
	d.Outputs = append(d.Outputs, imagePlan.Definition.Outputs...)
	plan, err := Compile(t.Context(), d, env, stack, "v0.0.0 (test)")
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func TestExecuteMixedLocalUtilitiesPreservesImageSubject(t *testing.T) {
	t.Chdir(t.TempDir())
	plan := mixedLocalUtilityPlan(t)
	_, artifacts := planIndex(plan)
	for _, id := range []string{"worker/terminal-observe", "worker/sbom", "package/worker"} {
		if err := ExecuteNode(t.Context(), plan, id, io.Discard, io.Discard); err != nil {
			t.Fatalf("offline utility %s rejected unrelated VM requirements: %v", id, err)
		}
	}
	result, err := readProducerResult(t.Context(), artifacts["result/worker/worker"].Path)
	if err != nil {
		t.Fatal(err)
	}
	image, err := delivery.InspectContainerImage(t.Context(), artifacts["package/worker"].Path, "linux/amd64")
	if err != nil || image.ManifestDigest != result.SHA256 {
		t.Fatalf("packaging changed the observed image: %#v, %v", image, err)
	}
	if _, err := os.Stat(filepath.Join(artifacts["sbom/worker"].Path, "index.json")); err != nil {
		t.Fatal(err)
	}
}

func TestMixedLocalUtilityCannotExecuteProducerOrObserveDisk(t *testing.T) {
	t.Chdir(t.TempDir())
	plan := mixedLocalUtilityPlan(t)
	for _, tc := range []struct {
		name string
		node string
		code e.Code
	}{
		{"VM producer", "node-image/build-image", e.CodeEngineCapability},
		{"disk observation", "node-image/terminal-observe", e.CodeEngineCapability},
		{"image acquisition", "worker/pull-worker", e.CodeEngineCapability},
		{"unknown node", "missing", e.CodeInvalidCatalog},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertFactoryErrorCode(t, ExecuteNode(t.Context(), plan, tc.node, nil, nil), tc.code)
			if _, err := os.Stat(".nvkr/factory/node-image"); !os.IsNotExist(err) {
				t.Fatalf("denied operation touched disk workspace: %v", err)
			}
		})
	}
}

func TestMixedLocalUtilityRejectsUnsupportedVMDriver(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*catalog.Component)
	}{
		{"command driver", func(c *catalog.Component) { c.Packer = nil; c.BuildCommand = []string{"make", "image"} }},
		{"encrypted disk", func(c *catalog.Component) { c.Disk.Encryption = "luks2" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			plan := mixedLocalUtilityPlan(t)
			base := plan.CatalogSnapshot
			c := base.Components["image"]
			tc.edit(&c)
			base.Components["image"] = c
			stack, err := catalog.ApplyStack(base)
			if err != nil {
				t.Fatal(err)
			}
			plan, err = Compile(t.Context(), plan.Definition, plan.Environment, stack, "test")
			if err != nil {
				t.Fatal(err)
			}
			assertFactoryErrorCode(t, ExecuteNode(t.Context(), plan, "worker/terminal-observe", nil, nil), e.CodeEngineCapability)
		})
	}
}

func TestMixedLocalUtilityRejectsOrdinaryBuildRequirements(t *testing.T) {
	t.Chdir(t.TempDir())
	plan := mixedLocalUtilityPlan(t)
	c := plan.CatalogSnapshot.Components["image"]
	c.BuilderType, c.OutputType, c.Disk, c.Packer = catalog.BuilderOCIHermetic, "host-package", nil, nil
	c.BuildCommand, c.ArtifactPath = []string{"make", "tool"}, "bin/tool"
	c.WriteDir = "bin"
	plan.CatalogSnapshot.Components["required-tool"] = c
	stack, err := catalog.ApplyStack(plan.CatalogSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	d := plan.Definition
	d.Outputs = append(d.Outputs, authored.DefinitionOutput{Name: "required-tool", Platform: "linux/amd64", Format: authored.OutputExecutable, Components: []string{"required-tool"}})
	sort.Slice(d.Outputs, func(i, j int) bool { return d.Outputs[i].Name < d.Outputs[j].Name })
	plan, err = Compile(t.Context(), d, plan.Environment, stack, "test")
	if err != nil {
		t.Fatal(err)
	}
	assertFactoryErrorCode(t, ExecuteNode(t.Context(), plan, "worker/terminal-observe", nil, nil), e.CodeEngineCapability)
}

// Exercise each admission term independently. These descriptive mutations are
// deliberately tested at the owning gate, before full-Plan validation can mask
// a missing term with an unrelated digest or projection mismatch.
func TestMixedLocalUtilityAdmissionRejectsEachUnsupportedRoute(t *testing.T) {
	t.Chdir(t.TempDir())
	base := mixedLocalUtilityPlan(t)
	const id = "worker/terminal-observe"
	if err := admitExecuteNodeRequirements(base, id); err != nil {
		t.Fatal(err)
	}
	raw, err := canonical.Marshal(base)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		edit  func(*Plan, *Node)
		guard string
	}{
		{"production qualification", func(p *Plan, _ *Node) { p.Qualification = TierPipelineAttested }, "cannot enforce execution requirements"},
		{"ARC", func(p *Plan, _ *Node) { p.Environment.GitHubARC = "native-kvm-v1" }, "requires the native-kvm-v1 ARC disk route"},
		{"publication", func(p *Plan, _ *Node) { p.Environment.Publication = &authored.Publication{} }, "cannot enforce execution requirements"},
		{"signing", func(p *Plan, _ *Node) { p.Environment.Signing = &authored.Signing{} }, "cannot enforce execution requirements"},
		{"network utility", func(_ *Plan, n *Node) { n.Network = ir.NetworkDefault }, "mixed local utility admission excludes"},
		{"utility requirements", func(_ *Plan, n *Node) { n.Requirements = testExecutionRequirements() }, "mixed local utility admission excludes"},
		{"utility execution binding", func(_ *Plan, n *Node) { n.ExecutionBinding = &execution.Binding{} }, "mixed local utility admission excludes"},
		{"OCI publication package", func(_ *Plan, n *Node) { n.Kind, n.PackageKind = NodePackage, PackageOCI }, "cannot package disk or release output"},
		{"disk package", func(_ *Plan, n *Node) { n.Kind, n.PackageKind = NodePackage, PackageDiskImage }, "cannot package disk or release output"},
		{"publish utility", func(_ *Plan, n *Node) { n.Kind = NodePublish }, "cannot execute mixed-Plan operation"},
		{"source credentials", func(p *Plan, _ *Node) {
			p.Environment.SourceAccess = map[string]authored.SourceAccess{"https://example.test/source": {CredentialRef: "secret"}}
		}, "cannot admit hosted credentials or dispatch policy"},
		{"dispatch policy", func(p *Plan, _ *Node) { p.Environment.GitHubDispatchBranches = []string{"candidate"} }, "cannot admit hosted credentials or dispatch policy"},
		{"development policy", func(p *Plan, _ *Node) { p.Environment.GitHubDevelopment = true }, "cannot admit hosted credentials or dispatch policy"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Admission reads no payload bytes. Clone only the descriptions each
			// mutation changes, keeping the shared image fixture immutable.
			var plan Plan
			if err := canonical.UnmarshalExact(raw, &plan); err != nil {
				t.Fatal(err)
			}
			var node *Node
			for i := range plan.Graph.Nodes {
				if plan.Graph.Nodes[i].ID == id {
					node = &plan.Graph.Nodes[i]
					break
				}
			}
			if node == nil || node.IRNode != "" {
				t.Fatal("fixture must select a non-IR offline utility")
			}
			tc.edit(&plan, node)
			err := admitExecuteNodeRequirements(&plan, id)
			assertFactoryErrorCode(t, err, e.CodeEngineCapability)
			if !strings.Contains(err.Error(), tc.guard) {
				t.Fatalf("wrong admission branch: %v; want %q", err, tc.guard)
			}
			if _, err := os.Stat(".nvkr/factory/node-image"); !os.IsNotExist(err) {
				t.Fatalf("admission touched disk workspace: %v", err)
			}
		})
	}
}

func TestMixedLocalUtilityLaunchBindsExactPlan(t *testing.T) {
	t.Chdir(t.TempDir())
	plan := mixedLocalUtilityPlan(t)
	digest, err := plan.Digest()
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	runtime := filepath.Join(root, "runtime")
	log := filepath.Join(root, "calls")
	t.Setenv("NVKR_TEST_RUNTIME_LOG", log)
	if err := os.WriteFile(runtime, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" >> \"$NVKR_TEST_RUNTIME_LOG\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Ordinary terminal observation runs on the host; SBOM production still
	// enters the offline child and must bind the complete original Plan.
	node := planNode(plan, "worker/sbom")
	options := withLocalDefaults(LocalRunOptions{Workspace: ".nvkr/test", Runtime: runtime})
	compiler := filepath.Join(root, "compiler")
	if err := os.WriteFile(compiler, []byte("#!/bin/sh\n[ \"$5\" = -o ] || exit 91\nprintf 'fixture runner' > \"$6\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	options.Go, options.RunnerSource = compiler, root
	runners, err := prepareLocalRunners(t.Context(), plan, options, []string{"linux/amd64"})
	if err != nil {
		t.Fatal(err)
	}
	if err := executeLocalFactoryNode(t.Context(), plan, node, options, runners); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "--plan-digest\n"+digest+"\n") {
		t.Fatalf("helper lacks exact Plan binding: %s", raw)
	}
	written, err := LoadPlanContext(t.Context(), filepath.Join(options.Workspace, localPlanPath))
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyPlanDigest(written, digest); err != nil {
		t.Fatalf("child argv does not bind written Plan: %v", err)
	}
	assertFactoryErrorCode(t, VerifyPlanDigest(written, "sha256:"+strings.Repeat("f", 64)), e.CodePipelineDrift)
}

func TestMixedLocalTerminalObservationUsesRelocatedHostBytes(t *testing.T) {
	for _, forged := range []bool{false, true} {
		t.Run(map[bool]string{false: "exact", true: "forged producer"}[forged], func(t *testing.T) {
			t.Chdir(t.TempDir())
			plan := mixedLocalUtilityPlan(t)
			options := withLocalDefaults(LocalRunOptions{Workspace: ".nvkr/test", Runtime: "missing-runtime"})
			if err := os.Rename(".nvkr/factory", options.Workspace); err != nil {
				t.Fatal(err)
			}
			relocated, err := relocateFactoryPlan(plan, options.Workspace)
			if err != nil {
				t.Fatal(err)
			}
			_, artifacts := planIndex(relocated)
			node := planNode(plan, "worker/terminal-observe")
			if forged {
				result, err := readProducerResult(t.Context(), artifacts["result/worker/worker"].Path)
				if err != nil {
					t.Fatal(err)
				}
				result.Size++
				writeAcquiredImageResult(t, artifacts["result/worker/worker"].Path, result)
			}
			err = executeLocalFactoryNode(t.Context(), plan, node, options, nil)
			output := artifacts[node.Outputs[0]].Path
			if forged {
				assertFactoryErrorCode(t, err, e.CodeVerifyFailed)
				if _, err := os.Stat(output); !os.IsNotExist(err) {
					t.Fatalf("forged producer exposed observation: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(output)
			if err != nil {
				t.Fatal(err)
			}
			var observed factoryObservation
			if err := canonical.UnmarshalExact([]byte(strings.TrimSpace(string(raw))), &observed); err != nil {
				t.Fatal(err)
			}
			if observed.BuildTarget != "worker" || observed.Qualification != TierStaticUnqualified || observed.BuildDigest == "" {
				t.Fatalf("unexpected host observation binding: %#v", observed)
			}
		})
	}
}
