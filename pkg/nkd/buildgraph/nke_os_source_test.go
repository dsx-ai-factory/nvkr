// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package buildgraph_test

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"debug/buildinfo"
	"debug/elf"
	"encoding/binary"
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
	var observed []string
	for _, artifact := range report.Artifacts {
		observed = append(observed, artifact.Component)
	}
	slices.Sort(observed)
	expected := slices.Clone(names)
	slices.Sort(expected)
	if !slices.Equal(observed, expected) {
		t.Fatalf("terminal artifact identities = %v, want %v", observed, expected)
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
			wantMachine := elf.EM_X86_64
			if runtime.GOARCH == "arm64" {
				wantMachine = elf.EM_AARCH64
			}
			if artifact.OutputType == "tarball" {
				checkPublicOSBinaryBundle(t, file, cases, artifact.Component, wantMachine)
				probePublicOSBundle(t, ctx, file, artifact.Component, cat.Components[artifact.Component].Tag)
				return
			}
			executable, err := elf.NewFile(file)
			if err != nil {
				t.Fatal(err)
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
				if artifact.Component == "helm-3-20-2" {
					probeHelmKubeVersion(t, ctx, artifact.AbsPath)
				}
			}
		})
	}
}

// probeHelmKubeVersion exercises both independently stamped upstream packages.
func probeHelmKubeVersion(t *testing.T, ctx context.Context, binary string) {
	t.Helper()
	chart := t.TempDir()
	if err := os.Mkdir(filepath.Join(chart, "templates"), 0700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"Chart.yaml":           "apiVersion: v2\nname: kube-version-probe\nversion: 0.1.0\nkubeVersion: '>=1.35.0-0'\n",
		"templates/probe.yaml": "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: kube-version-probe\ndata:\n  version: {{ .Capabilities.KubeVersion.Version | quote }}\n",
	} {
		if err := os.WriteFile(filepath.Join(chart, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"template", "probe", chart}, {"lint", chart}} {
		probeCtx, stop := context.WithTimeout(ctx, 10*time.Second)
		cmd := exec.CommandContext(probeCtx, binary, args...)
		cmd.WaitDelay = 5 * time.Second
		output, err := cmd.CombinedOutput()
		stop()
		if err != nil || (args[0] == "template" && !strings.Contains(string(output), "v1.35.0")) {
			t.Fatalf("Helm Kubernetes defaults %v: %v: %s", args, err, output)
		}
		t.Logf("Helm Kubernetes defaults %s: %s", args[0], output)
	}
}

// probePublicOSBundle uses the standard tar codec after exact membership admission.
// It runs only cheap native version queries, never service startup or CNI ADD.
func probePublicOSBundle(t *testing.T, ctx context.Context, file *os.File, component, tag string) {
	t.Helper()
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	reader := tar.NewReader(file)
	dir := t.TempDir()
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if header.Typeflag == tar.TypeDir {
			continue
		}
		name := strings.TrimPrefix(header.Name, "./")
		if strings.Contains(name, "/") || name == "." || name == ".." || name == "" || header.Size > 256<<20 {
			t.Fatal("invalid admitted binary member")
		}
		binary := filepath.Join(dir, name)
		out, err := os.OpenFile(binary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
		if err != nil {
			t.Fatal(err)
		}
		_, copyErr := io.CopyN(out, reader, header.Size)
		if err := errors.Join(copyErr, out.Close()); err != nil {
			t.Fatal(err)
		}
		info, err := buildinfo.ReadFile(binary)
		if err != nil {
			t.Fatal(err)
		}
		// Go intentionally omits -ldflags with -trimpath. Read the compiler
		// identity here; stamp declarations are guarded by the contract test,
		// and CLI-visible versions are checked through native probes below.
		if info.GoVersion != "go1.27.1" {
			t.Fatalf("built compiler version = %q, want go1.27.1", info.GoVersion)
		}
		var args []string
		switch {
		case name == "kubectl":
			args = []string{"version", "--client", "-o", "json"}
		case name == "kubeadm":
			args = []string{"version", "-o", "short"}
		case name == "containerd-shim-runc-v2":
			args = []string{"-v"}
		case strings.HasPrefix(component, "kubernetes-"), strings.HasPrefix(component, "containerd-"):
			args = []string{"--version"}
		case strings.HasPrefix(component, "cni-plugins-"):
			args = nil
		default:
			t.Fatal("unexpected bundle producer")
		}
		probeCtx, stop := context.WithTimeout(ctx, 10*time.Second)
		cmd := exec.CommandContext(probeCtx, binary, args...)
		cmd.WaitDelay = 5 * time.Second
		if strings.HasPrefix(component, "cni-plugins-") {
			cmd.Env = append(os.Environ(), "CNI_COMMAND=VERSION")
		}
		output, err := cmd.CombinedOutput()
		stop()
		want := strings.TrimPrefix(tag, "v")
		if strings.HasPrefix(component, "cni-plugins-") {
			want = "supportedVersions"
		}
		if err != nil || !strings.Contains(string(output), want) {
			t.Fatalf("native %s version probe: %v: %s", name, err, output)
		}
		t.Logf("native %s version probe: %s", name, output)
		if strings.HasPrefix(component, "cni-plugins-") {
			aboutCtx, aboutStop := context.WithTimeout(ctx, 10*time.Second)
			about := exec.CommandContext(aboutCtx, binary)
			about.WaitDelay = 5 * time.Second
			about.Env = append(os.Environ(), "CNI_COMMAND=")
			output, err := about.CombinedOutput()
			aboutStop()
			if err != nil || !strings.Contains(string(output), tag) {
				t.Fatalf("CNI %s build version: %v: %s", name, err, output)
			}
			t.Logf("native %s build version: %s", name, output)
		}
	}
}

// TestNKEOSPublicSourceBuildContracts guards upstream stamps and acquisition scope.
func TestNKEOSPublicSourceBuildContracts(t *testing.T) {
	cat := loadPublicOSSourceCatalog(t)
	for _, test := range publicOSSourceCases() {
		t.Run(test.name, func(t *testing.T) {
			component := cat.Components[test.name]
			command := strings.Join(component.BuildCommand, "\n")
			if len(test.members) == 0 && component.OutputType != "raw" {
				t.Fatal("standalone ELF must not be declared as a Debian host package")
			}
			var stamps []string
			switch {
			case strings.HasPrefix(test.name, "crictl-"):
				if len(component.VendorCommand) != 0 {
					t.Fatal("cri-tools must use its pinned committed vendor closure")
				}
			case strings.HasPrefix(test.name, "kubernetes-"):
				for _, namespace := range []string{"k8s.io/component-base/version", "k8s.io/client-go/pkg/version"} {
					for _, key := range []string{"gitVersion", "gitCommit", "gitMajor", "gitMinor", "gitTreeState", "buildDate"} {
						stamps = append(stamps, namespace+"."+key+"=")
					}
				}
			case test.name == "helm-3-20-2":
				for _, namespace := range []string{"pkg/chartutil", "pkg/lint/rules"} {
					for _, key := range []string{"k8sVersionMajor", "k8sVersionMinor"} {
						stamps = append(stamps, "helm.sh/helm/v3/"+namespace+"."+key+"=")
					}
				}
			case strings.HasPrefix(test.name, "containerd-"):
				stamps = []string{"version.Package=github.com/containerd/containerd/v2"}
			case strings.HasPrefix(test.name, "cni-plugins-"):
				stamps = []string{"github.com/containernetworking/plugins/pkg/utils/buildversion.BuildVersion="}
			case strings.Contains(test.name, "tpm"):
				if len(component.VersionStamp) != 0 {
					t.Fatal("TPM producers do not consume version stamps")
				}
			}
			for _, stamp := range stamps {
				if !strings.Contains(command, stamp) {
					t.Fatalf("upstream stamp %q missing", stamp)
				}
			}
		})
	}
}

// checkPublicOSBinaryBundle checks source-owned membership and native ELF architecture.
func checkPublicOSBinaryBundle(t *testing.T, file *os.File, cases []publicOSSourceCase, component string, wantMachine elf.Machine) {
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
	if err := validatePublicOSBinaryBundle(file, expected, wantMachine); err != nil {
		t.Fatal(err)
	}
}

// validatePublicOSBinaryBundle uses the standard ELF parser on each bounded member.
// ReadAt leaves the tar stream position unchanged and cannot read another entry's bytes.
func validatePublicOSBinaryBundle(file *os.File, expected []string, wantMachine elf.Machine) error {
	if len(expected) == 0 {
		return fmt.Errorf("binary bundle has no expected membership")
	}
	reader := tar.NewReader(file)
	var actual []string
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read binary bundle: %w", err)
		}
		if header.Typeflag == tar.TypeDir && header.Name == "./" {
			continue
		}
		if header.Typeflag != tar.TypeReg || header.Size <= 0 || len(actual) >= len(expected) {
			return fmt.Errorf("unexpected binary bundle entry: %+v", header)
		}
		name := strings.TrimPrefix(header.Name, "./")
		if path.Base(name) != name {
			return fmt.Errorf("unexpected binary bundle path: %q", header.Name)
		}
		offset, err := file.Seek(0, io.SeekCurrent)
		if err != nil {
			return fmt.Errorf("locate binary bundle member %q: %w", name, err)
		}
		executable, err := elf.NewFile(io.NewSectionReader(file, offset, header.Size))
		if err != nil {
			return fmt.Errorf("bundle member %q is not a valid ELF: %w", name, err)
		}
		if executable.Machine != wantMachine {
			return fmt.Errorf("bundle member %q architecture = %s, want %s", name, executable.Machine, wantMachine)
		}
		actual = append(actual, name)
	}
	slices.Sort(actual)
	expected = slices.Clone(expected)
	slices.Sort(expected)
	if !slices.Equal(actual, expected) {
		return fmt.Errorf("bundle membership = %v, want %v", actual, expected)
	}
	return nil
}

// TestNKEOSBinaryBundle exercises both architectures and malformed/mixed bundles
// without containers; synthetic ELF headers are parser fixtures, not executable proof.
func TestNKEOSBinaryBundle(t *testing.T) {
	elfFixture := func(machine elf.Machine) []byte {
		header := elf.Header64{
			Type: uint16(elf.ET_EXEC), Machine: uint16(machine), Version: uint32(elf.EV_CURRENT),
			Ehsize: 64, Phentsize: 56, Shentsize: 64,
		}
		copy(header.Ident[:], elf.ELFMAG)
		header.Ident[elf.EI_CLASS] = byte(elf.ELFCLASS64)
		header.Ident[elf.EI_DATA] = byte(elf.ELFDATA2LSB)
		header.Ident[elf.EI_VERSION] = byte(elf.EV_CURRENT)
		var data bytes.Buffer
		if err := binary.Write(&data, binary.LittleEndian, header); err != nil {
			t.Fatal(err)
		}
		return data.Bytes()
	}
	amd64, arm64 := elfFixture(elf.EM_X86_64), elfFixture(elf.EM_AARCH64)
	type member struct {
		name string
		data []byte
	}
	for _, test := range []struct {
		name     string
		machine  elf.Machine
		members  []member
		expected []string
		wantErr  string
	}{
		{name: "amd64", machine: elf.EM_X86_64, members: []member{{"./a", amd64}, {"./b", amd64}}, expected: []string{"b", "a"}},
		{name: "arm64", machine: elf.EM_AARCH64, members: []member{{"a", arm64}, {"b", arm64}}, expected: []string{"a", "b"}},
		{name: "foreign first", machine: elf.EM_AARCH64, members: []member{{"a", amd64}, {"b", arm64}}, expected: []string{"a", "b"}, wantErr: "member \"a\" architecture"},
		{name: "foreign later", machine: elf.EM_X86_64, members: []member{{"a", amd64}, {"b", arm64}}, expected: []string{"a", "b"}, wantErr: "member \"b\" architecture"},
		{name: "non ELF", machine: elf.EM_X86_64, members: []member{{"a", []byte("not an ELF")}}, expected: []string{"a"}, wantErr: "not a valid ELF"},
		{name: "truncated ELF", machine: elf.EM_X86_64, members: []member{{"a", amd64[:24]}, {"b", amd64}}, expected: []string{"a", "b"}, wantErr: "not a valid ELF"},
		{name: "duplicate", machine: elf.EM_X86_64, members: []member{{"a", amd64}, {"a", amd64}}, expected: []string{"a", "b"}, wantErr: "bundle membership"},
		{name: "missing", machine: elf.EM_X86_64, members: []member{{"a", amd64}}, expected: []string{"a", "b"}, wantErr: "bundle membership"},
		{name: "nested path", machine: elf.EM_X86_64, members: []member{{"subdir/a", amd64}}, expected: []string{"a"}, wantErr: "bundle path"},
		{name: "empty membership", machine: elf.EM_X86_64, wantErr: "no expected membership"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var data bytes.Buffer
			writer := tar.NewWriter(&data)
			for _, entry := range test.members {
				if err := writer.WriteHeader(&tar.Header{Name: entry.name, Size: int64(len(entry.data)), Typeflag: tar.TypeReg}); err != nil {
					t.Fatal(err)
				}
				if _, err := writer.Write(entry.data); err != nil {
					t.Fatal(err)
				}
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			filename := filepath.Join(t.TempDir(), "bundle.tar")
			if err := os.WriteFile(filename, data.Bytes(), 0o600); err != nil {
				t.Fatal(err)
			}
			file, err := os.Open(filename)
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			expected := slices.Clone(test.expected)
			err = validatePublicOSBinaryBundle(file, expected, test.machine)
			if test.wantErr == "" && err != nil {
				t.Fatal(err)
			}
			if test.wantErr != "" && (err == nil || !strings.Contains(err.Error(), test.wantErr)) {
				t.Fatalf("bundle error = %v, want %q", err, test.wantErr)
			}
			if !slices.Equal(expected, test.expected) {
				t.Fatal("validation mutated expected membership")
			}
		})
	}
}
