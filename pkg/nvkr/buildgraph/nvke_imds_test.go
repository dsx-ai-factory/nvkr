// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package buildgraph_test

import (
	"archive/tar"
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/buildgraph"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/execution"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
)

const imdsAgent = "dsx-imds-node-attestor-agent"

func TestNVKEIMDSSharedProducer(t *testing.T) {
	standalone, err := catalog.LoadContext(t.Context(), catalog.Dir("../../../inputs/catalogs/nvidia-workload-identity"))
	if err != nil {
		t.Fatal(err)
	}
	nodes, err := catalog.LoadContext(t.Context(), catalog.Dir("../../../inputs/catalogs/node-images"))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(nodes.Components[imdsAgent], standalone.Components[imdsAgent]) {
		t.Fatal("disk material differs from the independently pinned WLI producer")
	}
	if got := nodes.Components["nvke-spire-agent"].Materials; !reflect.DeepEqual(got, map[string]string{"imds-agent": imdsAgent}) {
		t.Fatalf("package material = %v", got)
	}
	if nodes.Components["nvke-image-inputs"].Materials["spire-agent"] != "nvke-spire-agent" {
		t.Fatal("image inputs lost their package edge")
	}
	for _, source := range nodes.Sources {
		if strings.Contains(strings.Join(source.VendorCommand, "\n"), "/nvkr/inputs/materials/imds-agent") {
			t.Fatal("vendor preparation consumes a runtime material")
		}
	}
}

// Execute the catalog's actual package adapter against controlled source/package
// fixtures. This proves input forwarding and embedded-byte verification, not an
// NVKE package build, guest boot or SPIRE attestation.
func TestNVKEIMDSPackageAdapter(t *testing.T) {
	cat, err := catalog.LoadContext(t.Context(), catalog.Dir("../../../inputs/catalogs/node-images"))
	if err != nil {
		t.Fatal(err)
	}
	command, _, found := strings.Cut(cat.Components["nvke-spire-agent"].BuildCommand[2], "\ntar --sort=name")
	if !found {
		t.Fatal("package adapter lost its final deterministic archive step")
	}
	for _, arch := range []string{"amd64", "arm64"} {
		for _, variant := range []string{"included", "omitted", "substituted", "missing input"} {
			t.Run(arch+"/"+variant, func(t *testing.T) {
				root := t.TempDir()
				write := func(name string, data []byte, mode os.FileMode) {
					t.Helper()
					name = filepath.Join(root, name)
					if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(name, data, mode); err != nil {
						t.Fatal(err)
					}
				}
				materialBytes := []byte("controlled IMDS executable for " + arch)
				if variant != "missing input" {
					write("inputs/materials/imds-agent", materialBytes, 0400)
				}
				write("make/tool-versions.mk", nil, 0600)
				write("os-image/files/fixture", nil, 0600)
				write("os-image/packages/spire-agent/Makefile", []byte(".PHONY: bundle\nbundle:\n\t@test \"$(ARCH)\" = \"$(IMDS_AGENT_ARCH)\"\n\t@test \"$(IMDS_AGENT_SHA256)\" = \"$(EXPECTED_IMDS_SHA256)\"\n\t@test -f \"$(IMDS_AGENT_FILE)\"\n"), 0600)
				write("os-image/packages/spire-agent/packaging/build-deb.sh", []byte("#!/bin/sh\nset -eu\ntest \"$DEB_REVISION\" = 5\ntest \"$1\" = \"$IMDS_AGENT_ARCH\"\ntest \"$IMDS_AGENT_SHA256\" = \"$EXPECTED_IMDS_SHA256\"\nprintf fixture > \"$3/spire-agent.deb\"\n"), 0700)
				var archive bytes.Buffer
				writer := tar.NewWriter(&archive)
				if variant != "omitted" {
					payload := materialBytes
					if variant == "substituted" {
						payload = []byte("different package executable")
					}
					if err := writer.WriteHeader(&tar.Header{Name: "./usr/local/lib/spire-agent/" + imdsAgent, Mode: 0755, Size: int64(len(payload))}); err != nil {
						t.Fatal(err)
					}
					if _, err := writer.Write(payload); err != nil {
						t.Fatal(err)
					}
				}
				if err := writer.Close(); err != nil {
					t.Fatal(err)
				}
				write("package.tar", archive.Bytes(), 0600)
				write("tools/dpkg-deb", []byte("#!/bin/sh\nset -eu\ntest \"$1\" = --fsys-tarfile\ncat \"$PACKAGE_TAR\"\n"), 0700)
				script := strings.NewReplacer("/src", root, "/nvkr/inputs", filepath.Join(root, "inputs"), "{{.Arch}}", arch).Replace(command)
				cmd := exec.CommandContext(t.Context(), "bash", "-ec", script)
				cmd.Dir = root
				cmd.Env = append(os.Environ(), "PATH="+filepath.Join(root, "tools")+string(os.PathListSeparator)+os.Getenv("PATH"), "PACKAGE_TAR="+filepath.Join(root, "package.tar"), "EXPECTED_IMDS_SHA256="+strings.TrimPrefix(pipeline.FileDigest(materialBytes), "sha256:"))
				output, err := cmd.CombinedOutput()
				if (err == nil) != (variant == "included") {
					t.Fatalf("package adapter: %v\n%s", err, output)
				}
			})
		}
	}
}

// Exercise the real maintained selections for both architectures, plain/FDE and
// Ubuntu releases. Graph contracts do not establish package or attestation runs.
func TestNVKEIMDSDiskClosure(t *testing.T) {
	base, err := catalog.LoadContext(t.Context(), catalog.Dir("../../../inputs/catalogs/node-images"))
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{"disks.yaml", "disks-ubuntu24.yaml"} {
		doc, err := definition.LoadDefinition(t.Context(), filepath.Join("../../../inputs/definitions/nvke", file))
		if err != nil {
			t.Fatal(err)
		}
		for _, output := range doc.Outputs {
			t.Run(output.Name, func(t *testing.T) {
				var overlays []catalog.Overlay
				for _, relative := range output.Catalog.Overlays {
					overlay, err := catalog.LoadOverlayContext(t.Context(), filepath.Join("../../../inputs/definitions/nvke", relative))
					if err != nil {
						t.Fatal(err)
					}
					overlays = append(overlays, overlay)
				}
				stack, err := catalog.ApplyStack(base, overlays...)
				if err != nil {
					t.Fatal(err)
				}
				selection := buildgraph.Selection{Components: output.Components, Platform: output.Platform, OutputFormat: output.Format}
				resolved, err := buildgraph.Resolve(stack.Catalog, selection)
				if err != nil {
					t.Fatal(err)
				}
				root, err := buildgraph.DiskRootName(resolved.Components)
				if err != nil {
					t.Fatal(err)
				}
				graph, err := buildgraph.CompileForHostPlatforms(resolved, map[string]execution.HostPlatform{root: execution.HostPlatform(output.Platform)})
				if err != nil {
					t.Fatal(err)
				}
				var agent, pkg ir.Node
				count := 0
				for _, node := range graph.Nodes {
					if node.Kind == ir.KindBuild && node.Component == imdsAgent {
						agent = node
						count++
					}
					if node.Kind == ir.KindBuild && node.Component == "nvke-spire-agent" {
						pkg = node
					}
				}
				arch := strings.TrimPrefix(output.Platform, "linux/")
				if count != 1 || agent.Toolchain != base.Components[imdsAgent].ToolchainImages[arch] {
					t.Fatalf("expected one native WLI producer, count=%d toolchain=%s", count, agent.Toolchain)
				}
				if len(pkg.Materials) != 1 || pkg.Materials[0] != (ir.MaterialBinding{Binding: "imds-agent", Component: imdsAgent, Producer: agent.ID}) || !slices.Contains(pkg.Needs, agent.ID) {
					t.Fatalf("package lost its exact material edge: %+v", pkg.Materials)
				}
				selection.Components = slices.DeleteFunc(slices.Clone(selection.Components), func(name string) bool { return name == imdsAgent })
				if got, err := buildgraph.Resolve(stack.Catalog, selection); got != nil || wliCode(err) != e.CodeInvalidCatalog {
					t.Fatalf("omitted material closure = %v, %v", got, err)
				}
				component := stack.Catalog.Components[imdsAgent]
				component.Platforms = []string{"linux/riscv64"}
				stack.Catalog.Components[imdsAgent] = component
				selection.Components = output.Components
				if got, err := buildgraph.Resolve(stack.Catalog, selection); got != nil || wliCode(err) != e.CodeInvalidCatalog {
					t.Fatalf("wrong-platform material = %v, %v", got, err)
				}
			})
		}
	}
}
