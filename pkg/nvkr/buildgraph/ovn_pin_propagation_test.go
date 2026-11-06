// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package buildgraph_test

import (
	"archive/tar"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/buildgraph"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

func ovnFixtureGraph(t *testing.T, cat *catalog.Catalog, arch string) *ir.Graph {
	t.Helper()
	selected, err := buildgraph.Resolve(cat, buildgraph.Selection{Components: []string{
		"nvke-cni-ovn", "ovn-kube-ubuntu", "ovn-kubernetes-chart",
	}, Platform: "linux/" + arch})
	if err != nil {
		t.Fatal(err)
	}
	graph, err := buildgraph.Compile(selected)
	if err != nil {
		t.Fatal(err)
	}
	return graph
}

// This fixture executes the catalog's packaging script. External Debian packing
// and timestamp normalization are modeled; values, control files, installer and
// gzip payload are produced by the actual shell. It makes no build/runtime claim.
func runOVNPackageFixture(t *testing.T, cat *catalog.Catalog, arch, chartStream string) (string, error) {
	t.Helper()
	root := t.TempDir()
	tools := filepath.Join(root, "tools")
	inputs := filepath.Join(root, "inputs")
	for _, dir := range []string{tools, inputs} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	chart, err := os.Create(filepath.Join(inputs, "ovn-chart"))
	if err != nil {
		t.Fatal(err)
	}
	archive := tar.NewWriter(chart)
	body := "name: ovn-kubernetes\nversion: " + chartStream + "\n"
	if err := archive.WriteHeader(&tar.Header{Name: "ovn-kubernetes/Chart.yaml", Mode: 0o644, Size: int64(len(body))}); err != nil {
		t.Fatal(err)
	}
	if _, err := archive.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := chart.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(inputs, "ovn-image"), []byte("synthetic OCI export"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"tar": `#!/bin/bash
args=()
for arg; do case "$arg" in --*) ;; *) args+=("$arg");; esac; done
exec /usr/bin/tar "${args[@]}"
`,
		"touch":     "#!/bin/sh\nexit 0\n",
		"sha256sum": "#!/bin/sh\nprintf '%064d  %s\\n' 0 \"$1\"\n",
		"dpkg-deb":  "#!/bin/bash\nprintf '%s\\n' \"$@\" >>\"$TEST_DEB_ARGS\"\n: >\"${@: -1}\"\n",
	} {
		if err := os.WriteFile(filepath.Join(tools, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, node := range ovnFixtureGraph(t, cat, arch).Nodes {
		if node.Kind != ir.KindBuild || node.Component != "nvke-cni-ovn" {
			continue
		}
		command := strings.NewReplacer("/src", root, "/nvkr/inputs/materials", inputs).Replace(node.Command[2])
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "bash", "--noprofile", "--norc", "-ec", command)
		cmd.Env = []string{"PATH=" + tools + ":/usr/bin:/bin", "TEST_DEB_ARGS=" + filepath.Join(root, "deb-args"), "NKE_CTR_BIN=must-remain-installer-time"}
		for _, variable := range node.Env {
			cmd.Env = append(cmd.Env, variable.Name+"="+variable.Value)
		}
		cmd.WaitDelay = time.Second
		out, err := cmd.CombinedOutput()
		if err != nil {
			return root, fmt.Errorf("package fixture: %w: %s", err, out)
		}
		return root, nil
	}
	t.Fatal("missing package producer")
	return "", nil
}

func TestOVNLockOnlyPropagation(t *testing.T) {
	versions := []struct {
		stream            string
		packageCompatible bool
	}{
		{"2.4.2", true},
		{"2.4.3", true},
		{"2.4.3-rc.1", true},
		{"2.4.3+build.1", true},
		{"2.4.3-rc.1+build.1", true},
		{"2.4.4-RC.1", false},
	}
	upstream := t.TempDir()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "url."+upstream+".insteadOf")
	t.Setenv("GIT_CONFIG_VALUE_0", "https://pin-propagation.example.invalid/ovn")
	git := func(args ...string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir, cmd.WaitDelay = upstream, time.Second
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Fixture", "GIT_COMMITTER_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid", "GIT_COMMITTER_EMAIL=fixture@example.invalid", "GIT_AUTHOR_DATE=2026-01-01T00:00:00Z", "GIT_COMMITTER_DATE=2026-01-01T00:00:00Z")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git fixture: %v: %s", err, out)
		}
	}
	git("init", "-q", "--template=", "--object-format=sha1", "--initial-branch=main")
	for _, tc := range versions {
		version := tc.stream
		if err := os.WriteFile(filepath.Join(upstream, "Chart.yaml"), []byte("version: "+version+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		git("add", "Chart.yaml")
		git("-c", "commit.gpgsign=false", "commit", "-qm", "fixture "+version)
		git("tag", "v"+version)
	}
	for _, flavor := range []string{"nvidia", "upstream"} {
		t.Run(flavor, func(t *testing.T) {
			directory := t.TempDir()
			original := "../../../inputs/catalogs/ovn-" + flavor
			registry, err := os.ReadFile(filepath.Join(original, catalog.RegistryFile))
			if err != nil {
				t.Fatal(err)
			}
			sourceRepo := "https://github.com/dsx-ai-factory/ovn-kubernetes"
			if flavor == "upstream" {
				sourceRepo = "https://github.com/ovn-kubernetes/ovn-kubernetes"
			}
			registry = []byte(strings.ReplaceAll(string(registry), sourceRepo, "https://pin-propagation.example.invalid/ovn"))
			if err := os.WriteFile(filepath.Join(directory, catalog.RegistryFile), registry, 0o600); err != nil {
				t.Fatal(err)
			}
			lock, err := os.ReadFile(filepath.Join(original, catalog.LockFile))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(directory, catalog.LockFile), lock, 0o600); err != nil {
				t.Fatal(err)
			}
			var previous string
			for _, tc := range versions {
				version := tc.stream
				t.Run(version, func(t *testing.T) {
					if _, err := catalog.UpdateSourcePins(t.Context(), catalog.SourcePinRequest{Directory: directory, Sources: []string{"ovn-kubernetes"}, Tag: "v" + version, Runtime: "docker"}); err != nil {
						t.Fatal(err)
					}
					cat, err := catalog.LoadContext(t.Context(), catalog.Dir(directory))
					if err != nil {
						t.Fatal(err)
					}
					pin, err := cat.SourcePin("ovn-kubernetes")
					if err != nil {
						t.Fatal(err)
					}
					graph := ovnFixtureGraph(t, cat, "arm64")
					assertOVNImagePinInvocation(t, graph, pin.Commit)
					assertOVNChartVersionRefusal(t, graph)
					digest, err := graph.Digest()
					if err != nil || digest == previous {
						t.Fatalf("pin did not change Build identity: %s %v", digest, err)
					}
					previous = digest
					root, err := runOVNPackageFixture(t, cat, "arm64", version)
					if got, err := os.ReadFile(filepath.Join(directory, catalog.RegistryFile)); err != nil || string(got) != string(registry) {
						t.Fatal("pin update changed registry bytes")
					}
					if !tc.packageCompatible {
						if err == nil {
							t.Fatal("package-incompatible stream passed packaging")
						}
						if _, err := os.Stat(filepath.Join(root, "nvkr-output")); !os.IsNotExist(err) {
							t.Fatal("package-incompatible stream wrote partial output")
						}
						return
					}
					if err != nil {
						t.Fatal(err)
					}
					for file, fragments := range map[string][]string{
						"nvkr-output/charts/etc/dgx/ovn-kubernetes/values-nke.yaml": {"tag: git-" + pin.Commit},
						"nvkr-output/charts/DEBIAN/control":                         {"Package: nke-cni-ovn-charts-" + version, "Version: 1+git." + pin.Commit[:12]},
						"nvkr-output/images/DEBIAN/postinst":                        {"/usr/lib/nke/cni-ovn/" + version, "ovn-images_" + version + "_linux_arm64.tgz", `${1:-}`, `${NKE_CTR_BIN:-}`},
						"deb-args":                                                  {"nke-cni-ovn-images-" + version + "_1+git." + pin.Commit[:12] + "_arm64.deb"},
					} {
						raw, err := os.ReadFile(filepath.Join(root, file))
						if err != nil {
							t.Fatal(err)
						}
						for _, fragment := range fragments {
							if !strings.Contains(string(raw), fragment) {
								t.Fatalf("%s lost pin-derived value %q: %s", file, fragment, raw)
							}
						}
					}
					if _, err := runOVNPackageFixture(t, cat, "arm64", "0.0.1"); err == nil {
						t.Fatal("source tag/chart disagreement passed packaging")
					}
				})
			}
		})
	}
}

func assertOVNImagePinInvocation(t *testing.T, graph *ir.Graph, commit string) {
	t.Helper()
	root := t.TempDir()
	arguments := filepath.Join(root, "arguments")
	if err := os.WriteFile(filepath.Join(root, "buildctl-daemonless.sh"), []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" >\"$TEST_ARGS\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, node := range graph.Nodes {
		if node.Kind != ir.KindBuild || node.Component != "ovn-kube-ubuntu" {
			continue
		}
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "/bin/sh", node.Command[1:]...)
		cmd.Env = []string{"PATH=" + root, "TEST_ARGS=" + arguments}
		for _, variable := range node.Env {
			cmd.Env = append(cmd.Env, variable.Name+"="+variable.Value)
		}
		cmd.WaitDelay = time.Second
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("image invocation: %v: %s", err, out)
		}
		raw, err := os.ReadFile(arguments)
		if err != nil || !strings.Contains(string(raw), "name=localhost/nvke/ovn-kubernetes:git-"+commit+"\n") {
			t.Fatalf("image tag did not follow pin: %s %v", raw, err)
		}
		return
	}
	t.Fatal("missing image producer")
}

func assertOVNChartVersionRefusal(t *testing.T, graph *ir.Graph) {
	t.Helper()
	root := t.TempDir()
	for _, relative := range []string{"charts/ovn-kubernetes", "helm/ovn-kubernetes"} {
		directory := filepath.Join(root, relative)
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, "Chart.yaml"), []byte("version: 0.0.1\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, node := range graph.Nodes {
		if node.Kind != ir.KindBuild || node.Component != "ovn-kubernetes-chart" {
			continue
		}
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "bash", "-ec", strings.ReplaceAll(node.Command[2], "/src", root))
		cmd.Env = []string{"PATH=/usr/bin:/bin"}
		for _, variable := range node.Env {
			cmd.Env = append(cmd.Env, variable.Name+"="+variable.Value)
		}
		cmd.WaitDelay = time.Second
		if out, err := cmd.CombinedOutput(); err == nil {
			t.Fatalf("chart version mismatch succeeded: %s", out)
		}
		if _, err := os.Stat(filepath.Join(root, "nvkr-output")); !os.IsNotExist(err) {
			t.Fatal("chart mismatch wrote partial output")
		}
		return
	}
	t.Fatal("missing chart producer")
}
