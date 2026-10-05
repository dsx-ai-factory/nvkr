package factory

import (
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/buildgraph"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/catalog"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/definition"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
)

func TestReleaseExecutableResolvedDependencies(t *testing.T) {
	cat, err := catalog.LoadContext(t.Context(), catalog.Dir(filepath.Join("../../../definitions/nke-m2", "catalog")))
	if err != nil {
		t.Fatal(err)
	}
	for _, component := range []string{"helm", "kubectl"} {
		t.Run(component, func(t *testing.T) {
			resolved, err := buildgraph.Resolve(cat, buildgraph.Selection{Components: []string{component}, Platform: "linux/amd64", OutputFormat: definition.OutputExecutable})
			if err != nil {
				t.Fatal(err)
			}
			graph, err := buildgraph.Compile(resolved)
			if err != nil {
				t.Fatal(err)
			}
			dependencies := resolvedDependencies(&Plan{Builds: []BuildPlan{{Graph: graph}}})
			for _, node := range graph.Nodes {
				if node.Kind != ir.KindPull {
					continue
				}
				want := node.Digest
				if node.ExecutableArchive != nil {
					want = node.ExecutableArchive.Digest
				}
				found, member := false, false
				for _, dependency := range dependencies {
					if dependency.URI == node.Artifact {
						found = true
						if dependency.Digest["sha256"] != strings.TrimPrefix(want, "sha256:") {
							t.Errorf("transport URI %s has executable digest, want archive %s", dependency.URI, want)
						}
					}
					if strings.HasPrefix(dependency.URI, "nkd:prebuilt/") && dependency.Digest["sha256"] == strings.TrimPrefix(node.Digest, "sha256:") {
						binding, err := url.Parse(dependency.URI)
						if err != nil {
							t.Fatal(err)
						}
						member = node.ExecutableArchive != nil && binding.Opaque == "prebuilt/"+component &&
							binding.Query().Get("archive") == node.Artifact && binding.Query().Get("member") == node.ExecutableArchive.Member
					}
				}
				if !found || (node.ExecutableArchive != nil && !member) {
					t.Fatalf("missing transport or selected member binding: %#v", dependencies)
				}
			}
		})
	}
}
