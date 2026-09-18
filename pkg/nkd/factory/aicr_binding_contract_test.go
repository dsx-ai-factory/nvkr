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
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
)

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
