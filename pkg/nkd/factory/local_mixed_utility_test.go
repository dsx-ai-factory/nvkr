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

	"github.com/NVIDIA/nke-recipes/pkg/nkd/catalog"
	authored "github.com/NVIDIA/nke-recipes/pkg/nkd/definition"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/delivery"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/execution"
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
	plan, err := Compile(t.Context(), d, env, stack, "test")
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
			t.Chdir(t.TempDir())
			plan := mixedLocalUtilityPlan(t)
			assertFactoryErrorCode(t, ExecuteNode(t.Context(), plan, tc.node, nil, nil), tc.code)
			if _, err := os.Stat(".nkd/factory/node-image"); !os.IsNotExist(err) {
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
	t.Setenv("NKD_TEST_RUNTIME_LOG", log)
	if err := os.WriteFile(runtime, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" >> \"$NKD_TEST_RUNTIME_LOG\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	node := planNode(plan, "worker/terminal-observe")
	options := withLocalDefaults(LocalRunOptions{Workspace: ".nkd/test", Runtime: runtime})
	if err := executeLocalFactoryNode(t.Context(), plan, node, options, map[string]string{"linux/amd64": "runner"}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "--plan-digest\n"+digest+"\n") {
		t.Fatalf("helper lacks exact Plan binding: %s", raw)
	}
}
