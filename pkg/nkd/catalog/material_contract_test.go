package catalog

import (
	"fmt"
	"strings"
	"testing"

	nkderr "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/execution"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/internal/packertransport"
)

func vmMaterialComponent(materials map[string]string) Component {
	component := goodComponent()
	component.BuilderType = BuilderVMImage
	component.OutputType = "disk-image"
	component.Disk = &DiskSpec{Format: "qcow2", Encryption: "none"}
	component.BuildRequirements = &execution.Requirements{
		Resources: &execution.ResourceRequest{
			CPUMillis: 2000, MemoryMiB: 4096, EphemeralStorageMiB: 8192,
		},
		AllowedHostPlatforms: []execution.HostPlatform{execution.HostPlatformLinuxAMD64},
		Capabilities: []execution.CapabilityRequirement{{
			Name: execution.CapabilityKVM, Policy: execution.CapabilityPreferred,
		}},
		TimeoutSeconds: 7200,
	}
	component.BuildCommand = []string{"make", "image"}
	component.ToolchainImages = map[string]string{"amd64": component.ToolchainImage}
	component.ArtifactPath = "output/node.qcow2"
	component.Materials = materials
	return component
}

func materialCatalog(t *testing.T) *Catalog {
	t.Helper()
	catalog := &Catalog{
		Components: map[string]Component{
			"agent":   goodComponent(),
			"image":   vmMaterialComponent(map[string]string{"agent-package": "agent", "runtime-package": "runtime"}),
			"runtime": goodPrebuilt(),
		},
	}
	if err := catalog.Validate(); err != nil {
		t.Fatalf("valid material catalog: %v", err)
	}
	return catalog
}

func TestVMMaterialBindingValidation(t *testing.T) {
	for _, test := range []struct {
		name     string
		mutate   func(*Catalog)
		wantText string
	}{
		{
			name: "unsupported BuildKit owner",
			mutate: func(catalog *Catalog) {
				agent := catalog.Components["agent"]
				agent.BuilderType = BuilderBuildKit
				agent.OutputType = "oci-image"
				agent.Materials = map[string]string{"runtime": "runtime"}
				catalog.Components["agent"] = agent
			},
			wantText: "require builderType oci-hermetic or vm-image",
		},
		{
			name: "prebuilt owner",
			mutate: func(catalog *Catalog) {
				component := goodPrebuilt()
				component.Materials = map[string]string{"runtime": "runtime"}
				catalog.Components["agent"] = component
			},
			wantText: "require a from-source component",
		},
		{
			name: "excluded owner",
			mutate: func(catalog *Catalog) {
				catalog.Components["agent"] = Component{
					Platforms: []string{"linux/amd64"}, ExclusionReason: "not distributed",
					Materials: map[string]string{"runtime": "runtime"},
				}
			},
			wantText: "require a from-source component",
		},
		{
			name: "noncanonical binding",
			mutate: func(catalog *Catalog) {
				image := catalog.Components["image"]
				image.Materials = map[string]string{" agent-package": "agent"}
				catalog.Components["image"] = image
			},
			wantText: "material binding name",
		},
		{
			name: "invalid target component name",
			mutate: func(catalog *Catalog) {
				image := catalog.Components["image"]
				image.Materials = map[string]string{"agent-package": " agent"}
				catalog.Components["image"] = image
			},
			wantText: "invalid component name",
		},
		{
			name: "display control in target component name",
			mutate: func(catalog *Catalog) {
				image := catalog.Components["image"]
				image.Materials = map[string]string{"agent-package": "agent\u202e"}
				catalog.Components["image"] = image
			},
			wantText: "invalid component name",
		},
		{
			name: "binding exceeds encoded path segment",
			mutate: func(catalog *Catalog) {
				image := catalog.Components["image"]
				image.Materials = map[string]string{strings.Repeat("é", 100): "agent"}
				catalog.Components["image"] = image
			},
			wantText: "encoded path-segment limit",
		},
		{
			name: "self reference",
			mutate: func(catalog *Catalog) {
				image := catalog.Components["image"]
				image.Materials = map[string]string{"image": "image"}
				catalog.Components["image"] = image
			},
			wantText: "refers to itself",
		},
		{
			name: "duplicate component aliases",
			mutate: func(catalog *Catalog) {
				image := catalog.Components["image"]
				image.Materials["second-agent"] = "agent"
				catalog.Components["image"] = image
			},
			wantText: "ambiguously select component",
		},
		{
			name: "unknown component",
			mutate: func(catalog *Catalog) {
				image := catalog.Components["image"]
				image.Materials["agent-package"] = "missing"
				catalog.Components["image"] = image
			},
			wantText: "references unknown component",
		},
		{
			name: "excluded component",
			mutate: func(catalog *Catalog) {
				catalog.Components["runtime"] = Component{Platforms: []string{"linux/amd64"}, ExclusionReason: "not distributed"}
			},
			wantText: "references excluded component",
		},
		{
			name: "cycle",
			mutate: func(catalog *Catalog) {
				catalog.Components["agent"] = vmMaterialComponent(map[string]string{"image": "image"})
			},
			wantText: "material dependency cycle",
		},
		{
			name: "three component cycle",
			mutate: func(catalog *Catalog) {
				catalog.Components["image"] = vmMaterialComponent(map[string]string{"agent": "agent"})
				catalog.Components["agent"] = vmMaterialComponent(map[string]string{"runtime": "runtime"})
				catalog.Components["runtime"] = vmMaterialComponent(map[string]string{"image": "image"})
			},
			wantText: "material dependency cycle",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			catalog, err := cloneCatalog(materialCatalog(t))
			if err != nil {
				t.Fatal(err)
			}
			test.mutate(catalog)
			err = catalog.Validate()
			if catalogErrorCode(err) != nkderr.CodeInvalidCatalog || !strings.Contains(err.Error(), test.wantText) {
				t.Fatalf("validation error = %v, want %s containing %q", err, nkderr.CodeInvalidCatalog, test.wantText)
			}
		})
	}
}

func TestOCIHermeticComponentAcceptsMaterialBindings(t *testing.T) {
	for _, builderType := range []string{"", BuilderOCIHermetic} {
		t.Run("builder-"+builderType, func(t *testing.T) {
			catalog := materialCatalog(t)
			component := catalog.Components["agent"]
			component.BuilderType = builderType
			component.Materials = map[string]string{"runtime-archive": "runtime"}
			catalog.Components["agent"] = component
			if err := catalog.Validate(); err != nil {
				t.Fatalf("OCI-hermetic material consumer was rejected: %v", err)
			}
		})
	}
}

func TestPackerPublicVariableValidation(t *testing.T) {
	packerImage := vmMaterialComponent(map[string]string{"agent-package": "agent"})
	packerImage.BuildCommand = nil
	packerImage.Packer = &PackerConfig{
		ProjectPath:              "packer",
		PackerVersion:            "1.14.2",
		BuildName:                "qemu.image",
		ArchitectureVariable:     "architecture",
		AcceleratorVariable:      "accelerator",
		MaterialManifestVariable: "material_manifest",
		OutputDirectoryVariable:  "output_directory",
		Variables:                map[string]string{"image_variant": "standard"},
	}

	for _, test := range []struct {
		name      string
		variables map[string]string
		wantText  string
	}{
		{name: "reserved collision", variables: map[string]string{"architecture": "arm64"}, wantText: "duplicates another binding"},
		{name: "empty name", variables: map[string]string{"": "value"}, wantText: "variable name"},
		{name: "delimiter in name", variables: map[string]string{"image_variant=other": "value"}, wantText: "variable name"},
		{name: "control in name", variables: map[string]string{"image\nvariant": "value"}, wantText: "variable name"},
		{name: "display control in name", variables: map[string]string{"image\u202evariant": "value"}, wantText: "variable name"},
		{name: "control in value", variables: map[string]string{"image_variant": "unsafe\nvalue"}, wantText: "not transport-safe"},
		{name: "display control in value", variables: map[string]string{"image_variant": "safe\u200bunsafe"}, wantText: "not transport-safe"},
		{name: "oversized value", variables: map[string]string{"image_variant": strings.Repeat("x", packertransport.MaxVariableValueBytes+1)}, wantText: "not transport-safe"},
		{name: "too many variables", variables: catalogPackerVariables(packertransport.MaxPublicVariables+1, 0), wantText: "variable bound"},
		{name: "aggregate argv too large", variables: catalogPackerVariables(17, packertransport.MaxVariableValueBytes), wantText: "argv bound"},
	} {
		t.Run(test.name, func(t *testing.T) {
			catalog, err := cloneCatalog(materialCatalog(t))
			if err != nil {
				t.Fatal(err)
			}
			component := packerImage
			config := *packerImage.Packer
			config.Variables = test.variables
			component.Packer = &config
			catalog.Components["image"] = component
			err = catalog.Validate()
			if catalogErrorCode(err) != nkderr.CodeInvalidCatalog || !strings.Contains(err.Error(), test.wantText) {
				t.Fatalf("validation error = %v, want %s containing %q", err, nkderr.CodeInvalidCatalog, test.wantText)
			}
		})
	}
}

func catalogPackerVariables(count, valueBytes int) map[string]string {
	variables := make(map[string]string, count)
	for index := 0; index < count; index++ {
		variables[fmt.Sprintf("variable_%d", index)] = strings.Repeat("x", valueBytes)
	}
	return variables
}

func TestValidateMaterialSetRejectsInvalidOrDuplicateResolvedComponents(t *testing.T) {
	component := NamedComponent{Name: "agent", Component: goodComponent()}
	for _, test := range []struct {
		name       string
		components []NamedComponent
		wantText   string
	}{
		{name: "empty name", components: []NamedComponent{{Name: "", Component: goodComponent()}}, wantText: "invalid component name"},
		{name: "surrounding whitespace", components: []NamedComponent{{Name: " agent", Component: goodComponent()}}, wantText: "invalid component name"},
		{name: "duplicate", components: []NamedComponent{component, component}, wantText: "repeats component"},
		{
			name: "selected producer set omits material",
			components: []NamedComponent{{
				Name: "image", Component: vmMaterialComponent(map[string]string{"agent-package": "agent"}),
			}},
			wantText: "references unknown component",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateMaterialSet(test.components)
			if catalogErrorCode(err) != nkderr.CodeInvalidCatalog || !strings.Contains(err.Error(), test.wantText) {
				t.Fatalf("validation error = %v, want %s containing %q", err, nkderr.CodeInvalidCatalog, test.wantText)
			}
		})
	}
}
