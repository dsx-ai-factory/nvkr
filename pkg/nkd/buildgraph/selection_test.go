package buildgraph

import (
	"reflect"
	"strings"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/catalog"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/execution"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
)

func TestDirectSelectionRejectsInvalidRequestsWithoutPartialResult(t *testing.T) {
	for _, test := range []struct {
		name      string
		selection Selection
		code      e.Code
	}{
		{"missing target", Selection{Components: []string{"tool"}}, e.CodeInvalidCatalog},
		{"unsupported target", Selection{Components: []string{"tool"}, Platform: "linux/riscv64"}, e.CodeInvalidCatalog},
		{"missing producers", Selection{Platform: "linux/amd64"}, e.CodeInvalidCatalog},
		{"unknown producer", Selection{Components: []string{"tool", "unknown"}, Platform: "linux/amd64"}, e.CodeInvalidCatalog},
		{"duplicate producer", Selection{Components: []string{"tool", "tool"}, Platform: "linux/amd64"}, e.CodeInvalidCatalog},
		{"padded producer", Selection{Components: []string{" tool "}, Platform: "linux/amd64"}, e.CodeInvalidCatalog},
		{"unknown output format", Selection{Components: []string{"tool"}, Platform: "linux/amd64", OutputFormat: "disk-image "}, e.CodeInvalidCatalog},
		{"unknown profile", Selection{AICRProfile: "missing", Platform: "linux/amd64"}, e.CodeInvalidCatalog},
		{"AICR disk output", Selection{AICRProfile: "missing", Platform: "linux/amd64", OutputFormat: "disk-image"}, e.CodeInvalidCatalog},
		{"ambiguous producer kinds", Selection{Components: []string{"tool"}, AICRProfile: "missing", Platform: "linux/amd64"}, e.CodeInvalidCatalog},
	} {
		t.Run(test.name, func(t *testing.T) {
			resolved, err := Resolve(nixCatalog(ir.BuilderOCIHermetic), test.selection)
			if resolved != nil || code(err) != test.code {
				t.Fatalf("Resolve = %+v, %v; want no result and %s", resolved, err, test.code)
			}
		})
	}
	cat := nixCatalog(ir.BuilderOCIHermetic)
	component := cat.Components["tool"]
	component.Platforms = []string{"linux/amd64"}
	cat.Components["tool"] = component
	resolved, err := Resolve(cat, Selection{Components: []string{"tool"}, Platform: "linux/arm64"})
	if resolved != nil || code(err) != e.CodeEngineCapability {
		t.Fatalf("unsupported producer target = %+v, %v", resolved, err)
	}
}

func TestSelectedGraphIdentityIgnoresUnrelatedProducerAndUnusedTargets(t *testing.T) {
	cat := nixCatalog(ir.BuilderOCIHermetic)
	selection := Selection{Components: []string{"tool"}, Platform: "linux/amd64"}
	digest := func(cat *catalog.Catalog, selection Selection) string {
		t.Helper()
		resolved, err := Resolve(cat, selection)
		if err != nil {
			t.Fatal(err)
		}
		digest, err := mustCompile(t, resolved).Digest()
		if err != nil {
			t.Fatal(err)
		}
		return digest
	}
	original := digest(cat, selection)
	component := cat.Components["tool"]
	component.Platforms = []string{"linux/amd64"}
	cat.Components["tool"] = component
	cat.Components["unrelated"] = component
	if got := digest(cat, selection); got != original {
		t.Fatalf("unselected producer or unused targets changed identity: %s != %s", got, original)
	}
	selection.Components = []string{"unrelated", "tool"}
	withSecond := digest(cat, selection)
	if withSecond == original {
		t.Fatal("selected producer membership did not change build identity")
	}
	selection.Components = []string{"tool", "unrelated"}
	if got := digest(cat, selection); got != withSecond {
		t.Fatal("input selection order changed build identity")
	}
}

func TestResolvedInputsOwnTheirMemory(t *testing.T) {
	cat := nixCatalog(ir.BuilderOCIHermetic)
	resolved, err := Resolve(cat, Selection{Components: []string{"tool"}, Platform: "linux/amd64"})
	if err != nil {
		t.Fatal(err)
	}
	before := append([]string(nil), resolved.Components[0].BuildCommand...)
	component := cat.Components["tool"]
	component.BuildCommand[0] = "mutated"
	if !reflect.DeepEqual(before, resolved.Components[0].BuildCommand) {
		t.Fatal("mutating catalog changed resolved producer instructions")
	}
}

func materialSelectionCatalog() *catalog.Catalog {
	cat := nixCatalog(ir.BuilderOCIHermetic)
	image := testVMImageComponent([]execution.HostPlatform{execution.HostPlatformLinuxAMD64}, execution.CapabilityPreferred)
	image.Materials = map[string]string{"tool-package": "tool", "runtime-package": "runtime"}
	cat.Components["image"] = image
	cat.Components["runtime"] = prebuiltCatalog().Components["tool"]
	return cat
}

func TestResolveRequiresDeclaredMaterials(t *testing.T) {
	for _, test := range []struct {
		name       string
		components []string
		wantCode   e.Code
	}{
		{"missing source material", []string{"image", "runtime"}, e.CodeInvalidCatalog},
		{"missing prebuilt material", []string{"image", "tool"}, e.CodeInvalidCatalog},
		{"complete material set", []string{"tool", "image", "runtime"}, ""},
		{"unselected image adds no requirements", []string{"tool"}, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			resolved, err := Resolve(materialSelectionCatalog(), Selection{
				Components: test.components, Platform: "linux/amd64",
			})
			if test.wantCode != "" {
				if resolved != nil || code(err) != test.wantCode || !strings.Contains(err.Error(), "incomplete material set") {
					t.Fatalf("Resolve = %+v, %v; want no result and %s for an incomplete material set", resolved, err, test.wantCode)
				}
				return
			}
			if err != nil || resolved == nil || len(resolved.Components) != len(test.components) {
				t.Fatalf("Resolve = %+v, %v; want exactly the selected producers", resolved, err)
			}
		})
	}
}

func TestResolveDiskSelectionRequiresOneRootAndItsExactMaterialClosure(t *testing.T) {
	for _, test := range []struct {
		name       string
		mutate     func(*catalog.Catalog)
		components []string
		format     string
		wantCode   e.Code
		wantText   string
	}{
		{
			name:       "complete closure",
			components: []string{"image", "runtime", "tool"},
		},
		{
			name: "multi-level diamond closure",
			mutate: func(cat *catalog.Catalog) {
				runtime := testVMImageComponent([]execution.HostPlatform{execution.HostPlatformLinuxAMD64}, execution.CapabilityPreferred)
				runtime.Materials = map[string]string{"grandchild-package": "grandchild"}
				cat.Components["runtime"] = runtime
				cat.Components["grandchild"] = cat.Components["tool"]
				image := cat.Components["image"]
				image.Materials["grandchild-package"] = "grandchild"
				cat.Components["image"] = image
			},
			components: []string{"grandchild", "image", "runtime", "tool"},
		},
		{
			name: "missing transitive grandchild",
			mutate: func(cat *catalog.Catalog) {
				runtime := testVMImageComponent([]execution.HostPlatform{execution.HostPlatformLinuxAMD64}, execution.CapabilityPreferred)
				runtime.Materials = map[string]string{"grandchild-package": "grandchild"}
				cat.Components["runtime"] = runtime
				cat.Components["grandchild"] = cat.Components["tool"]
			},
			components: []string{"image", "runtime", "tool"},
			wantCode:   e.CodeInvalidCatalog,
			wantText:   "incomplete material set",
		},
		{
			name:       "disk root with executable format",
			components: []string{"image", "runtime", "tool"},
			format:     "executable",
			wantCode:   e.CodeInvalidCatalog,
			wantText:   "cannot select a disk-image component",
		},
		{
			name:       "no disk root",
			components: []string{"tool"},
			format:     "disk-image",
			wantCode:   e.CodeInvalidCatalog,
			wantText:   "selects no QCOW2 root",
		},
		{
			name: "multiple disk roots",
			mutate: func(cat *catalog.Catalog) {
				cat.Components["second-image"] = cat.Components["image"]
			},
			components: []string{"image", "runtime", "second-image", "tool"},
			wantCode:   e.CodeInvalidCatalog,
			wantText:   "selects multiple disk roots",
		},
		{
			name: "unrelated component",
			mutate: func(cat *catalog.Catalog) {
				cat.Components["unrelated"] = cat.Components["tool"]
			},
			components: []string{"image", "runtime", "tool", "unrelated"},
			wantCode:   e.CodeInvalidCatalog,
			wantText:   "outside root \"image\" material closure: unrelated",
		},
		{
			name: "multiple unrelated components are reported canonically",
			mutate: func(cat *catalog.Catalog) {
				cat.Components["zeta"] = cat.Components["tool"]
				cat.Components["alpha"] = cat.Components["tool"]
			},
			components: []string{"zeta", "tool", "runtime", "image", "alpha"},
			wantCode:   e.CodeInvalidCatalog,
			wantText:   "outside root \"image\" material closure: alpha, zeta",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			cat := materialSelectionCatalog()
			if test.mutate != nil {
				test.mutate(cat)
			}
			resolved, err := Resolve(cat, Selection{
				Components:   test.components,
				Platform:     "linux/amd64",
				OutputFormat: test.format,
			})
			if test.wantCode == "" {
				if err != nil || resolved == nil {
					t.Fatalf("Resolve = %+v, %v; want a resolved disk selection", resolved, err)
				}
				return
			}
			if resolved != nil || code(err) != test.wantCode || !strings.Contains(err.Error(), test.wantText) {
				t.Fatalf("Resolve = %+v, %v; want no result, %s, and %q", resolved, err, test.wantCode, test.wantText)
			}
		})
	}
}

func TestResolvedVMInputsOwnTheirMemory(t *testing.T) {
	for _, direction := range []string{"catalog mutation", "resolved mutation"} {
		t.Run(direction, func(t *testing.T) {
			cat := materialSelectionCatalog()
			image := cat.Components["image"]
			image.BuildCommand = nil
			image.Packer = &catalog.PackerConfig{
				ProjectPath: "packer", PackerVersion: "1.14.2", BuildName: "qemu.image",
				ArchitectureVariable: "architecture", AcceleratorVariable: "accelerator", MaterialManifestVariable: "material_manifest",
				OutputDirectoryVariable: "output_directory", Variables: map[string]string{"image_variant": "standard"},
			}
			cat.Components["image"] = image
			resolved, err := Resolve(cat, Selection{
				Components: []string{"tool", "image", "runtime"}, Platform: "linux/amd64",
			})
			if err != nil {
				t.Fatal(err)
			}
			original, changed := resolved.Components[0].Component, image
			if direction == "resolved mutation" {
				original, changed = changed, original
			}
			changed.Materials["tool-package"] = "runtime"
			changed.Packer.Variables["image_variant"] = "mutated"
			if original.Materials["tool-package"] != "tool" || original.Packer.Variables["image_variant"] != "standard" {
				t.Fatal("catalog and resolved VM producer share material or Packer-variable maps")
			}
		})
	}
}

func TestResolvePreservesInvalidBytesForGraphRejection(t *testing.T) {
	cat := nixCatalog(ir.BuilderOCIHermetic)
	component := cat.Components["tool"]
	component.BuildCommand = []string{"invalid-" + string([]byte{0xff})}
	cat.Components["tool"] = component
	resolved, err := Resolve(cat, Selection{Components: []string{"tool"}, Platform: "linux/amd64"})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Components[0].BuildCommand[0] != component.BuildCommand[0] {
		t.Fatal("resolution repaired malformed producer bytes")
	}
	graph, err := Compile(resolved)
	if err != nil {
		t.Fatal(err)
	}
	if digest, err := graph.Digest(); err == nil || digest != "" {
		t.Fatalf("malformed producer bytes gained canonical identity: %s, %v", digest, err)
	}
}
