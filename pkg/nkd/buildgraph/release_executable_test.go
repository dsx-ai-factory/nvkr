package buildgraph

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/catalog"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/definition"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
)

// These transport subjects are the source-owned operator selections at NKE
// b30117c25b2dcdf6c16021dae88a2eafdafcae61, kubeadm-distro's AMD64 v1.36.3 lock.
// This freezes membership and logical pins, not publication or measured runtime.
func TestMaintainedOperatorInputMembership(t *testing.T) {
	const inputRoot = "../../../definitions/nke-m2"
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
			output.Catalog == nil || output.Catalog.Base != "./catalog" || !slices.Equal(output.Components, []string{"helm", "kubectl"}) {
			t.Fatalf("operator output differs from its explicit formal-v1 selection: %+v", output)
		}
	}
	if found != 1 {
		t.Fatalf("got %d operator outputs, want one explicit native selection", found)
	}
	cat, err := catalog.LoadContext(t.Context(), catalog.Dir(filepath.Join(inputRoot, "catalog")))
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ component, version, url, format, member, transport string }{
		{"helm", "v4.1.4", "https://get.helm.sh/helm-v4.1.4-linux-amd64.tar.gz", "tar-gzip", "linux-amd64/helm", "sha256:70b2c30a19da4db264dfd68c8a3664e05093a361cefd89572ffb36f8abfa3d09"},
		{"kubectl", "v1.36.3", "https://dl.k8s.io/release/v1.36.3/bin/linux/amd64/kubectl", "raw", "kubectl", "sha256:ebbd080e7c2e275093b55915722043257eb24004363e20acb3c4d71919f88336"},
	} {
		t.Run(test.component, func(t *testing.T) {
			component, exists := cat.Components[test.component]
			if !exists || component.FromSource || component.Prebuilt == nil || component.Prebuilt.Executable == nil {
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
	cat, err := catalog.LoadContext(t.Context(), catalog.Dir("../../../definitions/nke-m2/catalog"))
	if err != nil {
		t.Fatal(err)
	}
	selection := Selection{Components: []string{"helm", "kubectl"}, Platform: "linux/amd64", OutputFormat: definition.OutputExecutable}
	resolved, err := Resolve(cat, selection)
	if err != nil {
		t.Fatal(err)
	}
	cat.Components["helm"].Prebuilt.Executable.ExecutablePath = "mutated"
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
		if node.Component == "kubectl" {
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
