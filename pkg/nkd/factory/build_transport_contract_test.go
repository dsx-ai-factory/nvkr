package factory

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/catalog"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
)

// A Build Digest deliberately excludes transport. That does not authorize an
// executable Plan to acquire its source, image, or flake from a different place.
func TestPlanRejectsChangedBuildTransport(t *testing.T) {
	for _, tc := range []struct {
		name, lane, kind string
		edit             func(*ir.Node)
	}{
		{"source repository", "source", ir.KindFetch, func(n *ir.Node) {
			n.Repo = "https://other.invalid/source.git"
		}},
		{"prebuilt binary URL", "binary", ir.KindPull, func(n *ir.Node) {
			n.Artifact = "https://other.invalid/tool"
		}},
		{"flake reference", "nix", ir.KindBuild, func(n *ir.Node) {
			n.FlakeRef = "github:other/tool?rev=" + strings.Repeat("e", 40)
		}},
		{"vendor image repository", "source", ir.KindFetch, func(n *ir.Node) {
			n.VendorImage = "other.invalid/vendor@" + strings.SplitN(n.VendorImage, "@", 2)[1]
		}},
		{"toolchain image repository", "source", ir.KindBuild, func(n *ir.Node) {
			n.Toolchain = "other.invalid/toolchain@" + strings.SplitN(n.Toolchain, "@", 2)[1]
		}},
		{"unauthorized toolchain relocation", "relocated", ir.KindBuild, func(n *ir.Node) {
			n.Toolchain = "other.invalid/toolchain@" + strings.SplitN(n.Toolchain, "@", 2)[1]
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan := buildTransportPlan(t, tc.lane)
			if err := plan.Validate(); err != nil {
				t.Fatalf("invalid baseline: %v", err)
			}
			graph := plan.Builds[0].Graph
			changed := false
			for i := range graph.Nodes {
				if graph.Nodes[i].Kind == tc.kind {
					tc.edit(&graph.Nodes[i])
					changed = true
					break
				}
			}
			if !changed {
				t.Fatalf("fixture has no %s node", tc.kind)
			}
			if err := graph.Validate(); err != nil {
				t.Fatalf("transport mutation must remain structurally valid: %v", err)
			}
			if digest, err := graph.Digest(); err != nil || digest != plan.Builds[0].BuildDigest {
				t.Fatalf("transport mutation changed logical build identity: %s, %v", digest, err)
			}
			t.Run("validate", func(t *testing.T) {
				assertFactoryErrorCode(t, plan.Validate(), e.CodePipelineDrift)
			})
			t.Run("render", func(t *testing.T) {
				bundle, err := RenderGitHub(t.Context(), plan)
				if bundle != nil {
					t.Error("substituted transport emitted a workflow bundle")
				}
				assertFactoryErrorCode(t, err, e.CodePipelineDrift)
			})
			t.Run("execute", func(t *testing.T) {
				workspace := t.TempDir()
				t.Chdir(workspace)
				var output bytes.Buffer
				err := ExecuteNode(t.Context(), plan, "package/worker", &output, &output)
				assertFactoryErrorCode(t, err, e.CodePipelineDrift)
				entries, err := os.ReadDir(workspace)
				if err != nil {
					t.Fatal(err)
				}
				if len(entries) != 0 || output.Len() != 0 {
					t.Fatalf("substituted transport began execution: entries=%v output=%q", entries, output.String())
				}
			})
		})
	}
}

func TestPlanAcceptsEnvironmentToolchainRelocation(t *testing.T) {
	plan := buildTransportPlan(t, "relocated")
	var toolchain string
	for _, node := range plan.Builds[0].Graph.Nodes {
		if node.Kind == ir.KindBuild {
			toolchain = node.Toolchain
		}
	}
	if !strings.HasPrefix(toolchain, "mirror.example/nkd/nkd-toolchain-test@sha256:") {
		t.Fatalf("fixture did not relocate its toolchain: %q", toolchain)
	}
	raw, err := canonical.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Plan
	if err := canonical.UnmarshalExact(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		plan *Plan
	}{
		{"compiled", plan},
		{"serialized", &decoded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.plan.Validate(); err != nil {
				t.Fatal(err)
			}
			if _, err := RenderGitHub(t.Context(), tc.plan); err != nil {
				t.Fatal(err)
			}
			relocated, err := relocateFactoryPlan(tc.plan, ".nkd/private-workspace")
			if err != nil {
				t.Fatal(err)
			}
			if err := relocated.Validate(); err != nil {
				t.Fatalf("private artifact relocation changed producer admission: %v", err)
			}
		})
	}
}

func buildTransportPlan(t *testing.T, lane string) *Plan {
	t.Helper()
	base, err := catalog.LoadContext(t.Context(), testCatalogProvider(t))
	if err != nil {
		t.Fatal(err)
	}
	definition, environment := fixtureInputs("build-transport", "worker", DefaultRunsOn)
	definition.Outputs[0].Components = []string{"containerd"}
	component := base.Components["containerd"]
	switch lane {
	case "source":
	case "relocated":
		component.ToolchainImage = "nkd-toolchain-test@" + strings.SplitN(component.ToolchainImage, "@", 2)[1]
		environment.Runtime.ToolchainRegistry = "mirror.example/nkd"
	case "nix":
		component.BuilderType = ir.BuilderNix
		component.Flake = &catalog.FlakePin{Ref: "github:example/tool?rev=" + component.Commit,
			Output: "packages.x86_64-linux.default", LockDigest: "sha256:" + strings.Repeat("b", 64),
			Image: "nixos/nix@sha256:" + strings.Repeat("c", 64)}
	case "binary":
		component = catalog.Component{Platforms: []string{"linux/amd64"}, Tag: "v1.0.0",
			SourceDate: 1700000000, OutputType: "host-package",
			Prebuilt: &catalog.Prebuilt{URL: "https://supplier.example/tool", Evidence: &catalog.ExternalEvidence{None: true},
				Digests: map[string]string{"amd64": "sha256:" + strings.Repeat("d", 64)}}}
	default:
		t.Fatalf("unknown transport fixture %q", lane)
	}
	base.Components["containerd"] = component
	stack, err := catalog.ApplyStack(base)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := Compile(t.Context(), definition, environment, stack, "v0.0.0 (test)")
	if err != nil {
		t.Fatal(err)
	}
	plan.DefinitionPath = "examples/definition.yaml"
	plan.EnvironmentPath = "examples/environment.yaml"
	return plan
}
