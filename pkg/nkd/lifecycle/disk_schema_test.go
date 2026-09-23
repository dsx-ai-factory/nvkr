package lifecycle

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/catalog"
	nkderr "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/execution"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/factory"
)

func diskDefinition(t *testing.T) Definition {
	t.Helper()
	definition := testDefinition(t)
	definition.Outputs[0].Format = OutputDiskImage
	definition.Outputs[0].Components = []string{"kubernetes-packages", "nke-node-non-fde"}
	return definition
}

func TestDiskImageOutputSchemaAndIdentity(t *testing.T) {
	definition := diskDefinition(t)
	if err := definition.Validate(); err != nil {
		t.Fatalf("valid disk output rejected: %v", err)
	}
	diskDigest, err := definition.Digest()
	if err != nil {
		t.Fatal(err)
	}
	executable := definition
	executable.Outputs = append([]DefinitionOutput(nil), definition.Outputs...)
	executable.Outputs[0].Format = OutputExecutable
	executableDigest, err := executable.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if diskDigest == executableDigest {
		t.Fatal("disk output format did not affect Definition identity")
	}

	tests := []struct {
		name string
		edit func(*Definition)
	}{
		{"no component", func(d *Definition) { d.Outputs[0].Components = nil }},
		{"duplicate component", func(d *Definition) { d.Outputs[0].Components = []string{"a", "a"} }},
		{"unsorted components", func(d *Definition) { d.Outputs[0].Components = []string{"b", "a"} }},
		{"unsupported platform", func(d *Definition) { d.Outputs[0].Platform = "windows/amd64" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			invalid := diskDefinition(t)
			tt.edit(&invalid)
			assertCode(t, invalid.Validate(), nkderr.CodeInvalidCatalog)
		})
	}
}

func TestDiskImageCompileAndPrepareFreezeRootArtifactButExecutionStaysClosed(t *testing.T) {
	t.Chdir(t.TempDir())
	definition := diskDefinition(t)
	definition.Catalog.Base = "catalog"
	definition.Outputs[0].Components = []string{"nke-node-non-fde"}
	environment := testEnvironment()
	environment.Publication = nil
	environment.ExecutionClasses = []execution.Class{{
		Name: "amd64-kvm", RunsOn: "linux-amd64-cpu16",
		HostPlatform: execution.HostPlatformLinuxAMD64,
		Resources: execution.ResourceCapacity{
			CPUMillis: 8000, MemoryMiB: 16384, EphemeralStorageMiB: 65536,
		},
		Capabilities:      []execution.CapabilityName{execution.CapabilityKVM},
		MaxTimeoutSeconds: 10800,
	}}
	component := catalog.Component{
		Platforms: []string{"linux/amd64"}, FromSource: true,
		BuilderType: catalog.BuilderVMImage, OutputType: OutputDiskImage,
		Disk: &catalog.DiskSpec{Format: "qcow2", Encryption: "none"},
		BuildRequirements: &execution.Requirements{
			Resources: &execution.ResourceRequest{
				CPUMillis: 4000, MemoryMiB: 8192, EphemeralStorageMiB: 32768,
			},
			AllowedHostPlatforms: []execution.HostPlatform{execution.HostPlatformLinuxAMD64},
			Capabilities: []execution.CapabilityRequirement{{
				Name: execution.CapabilityKVM, Policy: execution.CapabilityRequired,
			}},
			TimeoutSeconds: 7200,
		},
		Repo: "https://example.test/nke", Tag: "v1.0.0",
		Commit: strings.Repeat("a", 40), TreeHash: strings.Repeat("b", 40),
		ToolchainImages: map[string]string{
			"amd64": "example.test/vm@sha256:" + strings.Repeat("c", 64),
		},
		BuildCommand: []string{"make", "image", "ARCH={{.Arch}}"},
		ArtifactPath: "out/node-amd64.qcow2", SourceDate: 1,
	}
	stack, err := catalog.ApplyStack(&catalog.Catalog{Components: map[string]catalog.Component{
		"nke-node-non-fde": component,
	}})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := factory.Compile(context.Background(), definition, environment, stack, "test (test)")
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := PrepareRun(definition, environment, plan, "disk-plan")
	if err != nil {
		t.Fatal(err)
	}
	wantArtifact := "artifact/cli/nke-node-non-fde"
	if len(prepared.Expected) != 1 || prepared.Expected[0].MemberArtifactID != wantArtifact ||
		prepared.Expected[0].ArtifactID != wantArtifact {
		t.Fatalf("prepared disk slot = %#v, want compiler-owned artifact %q", prepared.Expected, wantArtifact)
	}
	workspace := ".nkd/not-created"
	err = factory.AdmitLocal(t.Context(), plan, factory.LocalRunOptions{Workspace: workspace})
	assertCode(t, err, nkderr.CodeEngineCapability)
	if _, statErr := os.Stat(workspace); !os.IsNotExist(statErr) {
		t.Fatalf("failed VM admission touched workspace: %v", statErr)
	}
}
