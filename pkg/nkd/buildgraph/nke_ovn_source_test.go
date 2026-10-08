// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package buildgraph_test

// This file validates NVIDIA's supplied OVN catalog through generic NKD APIs.
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
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/buildgraph"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/catalog"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/definition"
	nkderr "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/factory"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/pipeline"
	"go.yaml.in/yaml/v3"
)

const ovnForkCommit = "26cc199de8e814a7fe6d899a770fc04df0bbc2f9"

func loadOVNForkCatalog(t *testing.T) *catalog.Catalog {
	t.Helper()
	cat, err := catalog.LoadContext(t.Context(), catalog.Dir("../../../definitions/nke-ovn-source/catalog"))
	if err != nil {
		t.Fatal(err)
	}
	return cat
}

// The complete candidate selection is separate from the maintained Calico/OVN
// image inputs. These contracts never acquire the internal fork in ordinary CI.
func TestNKEOVNForkGraphs(t *testing.T) {
	for _, arch := range []string{"amd64", "arm64"} {
		t.Run(arch, func(t *testing.T) {
			cat := loadOVNForkCatalog(t)
			if len(cat.Components) != 3 {
				t.Fatal("OVN selection must contain the image, chart and Debian bundle")
			}
			for _, component := range cat.Components {
				if !component.FromSource || component.Prebuilt != nil || component.Repo != "https://github.com/dsx-ai-factory/ovn-kubernetes" || component.Commit != ovnForkCommit {
					t.Fatal("OVN producer must use the requested exact source, without binary fallback")
				}
				if strings.Contains(strings.Join(component.BuildCommand, "\n"), "os-image/packages") {
					t.Fatal("OVN producer delegates packaging to the NKE package folder")
				}
			}
			resolved, err := buildgraph.Resolve(cat, buildgraph.Selection{
				Components: []string{"ovn-kube-ubuntu", "ovn-kubernetes-chart", "nke-cni-ovn"}, Platform: "linux/" + arch,
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
				if node.Platform != "linux/"+arch || !node.SourceReadOnly || node.WriteDir != "nkd-output" {
					t.Fatalf("lost native read-only source contract: %+v", node)
				}
				if node.Component == "ovn-kube-ubuntu" {
					if node.BuilderType != ir.BuilderBuildKit || node.Network != ir.NetworkDefault || node.OutputType != "oci-image" {
						t.Fatal("source-owned Dockerfile must retain the declared wrapped BuildKit lane")
					}
				} else if node.Component == "nke-cni-ovn" {
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
				selected := []string{"nke-cni-ovn", "ovn-kube-ubuntu", "ovn-kubernetes-chart"}
				selected = slices.DeleteFunc(selected, func(name string) bool { return name == missing })
				_, err := buildgraph.Resolve(cat, buildgraph.Selection{Components: selected, Platform: "linux/" + arch})
				var coded *nkderr.Error
				if !errors.As(err, &coded) || coded.Code != nkderr.CodeInvalidCatalog {
					t.Fatalf("missing %s material returned %v", missing, err)
				}
			}
			_ = compileOVNForkPlan(t, cat, arch, "podman")
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
			Format: definition.OutputExecutable, Components: []string{"nke-cni-ovn", "ovn-kube-ubuntu", "ovn-kubernetes-chart"}}},
	}, definition.Environment{
		APIVersion: definition.EnvironmentAPIVersion, Kind: definition.EnvironmentKind,
		Metadata:         definition.Metadata{Name: "ovn-source-diagnostic"},
		UtilityContainer: cat.Components["nke-cni-ovn"].ToolchainImages[arch], UtilityPlatform: "linux/" + arch,
		Runtime: definition.Runtime{Runtime: containerRuntime, BuildNetworkPolicy: definition.BuildNetworkProducerDeclared},
	}, stack, "ovn-source-diagnostic")
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

// TestNKEOVNForkLive exercises the real compiled producer operations and factory
// material owner. It deliberately stops before SBOM/package-publication operations:
// no Run/Release receipt, cluster acceptance or qualified execution is claimed.
func TestNKEOVNForkLive(t *testing.T) {
	containerRuntime := os.Getenv("NKD_OVN_SOURCE_RUNTIME")
	if containerRuntime == "" {
		t.Skip("set NKD_OVN_SOURCE_RUNTIME=docker or podman on native Linux with fork source access")
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
		if artifact.BuildTarget != "ovn" || artifact.Kind != "tarball" || artifact.Component != "nke-cni-ovn" {
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
  package=/tmp/nke-cni-ovn-${kind}-1.5.18_1+git.26cc199de8e8_$GOARCH.deb
  test "$(dpkg-deb -f "$package" Architecture)" = "$GOARCH"
  test "$(dpkg-deb -f "$package" Package)" = "nke-cni-ovn-$kind-1.5.18"
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
		name := fmt.Sprintf("nkd-ovn-check-%d-%s", os.Getpid(), filepath.Base(t.TempDir()))
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
			"--volume", absolute+":/input/bundle.tar:ro", cat.Components["nke-cni-ovn"].ToolchainImages[runtime.GOARCH],
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
					if value != "localhost/nke/ovn-kubernetes:git-"+ovnForkCommit {
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

func TestNKEOVNForkRenderedImages(t *testing.T) {
	for _, test := range []struct {
		name, document string
		wantError      bool
	}{
		{name: "built image", document: "spec:\n  containers:\n    - image: localhost/nke/ovn-kubernetes:git-" + ovnForkCommit},
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

func TestNKEOVNPostinstImportDiagnostics(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Fatal(err)
	}
	for _, arch := range []string{"amd64", "arm64"} {
		t.Run(arch, func(t *testing.T) {
			resolved, err := buildgraph.Resolve(loadOVNForkCatalog(t), buildgraph.Selection{
				Components: []string{"nke-cni-ovn", "ovn-kube-ubuntu", "ovn-kubernetes-chart"}, Platform: "linux/" + arch,
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
				if node.Kind != ir.KindBuild || node.Component != "nke-cni-ovn" {
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
				wantFailure                              bool
			}{
				{name: "success", argument: "configure", importStatus: "0", gzipStatus: "0"},
				{name: "import failure", argument: "configure", importStatus: "42", gzipStatus: "0", wantFailure: true},
				{name: "gzip failure", argument: "configure", importStatus: "0", gzipStatus: "43", wantFailure: true},
				{name: "other package action", argument: "abort-upgrade", importStatus: "42", gzipStatus: "43"},
			} {
				t.Run(tc.name, func(t *testing.T) {
					root := t.TempDir()
					logPath := filepath.Join(root, "import.log")
					for name, script := range map[string]string{
						"sha256sum": "#!/bin/sh\nexit 0\n",
						"gzip":      "#!/bin/sh\nprintf image\nexit \"$TEST_GZIP_STATUS\"\n",
						"timeout":   "#!/bin/sh\nshift 2\nexec \"$@\"\n",
						"ctr": "#!/bin/sh\n[ \"$1\" = version ] && exit 0\n" +
							"[ \"$*\" = '-n k8s.io images import -' ] || exit 99\n" +
							"cat >/dev/null\nprintf 'private import details\\n' >&2\nexit \"$TEST_IMPORT_STATUS\"\n",
					} {
						if err := os.WriteFile(filepath.Join(root, name), []byte(script), 0o755); err != nil {
							t.Fatal(err)
						}
					}
					// Relocate only host paths into test-owned scratch. External commands
					// are controlled fixtures; the actual pipeline and failure handler run.
					script := strings.ReplaceAll(hook, "/usr/lib/nke/cni-ovn/1.5.18", root)
					script = strings.ReplaceAll(script, "/var/log/nke-cni-ovn-postinst.log", logPath)
					ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
					defer cancel()
					cmd := exec.CommandContext(ctx, bash, "--noprofile", "--norc", "-c", script, "postinst", tc.argument)
					cmd.Env = []string{"PATH=" + root + string(os.PathListSeparator) + os.Getenv("PATH"),
						"NKE_CTR_BIN=" + filepath.Join(root, "ctr"), "TEST_IMPORT_STATUS=" + tc.importStatus, "TEST_GZIP_STATUS=" + tc.gzipStatus}
					cmd.WaitDelay = time.Second
					output, err := cmd.CombinedOutput()
					if ctx.Err() != nil {
						t.Fatalf("hook did not finish: %v", ctx.Err())
					}
					if tc.wantFailure {
						var exit *exec.ExitError
						if !errors.As(err, &exit) || exit.ExitCode() != 1 || !strings.Contains(string(output), "OVN import failed; see "+logPath) {
							t.Fatalf("missing import failure status/diagnostic: %v: %s", err, output)
						}
					} else if err != nil || len(output) != 0 {
						t.Fatalf("successful package action changed: %v: %s", err, output)
					}
					if strings.Contains(string(output), "private import details") {
						t.Fatal("hook dumped import log contents")
					}
					log, readErr := os.ReadFile(logPath)
					if tc.argument == "configure" {
						if readErr != nil || string(log) != "private import details\n" {
							t.Fatalf("import output was not retained in its log: %v: %q", readErr, log)
						}
					} else if !errors.Is(readErr, os.ErrNotExist) {
						t.Fatalf("other package action ran import: %v", readErr)
					}
				})
			}
		})
	}
}
