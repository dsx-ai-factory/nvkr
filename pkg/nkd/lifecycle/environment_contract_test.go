package lifecycle

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/catalog"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/execution"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/factory"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
)

func TestReleaseDescriptionRejectsAmbiguousArtifactOrder(t *testing.T) {
	for _, kind := range []string{"valid", "reversed", "duplicate", "invalid artifact", "unpinned subject"} {
		t.Run(kind, func(t *testing.T) {
			digest := "sha256:" + repeat("a", 64)
			r := Release{SchemaVersion: ReleaseSchemaVersion, Lane: LanePipeline, Scope: ScopeArtifactPublication, PolicyDigest: digest, ContentSubject: digest, Subject: "registry.example/product@" + digest,
				Artifacts: []Artifact{{Name: "a", Kind: OutputExecutable, Digest: digest}, {Name: "b", Kind: OutputExecutable, Digest: digest}}}
			want := e.CodeVerifyFailed
			switch kind {
			case "reversed":
				r.Artifacts[0], r.Artifacts[1] = r.Artifacts[1], r.Artifacts[0]
			case "duplicate":
				r.Artifacts[1] = r.Artifacts[0]
			case "invalid artifact":
				r.Artifacts[0].Digest = "invalid"
			case "unpinned subject":
				r.Subject = "registry.example/product:latest"
				want = e.CodeUnverifiedRef
			}
			err := r.validate()
			if kind == "valid" {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				assertCode(t, err, want)
			}
		})
	}
}

func TestEnvironmentRejectsInvalidExecutionAndPublicationInputs(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Environment)
		want   e.Code
	}{
		{"schema", func(v *Environment) { v.APIVersion = "future" }, e.CodeInvalidCatalog},
		{"platform", func(v *Environment) { v.UtilityPlatform = "windows/amd64" }, e.CodeUnpinnedSource},
		{"mutable utility", func(v *Environment) { v.UtilityContainer = "golang:latest" }, e.CodeUnpinnedSource},
		{"runner newline", func(v *Environment) { v.RunsOn = "ubuntu\nother" }, e.CodeInvalidCatalog},
		{"empty execution classes", func(v *Environment) { v.ExecutionClasses = []execution.Class{} }, e.CodeInvalidCatalog},
		{"invalid execution class", func(v *Environment) {
			v.ExecutionClasses = []execution.Class{{Name: "vm", RunsOn: "vm", HostPlatform: "linux/riscv64", MaxTimeoutSeconds: 1}}
		}, e.CodeInvalidCatalog},
		{"tagged prefix", func(v *Environment) { v.Publication.RepositoryPrefix = "registry.example.test/product:latest" }, e.CodeInvalidCatalog},
		{"ambiguous credentials", func(v *Environment) { v.Publication.CredentialRef = "github:TOKEN" }, e.CodeInvalidCatalog},
		{"digest prefix", func(v *Environment) {
			v.Publication.RepositoryPrefix = "registry.example.test/product@sha256:" + repeat("a", 64)
		}, e.CodeInvalidCatalog},
		{"unknown signing", func(v *Environment) { v.Signing = &Signing{Mode: "unknown"} }, e.CodeUnsupported},
		{"missing signer identity", func(v *Environment) { v.Signing = &Signing{Mode: "cosign"} }, e.CodeTrustPolicyMissing},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := testEnvironment()
			tc.change(&env)
			assertCode(t, env.Validate(), tc.want)
			digest, err := env.Digest()
			assertCode(t, err, tc.want)
			if digest != "" {
				t.Fatal("invalid environment received identity")
			}
		})
	}
}

func TestExecutionClassesChangeEnvironmentIdentity(t *testing.T) {
	environment := testEnvironment()
	baseline, err := environment.Digest()
	if err != nil {
		t.Fatal(err)
	}
	environment.ExecutionClasses = []execution.Class{{
		Name: "local", RunsOn: "local", HostPlatform: execution.HostPlatformLinuxAMD64,
		Resources: execution.ResourceCapacity{
			CPUMillis: 4000, MemoryMiB: 8192, EphemeralStorageMiB: 16384,
		},
		MaxTimeoutSeconds: 7200,
	}}
	withClass, err := environment.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if withClass == baseline {
		t.Fatal("execution classes did not change Environment identity")
	}
	for _, test := range []struct {
		name   string
		change func(*execution.Class)
	}{
		{name: "name", change: func(class *execution.Class) { class.Name = "other" }},
		{name: "route", change: func(class *execution.Class) { class.RunsOn = "other" }},
		{name: "host platform", change: func(class *execution.Class) { class.HostPlatform = execution.HostPlatformLinuxARM64 }},
		{name: "CPU", change: func(class *execution.Class) { class.Resources.CPUMillis++ }},
		{name: "memory", change: func(class *execution.Class) { class.Resources.MemoryMiB++ }},
		{name: "ephemeral storage", change: func(class *execution.Class) { class.Resources.EphemeralStorageMiB++ }},
		{name: "capabilities", change: func(class *execution.Class) {
			class.Capabilities = []execution.CapabilityName{execution.CapabilityKVM}
		}},
		{name: "timeout", change: func(class *execution.Class) { class.MaxTimeoutSeconds++ }},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := environment
			changed.ExecutionClasses = execution.CloneClasses(environment.ExecutionClasses)
			test.change(&changed.ExecutionClasses[0])
			digest, err := changed.Digest()
			if err != nil {
				t.Fatal(err)
			}
			if digest == withClass {
				t.Fatalf("changing execution class %s did not change Environment identity", test.name)
			}
		})
	}
}

func TestEnvironmentStrictlyDecodesExecutionClasses(t *testing.T) {
	for _, test := range []struct {
		name  string
		class string
	}{
		{name: "unknown class field", class: "    future: true\n"},
		{name: "unknown resource field", class: "    resources:\n      memoryMiB: 8192\n      future: 1\n"},
		{name: "unknown capability", class: "    capabilities: [gpu]\n"},
		{name: "empty inventory", class: "[]\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "environment.yaml")
			document := "apiVersion: nkd.dev/environment/v1\nkind: Environment\nmetadata:\n  name: strict\n" +
				"utilityContainer: golang@sha256:" + repeat("1", 64) + "\nutilityPlatform: linux/amd64\n"
			if test.name == "empty inventory" {
				document += "executionClasses: " + test.class
			} else {
				document += "executionClasses:\n"
				document += "  - name: local\n    runsOn: local\n    hostPlatform: linux/amd64\n    maxTimeoutSeconds: 7200\n"
				document += test.class
			}
			if err := os.WriteFile(path, []byte(document), 0600); err != nil {
				t.Fatal(err)
			}
			_, err := LoadEnvironment(t.Context(), path)
			assertCode(t, err, e.CodeInvalidCatalog)
		})
	}
}

func TestEnvironmentYAMLCompilesDeterministicPlanBinding(t *testing.T) {
	path := filepath.Join(t.TempDir(), "environment.yaml")
	document := `apiVersion: nkd.dev/environment/v1
kind: Environment
metadata:
  name: execution-classes
utilityContainer: golang@sha256:` + repeat("1", 64) + `
utilityPlatform: linux/amd64
executionClasses:
  - name: alpha-tcg
    runsOn: linux-amd64-tcg
    hostPlatform: linux/amd64
    resources:
      cpuMillis: 8000
      memoryMiB: 16384
      ephemeralStorageMiB: 65536
    maxTimeoutSeconds: 14400
  - name: beta-kvm
    runsOn: linux-amd64-kvm
    hostPlatform: linux/amd64
    resources:
      cpuMillis: 8000
      memoryMiB: 16384
      ephemeralStorageMiB: 65536
    capabilities:
      - kvm
    maxTimeoutSeconds: 14400
`
	if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}
	environment, err := LoadEnvironment(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	definition := testDefinition(t)
	definition.Outputs[0].Components = []string{"kubelet"}
	base, err := catalog.LoadContext(t.Context(), catalog.Dir(definition.Catalog.Base))
	if err != nil {
		t.Fatal(err)
	}
	component := base.Components["kubelet"]
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
	base.Components["kubelet"] = component
	stack, err := catalog.ApplyStack(base)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := factory.Compile(t.Context(), definition, environment, stack, "test")
	if err != nil {
		t.Fatal(err)
	}
	var buildNode *ir.Node
	for index := range plan.Builds[0].Graph.Nodes {
		if plan.Builds[0].Graph.Nodes[index].Component == "kubelet" {
			buildNode = &plan.Builds[0].Graph.Nodes[index]
			break
		}
	}
	if buildNode == nil || buildNode.ExecutionBinding == nil ||
		buildNode.ExecutionBinding.Class.Name != "beta-kvm" {
		t.Fatalf("public Environment did not select preferred class: %#v", buildNode)
	}
	var planNode *factory.Node
	for index := range plan.Graph.Nodes {
		if plan.Graph.Nodes[index].IRNode == buildNode.ID {
			planNode = &plan.Graph.Nodes[index]
			break
		}
	}
	if planNode == nil || planNode.ExecutionBinding != nil || planNode.Requirements != nil || planNode.RunsOn != "" {
		t.Fatalf("Plan copied producer execution fields: %#v", planNode)
	}
	instruction, err := plan.ExecutionFor(*planNode)
	if err != nil {
		t.Fatal(err)
	}
	if instruction.RunsOn != "linux-amd64-kvm" || !execution.EqualBinding(instruction.ExecutionBinding, buildNode.ExecutionBinding) {
		t.Fatalf("Plan did not resolve the deterministic binding: %#v", instruction)
	}
	if len(plan.Environment.ExecutionClasses) != 2 || plan.Environment.ExecutionClasses[1].Name != "beta-kvm" {
		t.Fatalf("compilation lost authored execution classes: %#v", plan.Environment.ExecutionClasses)
	}

	alternative, err := execution.Match(buildNode.Requirements,
		[]execution.Class{environment.ExecutionClasses[0]}, execution.MatchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	buildNode.ExecutionBinding = alternative
	assertCode(t, plan.Validate(), e.CodePipelineDrift)
}
