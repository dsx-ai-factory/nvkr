// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package buildgraph_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/buildgraph"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

func TestNightlyDOCAClosurePreservesTaggedInputs(t *testing.T) {
	const overlayName = "current-main-doca-ovs.json"
	for _, test := range []struct {
		file string
		want int
	}{
		{"demo/arm64-nvidia-kvm.json", 1},
		{"demo/arm64-nvidia-local-kvm.json", 0},
		{"demo/amd64-nvidia-local-kvm.json", 0},
		{"demo/arm64-nvidia-local-kvm-conformance.json", 0},
		{"nvke/definition.yaml", 0},
	} {
		t.Run(test.file, func(t *testing.T) {
			d, err := definition.LoadDefinition(t.Context(), filepath.Join("../../../inputs/definitions", test.file))
			if err != nil {
				t.Fatal(err)
			}
			found := 0
			for _, output := range d.Outputs {
				if output.Catalog == nil {
					continue
				}
				for _, path := range output.Catalog.Overlays {
					if filepath.Base(path) != overlayName {
						continue
					}
					found++
					if output.Name != "nvke-node-image-arm64" || output.Format != definition.OutputDiskImage {
						t.Fatal("current-main DOCA adaptation escaped its nightly disk output")
					}
				}
			}
			if found != test.want {
				t.Fatalf("DOCA overlay uses=%d, want %d", found, test.want)
			}
		})
	}
}

func TestNightlyDOCAAcquisitionFeedsOfflineImageComposition(t *testing.T) {
	base, err := catalog.LoadContext(t.Context(), catalog.Dir("../../../inputs/catalogs/node-images/release"))
	if err != nil {
		t.Fatal(err)
	}
	var overlays []catalog.Overlay
	for _, name := range []string{"native-nvidia.json", "native-local-kvm.json", "current-main-doca-ovs.json"} {
		overlay, err := catalog.LoadOverlayContext(t.Context(), filepath.Join("../../../inputs/catalogs/node-images/variants", name))
		if err != nil {
			t.Fatal(err)
		}
		overlays = append(overlays, overlay)
	}
	prior, err := catalog.ApplyStack(base, overlays[:2]...)
	if err != nil {
		t.Fatal(err)
	}
	stack, err := catalog.ApplyStack(base, overlays...)
	if err != nil {
		t.Fatal(err)
	}
	if len(stack.Catalog.Components) != len(prior.Catalog.Components) {
		t.Fatal("DOCA adaptation changed logical producer membership")
	}
	for name, component := range prior.Catalog.Components {
		if name != "nvke-image-inputs" && !reflect.DeepEqual(component, stack.Catalog.Components[name]) {
			t.Fatalf("DOCA adaptation changed unrelated producer %s", name)
		}
	}
	producer := stack.Catalog.Components["nvke-image-inputs"]
	if !producer.FromSource || producer.Tag != producer.Commit || !reflect.DeepEqual(producer.Materials, prior.Catalog.Components["nvke-image-inputs"].Materials) {
		t.Fatal("current-main recipe lost its exact source descriptor or original material closure")
	}
	staging, _, found := strings.Cut(prior.Catalog.Components["nvke-image-inputs"].BuildCommand[2], "python3 ")
	if !found || !strings.Contains(staging, "dhcp-identifier: mac") || !strings.HasPrefix(producer.BuildCommand[2], staging) ||
		!strings.Contains(producer.BuildCommand[2], `--source-files-dir "$files"`) {
		t.Fatal("DOCA composition dropped the native recipe's private MAC DHCP staging")
	}
	root := filepath.Join(t.TempDir(), "source with spaces")
	sourceFiles := filepath.Join(root, "os-image", "files")
	if err := os.MkdirAll(sourceFiles, 0700); err != nil {
		t.Fatal(err)
	}
	original := []byte("source-owned netplan without the local override\n")
	for name, body := range map[string][]byte{"00-dhcp-all.yaml": original, "retained-input": []byte("other source file\n")} {
		if err := os.WriteFile(filepath.Join(sourceFiles, name), body, 0600); err != nil {
			t.Fatal(err)
		}
	}
	// Execute the unchanged native staging prefix from the replacement command.
	// Only its absolute source/output root moves into this private fixture.
	command := strings.ReplaceAll(producer.BuildCommand[2][:len(staging)], "/src/", `"${NVKR_DOCA_FIXTURE_ROOT}/"`)
	cmd := exec.CommandContext(t.Context(), "bash", "-ec", command)
	cmd.Env = append(os.Environ(), "NVKR_DOCA_FIXTURE_ROOT="+root)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("private image-file staging: %v: %s", err, output)
	}
	stagedFiles := filepath.Join(root, "nvkr-output", "native-image-files")
	if raw, err := os.ReadFile(filepath.Join(stagedFiles, "00-dhcp-all.yaml")); err != nil || !strings.Contains(string(raw), "dhcp-identifier: mac\n") {
		t.Fatal("staged netplan omits MAC DHCP identity", err)
	}
	if raw, err := os.ReadFile(filepath.Join(stagedFiles, "retained-input")); err != nil || string(raw) != "other source file\n" {
		t.Fatal("private staging lost another source-owned image file", err)
	}
	if raw, err := os.ReadFile(filepath.Join(sourceFiles, "00-dhcp-all.yaml")); err != nil || string(raw) != string(original) {
		t.Fatal("private staging changed the pinned source's netplan bytes", err)
	}
	vendor := strings.Join(producer.VendorCommand, "\n")
	if !strings.Contains(vendor, "make -C os-image/packages/doca-ovs debs") || strings.Contains(vendor, "DOCA_VERSION=") || strings.Contains(vendor, "https://") {
		t.Fatal("DOCA acquisition must use the source-owned default version and lock")
	}
	for _, command := range [][]string{producer.VendorCommand, producer.BuildCommand} {
		if err := exec.CommandContext(t.Context(), "bash", "-n", "-c", command[2]).Run(); err != nil {
			t.Fatal("DOCA recipe shell syntax", err)
		}
	}
	for _, arch := range []string{"amd64", "arm64"} {
		t.Run(arch, func(t *testing.T) {
			selected, err := buildgraph.Resolve(stack.Catalog, buildgraph.Selection{Platform: "linux/" + arch, Components: []string{
				"nvke-agent", "nvke-cni-calico", "nvke-cni-ovn", "nvke-image-inputs", "nvke-kubeadm-distro", "nvke-spire-agent", "ovn-kube-ubuntu", "ovn-kubernetes-chart",
			}})
			if err != nil {
				t.Fatal(err)
			}
			graph, err := buildgraph.Compile(selected)
			if err != nil {
				t.Fatal(err)
			}
			acquired, composed := false, false
			for _, node := range graph.Nodes {
				if node.Kind == ir.KindFetch && strings.Contains(strings.Join(node.VendorCmd, "\n"), "doca-ovs debs") {
					acquired = true
					if node.Commit != producer.Commit || node.VendorSpecDigest == "" || node.Network != ir.NetworkDefault || !strings.Contains(strings.Join(node.VendorCmd, "\n"), "ARCH="+arch) {
						t.Fatal("DOCA acquisition lost exact source, vendoring identity or native architecture")
					}
				}
				if node.Kind == ir.KindBuild && node.Component == "nvke-image-inputs" {
					composed = true
					if node.SourceCommit != producer.Commit || node.Network != ir.NetworkNone || len(node.Materials) != len(producer.Materials) || !strings.Contains(strings.Join(node.Command, "\n"), "--architecture "+arch) {
						t.Fatal("image composition lost its offline material/source binding")
					}
				}
			}
			if !acquired || !composed {
				t.Fatal("DOCA acquisition or offline image composition missing from graph")
			}
		})
	}
}
