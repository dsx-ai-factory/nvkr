// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package planview

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/catalog"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/definition"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/execution"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/factory"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/material"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/pipeline"
)

const inspectionControlText = "visible 東\x1b[2J\x07\t\n\u009b\u202e\u2066\U000e0001end"

// Compile complete Plans from valid catalog/Definition/Environment inputs. The
// fields under review must survive ordinary compiler and full-Plan admission,
// rather than being injected into an otherwise stale executable graph.
func richInspectionPlan(t *testing.T, kind string) *factory.Plan {
	t.Helper()
	return comparisonPlan(t, func(cat *catalog.Catalog, def *definition.Definition, env *definition.Environment) {
		def.Outputs = def.Outputs[:1]
		component := cat.Components["containerd"]
		component.BuildCommand = []string{"make", "all", inspectionControlText}
		component.VendorCommand = []string{"make", "vendor", "ARCH={{.Arch}}", inspectionControlText}
		component.VersionStamp = map[string]string{"NKECTL_VERSION": inspectionControlText}
		for _, name := range []string{"z-first.patch", "a-second.patch"} {
			content := "--- a/version\n+++ b/version\n@@ -1 +1 @@\n-old\n+" + name + inspectionControlText + "\n"
			component.Patches = append(component.Patches, catalog.Patch{Name: name, Content: content, SHA256: pipeline.FileDigest([]byte(content))})
		}
		acquired := catalog.Component{
			Platforms: []string{"linux/amd64"}, OutputType: "host-package", Tag: "v2.3.4", SourceDate: 1700000000,
			Prebuilt: &catalog.Prebuilt{URL: "https://example.test/agent-amd64.deb", Digests: map[string]string{"amd64": "sha256:" + strings.Repeat("f", 64)}, Evidence: &catalog.ExternalEvidence{None: true}},
		}
		switch kind {
		case "oci":
		case "nix":
			component.BuilderType = ir.BuilderNix
			component.Flake = &catalog.FlakePin{Ref: "github:example/tool?rev=" + strings.Repeat("a", 40), Output: "packages.x86_64-linux.default", LockDigest: "sha256:" + strings.Repeat("b", 64), Image: "nixos/nix@sha256:" + strings.Repeat("c", 64)}
		case "acquired":
			component = acquired
		case "vm-command", "vm-packer":
			component.BuilderType = catalog.BuilderVMImage
			component.OutputType = definition.OutputDiskImage
			component.Disk = &catalog.DiskSpec{Format: "qcow2", Encryption: "none"}
			component.ToolchainImage = ""
			component.ToolchainImages = map[string]string{"amd64": "example.test/vm@sha256:" + strings.Repeat("c", 64)}
			component.WriteDir = ""
			component.VersionStamp = nil
			component.ArtifactPath = "out/node-amd64.qcow2"
			component.BuildRequirements = &execution.Requirements{
				Resources:            &execution.ResourceRequest{CPUMillis: 2000, MemoryMiB: 4096, EphemeralStorageMiB: 8192},
				AllowedHostPlatforms: []execution.HostPlatform{execution.HostPlatformLinuxAMD64},
				Capabilities:         []execution.CapabilityRequirement{{Name: execution.CapabilityKVM, Policy: execution.CapabilityPreferred}},
				TimeoutSeconds:       600,
			}
			env.ExecutionClasses = []execution.Class{{Name: "vm", RunsOn: "ubuntu-24.04", HostPlatform: execution.HostPlatformLinuxAMD64, Resources: execution.ResourceCapacity{CPUMillis: 4000, MemoryMiB: 8192, EphemeralStorageMiB: 16384}, MaxTimeoutSeconds: 900}}
			def.Outputs[0].Format = definition.OutputDiskImage
			if kind == "vm-packer" {
				component.BuildCommand = nil
				component.Materials = map[string]string{"agent": "crictl"}
				component.Packer = &catalog.PackerConfig{
					ProjectPath: "os-image/packer", PackerVersion: "1.14.2", BuildName: "image.qemu.node",
					ArchitectureVariable: "architecture", AcceleratorVariable: "accelerator",
					MaterialManifestVariable: "material_manifest", OutputDirectoryVariable: "output_directory",
					MaterialManifest: &material.Projection{SchemaVersion: "example.test/materials/v1", Kinds: map[string]string{"agent": "package"}},
					Variables:        map[string]string{"z_value": "last", "a_value": "first"},
				}
				cat.Components["crictl"] = acquired
				def.Outputs[0].Components = []string{"containerd", "crictl"}
			}
		default:
			t.Fatalf("unknown fixture kind %q", kind)
		}
		cat.Components["containerd"] = component
	})
}

func TestInspectionTextRetainsEverySerializedProducerField(t *testing.T) {
	for _, test := range []struct {
		kind   string
		fields []string
	}{
		{"oci", []string{"patches", "env", "workdir", "sourceDest", "sourceCommit", "sourceTreeHash", "sourceReadOnly", "writeDir", "outputType", "artifactPath", "version", "dest", "vendorImage", "vendorSpecDigest", "vendorCmd", "command"}},
		{"nix", []string{"flakeRef", "flakeOutput", "flakeLockDigest"}},
		{"acquired", []string{"artifact", "digest", "component", "env", "platform", "outputType", "artifactPath", "version"}},
		{"vm-command", []string{"disk", "toolchainPlatform", "requirements", "executionBinding", "command"}},
		{"vm-packer", []string{"disk", "packer", "materials", "toolchainPlatform", "requirements", "executionBinding"}},
	} {
		t.Run(test.kind, func(t *testing.T) {
			plan := richInspectionPlan(t, test.kind)
			doc := inspectPlan(t, plan)
			text, err := doc.Render(PlanOptions{Format: "text"})
			if err != nil {
				t.Fatal(err)
			}
			again, err := doc.Render(PlanOptions{Format: "text"})
			if err != nil || !bytes.Equal(text, again) {
				t.Fatalf("unstable text: %v", err)
			}
			for _, r := range string(text) {
				if r != '\n' && !strconv.IsPrint(r) {
					t.Fatalf("text contains terminal control %U", r)
				}
			}
			_, section, ok := strings.Cut(string(text), "Producer instructions\n")
			if !ok {
				t.Fatal("missing producer instructions")
			}
			section, _, ok = strings.Cut(section, "\nArtifact operations\n")
			if !ok {
				t.Fatal("missing artifact operations")
			}
			seen := map[string]bool{}
			for _, node := range plan.Builds[0].Graph.Nodes {
				fields := inspectionTextFields(t, section, node)
				original, err := json.Marshal(node)
				if err != nil {
					t.Fatal(err)
				}
				var want map[string]json.RawMessage
				if err := json.Unmarshal(original, &want); err != nil {
					t.Fatal(err)
				}
				for _, key := range []string{"id", "kind", "needs"} {
					delete(want, key)
				}
				gotRaw, err := canonical.Marshal(fields)
				if err != nil {
					t.Fatal(err)
				}
				wantRaw, err := canonical.Marshal(want)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(gotRaw, wantRaw) {
					t.Fatalf("producer %s lost fields or changed values/order:\ngot %s\nwant %s", node.ID, gotRaw, wantRaw)
				}
				for key := range fields {
					seen[key] = true
				}
			}
			for _, field := range test.fields {
				if !seen[field] {
					t.Fatalf("valid fixture did not exercise %s", field)
				}
			}
			assertInspectionSnapshotDetached(t, plan, doc, text)
		})
	}
}

func inspectionTextFields(t *testing.T, section string, node ir.Node) map[string]json.RawMessage {
	t.Helper()
	_, block, ok := strings.Cut(section, fmt.Sprintf("    %s %s\n", node.Kind, node.ID))
	if !ok {
		t.Fatalf("missing producer %s", node.ID)
	}
	fields := map[string]json.RawMessage{}
	var order []string
	for _, line := range strings.Split(block, "\n") {
		if !strings.HasPrefix(line, "      ") {
			break
		}
		key, value, ok := strings.Cut(strings.TrimPrefix(line, "      "), ": ")
		if !ok {
			t.Fatalf("invalid producer field line: %q", line)
		}
		if key == "requires" {
			if value != strings.Join(node.Needs, ", ") {
				t.Fatalf("changed producer dependencies: %q", value)
			}
			continue
		}
		key = strings.TrimSuffix(key, " argv")
		if _, duplicate := fields[key]; duplicate {
			t.Fatalf("duplicate producer field %s", key)
		}
		if !json.Valid([]byte(value)) {
			t.Fatalf("field %s is not lossless JSON: %q", key, value)
		}
		fields[key] = json.RawMessage(value)
		order = append(order, key)
	}
	if !slices.IsSorted(order) {
		t.Fatalf("producer fields are not ordered: %v", order)
	}
	return fields
}

func assertInspectionSnapshotDetached(t *testing.T, plan *factory.Plan, doc *Document, text []byte) {
	t.Helper()
	original, err := canonical.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "plan.json")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	for _, document := range []*Document{doc, loaded} {
		raw, err := document.Render(PlanOptions{Format: "json"})
		if err != nil || !bytes.Equal(raw, original) {
			t.Fatalf("canonical bytes changed: %v", err)
		}
		loadedText, err := document.Render(PlanOptions{Format: "text"})
		if err != nil || !bytes.Equal(loadedText, text) {
			t.Fatalf("load/inspect text mismatch: %v", err)
		}
	}
	for index := range plan.Builds[0].Graph.Nodes {
		node := &plan.Builds[0].Graph.Nodes[index]
		if len(node.Command) > 0 {
			node.Command[0] = "substituted"
		}
		if len(node.Env) > 0 {
			node.Env[0].Value = "substituted"
		}
		if len(node.Patches) > 0 {
			node.Patches[0].Content = "substituted"
		}
		if node.Disk != nil {
			node.Disk.Format = "substituted"
		}
		if node.Packer != nil {
			node.Packer.MaterialManifest.Kinds["agent"] = "substituted"
		}
		if node.Requirements != nil {
			node.Requirements.TimeoutSeconds++
		}
		if node.ExecutionBinding != nil {
			node.ExecutionBinding.Class.RunsOn = "substituted"
		}
	}
	still, err := doc.Render(PlanOptions{Format: "text"})
	if err != nil || !bytes.Equal(still, text) {
		t.Fatalf("caller mutated retained typed view: %v", err)
	}
}
