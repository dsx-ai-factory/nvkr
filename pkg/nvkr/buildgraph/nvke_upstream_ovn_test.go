// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package buildgraph_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/buildgraph"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
	"go.yaml.in/yaml/v3"
)

const upstreamOVNCommit = "d6a3812d1bc1c36bf24c7eda0e86e9b13d84907d"
const upstreamOVNTree = "ce0cb49cd14f2fc260457e61a5a222c027a07f79"
const upstreamOVNEpoch = 1788540677
const upstreamOVNDir = "../../../definitions/nvke-local-dev-arm64/upstream-ovn-catalog"

func loadUpstreamOVN(t *testing.T) *catalog.Catalog {
	t.Helper()
	cat, err := catalog.LoadContext(t.Context(), catalog.Dir(upstreamOVNDir))
	if err != nil {
		t.Fatal(err)
	}
	return cat
}

// These contracts compile both native producer routes without acquiring source,
// launching a daemon, or claiming image/runtime compatibility.
func TestNVKEUpstreamOVNGraphs(t *testing.T) {
	cat := loadUpstreamOVN(t)
	if len(cat.Components) != 3 {
		t.Fatal("upstream closure must contain exactly image, chart and CNI packaging")
	}
	for name, component := range cat.Components {
		if !component.FromSource || component.Prebuilt != nil || component.Repo != "https://github.com/ovn-kubernetes/ovn-kubernetes" || component.Commit != upstreamOVNCommit || component.TreeHash != upstreamOVNTree || component.SourceDate != upstreamOVNEpoch || component.Tag != "v1.4.0" {
			t.Fatalf("%s lost exact upstream source pin", name)
		}
		command := strings.Join(component.BuildCommand, "\n")
		if strings.Contains(command, ovnForkCommit) || strings.Contains(command, "1.5.18") || strings.Contains(command, "/src/charts/") {
			t.Fatalf("%s retains fork-specific source/package inputs", name)
		}
		if err := exec.CommandContext(t.Context(), "bash", "-n", "-c", component.BuildCommand[2]).Run(); err != nil {
			t.Fatalf("%s command syntax: %v", name, err)
		}
	}
	image := strings.Join(cat.Components["ovn-kube-ubuntu"].BuildCommand, "\n")
	for _, fragment := range []string{"build-arg:BUILDER_IMAGE=" + ovnGoBuilder, "build-arg:KUBECTL_MINOR=1.34", "name=localhost/nvke/ovn-kubernetes:git-" + upstreamOVNCommit} {
		if !strings.Contains(image, fragment) {
			t.Fatalf("source image lost %q", fragment)
		}
	}
	chart := cat.Components["ovn-kubernetes-chart"]
	if chart.Helm == nil || len(chart.Helm.Images) != 1 || chart.Helm.Images["ovn"].Values.Digest != "global.image.tag" || chart.Helm.Images["ovn"].Values.DigestFormat != "sha256-hex" {
		t.Fatal("upstream template must consume the authenticated global hex slot")
	}
	for _, arch := range []string{"amd64", "arm64"} {
		t.Run(arch, func(t *testing.T) {
			selected, err := buildgraph.Resolve(cat, buildgraph.Selection{Components: []string{"nvke-cni-ovn", "ovn-kube-ubuntu", "ovn-kubernetes-chart"}, Platform: "linux/" + arch})
			if err != nil {
				t.Fatal(err)
			}
			graph, err := buildgraph.Compile(selected)
			if err != nil {
				t.Fatal(err)
			}
			builds := 0
			for _, node := range graph.Nodes {
				if node.Kind != ir.KindBuild {
					continue
				}
				builds++
				if node.Platform != "linux/"+arch || !node.SourceReadOnly || node.WriteDir != "nvkr-output" {
					t.Fatalf("%s lost native read-only source route", node.Component)
				}
				if node.Component == "ovn-kube-ubuntu" {
					if node.Network != ir.NetworkDefault || node.BuilderType != ir.BuilderBuildKit {
						t.Fatal("upstream Dockerfile network must be explicit")
					}
				} else if node.Network != ir.NetworkNone {
					t.Fatal("chart and CNI packaging must remain offline")
				}
				if node.Component == "nvke-cni-ovn" && (len(node.Materials) != 2 || node.Materials[0].Binding != "ovn-chart" || node.Materials[1].Binding != "ovn-image") {
					t.Fatal("CNI bundle lost exact chart/image inputs")
				}
			}
			if builds != 3 {
				t.Fatalf("build count = %d", builds)
			}
			_ = compileOVNForkPlan(t, cat, arch, "docker")
		})
	}
}

func TestNVKEUpstreamOVNDiskSelection(t *testing.T) {
	base, err := catalog.LoadContext(t.Context(), catalog.Dir("../../../definitions/nvke-m2/os-catalog"))
	if err != nil {
		t.Fatal(err)
	}
	prior, err := catalog.LoadOverlayContext(t.Context(), "../../../definitions/nvke-local-dev-arm64/os-tcg.json")
	if err != nil {
		t.Fatal(err)
	}
	upstream, err := catalog.LoadOverlayContext(t.Context(), "../../../definitions/nvke-local-dev-arm64/os-upstream-ovn.json")
	if err != nil {
		t.Fatal(err)
	}
	stack, err := catalog.ApplyStack(base, prior, upstream)
	if err != nil {
		t.Fatal(err)
	}
	cat := loadUpstreamOVN(t)
	if len(upstream.Components) != 3 {
		t.Fatal("upstream overlay must replace only the three OVN producers")
	}
	for name, component := range cat.Components {
		if !reflect.DeepEqual(component, stack.Catalog.Components[name]) {
			t.Fatalf("disk %s differs from standalone producer", name)
		}
	}
	if stack.Catalog.Components["nvke-image-inputs"].Materials["ovn"] != "nvke-cni-ovn" {
		t.Fatal("OS lost authenticated CNI material binding")
	}
	_, err = catalog.ApplyStack(base, upstream)
	var coded *nvkrerr.Error
	if !errors.As(err, &coded) || coded.Code != nvkrerr.CodeUnverifiedRef {
		t.Fatalf("out-of-order overlay: %v", err)
	}

	const path = "../../../definitions/nvke-local-dev-arm64/definition-upstream-ovn.json"
	d, err := definition.LoadDefinition(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	baseline, err := definition.LoadDefinition(t.Context(), "../../../definitions/nvke-local-dev-arm64/definition.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Outputs) != 29 || len(baseline.Outputs) != 29 {
		t.Fatal("alternative changed required output count")
	}
	old := map[string]definition.DefinitionOutput{}
	for _, output := range baseline.Outputs {
		old[output.Name] = output
	}
	for _, output := range d.Outputs {
		switch output.Name {
		case "nvke-node-image-upstream-ovn-arm64":
			if len(output.Catalog.Overlays) != 2 || output.Catalog.Overlays[1] != "./os-upstream-ovn.json" {
				t.Fatal("disk lost explicit ordered override")
			}
		case "upstream-ovn-chart-arm64":
			if output.Catalog.Base != "./upstream-ovn-catalog" || len(output.Helm.Images) != 1 || output.Helm.Images["ovn"] != "upstream-ovn-image-arm64" {
				t.Fatal("upstream chart binds wrong image slot")
			}
		case "upstream-ovn-image-arm64":
			if output.Catalog.Base != "./upstream-ovn-catalog" {
				t.Fatal("upstream image selects the NVIDIA producer")
			}
		case "controller-profiles-upstream-ovn":
			if output.Catalog.Base != "./upstream-ovn-profiles-catalog" {
				t.Fatal("upstream profile selects NVIDIA install inputs")
			}
		default:
			if !reflect.DeepEqual(output, old[output.Name]) {
				t.Fatalf("unrelated output %s changed", output.Name)
			}
		}
		delete(old, output.Name)
	}
	if len(old) != 4 {
		t.Fatalf("alternative replaces %d baseline slots, want four", len(old))
	}
	profile, err := catalog.LoadContext(t.Context(), catalog.Dir("../../../definitions/nvke-local-dev-arm64/upstream-ovn-profiles-catalog"))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("../../../definitions/nvke-local-dev-arm64/upstream-ovn-profiles-catalog/patches/local-profile.patch")
	if err != nil {
		t.Fatal(err)
	}
	component := profile.Components["nvke-local-profiles"]
	if len(component.Patches) != 1 || component.Patches[0].SHA256 != fmt.Sprintf("sha256:%x", sha256.Sum256(raw)) {
		t.Fatal("profile package inputs lost exact patch binding")
	}
	for _, kind := range []string{"charts", "images"} {
		if !strings.Contains(string(raw), "nke-cni-ovn-"+kind+"-1.4.0") {
			t.Fatal("upstream profile installs the wrong stream")
		}
	}
	if strings.Contains(string(raw), "1.5.18") {
		t.Fatal("upstream profile retains NVIDIA package stream")
	}
	if !strings.Contains(string(raw), "set image-url http://172.30.0.1:18080/nvke-node-image-upstream-ovn-arm64.qcow2") {
		t.Fatal("upstream profile references the NVIDIA disk output basename")
	}
	if strings.Contains(string(raw), "http://172.30.0.1:18080/nvke-node-image-arm64.qcow2") {
		t.Fatal("upstream profile retains the NVIDIA disk route")
	}
	baselinePatch, err := os.ReadFile("../../../definitions/nvke-local-dev-arm64/profiles-catalog/patches/local-profile.patch")
	if err != nil {
		t.Fatal(err)
	}
	expected := strings.NewReplacer("nke-cni-ovn-images-1.5.18", "nke-cni-ovn-images-1.4.0", "nke-cni-ovn-charts-1.5.18", "nke-cni-ovn-charts-1.4.0", "nvke-node-image-arm64.qcow2", "nvke-node-image-upstream-ovn-arm64.qcow2").Replace(string(baselinePatch))
	if string(raw) != expected {
		t.Fatal("upstream profile alters unrelated bootstrap behavior")
	}
	baselineProfiles, err := catalog.LoadContext(t.Context(), catalog.Dir("../../../definitions/nvke-local-dev-arm64/profiles-catalog"))
	if err != nil {
		t.Fatal(err)
	}
	baselineComponent := baselineProfiles.Components["nvke-local-profiles"]
	if component.Commit != baselineComponent.Commit || component.TreeHash != baselineComponent.TreeHash || component.SourceDate != baselineComponent.SourceDate {
		t.Fatal("upstream profile changes the approved NVKE source pin")
	}
}

// Loading the complete Definition rederives the existing controller pin set from
// private pinned Git source. Keep that external admission out of ordinary CI.
func TestNVKEUpstreamOVNFullPlan(t *testing.T) {
	if os.Getenv("NVKR_TEST_UPSTREAM_OVN_PLAN") != "1" {
		t.Skip("requires authenticated access to the existing NVKE controller pin set")
	}
	repo, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(repo)
	d, err := definition.LoadDefinition(t.Context(), "definitions/nvke-local-dev-arm64/definition-upstream-ovn.json")
	if err != nil {
		t.Fatal(err)
	}
	env, err := definition.LoadEnvironment(t.Context(), "definitions/nvke-local-dev-arm64/environment-local-docker.json")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := factory.CompileDocuments(t.Context(), "definitions/nvke-local-dev-arm64/definition-upstream-ovn.json", d, "definitions/nvke-local-dev-arm64/environment-local-docker.json", env, "upstream-ovn-test")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Builds) != 29 {
		t.Fatal("full alternative Plan lost frozen membership")
	}
}

// Opt-in immutable source fixture. This performs the real source-owned Helm
// render after the catalog's actual preparation command, without a container,
// image bake, package publication or compatibility claim.
func TestNVKEUpstreamOVNSourceRender(t *testing.T) {
	source := os.Getenv("NVKR_UPSTREAM_OVN_CHECKOUT")
	if source == "" {
		t.Skip("requires a clean exact v1.4.0 source checkout")
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	commit, err := exec.CommandContext(ctx, "git", "-C", source, "rev-parse", "HEAD").Output()
	if err != nil || strings.TrimSpace(string(commit)) != upstreamOVNCommit {
		t.Fatalf("source ref: %s %v", commit, err)
	}
	dirty, err := exec.CommandContext(ctx, "git", "-C", source, "status", "--porcelain", "--", "helm/ovn-kubernetes").Output()
	if err != nil || len(dirty) != 0 {
		t.Fatalf("source chart is not clean: %s %v", dirty, err)
	}
	chart := filepath.Join(t.TempDir(), "ovn-kubernetes")
	command := loadUpstreamOVN(t).Components["ovn-kubernetes-chart"].BuildCommand[2]
	command, _, ok := strings.Cut(command, "\ntar --format=ustar")
	if !ok {
		t.Fatal("missing deterministic chart packaging boundary")
	}
	command = strings.Replace(command, "readonly chart=/src/helm/ovn-kubernetes", "readonly chart="+strconv.Quote(filepath.Join(source, "helm/ovn-kubernetes")), 1)
	command = strings.Replace(command, "readonly out=/src/nvkr-output/ovn-kubernetes", "readonly out="+strconv.Quote(chart), 1)
	if output, err := exec.CommandContext(ctx, "bash", "-ec", command).CombinedOutput(); err != nil {
		t.Fatalf("catalog chart preparation: %v\n%s", err, output)
	}
	const hex = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	want := "localhost/nvke/ovn-kubernetes@sha256:" + hex
	rendered, err := exec.CommandContext(ctx, "helm", "template", "ovn-kubernetes", chart, "--namespace", "ovn-kubernetes", "--set", "skipCallToK8s=true", "--set-string", "global.image.tag="+hex).CombinedOutput()
	if err != nil {
		t.Fatalf("source-owned chart render: %v\n%s", err, rendered)
	}
	decoder := yaml.NewDecoder(strings.NewReader(string(rendered)))
	images := 0
	var visit func(*yaml.Node)
	visit = func(node *yaml.Node) {
		if node.Kind == yaml.MappingNode {
			for i := 0; i+1 < len(node.Content); i += 2 {
				if node.Content[i].Value == "image" && node.Content[i+1].Kind == yaml.ScalarNode {
					images++
					if node.Content[i+1].Value != want {
						t.Fatalf("unbound rendered image %q", node.Content[i+1].Value)
					}
				}
			}
		}
		for _, child := range node.Content {
			visit(child)
		}
	}
	for {
		var document yaml.Node
		if err := decoder.Decode(&document); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		visit(&document)
	}
	if images != 7 {
		t.Fatalf("rendered images = %d, want control plane plus six node containers", images)
	}
}
