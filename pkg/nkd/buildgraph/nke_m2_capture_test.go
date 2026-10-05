package buildgraph

import (
	"path/filepath"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/catalog"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/definition"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/execution"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
)

func TestMaintainedM2NativeCaptureGraph(t *testing.T) {
	const inputRoot = "../../../definitions/nke-m2"
	authored, err := definition.LoadDefinition(t.Context(), filepath.Join(inputRoot, "definition.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	seen := make(map[string]bool)
	for _, output := range authored.Outputs {
		if output.Format != definition.OutputDiskImage {
			continue
		}
		if output.Platform != "linux/amd64" && output.Platform != "linux/arm64" {
			t.Fatalf("unsupported native disk platform %s", output.Platform)
		}
		if seen[output.Platform] {
			t.Fatalf("duplicate native disk platform %s", output.Platform)
		}
		seen[output.Platform] = true
		t.Run(output.Name, func(t *testing.T) {
			cat, err := catalog.LoadContext(t.Context(), catalog.Dir(filepath.Join(inputRoot, output.Catalog.Base)))
			if err != nil {
				t.Fatal(err)
			}
			selected, err := Resolve(cat, Selection{Components: output.Components, Platform: output.Platform, OutputFormat: output.Format})
			if err != nil {
				t.Fatal(err)
			}
			root := "nke-node-image"
			if output.Platform == "linux/amd64" {
				root += "-amd64"
			}
			graph, err := CompileForHostPlatforms(selected, map[string]execution.HostPlatform{root: execution.HostPlatform(output.Platform)})
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, node := range graph.Nodes {
				if node.Kind != ir.KindBuild || node.Component != root {
					continue
				}
				found = true
				if node.Platform != output.Platform || node.ToolchainPlatform != output.Platform || node.SourceCommit != "b30117c25b2dcdf6c16021dae88a2eafdafcae61" {
					t.Fatalf("native source/toolchain binding changed: %+v", node)
				}
				if node.Packer == nil || node.Packer.AcquisitionArchive != "non-fde-apt-acquisitions.tar" {
					t.Fatal("capture carrier did not reach compiled graph")
				}
				capture := false
				for _, variable := range node.Packer.Variables {
					if variable.Name == "capture_apt_acquisitions" && variable.Value == "true" {
						capture = true
					}
				}
				if !capture {
					t.Fatal("source capture opt-in did not reach Packer")
				}
				if node.Packer.MaterialManifest == nil || node.Packer.MaterialManifest.SchemaVersion != "nke.dev/os-image-materials/v1" {
					t.Fatal("source material projection is absent")
				}
				kinds := node.Packer.MaterialManifest.Kinds
				if len(kinds) != 2 || kinds["image-inputs"] != "archive" || kinds["installer-iso"] != "disk-image" {
					t.Fatalf("source material kinds changed: %+v", kinds)
				}
				if len(node.Materials) != 2 {
					t.Fatalf("disk material bindings changed: %+v", node.Materials)
				}
				for _, binding := range node.Materials {
					if _, ok := kinds[binding.Binding]; !ok {
						t.Fatalf("unprojected disk material %+v", binding)
					}
					matched := false
					for _, producer := range graph.Nodes {
						if producer.ID == binding.Producer {
							matched = true
							if producer.Component != binding.Component || producer.Platform != output.Platform {
								t.Fatalf("material is not bound to its native producer: %+v / %+v", binding, producer)
							}
						}
					}
					if !matched {
						t.Fatalf("material producer is absent: %+v", binding)
					}
				}
			}
			if !found {
				t.Fatal("disk producer missing from graph")
			}
		})
	}
	if len(seen) != 2 || !seen["linux/amd64"] || !seen["linux/arm64"] {
		t.Fatal("both native disk capture graphs are required")
	}
}
