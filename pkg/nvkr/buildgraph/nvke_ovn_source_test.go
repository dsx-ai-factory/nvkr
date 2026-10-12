// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package buildgraph_test

// This file validates NVIDIA's supplied OVN catalog through generic NVKR APIs.
// Component-specific Go tests are not a requirement for adding a catalog.

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
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/buildgraph"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/execution"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
	"go.yaml.in/yaml/v3"
)

const ovnForkCommit = "26cc199de8e814a7fe6d899a770fc04df0bbc2f9"

// ovnForkStream matches Chart.yaml at ovnForkCommit; it is not the Debian revision.
const ovnForkStream = "1.5.18"

// Both source trees use a vendored JSON adapter incompatible with Go 1.27.
const ovnGoBuilder = "quay.io/projectquay/golang@sha256:506a668eb4c763a964ffc4d4ad0125d45d75aac899392be7e5d39380f8452564"

func TestNVKEOVNBuilderPinBindings(t *testing.T) {
	for _, path := range []string{
		"../../../definitions/nvke-ovn-source/catalog",
		"../../../definitions/nvke-m2/os-catalog",
		"../../../definitions/nvke-os/catalog",
	} {
		t.Run(path, func(t *testing.T) {
			cat, err := catalog.LoadContext(t.Context(), catalog.Dir(path))
			if err != nil {
				t.Fatal(err)
			}
			command := strings.Join(cat.Components["ovn-kube-ubuntu"].BuildCommand, "\n")
			if !strings.Contains(command, "build-arg:BUILDER_IMAGE="+ovnGoBuilder) {
				t.Fatal("OVN producer lost the compatible digest-pinned Go builder")
			}
		})
	}
}

func loadOVNForkCatalog(t *testing.T) *catalog.Catalog {
	t.Helper()
	cat, err := catalog.LoadContext(t.Context(), catalog.Dir("../../../definitions/nvke-ovn-source/catalog"))
	if err != nil {
		t.Fatal(err)
	}
	return cat
}

// The local disk selects the same pinned fork producers as the standalone image
// and chart outputs, while retaining the source-owned image-inputs handoff.
func TestNVKEOVNForkLocalDiskSelection(t *testing.T) {
	base, err := catalog.LoadContext(t.Context(), catalog.Dir("../../../definitions/nvke-m2/os-catalog"))
	if err != nil {
		t.Fatal(err)
	}
	overlay, err := catalog.LoadOverlayContext(t.Context(), "../../../definitions/nvke-local-dev-arm64/os-tcg.json")
	if err != nil {
		t.Fatal(err)
	}
	stack, err := catalog.ApplyStack(base, overlay)
	if err != nil {
		t.Fatal(err)
	}
	fork := loadOVNForkCatalog(t)
	for _, name := range []string{"ovn-kube-ubuntu", "ovn-kubernetes-chart", "nvke-cni-ovn"} {
		if !reflect.DeepEqual(stack.Catalog.Components[name], fork.Components[name]) {
			t.Fatalf("local disk producer %s differs from the pinned NVIDIA fork", name)
		}
	}
	if got := stack.Catalog.Components["nvke-image-inputs"].Materials["ovn"]; got != "nvke-cni-ovn" {
		t.Fatalf("OS material binding = %q, want fork Debian bundle", got)
	}
	d, err := definition.LoadDefinition(t.Context(), "../../../definitions/nvke-local-dev-arm64/definition-os.json")
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := buildgraph.Resolve(stack.Catalog, buildgraph.Selection{
		Components: d.Outputs[0].Components, Platform: d.Outputs[0].Platform, OutputFormat: d.Outputs[0].Format,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := buildgraph.CompileForHostPlatforms(resolved, map[string]execution.HostPlatform{
		"nvke-node-image": execution.HostPlatformLinuxARM64,
	}); err != nil {
		t.Fatal(err)
	}
	profiles, err := catalog.LoadContext(t.Context(), catalog.Dir("../../../definitions/nvke-local-dev-arm64/profiles-catalog"))
	if err != nil {
		t.Fatal(err)
	}
	profile := profiles.Components["nvke-local-profiles"]
	raw, err := os.ReadFile("../../../definitions/nvke-local-dev-arm64/profiles-catalog/patches/local-profile.patch")
	if err != nil {
		t.Fatal(err)
	}
	wantDigest := fmt.Sprintf("sha256:%x", sha256.Sum256(raw))
	if len(profile.Patches) != 1 || profile.Patches[0].SHA256 != wantDigest {
		t.Fatal("local profile does not bind the exact fork package installation input")
	}
	for _, kind := range []string{"charts", "images"} {
		if !strings.Contains(string(raw), "nke-cni-ovn-"+kind+"-"+ovnForkStream) {
			t.Fatalf("profile does not install fork %s package", kind)
		}
	}
	if strings.Contains(string(raw), "nke-cni-ovn-charts-1.4.0") {
		t.Fatal("local profile retains the upstream OVN stream")
	}
}

// The complete candidate selection is separate from the maintained Calico/OVN
// image inputs. These contracts never acquire the internal fork in ordinary CI.
func TestNVKEOVNForkGraphs(t *testing.T) {
	for _, arch := range []string{"amd64", "arm64"} {
		t.Run(arch, func(t *testing.T) {
			cat := loadOVNForkCatalog(t)
			if len(cat.Components) != 3 {
				t.Fatal("OVN selection must contain the image, chart and Debian bundle")
			}
			for _, component := range cat.Components {
				if !component.FromSource || component.Prebuilt != nil || component.Repo != "https://github.com/dsx-ai-factory/ovn-kubernetes" || component.Commit != ovnForkCommit {
					t.Fatal("OVN producer must use the locked fork source, without binary fallback")
				}
				if strings.Contains(strings.Join(component.BuildCommand, "\n"), "os-image/packages") {
					t.Fatal("OVN producer delegates packaging to the NVKE package folder")
				}
			}
			resolved, err := buildgraph.Resolve(cat, buildgraph.Selection{
				Components: []string{"ovn-kube-ubuntu", "ovn-kubernetes-chart", "nvke-cni-ovn"}, Platform: "linux/" + arch,
			})
			if err != nil {
				t.Fatal(err)
			}
			graph, err := buildgraph.Compile(resolved)
			if err != nil {
				t.Fatal(err)
			}
			builds := 0
			for _, node := range graph.Nodes {
				if node.Kind == ir.KindPull {
					t.Fatal("source-built OVN silently became an acquired image")
				}
				if node.Kind != ir.KindBuild {
					continue
				}
				builds++
				if node.Platform != "linux/"+arch || !node.SourceReadOnly || node.WriteDir != "nvkr-output" {
					t.Fatalf("lost native read-only source contract: %+v", node)
				}
				if node.Component == "ovn-kube-ubuntu" {
					if node.BuilderType != ir.BuilderBuildKit || node.Network != ir.NetworkDefault || node.OutputType != "oci-image" {
						t.Fatal("source-owned Dockerfile must retain the declared wrapped BuildKit lane")
					}
				} else if node.Component == "nvke-cni-ovn" {
					if node.Network != ir.NetworkNone || len(node.Materials) != 2 || node.Materials[0].Binding != "ovn-chart" || node.Materials[1].Binding != "ovn-image" {
						t.Fatalf("Debian packaging lost its exact offline chart/image materials: %+v", node)
					}
				} else if node.Component != "ovn-kubernetes-chart" || node.Network != ir.NetworkNone || len(node.Materials) != 0 {
					t.Fatalf("source chart must be packaged offline: %+v", node)
				}
			}
			if builds != 3 {
				t.Fatalf("build membership = %d, want 3", builds)
			}
			for _, missing := range []string{"ovn-kube-ubuntu", "ovn-kubernetes-chart"} {
				selected := []string{"nvke-cni-ovn", "ovn-kube-ubuntu", "ovn-kubernetes-chart"}
				selected = slices.DeleteFunc(selected, func(name string) bool { return name == missing })
				_, err := buildgraph.Resolve(cat, buildgraph.Selection{Components: selected, Platform: "linux/" + arch})
				var coded *nvkrerr.Error
				if !errors.As(err, &coded) || coded.Code != nvkrerr.CodeInvalidCatalog {
					t.Fatalf("missing %s material returned %v", missing, err)
				}
			}
			_ = compileOVNForkPlan(t, cat, arch, "podman")
		})
	}
}

// TestNVKEOVNForkPinBindings guards the supplied catalog's repeated literals.
// Re-pinning this candidate requires reviewing its stream and packaging together.
func TestNVKEOVNForkPinBindings(t *testing.T) {
	lock, err := catalog.LoadLockContext(t.Context(), "../../../definitions/nvke-ovn-source/catalog")
	if err != nil {
		t.Fatal(err)
	}
	pin := lock.Pins["ovn-kubernetes"]
	if pin.Commit != ovnForkCommit {
		t.Fatal("fork lock changed without reviewing the catalog's stream and packaging bindings")
	}
	cat := loadOVNForkCatalog(t)
	for _, tc := range []struct {
		component string
		fragments []string
	}{
		{component: "ovn-kube-ubuntu", fragments: []string{
			"name=localhost/nvke/ovn-kubernetes:git-" + pin.Commit,
			// The fork's vendored JSON adapter needs Go 1.26, not Go 1.27's
			// changed JSON v2 API. This is the measured patched 1.26.9 builder.
			"build-arg:BUILDER_IMAGE=" + ovnGoBuilder,
			`--opt "build-arg:SOURCE_DATE_EPOCH=$SOURCE_DATE_EPOCH"`,
		}},
		{component: "ovn-kubernetes-chart", fragments: []string{
			"grep -qx 'version: " + ovnForkStream + "' /src/charts/ovn-kubernetes/Chart.yaml",
		}},
		{component: "nvke-cni-ovn", fragments: []string{
			"readonly stream=" + ovnForkStream,
			"readonly revision=1+git." + pin.Commit[:12],
			"tag: git-" + pin.Commit,
			"readonly image_dir=/usr/lib/nke/cni-ovn/" + ovnForkStream,
			"readonly image_name=ovn-images_" + ovnForkStream + "_linux_{{.Arch}}.tgz",
			"Maintainer: NKE <nke-dev@exchange.nvidia.com>",
		}},
	} {
		t.Run(tc.component, func(t *testing.T) {
			component := cat.Components[tc.component]
			if component.Commit != pin.Commit || component.SourceDate != pin.SourceDateEpoch {
				t.Fatal("component source differs from its lock")
			}
			command := strings.Join(component.BuildCommand, "\n")
			for _, fragment := range tc.fragments {
				if !strings.Contains(command, fragment) {
					t.Fatalf("catalog command lost pinned binding %q", fragment)
				}
			}
		})
	}
}

// TestNVKEOVNForkImageInvocation runs only a local BuildKit argument fixture.
// It proves the catalog passes the epoch; it does not prove image reproduction.
func TestNVKEOVNForkImageInvocation(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, arch := range []string{"amd64", "arm64"} {
		t.Run(arch, func(t *testing.T) {
			cat := loadOVNForkCatalog(t)
			resolved, err := buildgraph.Resolve(cat, buildgraph.Selection{
				Components: []string{"ovn-kube-ubuntu"}, Platform: "linux/" + arch,
			})
			if err != nil {
				t.Fatal(err)
			}
			graph, err := buildgraph.Compile(resolved)
			if err != nil {
				t.Fatal(err)
			}
			for _, node := range graph.Nodes {
				if node.Kind != ir.KindBuild || node.Component != "ovn-kube-ubuntu" {
					continue
				}
				if len(node.Command) != 3 || node.Command[0] != "sh" || node.Command[1] != "-ec" {
					t.Fatal("unexpected BuildKit invocation")
				}
				root := t.TempDir()
				argsPath := filepath.Join(root, "arguments")
				if err := os.WriteFile(filepath.Join(root, "buildctl-daemonless.sh"),
					[]byte("#!/bin/sh\nprintf '%s\\n' \"$@\" >\"$TEST_ARGS\"\n"), 0o755); err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, sh, node.Command[1:]...)
				cmd.Env = []string{"PATH=" + root, "TEST_ARGS=" + argsPath}
				for _, env := range node.Env {
					cmd.Env = append(cmd.Env, env.Name+"="+env.Value)
				}
				cmd.WaitDelay = 5 * time.Second
				output, err := cmd.CombinedOutput()
				if err != nil || !strings.Contains(string(output), "OVN IMAGE BUILD:") {
					t.Fatalf("image invocation: %v: %s", err, output)
				}
				args, err := os.ReadFile(argsPath)
				if err != nil {
					t.Fatal(err)
				}
				for _, want := range []string{
					"platform=linux/" + arch,
					fmt.Sprintf("build-arg:SOURCE_DATE_EPOCH=%d", cat.Components["ovn-kube-ubuntu"].SourceDate),
					"type=oci,dest=/src/nvkr-output/ovn-kube-ubuntu.oci.tar,oci-mediatypes=true,rewrite-timestamp=true,name=localhost/nvke/ovn-kubernetes:git-" + ovnForkCommit,
				} {
					if !slices.Contains(strings.Split(string(args), "\n"), want) {
						t.Fatalf("missing BuildKit argument %q in %s", want, args)
					}
				}
				return
			}
			t.Fatal("missing OVN image producer")
		})
	}
}

func compileOVNForkPlan(t *testing.T, cat *catalog.Catalog, arch, containerRuntime string) *factory.Plan {
	t.Helper()
	stack, err := catalog.ApplyStack(cat)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := factory.Compile(t.Context(), definition.Definition{
		APIVersion: definition.DefinitionAPIVersion, Kind: definition.DefinitionKind,
		Metadata: definition.Metadata{Name: "ovn-source-diagnostic"}, Catalog: definition.Catalog{Base: "catalog"},
		Outputs: []definition.DefinitionOutput{{Name: "ovn", Platform: "linux/" + arch,
			Format: definition.OutputExecutable, Components: []string{"nvke-cni-ovn", "ovn-kube-ubuntu", "ovn-kubernetes-chart"}}},
	}, definition.Environment{
		APIVersion: definition.EnvironmentAPIVersion, Kind: definition.EnvironmentKind,
		Metadata:         definition.Metadata{Name: "ovn-source-diagnostic"},
		UtilityContainer: cat.Components["nvke-cni-ovn"].ToolchainImages[arch], UtilityPlatform: "linux/" + arch,
		Runtime: definition.Runtime{Runtime: containerRuntime, BuildNetworkPolicy: definition.BuildNetworkProducerDeclared},
	}, stack, "ovn-source-diagnostic")
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

// TestNVKEOVNForkLive exercises the real compiled producer operations and factory
// material owner. It deliberately stops before SBOM/package-publication operations:
// no Run/Release receipt, cluster acceptance or qualified execution is claimed.
func TestNVKEOVNForkLive(t *testing.T) {
	containerRuntime := os.Getenv("NVKR_OVN_SOURCE_RUNTIME")
	if containerRuntime == "" {
		t.Skip("set NVKR_OVN_SOURCE_RUNTIME=docker or podman on native Linux with fork source access")
	}
	if runtime.GOOS != "linux" || (runtime.GOARCH != "arm64" && runtime.GOARCH != "amd64") ||
		(containerRuntime != "podman" && containerRuntime != "docker") {
		t.Fatal("OVN source diagnostic requires native Linux ARM64/AMD64 and Docker/Podman")
	}
	cat := loadOVNForkCatalog(t)
	plan := compileOVNForkPlan(t, cat, runtime.GOARCH, containerRuntime)
	helmpath := buildOVNTestHelm(t, containerRuntime)
	t.Chdir(t.TempDir())
	ctx, cancel := context.WithTimeout(t.Context(), 65*time.Minute)
	defer cancel()
	t.Log("UNQUALIFIED OVN OPTION: internal source acquired on host; no credential mounted in build; Ubuntu APT dependencies are not source-built")
	completed := map[string]bool{}
	remaining := 0
	for _, node := range plan.Graph.Nodes {
		if node.IRNode != "" {
			remaining++
		}
	}
	for remaining > 0 {
		progress := false
		for _, node := range plan.Graph.Nodes {
			if node.IRNode == "" || completed[node.ID] {
				continue
			}
			ready := true
			for _, input := range node.Inputs {
				if !completed[input] {
					ready = false
				}
			}
			if !ready {
				continue
			}
			t.Logf("execute exact producer operation %s", node.ID)
			if err := factory.ExecuteNode(ctx, plan, node.ID, os.Stdout, os.Stderr); err != nil {
				t.Fatal(err)
			}
			completed[node.ID] = true
			for _, output := range node.Outputs {
				completed[output] = true
			}
			remaining--
			progress = true
		}
		if !progress {
			t.Fatal("producer operations depend on an unexecuted non-producer operation")
		}
	}
	var imagePath string
	for _, artifact := range plan.Graph.Artifacts {
		if artifact.BuildTarget == "ovn" && artifact.Kind == "oci-image" {
			imagePath = artifact.Path
		}
	}
	image, err := os.Open(imagePath)
	if err != nil {
		t.Fatal(err)
	}
	imageHash := sha256.New()
	imageSize, hashErr := io.Copy(imageHash, image)
	if err := errors.Join(hashErr, image.Close()); err != nil {
		t.Fatal(err)
	}
	imageDigest := fmt.Sprintf("%x", imageHash.Sum(nil))
	t.Logf("OVN OCI export: bytes=%d sha256=%s", imageSize, imageDigest)
	for _, artifact := range plan.Graph.Artifacts {
		if artifact.BuildTarget != "ovn" || artifact.Kind != "tarball" || artifact.Component != "nvke-cni-ovn" {
			continue
		}
		file, err := os.Open(artifact.Path)
		if err != nil {
			t.Fatal(err)
		}
		hash := sha256.New()
		size, readErr := io.Copy(hash, file)
		closeErr := file.Close()
		if err := errors.Join(readErr, closeErr); err != nil {
			t.Fatal(err)
		}
		t.Logf("OVN Debian bundle: bytes=%d sha256=%x", size, hash.Sum(nil))
		absolute, err := filepath.Abs(artifact.Path)
		if err != nil {
			t.Fatal(err)
		}
		check := `set -euo pipefail
tar -xf /input/bundle.tar -C /tmp
for kind in charts images; do
  package=/tmp/nvke-cni-ovn-${kind}-1.5.18_1+git.26cc199de8e8_$GOARCH.deb
  test "$(dpkg-deb -f "$package" Architecture)" = "$GOARCH"
  test "$(dpkg-deb -f "$package" Package)" = "nvke-cni-ovn-$kind-1.5.18"
  test "$(dpkg-deb -f "$package" Version)" = "1+git.26cc199de8e8"
  test "$(dpkg-deb -f "$package" Maintainer)" = 'NVKE <nke-dev@exchange.nvidia.com>'
  dpkg-deb -x "$package" /tmp/root
  dpkg-deb -e "$package" "/tmp/control-$kind"
done
test -f /tmp/root/etc/dgx/ovn-kubernetes/Chart.yaml
test -f /tmp/root/etc/dgx/ovn-kubernetes/values-nke.yaml
bash -n /tmp/control-images/postinst
cd /tmp/root/usr/lib/nke/cni-ovn/1.5.18
sha256sum --strict --quiet -c ovn-images_1.5.18_linux_$GOARCH.tgz.sha256
gzip -t ovn-images_1.5.18_linux_$GOARCH.tgz
test "$(gzip -dc ovn-images_1.5.18_linux_$GOARCH.tgz | sha256sum | cut -d' ' -f1)" = "$OVN_IMAGE_SHA256"
/tools/helm template ovn /tmp/root/etc/dgx/ovn-kubernetes \
  -f /tmp/root/etc/dgx/ovn-kubernetes/values-nke.yaml \
  --set k8sAPIServer=https://127.0.0.1:6443 --set podNetwork=10.244.0.0/16
`
		name := fmt.Sprintf("nvkr-ovn-check-%d-%s", os.Getpid(), filepath.Base(t.TempDir()))
		t.Cleanup(func() {
			cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
			defer stop()
			cmd := exec.CommandContext(cleanup, containerRuntime, "rm", "--force", "--ignore", name)
			// Docker has no --ignore; tolerate only its explicit already-removed result.
			if containerRuntime == "docker" {
				cmd = exec.CommandContext(cleanup, containerRuntime, "rm", "--force", name)
			}
			cmd.WaitDelay = 5 * time.Second
			output, err := cmd.CombinedOutput()
			if err != nil && !(containerRuntime == "docker" && strings.Contains(string(output), "No such container")) {
				t.Errorf("joining OVN inspection container cleanup: %v: %s", err, output)
			}
		})
		cmd := exec.CommandContext(ctx, containerRuntime, "run", "--name", name, "--rm", "--network=none", "--cap-drop=ALL",
			"--security-opt=no-new-privileges", "--read-only", "--tmpfs=/tmp:rw,size=2g", "-e", "GOARCH="+runtime.GOARCH,
			"-e", "OVN_IMAGE_SHA256="+imageDigest, "--volume", helmpath+":/tools/helm:ro",
			"--volume", absolute+":/input/bundle.tar:ro", cat.Components["nvke-cni-ovn"].ToolchainImages[runtime.GOARCH],
			"bash", "-ec", check)
		cmd.WaitDelay = 10 * time.Second
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("independent Debian inspection: %v: %s", err, output)
		}
		if err := checkOVNRenderedImages(output); err != nil {
			t.Fatal(err)
		}
		t.Log("independent Debian inspection, exact image correspondence and Helm image selection passed")
		return
	}
	t.Fatal("compiled producer graph did not retain its Debian tar output")
}

func buildOVNTestHelm(t *testing.T, containerRuntime string) string {
	t.Helper()
	resolved, err := buildgraph.Resolve(loadPublicOSSourceCatalog(t), buildgraph.Selection{
		Components: []string{"helm-3-20-2"}, Platform: "linux/" + runtime.GOARCH,
	})
	if err != nil {
		t.Fatal(err)
	}
	graph, err := buildgraph.Compile(resolved)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := graph.Digest()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Minute)
	defer cancel()
	report, err := (pipeline.Local{}).Run(graph, digest, pipeline.RunOptions{
		Ctx: ctx, Runtime: containerRuntime, SourceRoot: filepath.Join(t.TempDir(), "helm"),
		Stdout: os.Stdout, Stderr: os.Stderr,
	})
	if report != nil {
		t.Cleanup(func() {
			if err := report.Close(); err != nil {
				t.Error(err)
			}
		})
	}
	if err != nil {
		t.Fatal(err)
	}
	if report == nil || len(report.Artifacts) != 1 || report.Artifacts[0].Component != "helm-3-20-2" {
		t.Fatal("missing source-built Helm test tool")
	}
	return report.Artifacts[0].AbsPath
}

// YAML decoding is delegated to the existing library. This is a regression
// assertion over Helm's rendered fixture, not a deployment admission validator.
func checkOVNRenderedImages(rendered []byte) error {
	decoder := yaml.NewDecoder(strings.NewReader(string(rendered)))
	images := 0
	var visit func(*yaml.Node) error
	visit = func(node *yaml.Node) error {
		if node.Kind == yaml.MappingNode {
			for i := 0; i < len(node.Content); i += 2 {
				if node.Content[i].Value == "image" && node.Content[i+1].Kind == yaml.ScalarNode {
					value := node.Content[i+1].Value
					if value != "localhost/nvke/ovn-kubernetes:git-"+ovnForkCommit {
						return fmt.Errorf("rendered OVN image %q does not match the built export", value)
					}
					images++
				}
			}
		}
		for _, child := range node.Content {
			if err := visit(child); err != nil {
				return err
			}
		}
		return nil
	}
	for {
		var document yaml.Node
		if err := decoder.Decode(&document); err == io.EOF {
			break
		} else if err != nil {
			return err
		}
		if err := visit(&document); err != nil {
			return err
		}
	}
	if images == 0 {
		return fmt.Errorf("Helm rendered no OVN images")
	}
	return nil
}

func TestNVKEOVNForkRenderedImages(t *testing.T) {
	for _, test := range []struct {
		name, document string
		wantError      bool
	}{
		{name: "built image", document: "spec:\n  containers:\n    - image: localhost/nvke/ovn-kubernetes:git-" + ovnForkCommit},
		{name: "old NVCR default", document: "spec:\n  containers:\n    - image: nvcr.io/nv-ngn/sdn/ovnkube:old", wantError: true},
		{name: "no images", document: "kind: ConfigMap", wantError: true},
		{name: "invalid YAML", document: "[", wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := checkOVNRenderedImages([]byte(test.document)); (err != nil) != test.wantError {
				t.Fatalf("rendered image check = %v, wantError=%t", err, test.wantError)
			}
		})
	}
}

func TestNVKEOVNPostinstImportDiagnostics(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Fatal(err)
	}
	for _, arch := range []string{"amd64", "arm64"} {
		t.Run(arch, func(t *testing.T) {
			resolved, err := buildgraph.Resolve(loadOVNForkCatalog(t), buildgraph.Selection{
				Components: []string{"nvke-cni-ovn", "ovn-kube-ubuntu", "ovn-kubernetes-chart"}, Platform: "linux/" + arch,
			})
			if err != nil {
				t.Fatal(err)
			}
			graph, err := buildgraph.Compile(resolved)
			if err != nil {
				t.Fatal(err)
			}
			var hook string
			for _, node := range graph.Nodes {
				if node.Kind != ir.KindBuild || node.Component != "nvke-cni-ovn" {
					continue
				}
				if len(node.Command) != 3 {
					t.Fatal("unexpected OVN packaging command")
				}
				// Exercise the compiled catalog's fixed hook heredoc; Bash owns its syntax.
				_, rest, found := strings.Cut(node.Command[2], "cat >\"$images/DEBIAN/postinst\" <<'EOF'\n")
				if !found {
					t.Fatal("missing OVN postinst heredoc")
				}
				hook, _, found = strings.Cut(rest, "\nEOF\n")
				if !found || !strings.Contains(hook, "ovn-images_1.5.18_linux_"+arch+".tgz") {
					t.Fatal("missing native OVN postinst hook")
				}
			}
			if hook == "" {
				t.Fatal("missing OVN package producer")
			}
			for _, tc := range []struct {
				name, argument, importStatus, gzipStatus string
				override, wantCtr, wantDiagnostic        string
				localCtr, pathCtr, wantImport            bool
			}{
				{name: "explicit override", argument: "configure", importStatus: "0", gzipStatus: "0", override: "override-ctr", localCtr: true, pathCtr: true, wantCtr: "override-ctr", wantImport: true},
				{name: "bare executable override", argument: "configure", importStatus: "0", gzipStatus: "0", override: "ctr", localCtr: true, pathCtr: true, wantCtr: "ctr", wantImport: true},
				{name: "import failure", argument: "configure", importStatus: "42", gzipStatus: "0", override: "override-ctr", wantCtr: "override-ctr", wantImport: true, wantDiagnostic: "OVN import failed; see "},
				{name: "gzip failure", argument: "configure", importStatus: "0", gzipStatus: "43", override: "override-ctr", wantCtr: "override-ctr", wantImport: true, wantDiagnostic: "OVN import failed; see "},
				{name: "other package action", argument: "abort-upgrade", override: "missing-ctr"},
				{name: "missing override", argument: "configure", override: "missing-ctr", localCtr: true, pathCtr: true, wantDiagnostic: "OVN import: ctr is not executable: "},
				{name: "nonexecutable override", argument: "configure", override: "nonexecutable-ctr", localCtr: true, pathCtr: true, wantDiagnostic: "OVN import: ctr is not executable: "},
				{name: "local binary preference", argument: "configure", importStatus: "0", gzipStatus: "0", localCtr: true, pathCtr: true, wantCtr: "local-ctr", wantImport: true},
				{name: "PATH fallback", argument: "configure", importStatus: "0", gzipStatus: "0", pathCtr: true, wantCtr: "ctr", wantImport: true},
				{name: "absent ctr", argument: "configure", wantDiagnostic: "OVN import: ctr not found"},
			} {
				t.Run(tc.name, func(t *testing.T) {
					root := t.TempDir()
					logPath := filepath.Join(root, "import.log")
					callsPath := filepath.Join(root, "ctr-calls")
					for name, script := range map[string]string{
						"sha256sum": "#!/bin/sh\nexit 0\n",
						"gzip":      "#!/bin/sh\nprintf image\nexit \"$TEST_GZIP_STATUS\"\n",
						"timeout":   "#!/bin/sh\nshift 2\nexec \"$@\"\n",
					} {
						if err := os.WriteFile(filepath.Join(root, name), []byte(script), 0o755); err != nil {
							t.Fatal(err)
						}
					}
					ctrScript := "#!/bin/sh\nprintf '%s %s\\n' \"$0\" \"$1\" >>\"$TEST_CTR_CALLS\"\n" +
						"[ \"$1\" = version ] && exit 0\n" +
						"[ \"$*\" = '-n k8s.io images import -' ] || exit 99\n" +
						"while IFS= read -r line; do :; done\nprintf 'private import details\\n' >&2\nexit \"$TEST_IMPORT_STATUS\"\n"
					for name, enabled := range map[string]bool{
						"override-ctr": tc.override == "override-ctr", "local-ctr": tc.localCtr, "ctr": tc.pathCtr,
					} {
						if enabled {
							if err := os.WriteFile(filepath.Join(root, name), []byte(ctrScript), 0o755); err != nil {
								t.Fatal(err)
							}
						}
					}
					if tc.override == "nonexecutable-ctr" {
						if err := os.WriteFile(filepath.Join(root, tc.override), []byte(ctrScript), 0o644); err != nil {
							t.Fatal(err)
						}
					}
					// Relocate only host paths into test-owned scratch. External commands
					// are controlled fixtures; the actual pipeline and failure handler run.
					script := strings.ReplaceAll(hook, "/usr/lib/nke/cni-ovn/1.5.18", root)
					script = strings.ReplaceAll(script, "/var/log/nke-cni-ovn-postinst.log", logPath)
					script = strings.ReplaceAll(script, "/usr/local/bin/ctr", filepath.Join(root, "local-ctr"))
					// Allow subprocess scheduling under the concurrent full-package gate;
					// this test bound does not change the hook's production timeouts.
					ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
					defer cancel()
					cmd := exec.CommandContext(ctx, bash, "--noprofile", "--norc", "-c", script, "postinst", tc.argument)
					// Isolate PATH so a host ctr cannot make missing-tool cases pass.
					cmd.Env = []string{"PATH=" + root, "TEST_CTR_CALLS=" + callsPath,
						"TEST_IMPORT_STATUS=" + tc.importStatus, "TEST_GZIP_STATUS=" + tc.gzipStatus}
					if tc.override != "" {
						override := filepath.Join(root, tc.override)
						if tc.override == "ctr" {
							override = "ctr"
						}
						cmd.Env = append(cmd.Env, "NKE_CTR_BIN="+override)
					}
					cmd.WaitDelay = 5 * time.Second
					output, err := cmd.CombinedOutput()
					if ctx.Err() != nil {
						t.Fatalf("hook did not finish: %v", ctx.Err())
					}
					if tc.wantDiagnostic != "" {
						var exit *exec.ExitError
						if !errors.As(err, &exit) || exit.ExitCode() != 1 || !strings.Contains(string(output), tc.wantDiagnostic) {
							t.Fatalf("missing failure status/diagnostic: %v: %s", err, output)
						}
						if tc.wantImport && !strings.Contains(string(output), logPath) {
							t.Fatalf("missing import log path: %s", output)
						}
					} else if err != nil || len(output) != 0 {
						t.Fatalf("successful package action changed: %v: %s", err, output)
					}
					if strings.Contains(string(output), "private import details") {
						t.Fatal("hook dumped import log contents")
					}
					calls, callsErr := os.ReadFile(callsPath)
					if tc.wantCtr != "" {
						selected := filepath.Join(root, tc.wantCtr)
						if callsErr != nil || string(calls) != selected+" version\n"+selected+" -n\n" {
							t.Fatalf("unexpected ctr selection/calls: %v: %q", callsErr, calls)
						}
					} else if !errors.Is(callsErr, os.ErrNotExist) {
						t.Fatalf("invalid tool or other package action reached readiness: %v: %q", callsErr, calls)
					}
					log, readErr := os.ReadFile(logPath)
					if tc.wantImport {
						if readErr != nil || string(log) != "private import details\n" {
							t.Fatalf("import output was not retained in its log: %v: %q", readErr, log)
						}
					} else if !errors.Is(readErr, os.ErrNotExist) {
						t.Fatalf("rejected tool or other package action ran import: %v", readErr)
					}
				})
			}
		})
	}
}
