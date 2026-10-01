//go:build linux

package pipeline

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/NVIDIA/nke-recipes/internal/transportarchive"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/execution"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/internal/heldfile"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
)

func testPackerDiagnosticSink(t *testing.T, root *os.File) *PackerDiagnosticSink {
	t.Helper()
	sink, err := NewPackerDiagnosticSink(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { makePackerDiagnosticsRemovable(root.Name()) })
	t.Cleanup(func() {
		if err := sink.Close(); err != nil {
			t.Errorf("closing test Packer diagnostic sink: %v", err)
		}
	})
	return sink
}

func testPackerDiagnosticSinkAt(t *testing.T, root string) *PackerDiagnosticSink {
	t.Helper()
	directory, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := directory.Close(); err != nil {
			t.Errorf("closing test Packer diagnostic root: %v", err)
		}
	}()
	return testPackerDiagnosticSink(t, directory)
}

func TestPackerInputExtractScriptHandlesSealedTopLevelDirectories(t *testing.T) {
	root := t.TempDir()
	t.Cleanup(func() { _ = removePrivateTree(root) })
	for _, name := range []string{"config", "inputs", "source"} {
		directory := filepath.Join(root, name)
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, name+".txt"), []byte(name+"\n"), 0o400); err != nil {
			t.Fatal(err)
		}
	}
	nested := filepath.Join(root, "source", "sealed", "nested")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "payload"), []byte("nested\n"), 0o400); err != nil {
		t.Fatal(err)
	}
	for directory := nested; directory != filepath.Join(root, "source"); directory = filepath.Dir(directory) {
		if err := os.Chmod(directory, 0o500); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"config", "inputs", "source"} {
		if err := os.Chmod(filepath.Join(root, name), 0o500); err != nil {
			t.Fatal(err)
		}
	}
	archive := filepath.Join(root, "input.tar")
	if err := transportarchive.Pack(t.Context(), archive, root, []string{"config", "inputs", "source"}); err != nil {
		t.Fatal(err)
	}
	archiveBytes, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(archiveBytes)
	destination := t.TempDir()
	t.Cleanup(func() { _ = removePrivateTree(destination) })
	work := t.TempDir()
	if err := os.Mkdir(filepath.Join(work, "stage"), 0o700); err != nil {
		t.Fatal(err)
	}
	chown, err := exec.LookPath("chown")
	if err != nil {
		t.Fatal(err)
	}
	commands := t.TempDir()
	// Check both ownership operands, then use the real chown. Without this
	// assertion an unprivileged test would miss an untouched, already-owned work root.
	if err := os.WriteFile(filepath.Join(commands, "chown"), []byte(`#!/bin/sh
set -eu
test "$#" -eq 4
test "$1" = -R
test "$3" = "$NKD_TEST_INPUT"
test "$4" = "$NKD_TEST_WORK"
exec "$NKD_TEST_CHOWN" "$@"
`), 0o700); err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(t.Context(), "sh", "-ec", packerInputExtractScript,
		"nkd-extract-input", fmt.Sprintf("%x", digest), archive, destination,
		fmt.Sprint(os.Getuid()), fmt.Sprint(os.Getgid()), work)
	command.Env = append(os.Environ(), "PATH="+commands+":"+os.Getenv("PATH"),
		"NKD_TEST_CHOWN="+chown, "NKD_TEST_INPUT="+destination, "NKD_TEST_WORK="+work)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("extracting sealed Packer input closure: %v: %s", err, output)
	}
	for _, name := range []string{"config", "inputs", "source"} {
		content, err := os.ReadFile(filepath.Join(destination, name, name+".txt"))
		if err != nil {
			t.Fatal(err)
		}
		if string(content) != name+"\n" {
			t.Fatalf("extracted %s content = %q", name, content)
		}
	}
	nestedContent, err := os.ReadFile(filepath.Join(destination, "source", "sealed", "nested", "payload"))
	if err != nil {
		t.Fatal(err)
	}
	if string(nestedContent) != "nested\n" {
		t.Fatalf("extracted nested content = %q", nestedContent)
	}
}

func TestPackerWorkspaceDockerBindPath(t *testing.T) {
	tests := []struct {
		name       string
		parentName string
		cancel     bool
		wantCode   e.Code
	}{
		{name: "ordinary path", parentName: "safe"},
		{name: "comma in path", parentName: "unsafe,option", wantCode: e.CodeEngineCapability},
		{name: "quote in path", parentName: "unsafe\"option", wantCode: e.CodeEngineCapability},
		{name: "control in path", parentName: "unsafe\noption", wantCode: e.CodeEngineCapability},
		{name: "canceled context", parentName: "safe-canceled", cancel: true, wantCode: e.CodeTimeout},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			parentPath := filepath.Join(t.TempDir(), test.parentName)
			if err := os.Mkdir(parentPath, 0o700); err != nil {
				t.Fatal(err)
			}
			parent, err := os.Open(parentPath)
			if err != nil {
				t.Fatal(err)
			}
			defer parent.Close()
			workspace, err := createPackerWorkspaceRoot(parent, "host-path-test")
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := workspace.cleanup(); err != nil {
					t.Errorf("cleaning Packer workspace: %v", err)
				}
			}()
			ctx := t.Context()
			if test.cancel {
				canceled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = canceled
			}
			root, err := workspace.dockerBindPath(ctx)
			if code(err) != test.wantCode {
				t.Fatalf("Docker bind path error = %v, want %s", err, test.wantCode)
			}
			identity, identityErr := workspace.prepareDockerIdentity(ctx)
			if code(identityErr) != test.wantCode {
				t.Fatalf("Docker identity error = %v, want %s", identityErr, test.wantCode)
			}
			if test.wantCode != "" {
				if _, err := os.Stat(filepath.Join(workspace.path(), "identity")); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("rejected host path created identity files: %v", err)
				}
				return
			}
			if strings.HasPrefix(root, "/proc/") {
				t.Fatalf("Docker bind root must be an ordinary host path: %q", root)
			}
			heldInfo, err := workspace.root.Stat()
			if err != nil {
				t.Fatal(err)
			}
			pathInfo, err := os.Stat(root)
			if err != nil {
				t.Fatal(err)
			}
			if !os.SameFile(heldInfo, pathInfo) {
				t.Fatalf("Docker bind root %q differs from the held workspace", root)
			}
			for name, hostPath := range map[string]string{"passwd": identity.passwdPath, "group": identity.groupPath} {
				heldPath := filepath.Join(workspace.path(), "identity", name)
				heldFile, err := os.Stat(heldPath)
				if err != nil {
					t.Fatal(err)
				}
				hostFile, err := os.Stat(hostPath)
				if err != nil {
					t.Fatal(err)
				}
				if !os.SameFile(heldFile, hostFile) {
					t.Fatalf("Docker host identity %q differs from held %q", hostPath, heldPath)
				}
			}
		})
	}
}

func TestDockerPackerIdentityBind(t *testing.T) {
	image := os.Getenv("NKD_TEST_DOCKER_TOOLCHAIN_IMAGE")
	if image == "" {
		t.Skip("set NKD_TEST_DOCKER_TOOLCHAIN_IMAGE to prove the bind against a native Docker daemon")
	}
	workspaceRoot, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer workspaceRoot.Close()
	workspace, err := createPackerWorkspaceRoot(workspaceRoot, "docker-bind-test")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := workspace.cleanup(); err != nil {
			t.Errorf("cleaning Docker identity workspace: %v", err)
		}
	}()
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	identity, err := workspace.prepareDockerIdentity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(ctx, "docker", "run", "--rm", "--pull=never",
		"--network=none", "--read-only", "--cap-drop=ALL",
		"--security-opt=no-new-privileges", "--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()),
		"--mount", "type=bind,src="+identity.passwdPath+",dst=/etc/passwd,readonly",
		"--mount", "type=bind,src="+identity.groupPath+",dst=/etc/group,readonly",
		"--entrypoint", "sh", image, "-ec", `test "$(id -un)" = nkd`)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("Docker could not start with the projected identity: %v: %s", err, output)
	}
}

func TestRunPackerContainerEndToEndFakeRuntime(t *testing.T) {
	node := testPackerContainerNode()
	node.Network = ir.NetworkDefault
	manifest := []byte("{}\n")
	manifestDigest := fmt.Sprintf("sha256:%x", sha256.Sum256(manifest))
	sourceRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(sourceRoot, "os-image", "packer"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceRoot, "os-image", "packer", "node.pkr.hcl"), []byte("packer {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	workspacePath := t.TempDir()
	workspace, err := os.Open(workspacePath)
	if err != nil {
		t.Fatal(err)
	}
	defer workspace.Close()

	versionDocument := []byte(`{"Client":{"Version":"1"},"Server":{"Version":"1"}}`)
	runtimeDigest := "sha256:" + repeatHex("9")
	runtimeIdentity, err := runtimeIdentityFromOutput(runtimeDigest, qualifiedRuntimeEndpoint, "daemon-id", versionDocument)
	if err != nil {
		t.Fatal(err)
	}
	artifactCarrier := testQCOW2Carrier(t, "node.qcow2")
	logCarrier := testPackerArtifactCarrier(t, "serial.log", []byte("serial console\n"))
	fake := &recordingPackerRuntime{
		endpoint: qualifiedRuntimeEndpoint, digest: runtimeDigest, identity: runtimeIdentity,
	}
	fake.run = func(stdin io.Reader, stdout, _ io.Writer, args []string) error {
		switch {
		case reflect.DeepEqual(args, []string{"version", "--format", "{{json .}}"}):
			_, _ = stdout.Write(versionDocument)
		case reflect.DeepEqual(args, []string{"info", "--format", "{{json .ID}}"}):
			_, _ = io.WriteString(stdout, `"daemon-id"`)
		case len(args) > 2 && args[0] == "image" && args[1] == "inspect":
			_, _ = fmt.Fprintf(stdout, `{"RepoDigests":["example.test/packer@sha256:%s"],"Os":"linux","Architecture":"amd64","Config":{"Env":["PATH=/ignored"]}}`, repeatHex("c"))
		case len(args) > 1 && args[0] == "volume" && args[1] == "create":
			_, _ = io.WriteString(stdout, args[len(args)-1]+"\n")
		case len(args) > 0 && args[0] == "create":
			_, _ = io.WriteString(stdout, "container-id\n")
		case len(args) > 1 && args[0] == "cp" && args[1] == "-":
			_, _ = io.Copy(io.Discard, stdin)
		case len(args) > 2 && args[0] == "cp" && args[len(args)-1] == "-":
			if strings.Contains(args[1], packerContainerBuildLogPath) {
				_, _ = stdout.Write(logCarrier)
			} else {
				_, _ = stdout.Write(artifactCarrier)
			}
		case len(args) > 2 && args[0] == "start" && strings.Contains(args[2], "-version"):
			_, _ = io.WriteString(stdout, "Packer v1.14.2\n")
		case len(args) > 2 && args[0] == "start" && strings.Contains(args[2], "-ssh-verify-"):
			_, _ = io.WriteString(stdout, "256 SHA256:matching-fingerprint generated-key (ED25519)\n")
		case len(args) > 2 && args[0] == "start" && strings.Contains(args[2], "-validate"):
			_, _ = io.WriteString(stdout, "The configuration is valid.\n")
		}
		return nil
	}

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	request := packerContainerRunRequest{
		runtime: fake, source: &materializedSource{Dir: sourceRoot, InputDigest: "sha256:" + repeatHex("8")},
		node: node, subject: "plan/node", manifest: manifest, digest: manifestDigest, workspace: workspace,
		diagnostics: testPackerDiagnosticSink(t, workspace),
		bindings: packerContainerBindings{
			packerVersion: "1.14.2", acceleratorVariable: "accelerator",
			buildLogDirectoryVariable: "build_logs",
			sshPrivateKeyVariable:     "ssh_private_key", sshPublicKeyVariable: "ssh_public_key",
		},
	}
	artifact, err := runPackerContainer(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	defer artifact.Close()
	var output bytes.Buffer
	if _, err := artifact.WriteToContext(t.Context(), &output); err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(output.Bytes(), []byte{'Q', 'F', 'I', 0xfb}) {
		t.Fatal("collected output is not the fake QCOW2")
	}

	validateIndex, buildIndex, sshContainers, networkedContainers := -1, -1, 0, 0
	workCopyIndex, carrierCopyIndex, stageStartIndex := -1, -1, -1
	hostUser := fmt.Sprintf("--user=%d:%d", os.Getuid(), os.Getgid())
	for index, call := range fake.calls {
		joined := strings.Join(call, " ")
		if strings.Contains(joined, " packer init ") || slices.Contains(call, "init") || slices.Contains(call, "pull") {
			t.Fatalf("runtime attempted forbidden acquisition: %#v", call)
		}
		if call[0] == "create" {
			if !slices.Contains(call, "--pull=never") || !slices.Contains(call, "--label") ||
				!slices.Contains(call, "--no-healthcheck") {
				t.Fatalf("container lacks isolation or ownership labels: %#v", call)
			}
			isBuild := strings.Contains(joined, "nkd.dev/role=build")
			if isBuild {
				networkedContainers++
				if !slices.Contains(call, "--network=bridge") || slices.Contains(call, "--network=none") {
					t.Fatalf("Packer build container lacks its admitted network: %#v", call)
				}
			} else if !slices.Contains(call, "--network=none") || slices.Contains(call, "--network=bridge") {
				t.Fatalf("Packer preparation container received network access: %#v", call)
			}
			if strings.Contains(joined, "nkd.dev/role=stage") {
				if !slices.Contains(call, "--user=0:0") || !slices.Contains(call, "--cap-add=CHOWN") ||
					!slices.Contains(call, "--cap-add=DAC_OVERRIDE") {
					t.Fatalf("authenticated input stage lacks its narrow extraction authority: %#v", call)
				}
			} else if !slices.Contains(call, hostUser) || slices.Contains(call, "--cap-add=CHOWN") ||
				slices.Contains(call, "--cap-add=DAC_OVERRIDE") {
				t.Fatalf("consumer container does not use the host identity without extra capabilities: %#v", call)
			}
			if strings.Contains(joined, "nkd.dev/role=ssh-") {
				sshContainers++
				assertPackerInputReadOnly(t, call)
			}
			if slices.Contains(call, "validate") {
				validateIndex = index
				assertPackerInputReadOnly(t, call)
				if slices.Contains(call, "--device") || slices.ContainsFunc(call, func(arg string) bool {
					return strings.HasPrefix(arg, "--group-add=")
				}) {
					t.Fatalf("validation container received KVM authority: %#v", call)
				}
			}
			if slices.Contains(call, "build") {
				buildIndex = index
				assertPackerInputReadOnly(t, call)
			}
		}
		if len(call) > 2 && call[0] == "cp" && call[1] == "-" {
			switch {
			case strings.HasSuffix(call[2], "-stage:/nkd/work"):
				workCopyIndex = index
			case strings.HasSuffix(call[2], "-stage:/nkd/work/stage"):
				carrierCopyIndex = index
			}
		}
		if len(call) > 2 && call[0] == "start" {
			if strings.Contains(call[2], "-stage") {
				stageStartIndex = index
			}
		}
	}
	if validateIndex < 0 || buildIndex <= validateIndex {
		t.Fatalf("validate/build order is invalid: validate=%d build=%d", validateIndex, buildIndex)
	}
	if sshContainers != 3 {
		t.Fatalf("SSH key containers = %d, want one generator and two verifiers", sshContainers)
	}
	if networkedContainers != 1 {
		t.Fatalf("networked Packer containers = %d, want only the build container", networkedContainers)
	}
	if workCopyIndex < 0 || carrierCopyIndex <= workCopyIndex ||
		stageStartIndex <= carrierCopyIndex || validateIndex <= stageStartIndex {
		t.Fatalf("authenticated staging order is invalid: work=%d carrier=%d stage=%d validate=%d",
			workCopyIndex, carrierCopyIndex, stageStartIndex, validateIndex)
	}
	if len(fake.cleanups) < 3 {
		t.Fatalf("cleanup calls = %#v, want build container and two volumes", fake.cleanups)
	}
	diagnosticLogs, err := filepath.Glob(filepath.Join(workspacePath, packerDiagnosticDirectory,
		packerDiagnosticProducerDir, "run-*", packerDiagnosticLogsName, "serial.log"))
	if err != nil || len(diagnosticLogs) != 1 {
		t.Fatalf("retained diagnostic logs = %#v, error=%v", diagnosticLogs, err)
	}
	if raw, err := os.ReadFile(diagnosticLogs[0]); err != nil || string(raw) != "serial console\n" {
		t.Fatalf("retained serial log = %q, error=%v", raw, err)
	}
}

func TestRunPackerContainerRejectsEncryptedDiskBeforeRuntimeWork(t *testing.T) {
	node := testPackerContainerNode()
	node.Disk.Encryption = "luks2"
	workspace, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer workspace.Close()
	fake := &recordingPackerRuntime{endpoint: qualifiedRuntimeEndpoint}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	_, err = runPackerContainer(ctx, packerContainerRunRequest{
		runtime: fake, source: &materializedSource{Dir: t.TempDir(), InputDigest: "sha256:" + repeatHex("8")},
		node: node, manifest: []byte("{}\n"), digest: fmt.Sprintf("sha256:%x", sha256.Sum256([]byte("{}\n"))),
		workspace: workspace, bindings: packerContainerBindings{packerVersion: "1.14.2"},
	})
	if code(err) != e.CodeEngineCapability {
		t.Fatalf("encrypted disk error = %v, want %s", err, e.CodeEngineCapability)
	}
	if len(fake.calls) != 0 {
		t.Fatalf("encrypted disk caused runtime side effects: %#v", fake.calls)
	}
}

func TestRunPackerContainerRejectsUnpairedSSHVariablesBeforeRuntimeWork(t *testing.T) {
	node := testPackerContainerNode()
	manifest := []byte("{}\n")
	workspace, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer workspace.Close()
	runtime := &recordingPackerRuntime{endpoint: qualifiedRuntimeEndpoint}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	request := packerContainerRunRequest{
		runtime: runtime, source: &materializedSource{Dir: t.TempDir(), InputDigest: "sha256:" + repeatHex("8")},
		node: node, subject: "plan/node", manifest: manifest, digest: fmt.Sprintf("sha256:%x", sha256.Sum256(manifest)), workspace: workspace,
		diagnostics: testPackerDiagnosticSink(t, workspace),
		bindings: packerContainerBindings{
			packerVersion: "1.14.2", acceleratorVariable: "accelerator", sshPrivateKeyVariable: "ssh_private_key",
		},
	}
	artifact, err := runPackerContainer(ctx, request)
	if artifact != nil || code(err) != e.CodeInvalidCatalog {
		t.Fatalf("unpaired SSH artifact/error = %#v/%v, want nil/%s", artifact, err, e.CodeInvalidCatalog)
	}
	if len(runtime.calls) != 0 || len(runtime.cleanups) != 0 {
		t.Fatalf("unpaired SSH variables caused runtime work: calls=%#v cleanups=%#v", runtime.calls, runtime.cleanups)
	}
}

func TestGenerateAndVerifyPackerSSHKeyRejectsMismatch(t *testing.T) {
	runtime := &recordingPackerRuntime{}
	runtime.run = func(_ io.Reader, stdout, _ io.Writer, args []string) error {
		switch {
		case len(args) > 0 && args[0] == "create":
			_, _ = io.WriteString(stdout, "container-id\n")
		case len(args) > 2 && args[0] == "start" && strings.Contains(args[2], "-ssh-verify-0"):
			_, _ = io.WriteString(stdout, "256 SHA256:private key (ED25519)\n")
		case len(args) > 2 && args[0] == "start" && strings.Contains(args[2], "-ssh-verify-1"):
			_, _ = io.WriteString(stdout, "256 SHA256:public key.pub (ED25519)\n")
		}
		return nil
	}
	resources := &packerContainerResources{runtime: runtime}
	err := generateAndVerifyPackerSSHKey(t.Context(), runtime, resources, testPackerContainerNode(),
		"input", "work", "nkd.dev/node=build-node", "nkd.dev/attempt=abc", "abc",
		packerContainerIdentity{passwdPath: "/private/passwd", groupPath: "/private/group"})
	if code(err) != e.CodeEngineCapability || !strings.Contains(err.Error(), "do not match") {
		t.Fatalf("SSH mismatch error = %v, want %s", err, e.CodeEngineCapability)
	}
}

func TestValidatePackerContainerKVMRejectsUnadmittedDevices(t *testing.T) {
	runtime := &recordingPackerRuntime{endpoint: qualifiedRuntimeEndpoint}
	regularPath := filepath.Join(t.TempDir(), "regular")
	if err := os.WriteFile(regularPath, []byte("not a device"), 0o600); err != nil {
		t.Fatal(err)
	}
	regular, err := os.Open(regularPath)
	if err != nil {
		t.Fatal(err)
	}
	defer regular.Close()
	if err := validatePackerContainerKVM(runtime, regular, true); code(err) != e.CodeEngineCapability {
		t.Fatalf("regular KVM error = %v, want %s", err, e.CodeEngineCapability)
	}
	nullDevice, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer nullDevice.Close()
	if err := validatePackerContainerKVM(runtime, nullDevice, true); code(err) != e.CodeEngineCapability ||
		!strings.Contains(err.Error(), "device handoff") {
		t.Fatalf("wrong character device error = %v, want handoff %s", err, e.CodeEngineCapability)
	}
	if err := validatePackerContainerKVM(runtime, nil, true); code(err) != e.CodeEngineCapability {
		t.Fatalf("missing KVM error = %v, want %s", err, e.CodeEngineCapability)
	}
	if err := validatePackerContainerKVM(runtime, nullDevice, false); code(err) != e.CodeEngineCapability {
		t.Fatalf("disabled KVM with device error = %v, want %s", err, e.CodeEngineCapability)
	}
	if err := validatePackerContainerKVM(runtime, nil, false); err != nil {
		t.Fatalf("disabled KVM without device: %v", err)
	}
}

func TestValidatePackerContainerKVMWithRealDevice(t *testing.T) {
	kvm, err := os.OpenFile("/dev/kvm", os.O_RDWR, 0)
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, os.ErrPermission) {
		t.Skipf("real KVM device is unavailable: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer kvm.Close()
	// Exercise the real ioctl ABI, not an injected return value. This catches
	// using a pointer-returning ioctl helper for KVM_GET_API_VERSION.
	if err := validatePackerContainerKVM(&recordingPackerRuntime{endpoint: qualifiedRuntimeEndpoint}, kvm, true); err != nil {
		t.Fatalf("available /dev/kvm failed API revalidation: %v", err)
	}
}

func TestPackerBuildContainerRequestThreadsKVMResolution(t *testing.T) {
	kvm, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer kvm.Close()
	node := testPackerContainerNode()
	node.ExecutionBinding.Capabilities[0].Enabled = true
	node.Network = ir.NetworkDefault
	request := packerContainerRunRequest{
		node: node, kvm: kvm,
		bindings: packerContainerBindings{acceleratorVariable: "accelerator"},
	}
	build := packerBuildContainerRequest(request, "input", "work", "/nkd/input/source",
		"nkd.dev/node=build-node", "nkd.dev/attempt=abc", "abc", "123", true)
	args := packerContainerCreateArgs("docker", build)
	if !slices.Contains(args, "accelerator=kvm") || !slices.Contains(args, "--group-add=123") ||
		!slices.Contains(args, "--network=bridge") {
		t.Fatalf("KVM build arguments do not preserve the resolved accelerator and device group: %#v", args)
	}
	wantDevice := fmt.Sprintf("/proc/%d/fd/%d:/dev/kvm:rw", os.Getpid(), kvm.Fd())
	if !slices.Contains(args, "--device") || !slices.Contains(args, wantDevice) {
		t.Fatalf("KVM build arguments do not preserve the held device: %#v", args)
	}
}

func TestRunPackerContainerRejectsMissingPluginBeforeBuild(t *testing.T) {
	node := testPackerContainerNode()
	manifest := []byte("{}\n")
	sourceRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(sourceRoot, "os-image", "packer"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceRoot, "os-image", "packer", "node.pkr.hcl"), []byte("packer {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	workspace, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer workspace.Close()

	versionDocument := []byte(`{"Client":{"Version":"1"},"Server":{"Version":"1"}}`)
	runtimeDigest := "sha256:" + repeatHex("9")
	runtimeIdentity, err := runtimeIdentityFromOutput(runtimeDigest, qualifiedRuntimeEndpoint, "daemon-id", versionDocument)
	if err != nil {
		t.Fatal(err)
	}
	buildCreated := false
	fake := &recordingPackerRuntime{
		endpoint: qualifiedRuntimeEndpoint, digest: runtimeDigest, identity: runtimeIdentity,
	}
	fake.run = func(stdin io.Reader, stdout, _ io.Writer, args []string) error {
		switch {
		case reflect.DeepEqual(args, []string{"version", "--format", "{{json .}}"}):
			_, _ = stdout.Write(versionDocument)
		case reflect.DeepEqual(args, []string{"info", "--format", "{{json .ID}}"}):
			_, _ = io.WriteString(stdout, `"daemon-id"`)
		case len(args) > 2 && args[0] == "image" && args[1] == "inspect":
			_, _ = fmt.Fprintf(stdout, `{"RepoDigests":["example.test/packer@sha256:%s"],"Os":"linux","Architecture":"amd64","Config":{"Env":["PATH=/ignored"]}}`, repeatHex("c"))
		case len(args) > 1 && args[0] == "volume" && args[1] == "create":
			_, _ = io.WriteString(stdout, args[len(args)-1]+"\n")
		case len(args) > 0 && args[0] == "create":
			if slices.Contains(args, "build") {
				buildCreated = true
			}
			_, _ = io.WriteString(stdout, "container-id\n")
		case len(args) > 1 && args[0] == "cp" && args[1] == "-":
			_, _ = io.Copy(io.Discard, stdin)
		case len(args) > 2 && args[0] == "start" && strings.Contains(args[2], "-version"):
			_, _ = io.WriteString(stdout, "Packer v1.14.2\n")
		case len(args) > 2 && args[0] == "start" && strings.Contains(args[2], "-validate"):
			return fmt.Errorf("required qemu plugin is unavailable")
		}
		return nil
	}

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	request := packerContainerRunRequest{
		runtime: fake, source: &materializedSource{Dir: sourceRoot, InputDigest: "sha256:" + repeatHex("8")},
		node: node, subject: "plan/node", manifest: manifest, digest: fmt.Sprintf("sha256:%x", sha256.Sum256(manifest)),
		workspace:   workspace,
		diagnostics: testPackerDiagnosticSink(t, workspace),
		bindings:    packerContainerBindings{packerVersion: "1.14.2", acceleratorVariable: "accelerator"},
	}
	_, err = runPackerContainer(ctx, request)
	if code(err) != e.CodeBuildFailed {
		t.Fatalf("missing plugin error = %v, want %s", err, e.CodeBuildFailed)
	}
	if buildCreated {
		t.Fatal("Packer build container was created after plugin validation failed")
	}
	for _, call := range fake.calls {
		if slices.Contains(call, "init") || slices.Contains(call, "pull") || slices.Contains(call, "--device") {
			t.Fatalf("validation failure attempted forbidden acquisition or QEMU/KVM work: %#v", call)
		}
	}
}

func TestRunPackerContainerRejectsCorruptedVolumeCarrierBeforePacker(t *testing.T) {
	node := testPackerContainerNode()
	manifest := []byte("{}\n")
	sourceRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(sourceRoot, "os-image", "packer"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceRoot, "os-image", "packer", "node.pkr.hcl"), []byte("packer {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	workspace, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer workspace.Close()

	versionDocument := []byte(`{"Client":{"Version":"1"},"Server":{"Version":"1"}}`)
	runtimeDigest := "sha256:" + repeatHex("9")
	runtimeIdentity, err := runtimeIdentityFromOutput(runtimeDigest, qualifiedRuntimeEndpoint, "daemon-id", versionDocument)
	if err != nil {
		t.Fatal(err)
	}
	fake := &recordingPackerRuntime{endpoint: qualifiedRuntimeEndpoint, digest: runtimeDigest, identity: runtimeIdentity}
	fake.run = func(stdin io.Reader, stdout, _ io.Writer, args []string) error {
		switch {
		case reflect.DeepEqual(args, []string{"version", "--format", "{{json .}}"}):
			_, _ = stdout.Write(versionDocument)
		case reflect.DeepEqual(args, []string{"info", "--format", "{{json .ID}}"}):
			_, _ = io.WriteString(stdout, `"daemon-id"`)
		case len(args) > 2 && args[0] == "image" && args[1] == "inspect":
			_, _ = fmt.Fprintf(stdout, `{"RepoDigests":["example.test/packer@sha256:%s"],"Os":"linux","Architecture":"amd64","Config":{"Env":["PATH=/ignored"]}}`, repeatHex("c"))
		case len(args) > 1 && args[0] == "volume" && args[1] == "create":
			_, _ = io.WriteString(stdout, args[len(args)-1]+"\n")
		case len(args) > 0 && args[0] == "create":
			_, _ = io.WriteString(stdout, "container-id\n")
		case len(args) > 1 && args[0] == "cp" && args[1] == "-":
			_, _ = io.Copy(io.Discard, stdin)
		case len(args) > 2 && args[0] == "start" && strings.Contains(args[2], "-stage"):
			return fmt.Errorf("input.tar digest mismatch")
		}
		return nil
	}

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	request := packerContainerRunRequest{
		runtime: fake, source: &materializedSource{Dir: sourceRoot, InputDigest: "sha256:" + repeatHex("8")},
		node: node, subject: "plan/node", manifest: manifest, digest: fmt.Sprintf("sha256:%x", sha256.Sum256(manifest)),
		workspace:   workspace,
		diagnostics: testPackerDiagnosticSink(t, workspace),
		bindings:    packerContainerBindings{packerVersion: "1.14.2", acceleratorVariable: "accelerator"},
	}
	_, err = runPackerContainer(ctx, request)
	if code(err) != e.CodeUnverifiedRef {
		t.Fatalf("corrupted carrier error = %v, want %s", err, e.CodeUnverifiedRef)
	}
	for _, call := range fake.calls {
		joined := strings.Join(call, " ")
		if call[0] == "create" && (strings.Contains(joined, "nkd.dev/role=version") ||
			strings.Contains(joined, "nkd.dev/role=validate") || strings.Contains(joined, "nkd.dev/role=build")) {
			t.Fatalf("corrupted input reached Packer work: %#v", call)
		}
		if slices.Contains(call, "init") || slices.Contains(call, "pull") {
			t.Fatalf("corrupted input attempted acquisition: %#v", call)
		}
	}
}

func TestRunPackerContainerFailsBeforeBuildOnVersionOrValidationDiagnostics(t *testing.T) {
	for _, test := range []struct {
		name             string
		version          string
		validationStdout string
		validationStderr string
		wantCode         e.Code
	}{
		{name: "version mismatch", version: "Packer v1.14.1\n", validationStdout: "The configuration is valid.\n", wantCode: e.CodeEngineCapability},
		{name: "validation stderr", version: "Packer v1.14.2\n", validationStdout: "The configuration is valid.\n", validationStderr: "warning\n", wantCode: e.CodeBuildFailed},
		{name: "validation stdout warning", version: "Packer v1.14.2\n", validationStdout: "warning\nThe configuration is valid.\n", wantCode: e.CodeBuildFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			request, _, buildCreated := newPackerContainerTestRun(t, test.version,
				test.validationStdout, test.validationStderr, nil)
			artifact, err := runPackerContainer(testContext(t), request)
			if artifact != nil || code(err) != test.wantCode {
				t.Fatalf("preflight artifact/error = %#v/%v, want nil/%s", artifact, err, test.wantCode)
			}
			if *buildCreated {
				t.Fatal("Packer build was created after preflight rejection")
			}
		})
	}
}

func TestRunPackerContainerReapsResourcesWhenBuildStartFails(t *testing.T) {
	request, runtime, _ := newPackerContainerTestRun(t, "Packer v1.14.2\n",
		"The configuration is valid.\n", "", nil)
	request.bindings.buildLogDirectoryVariable = "build_logs"
	fallback := runtime.run
	startFailure := errors.New("build start failed")
	runtime.run = func(stdin io.Reader, stdout, stderr io.Writer, args []string) error {
		if len(args) > 2 && args[0] == "start" && strings.Contains(args[2], "-build") {
			return startFailure
		}
		return fallback(stdin, stdout, stderr, args)
	}
	artifact, err := runPackerContainer(testContext(t), request)
	if artifact != nil || code(err) != e.CodeBuildFailed || !errors.Is(err, startFailure) {
		t.Fatalf("build-start artifact/error = %#v/%v, want nil/%s wrapping start failure",
			artifact, err, e.CodeBuildFailed)
	}
	buildRemoved, volumesRemoved := false, 0
	for _, call := range runtime.cleanups {
		if len(call) > 2 && call[0] == "rm" && strings.Contains(call[2], "-build") {
			buildRemoved = true
		}
		if len(call) > 3 && call[0] == "volume" && call[1] == "rm" {
			volumesRemoved++
		}
	}
	if !buildRemoved || volumesRemoved != 2 {
		t.Fatalf("build-start cleanup calls = %#v, want build container and two volumes reaped", runtime.cleanups)
	}
	logs, globErr := filepath.Glob(filepath.Join(request.workspace.Name(), packerDiagnosticDirectory,
		packerDiagnosticProducerDir, "run-*", packerDiagnosticLogsName, "serial.log"))
	if globErr != nil || len(logs) != 1 {
		t.Fatalf("failed-build diagnostic logs = %#v, error=%v", logs, globErr)
	}
}

func TestRunPackerContainerPreservesRuntimeCleanupFailure(t *testing.T) {
	cleanupFailure := errors.New("runtime cleanup failed")
	for _, test := range []struct {
		name     string
		version  string
		wantCode e.Code
	}{
		{name: "after successful build", version: "Packer v1.14.2\n", wantCode: e.CodeInternal},
		{name: "joined with primary failure", version: "Packer v1.14.1\n", wantCode: e.CodeEngineCapability},
	} {
		t.Run(test.name, func(t *testing.T) {
			request, _, _ := newPackerContainerTestRun(t, test.version,
				"The configuration is valid.\n", "", cleanupFailure)
			artifact, err := runPackerContainer(testContext(t), request)
			if artifact != nil || code(err) != test.wantCode || !errors.Is(err, cleanupFailure) {
				t.Fatalf("cleanup artifact/error = %#v/%v, want nil/%s joined cleanup", artifact, err, test.wantCode)
			}
		})
	}
}

func TestRunPackerContainerCancellationJoinsTransportWork(t *testing.T) {
	for _, test := range []struct {
		name  string
		block func([]string) bool
	}{
		{name: "input copy", block: func(args []string) bool {
			return len(args) > 1 && args[0] == "cp" && args[1] == "-"
		}},
		{name: "artifact copy", block: func(args []string) bool {
			return len(args) > 2 && args[0] == "cp" && args[len(args)-1] == "-"
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			request, runtime, _ := newPackerContainerTestRun(t, "Packer v1.14.2\n",
				"The configuration is valid.\n", "", nil)
			fallback := runtime.run
			blocked := make(chan struct{})
			runtime.runContext = func(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, args []string) error {
				if test.block(args) {
					close(blocked)
					<-ctx.Done()
					return ctx.Err()
				}
				return fallback(stdin, stdout, stderr, args)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			done := make(chan error, 1)
			go func() {
				_, err := runPackerContainer(ctx, request)
				done <- err
			}()
			select {
			case <-blocked:
			case err := <-done:
				t.Fatalf("Packer run ended before the selected transport stage: %v", err)
			case <-time.After(20 * time.Second):
				t.Fatal("Packer run did not reach the selected transport stage")
			}
			cancel()
			select {
			case err := <-done:
				if code(err) != e.CodeTimeout {
					t.Fatalf("canceled transport error = %v, want %s", err, e.CodeTimeout)
				}
			case <-time.After(20 * time.Second):
				t.Fatal("canceled Packer transport did not join")
			}
			if len(runtime.cleanups) == 0 {
				t.Fatal("canceled Packer transport did not reap runtime objects")
			}
		})
	}
}

func newPackerContainerTestRun(
	t *testing.T,
	versionOutput, validationStdout, validationStderr string,
	cleanupErr error,
) (packerContainerRunRequest, *recordingPackerRuntime, *bool) {
	t.Helper()
	manifest := []byte("{}\n")
	sourceRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(sourceRoot, "os-image", "packer"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceRoot, "os-image", "packer", "node.pkr.hcl"), []byte("packer {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	workspace, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = workspace.Close() })
	versionDocument := []byte(`{"Client":{"Version":"1"},"Server":{"Version":"1"}}`)
	runtimeDigest := "sha256:" + repeatHex("9")
	runtimeIdentity, err := runtimeIdentityFromOutput(runtimeDigest, qualifiedRuntimeEndpoint, "daemon-id", versionDocument)
	if err != nil {
		t.Fatal(err)
	}
	buildCreated := false
	runtime := &recordingPackerRuntime{
		endpoint: qualifiedRuntimeEndpoint, digest: runtimeDigest, identity: runtimeIdentity, cleanupErr: cleanupErr,
	}
	carrier := testQCOW2Carrier(t, "node.qcow2")
	logCarrier := testPackerArtifactCarrier(t, "serial.log", []byte("failed build serial\n"))
	runtime.run = func(stdin io.Reader, stdout, stderr io.Writer, args []string) error {
		switch {
		case reflect.DeepEqual(args, []string{"version", "--format", "{{json .}}"}):
			_, _ = stdout.Write(versionDocument)
		case reflect.DeepEqual(args, []string{"info", "--format", "{{json .ID}}"}):
			_, _ = io.WriteString(stdout, `"daemon-id"`)
		case len(args) > 2 && args[0] == "image" && args[1] == "inspect":
			_, _ = fmt.Fprintf(stdout, `{"RepoDigests":["example.test/packer@sha256:%s"],"Os":"linux","Architecture":"amd64","Config":{"Env":["PATH=/ignored"]}}`, repeatHex("c"))
		case len(args) > 1 && args[0] == "volume" && args[1] == "create":
			_, _ = io.WriteString(stdout, args[len(args)-1]+"\n")
		case len(args) > 0 && args[0] == "create":
			if slices.Contains(args, "build") {
				buildCreated = true
			}
			_, _ = io.WriteString(stdout, "container-id\n")
		case len(args) > 1 && args[0] == "cp" && args[1] == "-":
			_, _ = io.Copy(io.Discard, stdin)
		case len(args) > 2 && args[0] == "cp" && args[len(args)-1] == "-":
			if strings.Contains(args[1], packerContainerBuildLogPath) {
				_, _ = stdout.Write(logCarrier)
			} else {
				_, _ = stdout.Write(carrier)
			}
		case len(args) > 2 && args[0] == "start" && strings.Contains(args[2], "-version"):
			_, _ = io.WriteString(stdout, versionOutput)
		case len(args) > 2 && args[0] == "start" && strings.Contains(args[2], "-validate"):
			_, _ = io.WriteString(stdout, validationStdout)
			_, _ = io.WriteString(stderr, validationStderr)
		}
		return nil
	}
	return packerContainerRunRequest{
		runtime: runtime, source: &materializedSource{Dir: sourceRoot, InputDigest: "sha256:" + repeatHex("8")},
		node: testPackerContainerNode(), subject: "plan/node", manifest: manifest, digest: fmt.Sprintf("sha256:%x", sha256.Sum256(manifest)),
		workspace: workspace, diagnostics: testPackerDiagnosticSink(t, workspace),
		bindings: packerContainerBindings{packerVersion: "1.14.2", acceleratorVariable: "accelerator"},
	}, runtime, &buildCreated
}

func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestNodeSessionCloseReleasesPackerRuntime(t *testing.T) {
	capability, err := heldfile.FromBytesContext(t.Context(), []byte("runtime"), heldfile.Options{
		Label: "container-runtime", Executable: true, MaxBytes: 64,
	})
	if err != nil {
		t.Fatal(err)
	}
	ready := make(chan struct{})
	close(ready)
	session, err := NewNodeSession(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	session.state.packerRuntime = &nodeSessionPackerRuntime{
		ready: ready, path: "/proc/self/fd/runtime",
		runtime: &heldRuntimeExecutable{
			capability: capability, identity: "sha256:" + repeatHex("a"),
		},
	}
	session.Close()
	if err := capability.AssertUnchangedContext(t.Context()); err == nil {
		t.Fatal("NodeSession.Close left the held container runtime open")
	}
	copy := *session
	_, err = copy.AdmitVMImagePacker(t.Context(), []VMImagePackerSelection{{
		Subject: "plan/node", Node: testPackerContainerNode(),
	}}, nil, "docker")
	if code(err) != e.CodeEngineCapability {
		t.Fatalf("closed session admission error = %v, want %s", err, e.CodeEngineCapability)
	}
}

func TestVMImagePackerExecutorPreservesCallbackDeadline(t *testing.T) {
	node := testPackerContainerNode()
	raw, err := canonical.Marshal(node)
	if err != nil {
		t.Fatal(err)
	}
	session, err := NewNodeSession(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(session.Close)
	observed := make(chan time.Time, 1)
	runFailure := errors.New("stop after observing callback deadline")
	runtime := &recordingPackerRuntime{
		endpoint: qualifiedRuntimeEndpoint,
		digest:   "sha256:" + repeatHex("9"),
		identity: "sha256:" + repeatHex("a"),
	}
	runtime.runContext = func(ctx context.Context, _ io.Reader, _, _ io.Writer, _ []string) error {
		deadline, ok := ctx.Deadline()
		if !ok {
			return errors.New("runtime context has no deadline")
		}
		observed <- deadline
		return runFailure
	}
	executor := &VMImagePackerExecutor{
		session: session.state, runtime: runtime, nodes: map[string][]byte{node.ID: raw},
	}
	t.Cleanup(func() { _ = executor.Close() })
	manifest, err := heldfile.FromBytesContext(t.Context(), []byte("{}\n"), heldfile.Options{
		Label: "material-manifest", MaxBytes: maxBuildMaterialManifestBytes,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer manifest.Close()
	deadline := time.Now().Add(5 * time.Second)
	ctx, cancel := context.WithDeadline(t.Context(), deadline)
	defer cancel()
	_, _, err = executor.Run(ctx, node.ID, node, VMImagePackerRun{
		SourceRoot: t.TempDir(), Manifest: manifest, Runtime: &execution.LocalVMRuntime{},
		Diagnostics: testPackerDiagnosticSinkAt(t, t.TempDir()),
	})
	if code(err) != e.CodeEngineCapability || !errors.Is(err, runFailure) {
		t.Fatalf("deadline observation error = %v, want %s wrapping runtime failure", err, e.CodeEngineCapability)
	}
	select {
	case got := <-observed:
		if !got.Equal(deadline) {
			t.Fatalf("runtime deadline = %s, want callback deadline %s", got, deadline)
		}
	default:
		t.Fatal("runtime did not observe the callback deadline")
	}
}

type blockingPackerRuntime struct {
	started chan struct{}
}

func (runtime *blockingPackerRuntime) Endpoint() string { return qualifiedRuntimeEndpoint }

func (runtime *blockingPackerRuntime) RuntimeName() string { return "docker" }

func (runtime *blockingPackerRuntime) AdmittedIdentity() (string, string) {
	return "sha256:" + repeatHex("9"), "sha256:" + repeatHex("a")
}

func (runtime *blockingPackerRuntime) Run(ctx context.Context, _ io.Reader, _, _ io.Writer, _ ...string) error {
	select {
	case <-runtime.started:
	default:
		close(runtime.started)
	}
	<-ctx.Done()
	return ctx.Err()
}

func (*blockingPackerRuntime) Cleanup(context.Context, time.Duration, io.Writer, io.Writer, ...string) error {
	return nil
}

func TestVMImagePackerExecutorCloseAndSessionCloseJoinActiveRun(t *testing.T) {
	node := testPackerContainerNode()
	raw, err := canonical.Marshal(node)
	if err != nil {
		t.Fatal(err)
	}
	session, err := NewNodeSession(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	runtime := &blockingPackerRuntime{started: make(chan struct{})}
	executor := &VMImagePackerExecutor{
		session: session.state, runtime: runtime, nodes: map[string][]byte{node.ID: raw},
	}
	manifest, err := heldfile.FromBytesContext(t.Context(), []byte("{}\n"), heldfile.Options{
		Label: "material-manifest", MaxBytes: maxBuildMaterialManifestBytes,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer manifest.Close()
	sourceRoot := t.TempDir()
	runDone := make(chan error, 1)
	go func() {
		_, _, runErr := executor.Run(t.Context(), node.ID, node, VMImagePackerRun{
			SourceRoot: sourceRoot, Manifest: manifest, Runtime: &execution.LocalVMRuntime{},
			Diagnostics: testPackerDiagnosticSinkAt(t, t.TempDir()),
		})
		runDone <- runErr
	}()
	select {
	case <-runtime.started:
	case <-time.After(5 * time.Second):
		t.Fatal("active Packer run did not reach runtime re-observation")
	}
	executorClosed := make(chan error, 1)
	go func() { executorClosed <- executor.Close() }()
	select {
	case err := <-executorClosed:
		t.Fatalf("executor Close returned before active Run joined: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	sessionClosed := make(chan struct{})
	go func() {
		session.Close()
		close(sessionClosed)
	}()
	select {
	case err := <-runDone:
		if code(err) != e.CodeTimeout {
			t.Fatalf("canceled active Run error = %v, want %s", err, e.CodeTimeout)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("NodeSession.Close did not cancel active Packer Run")
	}
	select {
	case err := <-executorClosed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("executor Close did not join canceled Run")
	}
	select {
	case <-sessionClosed:
	case <-time.After(5 * time.Second):
		t.Fatal("NodeSession.Close did not join canceled Run")
	}
}

func testPackerContainerNode() ir.Node {
	requirements := &execution.Requirements{
		Resources:            &execution.ResourceRequest{CPUMillis: 1000, MemoryMiB: 1024, EphemeralStorageMiB: 1024},
		AllowedHostPlatforms: []execution.HostPlatform{execution.HostPlatformLinuxAMD64},
		Capabilities:         []execution.CapabilityRequirement{{Name: execution.CapabilityKVM, Policy: execution.CapabilityPreferred}},
		TimeoutSeconds:       60,
	}
	class := execution.Class{
		Name: "linux-amd64", RunsOn: "linux-amd64", HostPlatform: execution.HostPlatformLinuxAMD64,
		Resources:         execution.ResourceCapacity{CPUMillis: 2000, MemoryMiB: 2048, EphemeralStorageMiB: 2048},
		MaxTimeoutSeconds: 120,
	}
	return ir.Node{
		ID: "build-node", Kind: ir.KindBuild, Needs: []string{"fetch-node"}, Requirements: requirements,
		ExecutionBinding: &execution.Binding{Class: class, Capabilities: []execution.CapabilityResolution{{Name: execution.CapabilityKVM, Enabled: false}}},
		Component:        "node", BuilderType: ir.BuilderVMImage,
		Toolchain: "example.test/packer@sha256:" + repeatHex("c"), ToolchainPlatform: "linux/amd64",
		Platform: "linux/arm64", Network: ir.NetworkNone, Workdir: "/src", SourceDest: "node-source",
		SourceCommit: strings.Repeat("a", 40), SourceTreeHash: strings.Repeat("b", 40),
		VendorSpecDigest: "sha256:" + repeatHex("d"), Disk: &ir.DiskSpec{Format: "qcow2", Encryption: "none"},
		Packer: &ir.PackerConfig{
			ProjectPath: "os-image/packer", PackerVersion: "1.14.2", BuildName: "node.qemu.image",
			ArchitectureVariable: "architecture", AcceleratorVariable: "accelerator", MaterialManifestVariable: "material_manifest",
			OutputDirectoryVariable: "output_directory",
		},
		OutputType: "disk-image", ArtifactPath: "node.qcow2", Version: "1.0.0",
	}
}

func testQCOW2Carrier(t *testing.T, name string) []byte {
	t.Helper()
	var carrier bytes.Buffer
	writer := tar.NewWriter(&carrier)
	payload := append([]byte{'Q', 'F', 'I', 0xfb}, []byte("fake-image")...)
	if err := writer.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o600, Size: int64(len(payload))}); err != nil {
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

func assertPackerInputReadOnly(t *testing.T, args []string) {
	t.Helper()
	for _, arg := range args {
		if strings.Contains(arg, "dst="+packerContainerInputRoot) && strings.Contains(arg, "readonly") {
			return
		}
	}
	t.Fatalf("Packer container input is not read-only: %#v", args)
}
