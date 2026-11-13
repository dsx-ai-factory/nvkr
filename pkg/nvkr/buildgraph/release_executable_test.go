// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package buildgraph

import (
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

// These transport subjects are the source-owned operator selections at NVKE
// ff21ba8c07cafff74b68e238a8876af11e5beded, kubeadm-distro's AMD64 v1.36.3 lock.
// This freezes membership and logical pins, not publication or measured runtime.
func TestMaintainedOperatorInputMembership(t *testing.T) {
	const inputRoot = "../../../inputs/definitions/nvke"
	authored, err := definition.LoadDefinition(t.Context(), filepath.Join(inputRoot, "definition.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, output := range authored.Outputs {
		if output.Name != "operator-tools" {
			continue
		}
		found++
		if output.Platform != "linux/amd64" || output.Format != definition.OutputExecutable ||
			output.Catalog == nil || output.Catalog.Base != "../../catalogs/tools" || !slices.Equal(output.Components, []string{"helm-operator", "kubectl-operator"}) {
			t.Fatalf("operator output differs from its explicit formal-v1 selection: %+v", output)
		}
	}
	if found != 1 {
		t.Fatalf("got %d operator outputs, want one explicit native selection", found)
	}
	cat, err := catalog.LoadContext(t.Context(), catalog.Dir(filepath.Join(inputRoot, "../../catalogs/tools")))
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ component, version, url, format, member, transport string }{
		{"helm-operator", "v4.1.4", "https://get.helm.sh/helm-v4.1.4-linux-amd64.tar.gz", "tar-gzip", "linux-amd64/helm", "sha256:70b2c30a19da4db264dfd68c8a3664e05093a361cefd89572ffb36f8abfa3d09"},
		{"kubectl-operator", "v1.36.3", "https://dl.k8s.io/release/v1.36.3/bin/linux/amd64/kubectl", "raw", "kubectl", "sha256:ebbd080e7c2e275093b55915722043257eb24004363e20acb3c4d71919f88336"},
	} {
		t.Run(test.component, func(t *testing.T) {
			component, exists := cat.Components[test.component]
			if !exists {
				t.Fatal("missing operator producer")
			}
			component, err = component.ForPlatform("linux/amd64")
			if err != nil {
				t.Fatal(err)
			}
			if component.FromSource || component.Prebuilt == nil || component.Prebuilt.Executable == nil {
				t.Fatal("operator input is not a source-pinned executable acquisition")
			}
			pin := component.Prebuilt.Executable
			if component.Tag != test.version || pin.Platform != "linux/amd64" || pin.URL != test.url ||
				pin.Format != test.format || pin.ExecutablePath != test.member || pin.ArchiveDigest != test.transport {
				t.Fatalf("operator input differs from selected source-owned release: %+v, %+v", component, pin)
			}
		})
	}
}

func TestMaintainedOperatorExecutableGraph(t *testing.T) {
	cat, err := catalog.LoadContext(t.Context(), catalog.Dir("../../../inputs/catalogs/tools"))
	if err != nil {
		t.Fatal(err)
	}
	selection := Selection{Components: []string{"helm-operator", "kubectl-operator"}, Platform: "linux/amd64", OutputFormat: definition.OutputExecutable}
	resolved, err := Resolve(cat, selection)
	if err != nil {
		t.Fatal(err)
	}
	cat.Components["helm-operator"].Prebuilt.Executables[0].ExecutablePath = "mutated"
	if resolved.Components[0].Prebuilt.Executable.ExecutablePath != "linux-amd64/helm" {
		t.Fatal("resolved output aliases source tool pin")
	}
	graph, err := Compile(resolved)
	if err != nil {
		t.Fatal(err)
	}
	if len(graph.Nodes) != 3 {
		t.Fatalf("got %d nodes; want two acquisitions and terminal verification, no source build", len(graph.Nodes))
	}
	for _, node := range graph.Nodes {
		if node.Kind == ir.KindVerify {
			continue
		}
		component := resolved.Components[0]
		if node.Component == "kubectl-operator" {
			component = resolved.Components[1]
		}
		pin := component.Prebuilt.Executable
		if node.Kind != ir.KindPull || node.Digest != pin.ExecutableDigest {
			t.Fatalf("wrong executable subject: %+v", node)
		}
		if pin.Format == "raw" {
			if node.ExecutableArchive != nil {
				t.Fatal("raw release has an invented archive projection")
			}
		} else if node.ExecutableArchive == nil || node.ExecutableArchive.Digest != pin.ArchiveDigest ||
			node.ExecutableArchive.Member != pin.ExecutablePath {
			t.Fatal("archive transport/member omitted from graph")
		}
	}
}

func TestMultiPlatformExecutablePreservesNativeGraph(t *testing.T) {
	platforms := []string{"darwin/arm64", "linux/amd64", "linux/arm64"}
	pins := make([]catalog.ToolPin, 0, len(platforms))
	for _, platform := range platforms {
		pins = append(pins, catalog.ToolPin{Format: "tar-gzip", URL: "https://example.test/" + platform + "/tool.tar.gz", Platform: platform, ExecutablePath: "bin/tool", ArchiveDigest: "sha256:" + strings.Repeat("a", 64), ExecutableDigest: "sha256:" + strings.Repeat("b", 64)})
	}
	for _, pin := range pins {
		t.Run(pin.Platform, func(t *testing.T) {
			_, arch, _ := strings.Cut(pin.Platform, "/")
			native := catalog.Component{Platforms: []string{pin.Platform}, OutputType: "host-package", Tag: "v1.2.3", SourceDate: 1, Prebuilt: &catalog.Prebuilt{URL: pin.URL, Digests: map[string]string{arch: pin.ExecutableDigest}, Executable: &pin, Evidence: &catalog.ExternalEvidence{None: true}}}
			multi := native
			multi.Platforms = platforms
			multi.Prebuilt = &catalog.Prebuilt{Executables: pins, Evidence: &catalog.ExternalEvidence{None: true}}
			selection := Selection{Components: []string{"tool"}, Platform: pin.Platform, OutputFormat: definition.OutputExecutable}
			compile := func(component catalog.Component) *ir.Graph {
				t.Helper()
				resolved, err := Resolve(&catalog.Catalog{Components: map[string]catalog.Component{"tool": component}}, selection)
				if err != nil {
					t.Fatal(err)
				}
				return mustCompile(t, resolved)
			}
			before, after := compile(native), compile(multi)
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("platform projection changed native graph: before=%+v after=%+v", before, after)
			}
		})
	}
}
