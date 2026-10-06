package factory

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/catalog"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/execution"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
)

func arcTestPlan(t *testing.T, platform string) *Plan {
	t.Helper()
	actions := githubTestPlan(t).Actions
	base := diskPublicationPlanFixture(t, platform)
	environment := base.Environment.Clone()
	environment.GitHubARC = "native-kvm-v1"
	environment.Runtime.Runtime = "docker"
	for i := range environment.ExecutionClasses {
		environment.ExecutionClasses[i].RunsOn = githubARCRunner(string(environment.ExecutionClasses[i].HostPlatform))
	}
	cat := base.CatalogSnapshot
	cat.Actions = actions
	root := cat.Components["image"]
	root.BuildRequirements = execution.Clone(root.BuildRequirements)
	root.BuildRequirements.Capabilities[0].Policy = execution.CapabilityRequired
	cat.Components["image"] = root
	stack, err := catalog.ApplyStack(cat)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := Compile(t.Context(), base.Definition, environment, stack, "dev (unknown)")
	if err != nil {
		t.Fatal(err)
	}
	plan.DefinitionPath, plan.EnvironmentPath = "definitions/test.yaml", "definitions/environment.yaml"
	return plan
}

func TestARCNativeDiskGeneration(t *testing.T) {
	for _, platform := range []string{"linux/amd64", "linux/arm64"} {
		t.Run(platform, func(t *testing.T) {
			plan := arcTestPlan(t, platform)
			bundle, err := RenderGitHub(t.Context(), plan)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Contains(bundle.Files[GitHubWorkflowPath], []byte("factory github run-disk")) {
				t.Fatal("missing grouped executor")
			}
			stages, jobs, err := githubStages(plan)
			if err != nil {
				t.Fatal(err)
			}
			groups := 0
			for _, stage := range stages {
				for _, node := range stage.Nodes {
					if node.Kind == NodeFetch && node.RunsOn != githubBootstrapRunner(githubARCRunner(platform)) {
						t.Fatal("source fetch must use a native generic hosted runner")
					}
				}
				if stage.DiskOutput == "" {
					continue
				}
				groups++
				for _, node := range stage.Nodes {
					if node.Kind == NodeFetch || node.RunsOn != githubARCRunner(platform) || jobs[node.ID] != stage.ID {
						t.Fatal("disk grouping lost route or credential boundary")
					}
					for _, id := range node.Outputs {
						if id == plan.LifecycleOutputs[0].ArtifactID {
							t.Fatal("raw disk cannot become an independent carrier")
						}
					}
				}
			}
			if groups != 1 {
				t.Fatalf("got %d disk groups", groups)
			}
			second, err := RenderGitHub(t.Context(), plan)
			if err != nil || !bytes.Equal(bundle.Files[GitHubWorkflowPath], second.Files[GitHubWorkflowPath]) {
				t.Fatal("non-deterministic generation", err)
			}
		})
	}
}

func TestARCAdmissionRejectsUnsupportedPolicy(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*Plan)
	}{
		{"no opt-in", func(p *Plan) { p.Environment.GitHubARC = "" }},
		{"ambient setup", func(p *Plan) { p.Environment.Runtime.Setup = []string{"true"} }},
		{"other runtime", func(p *Plan) { p.Environment.Runtime.Runtime = "podman" }},
		{"unqualified", func(p *Plan) { p.Qualification = TierStaticUnqualified }},
		{"missing profile", func(p *Plan) { p.Environment.DiskInspectionProfiles = nil }},
		{"memory scratch", func(p *Plan) {
			profile := p.Environment.DiskInspectionProfiles["linux/amd64"]
			profile.ScratchStorage = "memory"
			p.Environment.DiskInspectionProfiles["linux/amd64"] = profile
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			plan := arcTestPlan(t, "linux/amd64")
			test.change(plan)
			if code := factoryErrorCode(admitGitHubRequirements(plan, "test")); code != e.CodeEngineCapability {
				t.Fatalf("code = %s", code)
			}
		})
	}
}

func TestARCGroupedNodesRejectIndividualDispatch(t *testing.T) {
	plan := arcTestPlan(t, "linux/amd64")
	for _, node := range plan.Graph.Nodes {
		if githubDiskOutput(plan, node) == "" {
			continue
		}
		err := ExecuteNode(t.Context(), plan, node.ID, nil, nil)
		if factoryErrorCode(err) != e.CodeEngineCapability || !strings.Contains(err.Error(), "grouped executor") {
			t.Fatalf("%s: %v", node.ID, err)
		}
	}
}

func TestRunGitHubDiskRejectsAmbientCredentialsBeforeWork(t *testing.T) {
	keys := []string{sourceTokenEnv, githubImageToken, "NKD_CREDENTIAL", "ACTIONS_ID_TOKEN_REQUEST_TOKEN"}
	for _, platform := range []string{"linux/amd64", "linux/arm64"} {
		for _, key := range keys {
			t.Run(platform+"/"+key, func(t *testing.T) {
				plan := arcTestPlan(t, platform)
				for _, name := range keys {
					t.Setenv(name, "")
				}
				t.Setenv(key, "synthetic-credential")
				t.Chdir(t.TempDir())
				err := RunGitHubDisk(t.Context(), plan, plan.LifecycleOutputs[0].Name, GitHubStepOptions{})
				assertFactoryErrorCode(t, err, e.CodeEngineCapability)
				if !strings.Contains(err.Error(), "unrelated credential") {
					t.Fatalf("wrong admission failed: %v", err)
				}
				entries, err := os.ReadDir(".")
				if err != nil || len(entries) != 0 {
					t.Fatalf("credential rejection performed filesystem work: %v / %v", entries, err)
				}
			})
		}
	}
}

// Exercise the adapter boundary directly: malformed instructions must be denied
// here even when they have not passed through the separate Plan validator.
func TestARCAdmissionDeniesEachExecutionBoundary(t *testing.T) {
	for _, platform := range []string{"linux/amd64", "linux/arm64"} {
		for _, test := range []struct {
			name   string
			change func(*Plan, *ir.Node)
			want   e.Code
		}{
			{"unknown policy", func(p *Plan, _ *ir.Node) { p.Environment.GitHubARC = "unknown" }, e.CodeEngineCapability},
			{"runner override", func(p *Plan, _ *ir.Node) { p.Environment.RunsOn = "self-hosted" }, e.CodeEngineCapability},
			{"job container", func(p *Plan, _ *ir.Node) { p.Environment.Runtime.RuntimeImage = "image" }, e.CodeEngineCapability},
			{"services", func(p *Plan, _ *ir.Node) { p.Environment.Runtime.Services = []string{"service"} }, e.CodeEngineCapability},
			{"variables", func(p *Plan, _ *ir.Node) { p.Environment.Runtime.Variables = map[string]string{"MODE": "ambient"} }, e.CodeEngineCapability},
			{"qualified", func(p *Plan, _ *ir.Node) { p.Qualification = "qualified" }, e.CodeEngineCapability},
			{"kind", func(_ *Plan, n *ir.Node) { n.Kind = ir.KindFetch }, e.CodeEngineCapability},
			{"builder", func(_ *Plan, n *ir.Node) { n.BuilderType = ir.BuilderOCIHermetic }, e.CodeEngineCapability},
			{"missing Packer", func(_ *Plan, n *ir.Node) { n.Packer = nil }, e.CodeEngineCapability},
			{"command", func(_ *Plan, n *ir.Node) { n.Command = []string{"make"} }, e.CodeEngineCapability},
			{"missing disk", func(_ *Plan, n *ir.Node) { n.Disk = nil }, e.CodeEngineCapability},
			{"disk format", func(_ *Plan, n *ir.Node) { n.Disk.Format = "raw" }, e.CodeEngineCapability},
			{"encryption", func(_ *Plan, n *ir.Node) { n.Disk.Encryption = "luks2" }, e.CodeEngineCapability},
			{"missing acquisitions", func(_ *Plan, n *ir.Node) { n.Packer.AcquisitionArchive = "" }, e.CodeEngineCapability},
			{"missing requirements", func(_ *Plan, n *ir.Node) { n.Requirements = nil }, e.CodeEngineCapability},
			{"missing binding", func(_ *Plan, n *ir.Node) { n.ExecutionBinding = nil }, e.CodeEngineCapability},
			{"foreign host", func(_ *Plan, n *ir.Node) {
				n.ExecutionBinding.Class.HostPlatform = execution.HostPlatformLinuxAMD64
				n.ToolchainPlatform = "linux/arm64"
			}, e.CodeEngineCapability},
			{"wrong route", func(_ *Plan, n *ir.Node) { n.ExecutionBinding.Class.RunsOn = "ubuntu-latest" }, e.CodeEngineCapability},
			{"unknown host", func(_ *Plan, n *ir.Node) { n.ExecutionBinding.Class.HostPlatform = "linux/unknown" }, e.CodeEngineCapability},
			{"no KVM", func(_ *Plan, n *ir.Node) { n.Requirements.Capabilities = nil }, e.CodeEngineCapability},
			{"duplicate KVM", func(_ *Plan, n *ir.Node) {
				n.Requirements.Capabilities = append(n.Requirements.Capabilities, n.Requirements.Capabilities[0])
			}, e.CodeEngineCapability},
			{"optional KVM", func(_ *Plan, n *ir.Node) { n.Requirements.Capabilities[0].Policy = execution.CapabilityPreferred }, e.CodeEngineCapability},
			{"local toolchain", func(_ *Plan, n *ir.Node) { n.Toolchain = "sha256:" + strings.Repeat("a", 64) }, e.CodeUnpinnedInput},
			{"utility requirement", func(p *Plan, n *ir.Node) {
				p.LifecycleOutputs = nil // isolate adapter admission from the separate lifecycle graph gate
				p.Graph.Nodes[0].Requirements = execution.Clone(n.Requirements)
			}, e.CodeEngineCapability},
			{"ordinary component requirement", func(p *Plan, n *ir.Node) {
				p.Builds[0].ResolvedComponents[0].BuilderType = ir.BuilderOCIHermetic
				p.Builds[0].ResolvedComponents[0].BuildRequirements = execution.Clone(n.Requirements)
			}, e.CodeEngineCapability},
		} {
			t.Run(platform+"/"+test.name, func(t *testing.T) {
				plan := arcTestPlan(t, platform)
				var root *ir.Node
				for i := range plan.Builds[0].Graph.Nodes {
					if plan.Builds[0].Graph.Nodes[i].BuilderType == ir.BuilderVMImage {
						root = &plan.Builds[0].Graph.Nodes[i]
						break
					}
				}
				if root == nil {
					t.Fatal("fixture has no VM producer")
				}
				test.change(plan, root)
				assertFactoryErrorCode(t, admitGitHubRequirements(plan, "test"), test.want)
			})
		}
	}
}

func TestARCOneDefinitionBothNativeArchitectures(t *testing.T) {
	base := arcTestPlan(t, "linux/amd64")
	definition, environment := base.Definition.Clone(), base.Environment.Clone()
	second := definition.Outputs[0]
	second.Name, second.Platform = "node-image-arm64", "linux/arm64"
	definition.Outputs = append(definition.Outputs, second)
	armClass := environment.ExecutionClasses[0]
	armClass.Name, armClass.RunsOn = "arm64-kvm", githubARCRunner("linux/arm64")
	armClass.HostPlatform = execution.HostPlatformLinuxARM64
	environment.ExecutionClasses = append(environment.ExecutionClasses, armClass)
	environment.DiskInspectionProfiles["linux/arm64"] = environment.DiskInspectionProfiles["linux/amd64"]
	cat := base.CatalogSnapshot
	for _, name := range []string{"agent", "runtime"} {
		component := cat.Components[name]
		component.Platforms = []string{"linux/amd64", "linux/arm64"}
		component.Prebuilt.Digests["arm64"] = component.Prebuilt.Digests["amd64"]
		cat.Components[name] = component
	}
	stack, err := catalog.ApplyStack(cat)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := Compile(t.Context(), definition, environment, stack, "dev (unknown)")
	if err != nil {
		t.Fatal(err)
	}
	plan.DefinitionPath, plan.EnvironmentPath = base.DefinitionPath, base.EnvironmentPath
	bundle, err := RenderGitHub(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, runner := range []string{"arc-amd64-kvm", "arc-arm64-kvm", "ubuntu-24.04", "ubuntu-24.04-arm"} {
		if !bytes.Contains(bundle.Files[GitHubWorkflowPath], []byte(runner)) {
			t.Fatalf("combined workflow lacks %s", runner)
		}
	}
	stages, _, err := githubStages(plan)
	if err != nil {
		t.Fatal(err)
	}
	groups := 0
	for _, stage := range stages {
		if stage.DiskOutput != "" {
			groups++
		}
	}
	if groups != 2 {
		t.Fatalf("combined definition has %d disk groups", groups)
	}
}
