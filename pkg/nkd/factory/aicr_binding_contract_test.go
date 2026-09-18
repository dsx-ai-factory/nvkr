package factory

import (
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/aicr"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/catalog"
	authored "github.com/NVIDIA/nke-recipes/pkg/nkd/definition"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
)

func TestAICRCompilerSelectsGeneratorFromOutputCatalog(t *testing.T) {
	for _, tc := range []struct {
		name         string
		baseOutput   bool
		generatorPin string
		toolPlatform string
		changeURL    bool
		missingTool  bool
		code         e.Code
	}{
		{name: "output overrides profile and generator", generatorPin: "b"},
		{name: "outputs share the same generator pin", baseOutput: true, generatorPin: "a"},
		{name: "outputs select conflicting generator bytes", baseOutput: true, generatorPin: "b", code: e.CodeUnpinnedSource},
		{name: "outputs select conflicting generator locations", baseOutput: true, generatorPin: "a", changeURL: true, code: e.CodeUnpinnedSource},
		{name: "output catalog omits generator", generatorPin: "a", missingTool: true, code: e.CodeUnpinnedSource},
		{name: "output generator cannot execute on utility platform", generatorPin: "a", toolPlatform: "linux/arm64", code: e.CodeEngineCapability},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := aicrCompilerCatalog(t, "a")
			override := aicrCompilerCatalog(t, tc.generatorPin)
			if tc.changeURL {
				pin := override.Catalog.Tools["aicr"]
				pin.URL = "https://mirror.example/aicr"
				override.Catalog.Tools["aicr"] = pin
			}
			if tc.missingTool {
				delete(override.Catalog.Tools, "aicr")
			}
			if tc.toolPlatform != "" {
				pin := override.Catalog.Tools["aicr"]
				pin.Platform = tc.toolPlatform
				override.Catalog.Tools["aicr"] = pin
			}
			var err error
			override, err = catalog.ApplyStack(override.Catalog)
			if err != nil {
				t.Fatal(err)
			}
			definition, environment := fixtureInputs("aicr", "runtime", "")
			definition.Outputs = []authored.DefinitionOutput{{
				Name: "runtime", Platform: "linux/amd64", Format: authored.OutputHelm,
				Catalog: &authored.Catalog{Base: "output-catalog"},
				AICR:    &authored.AICROutput{Profile: "runtime", Generator: "aicr"},
			}}
			if tc.baseOutput {
				definition.Outputs = append([]authored.DefinitionOutput{{
					Name: "base", Platform: "linux/amd64", Format: authored.OutputHelm,
					AICR: &authored.AICROutput{Profile: "runtime", Generator: "aicr"},
				}}, definition.Outputs...)
			}
			plan, err := compileWithTargetStacks(t.Context(), definition, environment, base,
				map[string]*catalog.Stack{"runtime": override}, "test", "", "")
			if tc.code != "" {
				assertFactoryErrorCode(t, err, tc.code)
				if plan != nil {
					t.Fatal("invalid tool selection produced a plan")
				}
				if tc.baseOutput && !strings.Contains(err.Error(), "conflicting pins") {
					t.Fatalf("tool conflict was not rejected during selection: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			generator := override.Catalog.Tools["aicr"]
			toolCount := 0
			for _, node := range plan.Graph.Nodes {
				if node.ID == "tool/aicr" {
					toolCount++
					if node.URL != generator.URL || node.Digest != generator.ArchiveDigest || node.ExecutableDigest != generator.ExecutableDigest {
						t.Fatalf("tool acquisition differs from selected output catalog: %+v", node)
					}
				}
			}
			if toolCount != 1 {
				t.Fatalf("selected shared generator has %d acquisition nodes, want one", toolCount)
			}
			for _, build := range plan.Builds {
				if build.Graph.AICR.GeneratorDigest != generator.ExecutableDigest {
					t.Fatalf("output %q does not bind the selected generator", build.Name)
				}
			}
		})
	}
}

func aicrCompilerCatalog(t *testing.T, digestByte string) *catalog.Stack {
	t.Helper()
	digest := "sha256:" + strings.Repeat(digestByte, 64)
	stack, err := catalog.ApplyStack(&catalog.Catalog{
		APIVersion: "nkd.dev/catalog/v3", Kind: "SourceBuildRegistry", Metadata: catalog.Metadata{Name: "aicr"},
		Tools: map[string]catalog.ToolPin{"aicr": {
			Format: "raw", URL: "https://example.com/aicr", Platform: "linux/amd64",
			ExecutablePath: "aicr", ArchiveDigest: digest, ExecutableDigest: digest,
		}},
		AICRProfiles: map[string]aicr.Profile{"runtime": {
			Identity: "runtime", Digest: "sha256:" + strings.Repeat("c", 64), GeneratorDigest: digest,
			Service: "eks", Accelerator: "h100", OS: "ubuntu", Intent: "training",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return stack
}

func TestAICRExecutorAdmitsExactActivationBeforeOutput(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*Plan, *Node)
		code e.Code
	}{
		{"missing build", func(p *Plan, _ *Node) { p.Builds = nil }, e.CodePipelineDrift},
		{"missing activation", func(p *Plan, _ *Node) { p.Builds[0].Graph.AICR = nil }, e.CodePipelineDrift},
		{"missing graph", func(p *Plan, _ *Node) { p.Builds[0].Graph = nil }, e.CodePipelineDrift},
		{"missing output", func(_ *Plan, n *Node) { n.Outputs = nil }, e.CodePipelineDrift},
		{"missing bundle", func(p *Plan, _ *Node) { p.Builds[0].Graph.AICR.Bundle = aicr.BundleInputs{} }, e.CodeInvalidCatalog},
		{"missing generator input", func(_ *Plan, n *Node) { n.Inputs = nil }, e.CodePipelineDrift},
		{"multiple generator inputs", func(_ *Plan, n *Node) { n.Inputs = []string{"generator", "other"} }, e.CodePipelineDrift},
		{"invalid profile", func(p *Plan, _ *Node) { p.Builds[0].AICRProfileIdentity = "" }, e.CodeInvalidCatalog},
		{"missing executable", func(*Plan, *Node) {}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pin := "sha256:" + strings.Repeat("a", 64)
			bundle := &aicr.BundleInputs{Deployer: "helm", StorageClass: "gp3", AcceleratedNodeSelectors: []string{"gpu=true"}, AcceleratedNodeTolerations: []string{"gpu=true:NoSchedule"}, WorkloadSelectors: []string{"workload=training"}}
			plan := &Plan{Builds: []BuildPlan{{Name: "accelerated", BuildDigest: pin, AICRProfileIdentity: "h100-eks",
				Graph: &ir.Graph{AICR: &ir.AICRActivation{ProfileDigest: pin, GeneratorDigest: pin, Service: "eks", Accelerator: "h100", OS: "ubuntu", Intent: "training", Bundle: *bundle}}}}}
			node := Node{ID: "aicr/accelerated", BuildTarget: "accelerated", Inputs: []string{"generator"}, Outputs: []string{"bundle"}}
			tc.edit(plan, &node)
			parent := t.TempDir()
			err := executeAICR(t.Context(), plan, node, map[string]Artifact{"generator": {Path: filepath.Join(parent, "absent-generator")}, "bundle": {Path: filepath.Join(parent, "output", "bundle")}}, io.Discard, io.Discard)
			want := tc.code
			if want == "" {
				want = e.CodeEngineCapability
				if runtime.GOOS == "linux" {
					want = e.CodeUnsupported
				}
			}
			assertFactoryErrorCode(t, err, want)
			entries, err := os.ReadDir(parent)
			if err != nil || len(entries) != 0 {
				t.Fatalf("failed admission produced output: %v, %v", entries, err)
			}
		})
	}
}

func TestAICRBundleOptionsPreserveEachOutputSettings(t *testing.T) {
	for _, tc := range []struct {
		name   string
		bundle aicr.BundleInputs
		args   []string
	}{
		{"defaults", aicr.BundleInputs{Deployer: "helm"}, nil},
		{"configured", aicr.BundleInputs{Deployer: "flux", StorageClass: "gp3",
			AcceleratedNodeSelectors: []string{"gpu=true"}, AcceleratedNodeTolerations: []string{"gpu=true:NoSchedule"}, WorkloadSelectors: []string{"training=true"}},
			[]string{"--storage-class", "gp3", "--accelerated-node-selector", "gpu=true", "--accelerated-node-toleration", "gpu=true:NoSchedule", "--workload-selector", "training=true"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			options := aicrBundleOptions(tc.bundle, "output/"+tc.name)
			if options.Deployer != tc.bundle.Deployer || options.OutputDir != "output/"+tc.name || !reflect.DeepEqual(options.ExtraBundleArgs, tc.args) {
				t.Fatalf("renderer options = %+v", options)
			}
			other := aicrBundleOptions(aicr.BundleInputs{Deployer: "helm", StorageClass: "other"}, "output/other")
			other.ExtraBundleArgs[1] = "changed"
			if !reflect.DeepEqual(options.ExtraBundleArgs, tc.args) {
				t.Fatal("another output changed renderer arguments")
			}
		})
	}
}
