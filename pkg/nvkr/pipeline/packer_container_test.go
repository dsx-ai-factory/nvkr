// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/transportarchive"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

func TestPreparePackerContainerIdentity(t *testing.T) {
	tests := []struct {
		name     string
		runtime  string
		created  bool
		cancel   bool
		wantCode e.Code
	}{
		{name: "docker projection", runtime: "docker", created: true},
		{name: "podman native entries", runtime: "podman"},
		{name: "canceled Docker projection", runtime: "docker", cancel: true, wantCode: e.CodeTimeout},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			ctx := t.Context()
			if test.cancel {
				canceled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = canceled
			}
			identity, err := preparePackerContainerIdentity(ctx, root, test.runtime)
			if code(err) != test.wantCode {
				t.Fatalf("Docker identity error = %v, want %s", err, test.wantCode)
			}
			if !test.created {
				if identity != (packerContainerIdentity{}) {
					t.Fatalf("non-Docker identity = %#v", identity)
				}
				if _, err := os.Stat(filepath.Join(root, "identity")); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("non-Docker identity directory error = %v, want not-exist", err)
				}
				return
			}
			want := map[string]string{
				identity.passwdPath: fmt.Sprintf("nvkr:x:%d:%d:NVKR Builder:/nvkr/work/home:/bin/sh\n", os.Getuid(), os.Getgid()),
				identity.groupPath:  fmt.Sprintf("nvkr:x:%d:\n", os.Getgid()),
			}
			directory, err := os.Stat(filepath.Dir(identity.passwdPath))
			if err != nil {
				t.Fatal(err)
			}
			if directory.Mode().Perm() != 0o700 {
				t.Fatalf("identity directory mode = %o, want 0700", directory.Mode().Perm())
			}
			for name, wantContent := range want {
				raw, err := os.ReadFile(name)
				if err != nil {
					t.Fatal(err)
				}
				if string(raw) != wantContent {
					t.Fatalf("identity projection %q = %q, want %q", name, raw, wantContent)
				}
				info, err := os.Stat(name)
				if err != nil {
					t.Fatal(err)
				}
				if info.Mode().Perm() != 0o400 {
					t.Fatalf("identity projection %q mode = %o, want 0400", name, info.Mode().Perm())
				}
			}
		})
	}
}

func TestPreparePackerContainerTransportAuthenticatesManifestInVolumeClosure(t *testing.T) {
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "node.pkr.hcl"), []byte("packer {}\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("node.pkr.hcl", filepath.Join(source, "current.pkr.hcl")); err != nil {
		t.Fatal(err)
	}
	manifest := []byte("{\"schema\":\"nvkr.dev/build-materials/v2\"}\n")
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256(manifest))
	transportRoot := t.TempDir()
	t.Cleanup(func() { _ = removePrivateTree(transportRoot) })
	input, work, staged, err := preparePackerContainerTransport(t.Context(), transportRoot, packerContainerRunRequest{
		source:   &materializedSource{Dir: source, InputDigest: "sha256:" + repeatHex("8")},
		manifest: manifest, digest: digest,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	defer work.Close()
	secondRoot := t.TempDir()
	t.Cleanup(func() { _ = removePrivateTree(secondRoot) })
	secondInput, secondWork, secondStaged, err := preparePackerContainerTransport(t.Context(), secondRoot, packerContainerRunRequest{
		source:   &materializedSource{Dir: source, InputDigest: "sha256:" + repeatHex("8")},
		manifest: manifest, digest: digest,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer secondInput.Close()
	defer secondWork.Close()
	if secondStaged.inputArchiveDigest != staged.inputArchiveDigest {
		t.Fatalf("identical input closure digest changed across runs: first=%s second=%s",
			staged.inputArchiveDigest, secondStaged.inputArchiveDigest)
	}
	if _, err := input.OSFile().Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	reader := tar.NewReader(input.OSFile())
	var inner []byte
	entries := 0
	for {
		header, readErr := reader.Next()
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			t.Fatal(readErr)
		}
		entries++
		if header.Name != "input.tar" || header.Typeflag != tar.TypeReg {
			t.Fatalf("outer carrier entry = %q/%d, want one regular input.tar", header.Name, header.Typeflag)
		}
		inner, err = io.ReadAll(reader)
		if err != nil {
			t.Fatal(err)
		}
	}
	if entries != 1 {
		t.Fatalf("outer carrier has %d entries, want one", entries)
	}
	innerDigest := fmt.Sprintf("sha256:%x", sha256.Sum256(inner))
	if innerDigest != staged.inputArchiveDigest {
		t.Fatalf("inner closure digest = %s, want %s", innerDigest, staged.inputArchiveDigest)
	}

	innerReader := tar.NewReader(bytes.NewReader(inner))
	seen := make(map[string]*tar.Header)
	for {
		header, readErr := innerReader.Next()
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			t.Fatal(readErr)
		}
		copy := *header
		seen[header.Name] = &copy
	}
	for _, want := range []string{"config/packer.json", "inputs/material-manifest.json", "source/node.pkr.hcl", "source/current.pkr.hcl"} {
		if seen[want] == nil {
			t.Fatalf("authenticated inner closure is missing %q: %#v", want, seen)
		}
	}
	if seen["source/node.pkr.hcl"].Mode != 0o700 {
		t.Fatalf("source executable mode = %#o, want 0700", seen["source/node.pkr.hcl"].Mode)
	}
	link := seen["source/current.pkr.hcl"]
	if link.Typeflag != tar.TypeSymlink || link.Linkname != "node.pkr.hcl" {
		t.Fatalf("source symlink = type %d target %q", link.Typeflag, link.Linkname)
	}
}

func TestPackerInputCarrierBoundsIncludeOuterWrapper(t *testing.T) {
	if maxPackerInputClosureBytes+packerInputCarrierReserveBytes != MaxVMImagePackerInputBytes {
		t.Fatal("Packer input closure reserve does not cover the complete carrier bound")
	}
	for _, test := range []struct {
		name          string
		inner, outer  int64
		wantErrorCode e.Code
		wantMessage   string
	}{
		{name: "within aggregate", inner: maxPackerInputClosureBytes, outer: MaxVMImagePackerInputBytes},
		{name: "inner consumes wrapper reserve", inner: maxPackerInputClosureBytes + 1, outer: MaxVMImagePackerInputBytes, wantErrorCode: e.CodeBuildFailed, wantMessage: "closure exceeds"},
		{name: "outer exceeds aggregate", inner: maxPackerInputClosureBytes, outer: MaxVMImagePackerInputBytes + 1, wantErrorCode: e.CodeBuildFailed, wantMessage: "carrier exceeds"},
		{name: "outer does not wrap inner", inner: 1024, outer: 1024, wantErrorCode: e.CodeBuildFailed, wantMessage: "does not wrap"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := validatePackerInputCarrierBounds(test.inner, test.outer)
			if code(err) != test.wantErrorCode {
				t.Fatalf("carrier bounds error = %v, want %s", err, test.wantErrorCode)
			}
			if test.wantMessage != "" && !strings.Contains(err.Error(), test.wantMessage) {
				t.Fatalf("carrier bounds error = %v, want message containing %q", err, test.wantMessage)
			}
		})
	}
}

func TestFinalizePackerContainerRunPreservesEveryFailure(t *testing.T) {
	cleanupFailure := errors.New("cleanup failed")
	primaryFailure := e.New(e.CodeBuildFailed, "build failed")
	closedFile, err := os.CreateTemp(t.TempDir(), "closed-artifact-")
	if err != nil {
		t.Fatal(err)
	}
	if err := closedFile.Close(); err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name              string
		artifact          *PackerArtifact
		primary           error
		wantCode          e.Code
		wantArtifactClose bool
	}{
		{
			name:              "successful build with cleanup and artifact close failures",
			artifact:          &PackerArtifact{file: closedFile},
			wantCode:          e.CodeInternal,
			wantArtifactClose: true,
		},
		{
			name:     "primary build and cleanup failures",
			primary:  primaryFailure,
			wantCode: e.CodeBuildFailed,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			artifact := test.artifact
			err := finalizePackerContainerRun(&artifact, test.primary, cleanupFailure)
			if code(err) != test.wantCode {
				t.Fatalf("final error = %v, want code %s", err, test.wantCode)
			}
			if !errors.Is(err, cleanupFailure) {
				t.Fatalf("final error = %v, want cleanup failure", err)
			}
			if test.primary != nil && !errors.Is(err, test.primary) {
				t.Fatalf("final error = %v, want primary failure", err)
			}
			if test.wantArtifactClose && !errors.Is(err, fs.ErrClosed) {
				t.Fatalf("final error = %v, want artifact close failure", err)
			}
			if artifact != nil {
				t.Fatal("rejected artifact remained available after cleanup failure")
			}
		})
	}
}

func TestFinalizePackerContainerRunClosesArtifactOnPrimaryFailure(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "rejected-artifact-")
	if err != nil {
		t.Fatal(err)
	}
	artifact := &PackerArtifact{file: file}
	primary := e.New(e.CodeBuildFailed, "build failed after collection")
	if err := finalizePackerContainerRun(&artifact, primary, nil); !errors.Is(err, primary) {
		t.Fatalf("final error = %v, want primary failure", err)
	}
	if artifact != nil {
		t.Fatal("artifact remained available after the run failed")
	}
	if _, err := file.Stat(); !errors.Is(err, fs.ErrClosed) {
		t.Fatalf("rejected artifact file remained open: %v", err)
	}
}

func TestPackerContainerCleanupGivesVolumesAnIndependentBudget(t *testing.T) {
	runtime := &cleanupBudgetPackerRuntime{}
	resources := &packerContainerResources{
		runtime: runtime, containers: []string{"build"}, volumes: []string{"input"},
	}
	err := resources.cleanupWithTimeout(10 * time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cleanup error = %v, want the container deadline", err)
	}
	if runtime.volumeContextErr != nil {
		t.Fatalf("volume removal inherited the exhausted container budget: %v", runtime.volumeContextErr)
	}
	if !runtime.volumeRemoved {
		t.Fatal("volume removal was not attempted after container cleanup exhausted its budget")
	}
}

func TestPackerContainerCreateArgsEnforceIsolation(t *testing.T) {
	kvm, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer kvm.Close()

	request := packerContainerCreateRequest{
		name: "nvkr-packer-build", image: "registry.example/packer@sha256:" + repeatHex("a"),
		platform: "linux/amd64", input: "nvkr-input", work: "nvkr-work",
		project: "/nvkr/input/source/os-image/packer", packerArgs: []string{"build", "-only=build.qemu.node", "."},
		labels: []string{"nvkr.dev/node=build-node", "nvkr.dev/attempt=abc"}, inputReadOnly: true,
		identity: packerContainerIdentity{passwdPath: "/private/passwd", groupPath: "/private/group"},
	}
	withoutKVM := packerContainerCreateArgs("docker", request)
	want := []string{
		"create", "--name", "nvkr-packer-build", "--pull=never", "--platform", "linux/amd64",
		"--network=none", "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges",
		"--no-healthcheck", "--hostname=nvkr-packer",
		"--mount", "type=bind,src=/private/passwd,dst=/etc/passwd,readonly",
		"--mount", "type=bind,src=/private/group,dst=/etc/group,readonly",
		fmt.Sprintf("--user=%d:%d", os.Getuid(), os.Getgid()),
		"--label", "nvkr.dev/node=build-node", "--label", "nvkr.dev/attempt=abc",
		"--mount", "type=volume,src=nvkr-input,dst=/nvkr/input,volume-nocopy,readonly",
		"--mount", "type=volume,src=nvkr-work,dst=/nvkr/work,volume-nocopy",
		"--workdir", "/nvkr/input/source/os-image/packer",
		"--env", "CHECKPOINT_DISABLE=1", "--env", "HOME=/nvkr/work/home", "--env", "PACKER_CACHE_DIR=/nvkr/work/cache",
		"--env", "PACKER_CONFIG=/nvkr/input/config/packer.json", "--env", "PACKER_NO_COLOR=1",
		"--env", "PACKER_PLUGIN_PATH=/nvkr/toolchain/packer/plugins",
		"--env", "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"--env", "TMPDIR=/nvkr/work/tmp", "--entrypoint=packer",
		"registry.example/packer@sha256:" + repeatHex("a"), "build", "-only=build.qemu.node", ".",
	}
	if !reflect.DeepEqual(withoutKVM, want) {
		t.Fatalf("container args = %#v, want %#v", withoutKVM, want)
	}
	if !slices.Contains(withoutKVM, "type=bind,src=/private/passwd,dst=/etc/passwd,readonly") ||
		!slices.Contains(withoutKVM, "type=bind,src=/private/group,dst=/etc/group,readonly") {
		t.Fatalf("container args do not carry the bounded public identity mounts: %#v", withoutKVM)
	}
	for _, arg := range withoutKVM {
		if (strings.Contains(arg, "type=bind") && !strings.Contains(arg, "src=/private/")) || arg == "-v" {
			t.Fatalf("container args contain an unexpected host bind mount: %#v", withoutKVM)
		}
	}
	podman := packerContainerCreateArgs("podman", request)
	if slices.Contains(podman, "type=volume,src=nvkr-work,dst=/nvkr/work,volume-nocopy") ||
		!slices.Contains(podman, "nvkr-input:/nvkr/input:nocopy,ro") ||
		!slices.Contains(podman, "nvkr-work:/nvkr/work:nocopy") ||
		!slices.Contains(podman, fmt.Sprintf("--passwd-entry=nvkr:x:%d:%d:NVKR Builder:/nvkr/work/home:/bin/sh", os.Getuid(), os.Getgid())) ||
		!slices.Contains(podman, fmt.Sprintf("--group-entry=nvkr:x:%d:", os.Getgid())) ||
		slices.ContainsFunc(podman, func(arg string) bool { return strings.Contains(arg, "dst=/etc/passwd") }) {
		t.Fatalf("Podman container args do not use its named-volume nocopy syntax: %#v", podman)
	}
	request.kvm, request.groupAdd = kvm, "123"
	request.kvmProjection = &packerKVMProjection{device: kvm, bootID: "test-boot", deviceNumber: "a:e8"}
	withKVM := packerContainerCreateArgs("docker", request)
	if !slices.Contains(withKVM, "--group-add=123") ||
		!slices.Contains(withKVM, "--device") {
		t.Fatalf("KVM container args do not add the admitted group and device: %#v", withKVM)
	}
	if !slices.Contains(withKVM, "/dev/kvm:/dev/kvm:rw") ||
		!slices.Contains(withKVM, "--entrypoint=sh") || !slices.Contains(withKVM, packerKVMProjectionScript) {
		t.Fatalf("KVM container args do not use the minimal rw device grant: %#v", withKVM)
	}
	if !reflect.DeepEqual(withKVM[len(withKVM)-10:], []string{"-ec", packerKVMProjectionScript,
		"nvkr-kvm-projection", "test-boot", "a:e8", "/dev/kvm", "packer", "build", "-only=build.qemu.node", "."}) {
		t.Fatalf("Docker KVM projection does not guard the exact Packer argv: %#v", withKVM)
	}
	request.network = ir.NetworkDefault
	withDockerNetwork := packerContainerCreateArgs("docker", request)
	if !slices.Contains(withDockerNetwork, "--network=bridge") || slices.Contains(withDockerNetwork, "--network=none") {
		t.Fatalf("networked Docker Packer build args = %#v", withDockerNetwork)
	}
	request.kvmProjection = nil
	withPodmanNetwork := packerContainerCreateArgs("podman", request)
	if !slices.Contains(withPodmanNetwork, "--network=default") || slices.Contains(withPodmanNetwork, "--network=none") {
		t.Fatalf("networked Podman Packer build args = %#v", withPodmanNetwork)
	}
	if !slices.Contains(withPodmanNetwork, fmt.Sprintf("/proc/%d/fd/%d:/dev/kvm:rw", os.Getpid(), kvm.Fd())) ||
		slices.Contains(withPodmanNetwork, packerKVMProjectionScript) {
		t.Fatalf("Podman descriptor handoff changed: %#v", withPodmanNetwork)
	}
}

func TestCreatePackerContainerRejectsUnknownNetworkBeforeRuntime(t *testing.T) {
	runtime := &recordingPackerRuntime{}
	err := createPackerContainer(t.Context(), runtime, packerContainerCreateRequest{
		name: "build", network: "host",
	})
	var coded *e.Error
	if !errors.As(err, &coded) || coded.Code != e.CodeEngineCapability {
		t.Fatalf("unknown network error = %v, want %s", err, e.CodeEngineCapability)
	}
	if len(runtime.calls) != 0 {
		t.Fatalf("runtime called before network admission: %#v", runtime.calls)
	}
}

func TestCreatePackerContainerRejectsUnknownRuntimeBeforeWork(t *testing.T) {
	runtime := &recordingPackerRuntime{name: "containerd"}
	err := createPackerContainer(t.Context(), runtime, packerContainerCreateRequest{name: "build"})
	var coded *e.Error
	if !errors.As(err, &coded) || coded.Code != e.CodeEngineCapability {
		t.Fatalf("unknown runtime error = %v, want %s", err, e.CodeEngineCapability)
	}
	if len(runtime.calls) != 0 {
		t.Fatalf("runtime called before adapter admission: %#v", runtime.calls)
	}
}

func TestCreatePackerContainerRejectsMissingDockerIdentityBeforeWork(t *testing.T) {
	runtime := &recordingPackerRuntime{name: "docker"}
	err := createPackerContainer(t.Context(), runtime, packerContainerCreateRequest{name: "build"})
	var coded *e.Error
	if !errors.As(err, &coded) || coded.Code != e.CodeEngineCapability {
		t.Fatalf("missing identity error = %v, want %s", err, e.CodeEngineCapability)
	}
	if len(runtime.calls) != 0 {
		t.Fatalf("runtime called before identity admission: %#v", runtime.calls)
	}
}

func TestPackerInputStageIsTheOnlyRootContainer(t *testing.T) {
	base := packerContainerCreateRequest{
		name: "consumer", image: "registry.example/packer@sha256:" + repeatHex("a"), platform: "linux/amd64",
		input: "nvkr-input", work: "nvkr-work", project: "/nvkr/work", entrypoint: "sh",
		packerArgs: []string{"-ec", "true"},
		identity:   packerContainerIdentity{passwdPath: "/private/passwd", groupPath: "/private/group"},
	}
	consumer := packerContainerCreateArgs("docker", base)
	if !slices.Contains(consumer, fmt.Sprintf("--user=%d:%d", os.Getuid(), os.Getgid())) ||
		slices.Contains(consumer, "--user=0:0") || slices.Contains(consumer, "--cap-add=CHOWN") ||
		slices.Contains(consumer, "--cap-add=DAC_OVERRIDE") {
		t.Fatalf("consumer identity/capabilities = %#v", consumer)
	}
	base.name, base.user, base.allowChown, base.allowDAC = "stage", "0:0", true, true
	stage := packerContainerCreateArgs("docker", base)
	if !slices.Contains(stage, "--user=0:0") || !slices.Contains(stage, "--cap-add=CHOWN") ||
		!slices.Contains(stage, "--cap-add=DAC_OVERRIDE") {
		t.Fatalf("stage identity/capabilities = %#v", stage)
	}
	if slices.ContainsFunc(stage, func(arg string) bool {
		return strings.Contains(arg, "dst=/etc/passwd") || strings.Contains(arg, "dst=/etc/group")
	}) {
		t.Fatalf("root stage received unnecessary host identity mounts: %#v", stage)
	}
	for _, args := range [][]string{consumer, stage} {
		for _, required := range []string{"--network=none", "--read-only", "--no-healthcheck", "--hostname=nvkr-packer", "--cap-drop=ALL"} {
			if !slices.Contains(args, required) {
				t.Fatalf("container args %#v omit %q", args, required)
			}
		}
	}
}

func TestPackerKVMGroupUsesHeldDeviceGID(t *testing.T) {
	device, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer device.Close()
	info, err := device.Stat()
	if err != nil {
		t.Fatal(err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatalf("device stat type = %T", info.Sys())
	}
	group, err := packerKVMGroup(device, true)
	if err != nil {
		t.Fatal(err)
	}
	if group != strconv.FormatUint(uint64(stat.Gid), 10) {
		t.Fatalf("KVM group = %q, want %d", group, stat.Gid)
	}
	if _, err := packerKVMGroup(device, false); code(err) != e.CodeEngineCapability {
		t.Fatalf("disabled KVM with device error = %v, want %s", err, e.CodeEngineCapability)
	}
}

func TestStartPackerContainerReturnsBoundedRuntimeDiagnostic(t *testing.T) {
	runFailure := errors.New("start failed")
	var forwarded bytes.Buffer
	runtime := &recordingPackerRuntime{run: func(_ io.Reader, _ io.Writer, stderr io.Writer, _ []string) error {
		_, _ = io.WriteString(stderr, "OCI runtime rejected device setup")
		return runFailure
	}}
	err := startPackerContainer(t.Context(), runtime, "build", io.Discard, &forwarded)
	if code(err) != e.CodeBuildFailed || !errors.Is(err, runFailure) ||
		!strings.Contains(err.Error(), "OCI runtime rejected device setup") {
		t.Fatalf("start error = %v, want coded runtime diagnostic", err)
	}
	if forwarded.String() != "OCI runtime rejected device setup" {
		t.Fatalf("forwarded stderr = %q", forwarded.String())
	}
}

func TestStartPackerBuildContainerClassifiesProjectionFailure(t *testing.T) {
	for _, test := range []struct {
		name    string
		status  string
		guarded bool
		want    e.Code
	}{
		{name: "successful guarded build", status: "0", guarded: true},
		{name: "projection rejected", status: "97", guarded: true, want: e.CodeEngineCapability},
		{name: "ordinary build failed", status: "1", guarded: true, want: e.CodeBuildFailed},
		{name: "unguarded invocation", status: "97", want: e.CodeBuildFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			cause := exec.CommandContext(t.Context(), "sh", "-c", "exit \"$1\"", "test", test.status).Run()
			runtime := &recordingPackerRuntime{run: func(io.Reader, io.Writer, io.Writer, []string) error { return cause }}
			err := startPackerBuildContainer(t.Context(), runtime, "build", nil, nil, test.guarded)
			if code(err) != test.want || !errors.Is(err, cause) {
				t.Fatalf("error=%v, want %s with the subprocess cause", err, test.want)
			}
		})
	}
}

type recordingPackerRuntime struct {
	mu         sync.Mutex
	name       string
	endpoint   string
	digest     string
	identity   string
	calls      [][]string
	cleanups   [][]string
	run        func(io.Reader, io.Writer, io.Writer, []string) error
	runContext func(context.Context, io.Reader, io.Writer, io.Writer, []string) error
	cleanupErr error
}

func makePackerDiagnosticsRemovable(root string) {
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			_ = os.Chmod(path, 0o700)
		}
		return nil
	})
}

type cleanupBudgetPackerRuntime struct {
	volumeContextErr error
	volumeRemoved    bool
}

func (*cleanupBudgetPackerRuntime) Endpoint() string { return qualifiedRuntimeEndpoint }

func (*cleanupBudgetPackerRuntime) RuntimeName() string { return "docker" }

func (*cleanupBudgetPackerRuntime) AdmittedIdentity() (string, string) { return "", "" }

func (*cleanupBudgetPackerRuntime) Run(context.Context, io.Reader, io.Writer, io.Writer, ...string) error {
	return nil
}

func (runtime *cleanupBudgetPackerRuntime) Cleanup(ctx context.Context, _ time.Duration, _, _ io.Writer, args ...string) error {
	if len(args) > 0 && args[0] == "rm" {
		<-ctx.Done()
		return ctx.Err()
	}
	runtime.volumeContextErr = ctx.Err()
	runtime.volumeRemoved = true
	return nil
}

func (runtime *recordingPackerRuntime) Endpoint() string { return runtime.endpoint }

func (runtime *recordingPackerRuntime) RuntimeName() string {
	if runtime.name == "" {
		return "docker"
	}
	return runtime.name
}

func (runtime *recordingPackerRuntime) AdmittedIdentity() (string, string) {
	return runtime.digest, runtime.identity
}

func (runtime *recordingPackerRuntime) Run(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, args ...string) error {
	runtime.mu.Lock()
	runtime.calls = append(runtime.calls, append([]string(nil), args...))
	runtime.mu.Unlock()
	if runtime.runContext != nil {
		return runtime.runContext(ctx, stdin, stdout, stderr, args)
	}
	if runtime.run != nil {
		return runtime.run(stdin, stdout, stderr, args)
	}
	return nil
}

func (runtime *recordingPackerRuntime) Cleanup(_ context.Context, _ time.Duration, _, _ io.Writer, args ...string) error {
	runtime.cleanups = append(runtime.cleanups, append([]string(nil), args...))
	return runtime.cleanupErr
}

func TestCreatePackerVolumeUsesPositionalNameAndLabels(t *testing.T) {
	runtime := &recordingPackerRuntime{run: func(_ io.Reader, stdout, _ io.Writer, args []string) error {
		_, _ = io.WriteString(stdout, "nvkr-input\n")
		return nil
	}}
	if err := createPackerVolume(t.Context(), runtime, "nvkr-input",
		[]string{"nvkr.dev/node=build-node", "nvkr.dev/attempt=abc"}); err != nil {
		t.Fatal(err)
	}
	want := []string{"volume", "create", "--label", "nvkr.dev/node=build-node", "--label", "nvkr.dev/attempt=abc", "nvkr-input"}
	if !reflect.DeepEqual(runtime.calls, [][]string{want}) {
		t.Fatalf("volume create calls = %#v, want %#v", runtime.calls, [][]string{want})
	}
	if slices.Contains(runtime.calls[0], "--name") {
		t.Fatal("Docker volume create does not support --name")
	}
}

func TestObserveLocalPackerImageUsesExactHostPlatformWithoutPull(t *testing.T) {
	digest := "sha256:" + repeatHex("a")
	for _, test := range []struct {
		name       string
		runtime    string
		toolchain  string
		inspection string
		want       []string
	}{
		{
			name:       "repository manifest digest",
			toolchain:  "registry.example/packer@" + digest,
			inspection: fmt.Sprintf(`{"Id":"sha256:%s","RepoDigests":["registry.example/packer@%s"],"Os":"linux","Architecture":"arm64","Config":{"Env":["PATH=/ignored"]}}`, repeatHex("b"), digest),
		},
		{
			name:       "bare image ID",
			toolchain:  digest,
			inspection: fmt.Sprintf(`{"Id":"%s","RepoDigests":[],"Os":"linux","Architecture":"arm64","Config":{"Env":["PATH=/ignored"]}}`, digest),
		},
		{
			name:       "Podman bare image ID",
			runtime:    "podman",
			toolchain:  digest,
			inspection: fmt.Sprintf(`{"Id":"%s","RepoDigests":[],"Os":"linux","Architecture":"arm64","Config":{"Env":["PATH=/ignored"]}}`, strings.TrimPrefix(digest, "sha256:")),
			want:       []string{"image", "inspect", "--format", "{{json .}}", digest},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			runtime := &recordingPackerRuntime{name: test.runtime, run: func(_ io.Reader, stdout, stderr io.Writer, args []string) error {
				if slices.Contains(args, "--platform") {
					_, _ = io.WriteString(stderr, "unknown flag: --platform")
					return errors.New("exit status 125")
				}
				_, _ = io.WriteString(stdout, test.inspection)
				return nil
			}}
			node := ir.Node{ID: "build-node", Toolchain: test.toolchain, ToolchainPlatform: "linux/arm64"}
			if err := observeLocalPackerImage(t.Context(), runtime, node); err != nil {
				t.Fatal(err)
			}
			want := test.want
			if want == nil {
				want = []string{"image", "inspect", "--format", "{{json .}}", node.Toolchain}
			}
			if !reflect.DeepEqual(runtime.calls, [][]string{want}) {
				t.Fatalf("image calls = %#v, want %#v", runtime.calls, [][]string{want})
			}
			if slices.Contains(runtime.calls[0], "pull") {
				t.Fatalf("image observation attempted a pull: %#v", runtime.calls[0])
			}
		})
	}
}

func TestObserveLocalPackerImageRejectsInheritedMutableConfig(t *testing.T) {
	digest := "sha256:" + repeatHex("a")
	node := ir.Node{ID: "build-node", Toolchain: "registry.example/packer@" + digest, ToolchainPlatform: "linux/amd64"}
	for _, test := range []struct {
		name   string
		config string
	}{
		{name: "missing configuration", config: `null`},
		{name: "environment", config: `{"Env":["TOKEN=ambient"]}`},
		{name: "duplicate PATH", config: `{"Env":["PATH=/one","PATH=/two"]}`},
		{name: "declared volume", config: `{"Volumes":{"/data":{}}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			runtime := &recordingPackerRuntime{run: func(_ io.Reader, stdout, _ io.Writer, _ []string) error {
				_, _ = fmt.Fprintf(stdout,
					`{"RepoDigests":["registry.example/packer@%s"],"Os":"linux","Architecture":"amd64","Config":%s}`,
					digest, test.config)
				return nil
			}}
			if err := observeLocalPackerImage(t.Context(), runtime, node); code(err) != e.CodePreflightFailed {
				t.Fatalf("image config error = %v, want %s", err, e.CodePreflightFailed)
			}
		})
	}
}

func TestObserveLocalPackerImageRejectsIdentityMismatch(t *testing.T) {
	digest := "sha256:" + repeatHex("a")
	for _, test := range []struct {
		name       string
		toolchain  string
		imageID    string
		repoDigest string
		os         string
		arch       string
	}{
		{name: "manifest digest", toolchain: "registry.example/packer@" + digest, repoDigest: "sha256:" + repeatHex("b"), os: "linux", arch: "amd64"},
		{name: "bare image ID", toolchain: digest, imageID: "sha256:" + repeatHex("b"), os: "linux", arch: "amd64"},
		{name: "operating system", toolchain: "registry.example/packer@" + digest, repoDigest: digest, os: "windows", arch: "amd64"},
		{name: "architecture", toolchain: "registry.example/packer@" + digest, repoDigest: digest, os: "linux", arch: "arm64"},
	} {
		t.Run(test.name, func(t *testing.T) {
			repoDigests := "[]"
			if test.repoDigest != "" {
				repoDigests = fmt.Sprintf("[%q]", "registry.example/packer@"+test.repoDigest)
			}
			runtime := &recordingPackerRuntime{run: func(_ io.Reader, stdout, _ io.Writer, _ []string) error {
				_, _ = fmt.Fprintf(stdout,
					`{"Id":%q,"RepoDigests":%s,"Os":%q,"Architecture":%q,"Config":{"Env":["PATH=/ignored"]}}`,
					test.imageID, repoDigests, test.os, test.arch)
				return nil
			}}
			node := ir.Node{ID: "build-node", Toolchain: test.toolchain, ToolchainPlatform: "linux/amd64"}
			if err := observeLocalPackerImage(t.Context(), runtime, node); code(err) != e.CodePreflightFailed {
				t.Fatalf("image identity error = %v, want %s", err, e.CodePreflightFailed)
			}
		})
	}
}

func TestObservePackerRuntimeFreshnessRequiresAdmittedIdentity(t *testing.T) {
	digest := "sha256:" + repeatHex("b")
	version := []byte(`{"Client":{"Version":"1"},"Server":{"Version":"1"}}`)
	identity, err := runtimeIdentityFromOutput(digest, qualifiedRuntimeEndpoint, "daemon-id", version)
	if err != nil {
		t.Fatal(err)
	}
	runtime := &recordingPackerRuntime{endpoint: qualifiedRuntimeEndpoint, digest: digest, identity: identity}
	runtime.run = func(_ io.Reader, stdout, _ io.Writer, args []string) error {
		if args[0] == "version" {
			_, _ = stdout.Write(version)
		} else {
			_, _ = io.WriteString(stdout, `"daemon-id"`)
		}
		return nil
	}
	if err := observePackerRuntimeFreshness(t.Context(), runtime); err != nil {
		t.Fatal(err)
	}
	runtime.identity = "sha256:" + repeatHex("c")
	if err := observePackerRuntimeFreshness(t.Context(), runtime); code(err) != e.CodeEngineCapability {
		t.Fatalf("identity substitution error = %v, want %s", err, e.CodeEngineCapability)
	}
}

func TestObservePackerRuntimeFreshnessUsesPodmanIdentity(t *testing.T) {
	digest := "sha256:" + repeatHex("b")
	version := []byte(`{"Client":{"APIVersion":"5.6.2","Version":"5.6.2","GitCommit":"abc","OsArch":"linux/arm64"}}`)
	info := []byte(`{"host":{"arch":"arm64","cgroupManager":"systemd","cgroupVersion":"v2","databaseBackend":"sqlite","hostname":"builder","kernel":"6.1","networkBackend":"netavark","os":"linux","security":{"rootless":true,"seccompEnabled":true,"selinuxEnabled":true}},"store":{"graphDriverName":"overlay","graphRoot":"/var/lib/containers","runRoot":"/run/containers"},"version":{"APIVersion":"5.6.2","GitCommit":"abc","OsArch":"linux/arm64","Version":"5.6.2"}}`)
	serverID, err := podmanServerIdentity(info)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := runtimeIdentityFromOutput(digest, qualifiedRuntimeEndpoint, serverID, version)
	if err != nil {
		t.Fatal(err)
	}
	runtime := &recordingPackerRuntime{
		name: "podman", endpoint: qualifiedRuntimeEndpoint, digest: digest, identity: identity,
		run: func(_ io.Reader, stdout, _ io.Writer, args []string) error {
			switch args[0] {
			case "version":
				_, _ = stdout.Write(version)
			case "info":
				_, _ = stdout.Write(info)
			default:
				return fmt.Errorf("unexpected Podman freshness command: %v", args)
			}
			return nil
		},
	}
	if err := observePackerRuntimeFreshness(t.Context(), runtime); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(runtime.calls, [][]string{
		{"version", "--format", "{{json .}}"},
		{"info", "--format", "{{json .}}"},
	}) {
		t.Fatalf("Podman freshness calls = %#v", runtime.calls)
	}
}

func TestCollectPackerContainerArtifactHoldsOneQCOW2(t *testing.T) {
	var carrier bytes.Buffer
	tw := tar.NewWriter(&carrier)
	payload := append([]byte{'Q', 'F', 'I', 0xfb}, []byte("authenticated-qcow2")...)
	if err := tw.WriteHeader(&tar.Header{Name: "node.qcow2", Mode: 0o600, Size: int64(len(payload)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	runtime := &recordingPackerRuntime{run: func(_ io.Reader, stdout, _ io.Writer, args []string) error {
		if !reflect.DeepEqual(args, []string{"cp", "build:/nvkr/work/output/node.qcow2", "-"}) {
			return fmt.Errorf("unexpected args: %#v", args)
		}
		_, err := stdout.Write(carrier.Bytes())
		return err
	}}
	artifact, err := collectPackerContainerArtifact(t.Context(), runtime, "build",
		"/nvkr/work/output/node.qcow2", t.TempDir(), 1)
	if err != nil {
		t.Fatal(err)
	}
	defer artifact.Close()
	var got bytes.Buffer
	if _, err := artifact.WriteToContext(t.Context(), &got); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.Bytes(), payload) {
		t.Fatalf("artifact = %x, want %x", got.Bytes(), payload)
	}
}

func TestCollectPackerContainerArtifactRejectsInvalidOutput(t *testing.T) {
	for _, test := range []struct {
		name       string
		payload    []byte
		overflow   bool
		storageMiB uint64
	}{
		{name: "truncated", payload: []byte{'Q', 'F', 'I'}, storageMiB: 1},
		{name: "wrong magic", payload: []byte("not-a-qcow2"), storageMiB: 1},
		{name: "transport exceeds bound", overflow: true, storageMiB: 4},
	} {
		t.Run(test.name, func(t *testing.T) {
			var carrier []byte
			if test.overflow {
				carrier = bytes.Repeat([]byte("x"), (2<<20)+1)
			} else {
				carrier = testPackerArtifactCarrier(t, "node.qcow2", test.payload)
			}
			runtime := &recordingPackerRuntime{run: func(_ io.Reader, stdout, _ io.Writer, _ []string) error {
				_, err := stdout.Write(carrier)
				return err
			}}
			artifact, err := collectPackerContainerArtifact(t.Context(), runtime, "build",
				"/nvkr/work/output/node.qcow2", t.TempDir(), test.storageMiB)
			if artifact != nil || code(err) != e.CodeVerifyFailed {
				t.Fatalf("invalid QCOW2 artifact/error = %#v/%v, want nil/%s", artifact, err, e.CodeVerifyFailed)
			}
		})
	}
}

func TestPackerArtifactArchiveLimitAccountsForConcurrentCopies(t *testing.T) {
	for _, test := range []struct {
		name       string
		storageMiB uint64
		want       int64
	}{
		{name: "absent admission", want: 0},
		{name: "three mebibytes", storageMiB: 3, want: 1 << 20},
		{name: "shared archive cap", storageMiB: 3 * 64 * 1024, want: transportarchive.MaxArchiveBytes},
		{name: "integer overflow", storageMiB: uint64(math.MaxInt64>>20) + 1, want: transportarchive.MaxArchiveBytes},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := packerArtifactArchiveLimit(test.storageMiB); got != test.want {
				t.Fatalf("packerArtifactArchiveLimit(%d) = %d, want %d", test.storageMiB, got, test.want)
			}
		})
	}
}

func testPackerArtifactCarrier(t *testing.T, name string, payload []byte) []byte {
	t.Helper()
	var carrier bytes.Buffer
	writer := tar.NewWriter(&carrier)
	if err := writer.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(payload)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return carrier.Bytes()
}

func TestPackerContainerVariableArgsAreCanonical(t *testing.T) {
	node := ir.Node{
		Platform: "linux/arm64",
		Packer: &ir.PackerConfig{
			ArchitectureVariable: "architecture", MaterialManifestVariable: "material_manifest",
			OutputDirectoryVariable: "output_directory",
			Variables:               []ir.PackerVariable{{Name: "zeta", Value: "last"}, {Name: "alpha", Value: "first"}},
		},
	}
	want := []string{
		"-var", "accelerator=tcg", "-var", "alpha=first", "-var", "architecture=arm64",
		"-var", "build_logs=/nvkr/work/build-logs", "-var", "http_data=/nvkr/work/http-data",
		"-var", "material_manifest=/nvkr/input/inputs/material-manifest.json",
		"-var", "output_directory=/nvkr/work/output", "-var", "ssh_private=/nvkr/work/ssh/id_ed25519",
		"-var", "ssh_public=/nvkr/work/ssh/id_ed25519.pub", "-var", "zeta=last",
	}
	bindings := packerContainerBindings{
		acceleratorVariable: "accelerator", buildLogDirectoryVariable: "build_logs",
		httpDirectoryVariable: "http_data", sshPrivateKeyVariable: "ssh_private", sshPublicKeyVariable: "ssh_public",
	}
	if got := packerContainerVariableArgs(node, bindings, false); !reflect.DeepEqual(got, want) {
		t.Fatalf("variable args = %#v, want %#v", got, want)
	}
}
