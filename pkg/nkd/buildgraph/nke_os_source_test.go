// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package buildgraph_test

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"debug/elf"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/buildgraph"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/catalog"
	nkderr "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/pipeline"
)

type publicOSSourceCase struct {
	name, repository, tag string
	members               []string
}

func publicOSSourceCases() []publicOSSourceCase {
	return []publicOSSourceCase{
		{name: "cni-plugins-1-6-0", repository: "containernetworking/plugins", tag: "v1.6.0",
			members: strings.Fields("bandwidth bridge dhcp dummy firewall host-device host-local ipvlan loopback macvlan portmap ptp sbr static tap tuning vlan vrf")},
		{name: "containerd-2-1-7", repository: "containerd/containerd", tag: "v2.1.7", members: strings.Fields("containerd containerd-shim-runc-v2 ctr")},
		{name: "containerd-2-2-3", repository: "containerd/containerd", tag: "v2.2.3", members: strings.Fields("containerd containerd-shim-runc-v2 ctr")},
		{name: "containerd-2-3-0", repository: "containerd/containerd", tag: "v2.3.0", members: strings.Fields("containerd containerd-shim-runc-v2 ctr")},
		{name: "crictl-1-34-0", repository: "kubernetes-sigs/cri-tools", tag: "v1.34.0"},
		{name: "crictl-1-35-0", repository: "kubernetes-sigs/cri-tools", tag: "v1.35.0"},
		{name: "crictl-1-36-0", repository: "kubernetes-sigs/cri-tools", tag: "v1.36.0"},
		{name: "get-tpm-pubhash-1-9-0", repository: "spiffe/spire-tpm-plugin", tag: "v1.9.0"},
		{name: "helm-3-20-2", repository: "helm/helm", tag: "v3.20.2"},
		{name: "helm-4-1-4", repository: "helm/helm", tag: "v4.1.4"},
		{name: "kubernetes-1-34-2", repository: "kubernetes/kubernetes", tag: "v1.34.2", members: kubernetesBinaryMembers()},
		{name: "kubernetes-1-35-3", repository: "kubernetes/kubernetes", tag: "v1.35.3", members: kubernetesBinaryMembers()},
		{name: "kubernetes-1-36-3", repository: "kubernetes/kubernetes", tag: "v1.36.3", members: kubernetesBinaryMembers()},
		{name: "tpm-attestor-agent-1-9-0", repository: "spiffe/spire-tpm-plugin", tag: "v1.9.0"},
	}
}

func kubernetesBinaryMembers() []string {
	return strings.Fields("kube-apiserver kube-controller-manager kube-proxy kube-scheduler kubeadm kubectl kubelet")
}

func loadPublicOSSourceCatalog(t *testing.T) *catalog.Catalog {
	t.Helper()
	cat, err := catalog.LoadContext(t.Context(), catalog.Dir("../../../definitions/nke-os-source/catalog"))
	if err != nil {
		t.Fatal(err)
	}
	return cat
}

func TestNKEOSPublicSourceGraphs(t *testing.T) {
	cat := loadPublicOSSourceCatalog(t)
	cases := publicOSSourceCases()
	if len(cat.Components) != len(cases) {
		t.Fatalf("producer membership = %d, want %d", len(cat.Components), len(cases))
	}
	for _, platform := range []string{"linux/amd64", "linux/arm64"} {
		for _, test := range cases {
			t.Run(platform+"/"+test.name, func(t *testing.T) {
				component, ok := cat.Components[test.name]
				if !ok || !component.FromSource || component.Prebuilt != nil {
					t.Fatalf("%s must remain source-built, not acquired", test.name)
				}
				if component.Repo != "https://github.com/"+test.repository || component.Tag != test.tag {
					t.Fatalf("source = %s @ %s, want %s @ %s", component.Repo, component.Tag, test.repository, test.tag)
				}
				workMode := "GOWORK=off"
				if test.repository == "kubernetes/kubernetes" {
					workMode = "GOWORK=/src/go.work"
				}
				if !strings.Contains(strings.Join(component.BuildCommand, "\n"), workMode) {
					t.Fatalf("producer must explicitly select its upstream workspace mode %s", workMode)
				}
				if strings.Contains(strings.Join(component.BuildCommand, "\n"), "os-image/packages") ||
					strings.Contains(strings.Join(component.VendorCommand, "\n"), "os-image/packages") {
					t.Fatal("producer delegates back to the NKE package folder")
				}
				resolved, err := buildgraph.Resolve(cat, buildgraph.Selection{Components: []string{test.name}, Platform: platform})
				if err != nil {
					t.Fatal(err)
				}
				graph, err := buildgraph.Compile(resolved)
				if err != nil {
					t.Fatal(err)
				}
				buildCount := 0
				for _, node := range graph.Nodes {
					if node.Kind == ir.KindPull {
						t.Fatal("source graph silently acquired a prebuilt producer")
					}
					if node.Kind == ir.KindBuild && (node.Network != ir.NetworkNone || !node.SourceReadOnly ||
						node.WriteDir != "nkd-output" || node.Platform != platform) {
						t.Fatalf("producer lost offline isolated native execution: %+v", node)
					}
					if node.Kind == ir.KindBuild {
						buildCount++
					}
				}
				if buildCount != 1 {
					t.Fatalf("producer graph has %d builds, want exactly one", buildCount)
				}
			})
		}
	}
}

func TestNKEOSPublicSourceMissingToolchainFailsClosed(t *testing.T) {
	for _, arch := range []string{"amd64", "arm64"} {
		t.Run(arch, func(t *testing.T) {
			cat := loadPublicOSSourceCatalog(t)
			component := cat.Components["crictl-1-36-0"]
			component.ToolchainImages = map[string]string{}
			cat.Components["crictl-1-36-0"] = component
			_, err := buildgraph.Resolve(cat, buildgraph.Selection{
				Components: []string{"crictl-1-36-0"}, Platform: "linux/" + arch,
			})
			var coded *nkderr.Error
			if !errors.As(err, &coded) || coded.Code != nkderr.CodeTagToolchain {
				t.Fatalf("missing compiler must fail with toolchain-tag, got %v", err)
			}
		})
	}
}

// Opt-in external execution. The same test runs in native ARM64 and AMD64 CI.
// It does not issue a qualified receipt, install Debian packages, or bake a disk.
func TestNKEOSPublicSourcesLive(t *testing.T) {
	containerRuntime := os.Getenv("NKD_OS_SOURCE_RUNTIME")
	if containerRuntime == "" {
		t.Skip("set NKD_OS_SOURCE_RUNTIME=docker or podman on native Linux for real source execution")
	}
	if runtime.GOOS != "linux" || (runtime.GOARCH != "arm64" && runtime.GOARCH != "amd64") {
		t.Fatal("real source execution requires native Linux ARM64 or AMD64")
	}
	if containerRuntime != "docker" && containerRuntime != "podman" {
		t.Fatal("only the documented Docker and Podman diagnostic runtimes are admitted")
	}
	cat := loadPublicOSSourceCatalog(t)
	cases := publicOSSourceCases()
	var names []string
	for _, test := range cases {
		names = append(names, test.name)
	}
	if selected := os.Getenv("NKD_OS_SOURCE_COMPONENTS"); selected != "" {
		names = strings.Split(selected, ",")
	}
	resolved, err := buildgraph.Resolve(cat, buildgraph.Selection{Components: names, Platform: "linux/" + runtime.GOARCH})
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
	ctx, cancel := context.WithTimeout(t.Context(), 70*time.Minute)
	defer cancel()
	t.Log("UNQUALIFIED SOURCE BUILD TEST: no OS-image, package-installation, TPM-attestation or release-acceptance claim")
	t.Log("PREBUILT EXCEPTION PENDING: DSX IMDS public source is unresolved in the release inventory; retain the existing pinned payloads in the eventual package graph. This public-source-only test does not acquire DSX or establish complete release membership.")
	report, runErr := (pipeline.Local{}).Run(graph, digest, pipeline.RunOptions{
		Ctx: ctx, SourceRoot: filepath.Join(t.TempDir(), "src"), Runtime: containerRuntime,
		Jobs: 1, Stdout: os.Stdout, Stderr: os.Stderr,
	})
	if report != nil {
		defer func() {
			if closeErr := report.Close(); closeErr != nil {
				t.Errorf("closing source test report: %v", closeErr)
			}
		}()
	}
	if runErr != nil {
		t.Fatal(runErr)
	}
	if report == nil || len(report.Artifacts) != len(names) {
		t.Fatalf("terminal artifact membership does not match %v", names)
	}
	for _, artifact := range report.Artifacts {
		t.Run(artifact.Component, func(t *testing.T) {
			if artifact.Qualification != pipeline.ArtifactUnqualified || artifact.InputDigest == "" || artifact.Size <= 0 {
				t.Fatalf("invalid or overclaimed observation: %+v", artifact)
			}
			file, err := os.Open(artifact.AbsPath)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = file.Close() }()
			hash := sha256.New()
			size, err := io.Copy(hash, file)
			if err != nil || size != artifact.Size || fmt.Sprintf("sha256:%x", hash.Sum(nil)) != artifact.SHA256 {
				t.Fatalf("exact output read-back disagrees with terminal observation: size=%d err=%v", size, err)
			}
			t.Logf("component=%s bytes=%d sha256=%s source-input=%s compiler=%s", artifact.Component,
				artifact.Size, artifact.SHA256, artifact.InputDigest, artifact.Toolchain)
			if _, err := file.Seek(0, io.SeekStart); err != nil {
				t.Fatal(err)
			}
			if artifact.OutputType == "tarball" {
				checkPublicOSBinaryBundle(t, file, cases, artifact.Component)
				return
			}
			executable, err := elf.NewFile(file)
			if err != nil {
				t.Fatal(err)
			}
			wantMachine := elf.EM_X86_64
			if runtime.GOARCH == "arm64" {
				wantMachine = elf.EM_AARCH64
			}
			if executable.Machine != wantMachine {
				t.Fatalf("binary architecture = %s, want %s", executable.Machine, wantMachine)
			}
			if strings.HasPrefix(artifact.Component, "crictl-") || strings.HasPrefix(artifact.Component, "helm-") {
				smokeCtx, stop := context.WithTimeout(ctx, 10*time.Second)
				defer stop()
				args := []string{"--version"}
				if strings.HasPrefix(artifact.Component, "helm-") {
					args = []string{"version", "--short"}
				}
				output, err := exec.CommandContext(smokeCtx, artifact.AbsPath, args...).CombinedOutput()
				if err != nil || !strings.Contains(string(output), cat.Components[artifact.Component].Tag) {
					t.Fatalf("version smoke failed: %v: %s", err, output)
				}
				t.Logf("version smoke: %s", output)
			}
		})
	}
}

func checkPublicOSBinaryBundle(t *testing.T, file *os.File, cases []publicOSSourceCase, component string) {
	t.Helper()
	var expected []string
	for _, test := range cases {
		if test.name == component {
			expected = slices.Clone(test.members)
		}
	}
	if len(expected) == 0 {
		t.Fatalf("bundle %s has no source-owned expected membership", component)
	}
	reader := tar.NewReader(file)
	var actual []string
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if header.Typeflag == tar.TypeDir && header.Name == "./" {
			continue
		}
		if header.Typeflag != tar.TypeReg || header.Size <= 0 || len(actual) >= len(expected) {
			t.Fatalf("unexpected binary bundle entry: %+v", header)
		}
		name := strings.TrimPrefix(header.Name, "./")
		if path.Base(name) != name {
			t.Fatalf("unexpected binary bundle path: %q", header.Name)
		}
		actual = append(actual, name)
	}
	slices.Sort(actual)
	slices.Sort(expected)
	if !slices.Equal(actual, expected) {
		t.Fatalf("bundle membership = %v, want %v", actual, expected)
	}
}
