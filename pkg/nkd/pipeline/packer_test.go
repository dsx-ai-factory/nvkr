package pipeline

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	nkderr "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/internal/heldfile"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/internal/packertransport"
)

var (
	packerTestHelperOnce sync.Once
	packerTestHelperRoot string
	packerTestHelperPath string
	packerTestHelperErr  error
)

func TestMain(m *testing.M) {
	code := m.Run()
	if packerTestHelperRoot != "" {
		_ = os.RemoveAll(packerTestHelperRoot)
	}
	os.Exit(code)
}

const packerTestHelperSource = `package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const manifestBytes = "{\"materials\":[{\"path\":\"materials/component\",\"sha256\":\"sha256:c2aaf954104b212a6339d4f99a1f156e153868345a25a03e3d264ef2a533c1d9\"}],\"schema\":\"test\"}"

func main() {
	if filepath.Base(os.Args[0]) == "nkd-test-helper" {
		fmt.Println("held-host-tool")
		return
	}
	if os.Getenv("NKD_PACKER_GO_HOST_TOOL_HELPER") == "1" {
		output, err := exec.Command("nkd-test-helper").Output()
		if err != nil || string(output) != "held-host-tool\n" {
			os.Exit(97)
		}
		return
	}
	if len(os.Args) < 2 || (os.Args[1] != "validate" && os.Args[1] != "build") {
		os.Exit(96)
	}
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if os.Getenv("CHECKPOINT_DISABLE") != "1" || os.Getenv("PACKER_NO_COLOR") != "1" {
		return 81
	}
	if os.Getenv("PKR_VAR_SECRET") != "" || os.Getenv("HTTP_PROXY") != "" || os.Getenv("HTTPS_PROXY") != "" {
		return 82
	}
	for _, name := range []string{"PACKER_PLUGIN_PATH", "PACKER_CACHE_DIR", "TMPDIR"} {
		if info, err := os.Stat(os.Getenv(name)); err != nil || !info.IsDir() {
			return 83
		}
	}
	if _, err := exec.LookPath("sh"); err == nil {
		return 88
	}
	if _, err := os.Stat("require-host-tool"); err == nil {
		output, runErr := exec.Command("nkd-test-helper").Output()
		if runErr != nil || string(output) != "held-host-tool\n" {
			return 90
		}
	}
	log, err := os.OpenFile(".nkd-test-calls", os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return 91
	}
	_, writeErr := fmt.Fprintf(log, "CALL\t%s\n", strings.Join(args, "\t"))
	closeErr := log.Close()
	if writeErr != nil || closeErr != nil {
		return 91
	}

	command := args[0]
	if command == "validate" && marker("fail-validate") {
		return 84
	}
	if command == "validate" && marker("no-match-validate") {
		fmt.Print("Warning: No matching builds found for the exact selector.\n\nThe configuration is valid.\n")
		return 0
	}
	if command == "validate" && marker("stderr-validate") {
		fmt.Fprintln(os.Stderr, "Warning: validation diagnostic on stderr")
	}
	if command == "build" && marker("fail-build") {
		return 85
	}
	if command == "validate" && marker("sleep-validate") {
		time.Sleep(30 * time.Second)
	}
	if command == "validate" && marker("block-validate") {
		if err := os.WriteFile("validate-started", nil, 0o600); err != nil {
			return 94
		}
		deadline := time.Now().Add(30 * time.Second)
		for !marker("release-validate") {
			if time.Now().After(deadline) {
				return 95
			}
			time.Sleep(5 * time.Millisecond)
		}
	}

	bindings := make(map[string]string)
	for index := 0; index+1 < len(args); index++ {
		if args[index] != "-var" {
			continue
		}
		name, value, found := strings.Cut(args[index+1], "=")
		if found {
			bindings[name] = value
		}
	}
	outputDirectory := bindings["output_directory"]
	materialManifest := bindings["material_manifest"]
	if outputDirectory == "" || materialManifest == "" {
		return 86
	}
	manifest, err := os.ReadFile(materialManifest)
	if err != nil || string(manifest) != manifestBytes {
		return 87
	}
	materialPath := filepath.Join(filepath.Dir(materialManifest), "materials", "component")
	material, err := os.ReadFile(materialPath)
	if err != nil || string(material) != "component-bytes\n" {
		return 89
	}
	if command == "validate" && marker("mutate-material") {
		if os.Chmod(materialPath, 0o600) != nil || os.WriteFile(materialPath, []byte("changed\n"), 0o600) != nil {
			return 92
		}
	}
	if command != "build" {
		fmt.Println("The configuration is valid.")
		return 0
	}
	if marker("omit-artifact") {
		return 0
	}

	images := filepath.Join(outputDirectory, "images")
	artifact := filepath.Join(images, "node.qcow2")
	if err := os.MkdirAll(images, 0o700); err != nil {
		return 93
	}
	cwd, err := os.Getwd()
	if err != nil {
		return 93
	}
	external := filepath.Join(cwd, "external.qcow2")
	switch {
	case marker("symlink-artifact"):
		if os.WriteFile(external, []byte("outside\n"), 0o600) != nil || os.Symlink(external, artifact) != nil {
			return 93
		}
	case marker("symlink-artifact-parent"):
		if os.Remove(images) != nil || os.Symlink(cwd, images) != nil ||
			os.WriteFile(external, []byte("outside\n"), 0o600) != nil {
			return 93
		}
	default:
		if os.WriteFile(artifact, []byte("packer-artifact\n"), 0o600) != nil {
			return 93
		}
	}
	return 0
}

func marker(name string) bool {
	_, err := os.Stat(name)
	return err == nil
}
`

func packerTestHelperExecutable(t *testing.T) string {
	t.Helper()
	packerTestHelperOnce.Do(func() {
		packerTestHelperRoot, packerTestHelperErr = os.MkdirTemp("", "nkd-packer-test-helper-")
		if packerTestHelperErr != nil {
			return
		}
		sourcePath := filepath.Join(packerTestHelperRoot, "main.go")
		if packerTestHelperErr = os.WriteFile(sourcePath, []byte(packerTestHelperSource), 0o600); packerTestHelperErr != nil {
			return
		}
		packerTestHelperPath = filepath.Join(packerTestHelperRoot, "packer-helper")
		command := exec.Command("go", "build", "-trimpath", "-o", packerTestHelperPath, sourcePath)
		command.Env = append(os.Environ(), "GOTOOLCHAIN=local")
		if output, err := command.CombinedOutput(); err != nil {
			packerTestHelperErr = fmt.Errorf("building uninstrumented Packer test helper: %w: %s", err, output)
		}
	})
	if packerTestHelperErr != nil {
		t.Fatal(packerTestHelperErr)
	}
	return packerTestHelperPath
}

func packerErrorCode(err error) nkderr.Code {
	var coded *nkderr.Error
	if errors.As(err, &coded) {
		return coded.Code
	}
	return ""
}

func requireLinuxPackerExecution(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("held executable authority is Linux-only")
	}
}

func writeFakePacker(t *testing.T) (string, string) {
	t.Helper()
	return copyTestExecutable(t, "packer")
}

func writeFakePackerHostTool(t *testing.T) (string, string) {
	t.Helper()
	return copyTestExecutable(t, "nkd-test-helper")
}

func copyTestExecutable(t *testing.T, name string) (string, string) {
	t.Helper()
	executable := packerTestHelperExecutable(t)
	raw, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, raw, 0o700); err != nil {
		t.Fatal(err)
	}
	return path, FileDigest(raw)
}

func testPackerToolchain(t *testing.T) (*PackerToolchain, string) {
	t.Helper()
	requireLinuxPackerExecution(t)
	path, digest := writeFakePacker(t)
	toolchain, err := OpenPackerToolchain(t.Context(), path, digest, nil, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := toolchain.Close(); err != nil {
			t.Errorf("closing Packer toolchain: %v", err)
		}
	})
	return toolchain, path
}

func testPackerRequest(t *testing.T, projectDir string) PackerBuildRequest {
	t.Helper()
	manifest := filepath.Join(t.TempDir(), "materials.json")
	raw := []byte(`{"materials":[{"path":"materials/component","sha256":"sha256:c2aaf954104b212a6339d4f99a1f156e153868345a25a03e3d264ef2a533c1d9"}],"schema":"test"}`)
	if err := os.WriteFile(manifest, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	material := filepath.Join(t.TempDir(), "component.deb")
	materialBytes := []byte("component-bytes\n")
	if err := os.WriteFile(material, materialBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	heldMaterial, err := heldfile.OpenContext(t.Context(), material, heldfile.Options{
		Label: "test-packer-material", MaxBytes: maxPackerMaterialBytes, ExpectedDigest: FileDigest(materialBytes),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = heldMaterial.Close() })
	return PackerBuildRequest{
		ProjectPath:              ".",
		BuildName:                "qemu.node",
		ArchitectureVariable:     "architecture",
		Architecture:             "arm64",
		AcceleratorVariable:      "accelerator",
		Accelerator:              "tcg",
		MaterialManifestVariable: "material_manifest",
		MaterialManifestPath:     manifest,
		MaterialManifestSHA256:   FileDigest(raw),
		Materials: []PackerMaterialInput{{
			Path: "materials/component", SHA256: FileDigest(materialBytes), Content: heldMaterial,
		}},
		OutputDirectoryVariable: "output_directory",
		OutputParent:            t.TempDir(),
		Variables:               []PackerVariable{{Name: "custom", Value: "value"}},
		ArtifactPath:            "images/node.qcow2",
		Timeout:                 5 * time.Second,
	}
}

func testMaterializedPackerSource(projectDir string) *materializedSource {
	return &materializedSource{
		Dir:         projectDir,
		InputDigest: FileDigest([]byte("authenticated prepared source")),
		cleanup:     func() {},
	}
}

func readPackerCalls(t *testing.T, projectDir string) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(projectDir, ".nkd-test-calls"))
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(raw)), "\n")
}

func TestPackerDriverValidatesThenBuildsExactArgvAndCollectsArtifact(t *testing.T) {
	toolchain, configuredExecutable := testPackerToolchain(t)
	projectDir := t.TempDir()
	request := testPackerRequest(t, projectDir)
	var output bytes.Buffer
	request.Stdout = &output
	request.Stderr = &output

	// The admitted held executable, not this replacement pathname, must run.
	if err := os.Rename(configuredExecutable, configuredExecutable+".admitted"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configuredExecutable, []byte("#!/bin/sh\nexit 99\n"), 0o700); err != nil {
		t.Fatal(err)
	}

	t.Setenv("PKR_VAR_SECRET", "must-not-propagate")
	t.Setenv("HTTP_PROXY", "http://must-not-propagate.invalid")
	t.Setenv("HTTPS_PROXY", "http://must-not-propagate.invalid")
	artifact, err := runPacker(t.Context(), toolchain, testMaterializedPackerSource(projectDir), request)
	if err != nil {
		t.Fatal(err)
	}
	defer artifact.Close()

	lines := readPackerCalls(t, projectDir)
	if len(lines) != 2 {
		t.Fatalf("invocations = %q, want validate then build", output.String())
	}
	validate := strings.Split(lines[0], "\t")
	build := strings.Split(lines[1], "\t")
	if len(validate) != 14 || validate[0] != "CALL" || validate[1] != "validate" ||
		validate[2] != "-only=qemu.node" || validate[3] != "-var" || validate[4] != "accelerator=tcg" ||
		validate[5] != "-var" || validate[6] != "architecture=arm64" || validate[7] != "-var" ||
		validate[8] != "custom=value" || validate[9] != "-var" ||
		!strings.HasPrefix(validate[10], "material_manifest=") || validate[11] != "-var" ||
		!strings.HasPrefix(validate[12], "output_directory=") || validate[13] != "." {
		t.Fatalf("validate argv = %#v", validate)
	}
	if len(build) != 15 || build[0] != "CALL" || build[1] != "build" || build[2] != "-on-error=cleanup" ||
		build[3] != "-only=qemu.node" || !equalStrings(build[4:], validate[3:]) {
		t.Fatalf("build argv = %#v; validate argv = %#v", build, validate)
	}
	if output.String() != "The configuration is valid.\n" {
		t.Fatalf("Packer output = %q, want one clean validation result", output.String())
	}
	manifestPath := strings.TrimPrefix(validate[10], "material_manifest=")
	outputPath := strings.TrimPrefix(validate[12], "output_directory=")
	if _, err := os.Lstat(manifestPath); !os.IsNotExist(err) {
		t.Fatalf("private manifest survived driver cleanup: %v", err)
	}
	if _, err := os.Lstat(outputPath); !os.IsNotExist(err) {
		t.Fatalf("private output survived driver cleanup: %v", err)
	}
	if entries, err := os.ReadDir(request.OutputParent); err != nil || len(entries) != 0 {
		t.Fatalf("output parent contains private-run residue: entries=%v err=%v", entries, err)
	}

	var collected bytes.Buffer
	size, err := artifact.WriteToContext(t.Context(), &collected)
	if err != nil {
		t.Fatal(err)
	}
	want := []byte("packer-artifact\n")
	if !bytes.Equal(collected.Bytes(), want) || size != int64(len(want)) || artifact.Size() != int64(len(want)) || artifact.Digest() != FileDigest(want) {
		t.Fatalf("collected = %q/%d/%s", collected.Bytes(), artifact.Size(), artifact.Digest())
	}
	info, err := artifact.file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o400 {
		t.Fatalf("collected artifact mode = %#o, want 0400", info.Mode().Perm())
	}
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func TestPackerDriverCommandFailuresAreCodedAndCleaned(t *testing.T) {
	toolchain, _ := testPackerToolchain(t)
	for _, test := range []struct {
		name       string
		marker     string
		wantCalls  int
		lastPrefix string
	}{
		{name: "validation failure prevents build", marker: "fail-validate", wantCalls: 1, lastPrefix: "CALL\tvalidate\t"},
		{name: "build failure", marker: "fail-build", wantCalls: 2, lastPrefix: "CALL\tbuild\t"},
	} {
		t.Run(test.name, func(t *testing.T) {
			projectDir := t.TempDir()
			if err := os.WriteFile(filepath.Join(projectDir, test.marker), nil, 0o600); err != nil {
				t.Fatal(err)
			}
			request := testPackerRequest(t, projectDir)
			var output bytes.Buffer
			request.Stdout = &output
			request.Stderr = &output
			artifact, err := runPacker(t.Context(), toolchain, testMaterializedPackerSource(projectDir), request)
			if artifact != nil || packerErrorCode(err) != nkderr.CodeBuildFailed {
				t.Fatalf("command result = %#v, %v", artifact, err)
			}
			lines := readPackerCalls(t, projectDir)
			if len(lines) != test.wantCalls || !strings.HasPrefix(lines[len(lines)-1], test.lastPrefix) {
				t.Fatalf("command failure ran unexpected commands: %q", output.String())
			}
			if entries, readErr := os.ReadDir(request.OutputParent); readErr != nil || len(entries) != 0 {
				t.Fatalf("failed command left private-run residue: entries=%v err=%v", entries, readErr)
			}
		})
	}
}

func TestPackerDriverRejectsMaterialMutationBeforeBuild(t *testing.T) {
	toolchain, _ := testPackerToolchain(t)
	projectDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(projectDir, "mutate-material"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	request := testPackerRequest(t, projectDir)
	var output bytes.Buffer
	request.Stdout = &output
	request.Stderr = &output
	artifact, err := runPacker(t.Context(), toolchain, testMaterializedPackerSource(projectDir), request)
	if artifact != nil || packerErrorCode(err) != nkderr.CodeUnverifiedRef {
		t.Fatalf("material mutation result = %#v, %v", artifact, err)
	}
	if calls := len(readPackerCalls(t, projectDir)); calls != 1 {
		t.Fatalf("material mutation ran %d commands, want validation only", calls)
	}
}

func TestPackerDriverRejectsExitZeroValidationDiagnostics(t *testing.T) {
	toolchain, _ := testPackerToolchain(t)
	for _, test := range []struct {
		name       string
		marker     string
		wantOutput string
	}{
		{name: "stdout warning", marker: "no-match-validate", wantOutput: "No matching builds"},
		{name: "stderr warning", marker: "stderr-validate", wantOutput: "diagnostic on stderr"},
	} {
		t.Run(test.name, func(t *testing.T) {
			projectDir := t.TempDir()
			if err := os.WriteFile(filepath.Join(projectDir, test.marker), nil, 0o600); err != nil {
				t.Fatal(err)
			}
			request := testPackerRequest(t, projectDir)
			var output bytes.Buffer
			request.Stdout = &output
			request.Stderr = &output
			artifact, err := runPacker(t.Context(), toolchain, testMaterializedPackerSource(projectDir), request)
			if artifact != nil || packerErrorCode(err) != nkderr.CodeBuildFailed {
				t.Fatalf("diagnostic validation result = %#v, %v", artifact, err)
			}
			if calls := readPackerCalls(t, projectDir); len(calls) != 1 || !strings.HasPrefix(calls[0], "CALL\tvalidate\t") {
				t.Fatalf("diagnostic validation ran unexpected commands: %q", calls)
			}
			if !strings.Contains(output.String(), test.wantOutput) {
				t.Fatalf("validation diagnostic was not preserved for the caller: %q", output.String())
			}
		})
	}
}

func TestPackerValidationOutputAcceptsOnlyBoundedExactOutput(t *testing.T) {
	for _, test := range []struct {
		name  string
		raw   []byte
		want  string
		exact bool
		empty bool
	}{
		{name: "clean", raw: []byte("The configuration is valid.\n"), want: "The configuration is valid.", exact: true},
		{name: "diagnostic", raw: []byte("Warning\nThe configuration is valid.\n"), want: "The configuration is valid."},
		{name: "empty", want: "The configuration is valid.", empty: true},
		{name: "overflow", raw: bytes.Repeat([]byte("x"), maxPackerValidationOutputBytes+1), want: strings.Repeat("x", maxPackerValidationOutputBytes)},
	} {
		t.Run(test.name, func(t *testing.T) {
			var forwarded bytes.Buffer
			output := newPackerValidationOutput(&forwarded)
			written, err := output.Write(test.raw)
			if err != nil || written != len(test.raw) || !bytes.Equal(forwarded.Bytes(), test.raw) {
				t.Fatalf("forwarded %d bytes/%v = %d bytes", written, err, forwarded.Len())
			}
			if got := output.exact(test.want); got != test.exact {
				t.Fatalf("exact(%q) = %v, want %v", test.want, got, test.exact)
			}
			if got := output.empty(); got != test.empty {
				t.Fatalf("empty() = %v, want %v", got, test.empty)
			}
		})
	}
}

func TestPackerDriverCancellationAndTimeoutAreCodedAndCleaned(t *testing.T) {
	toolchain, _ := testPackerToolchain(t)
	for _, test := range []struct {
		name    string
		ctx     func() (context.Context, context.CancelFunc)
		timeout time.Duration
	}{
		{
			name: "caller cancellation",
			ctx: func() (context.Context, context.CancelFunc) {
				ctx, cancel := context.WithCancel(context.Background())
				time.AfterFunc(100*time.Millisecond, cancel)
				return ctx, cancel
			},
			timeout: 5 * time.Second,
		},
		{
			name: "executor timeout",
			ctx: func() (context.Context, context.CancelFunc) {
				return context.WithCancel(context.Background())
			},
			timeout: 100 * time.Millisecond,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			projectDir := t.TempDir()
			if err := os.WriteFile(filepath.Join(projectDir, "sleep-validate"), nil, 0o600); err != nil {
				t.Fatal(err)
			}
			request := testPackerRequest(t, projectDir)
			request.Timeout = test.timeout
			ctx, cancel := test.ctx()
			defer cancel()
			started := time.Now()
			artifact, err := runPacker(ctx, toolchain, testMaterializedPackerSource(projectDir), request)
			if artifact != nil || packerErrorCode(err) != nkderr.CodeTimeout {
				t.Fatalf("canceled result = %#v, %v", artifact, err)
			}
			if elapsed := time.Since(started); elapsed > 3*time.Second {
				t.Fatalf("canceled Packer process took %s to terminate", elapsed)
			}
			if entries, readErr := os.ReadDir(request.OutputParent); readErr != nil || len(entries) != 0 {
				t.Fatalf("canceled run left private-run residue: entries=%v err=%v", entries, readErr)
			}
		})
	}
}

func TestPackerDriverRejectsMissingAndSymlinkedOutputs(t *testing.T) {
	toolchain, _ := testPackerToolchain(t)
	for _, test := range []struct {
		name   string
		marker string
	}{
		{name: "missing artifact", marker: "omit-artifact"},
		{name: "artifact symlink", marker: "symlink-artifact"},
		{name: "artifact parent symlink", marker: "symlink-artifact-parent"},
	} {
		t.Run(test.name, func(t *testing.T) {
			projectDir := t.TempDir()
			if err := os.WriteFile(filepath.Join(projectDir, test.marker), nil, 0o600); err != nil {
				t.Fatal(err)
			}
			request := testPackerRequest(t, projectDir)
			artifact, err := runPacker(t.Context(), toolchain, testMaterializedPackerSource(projectDir), request)
			if artifact != nil || packerErrorCode(err) != nkderr.CodeVerifyFailed {
				t.Fatalf("output result = %#v, %v", artifact, err)
			}
			if entries, readErr := os.ReadDir(request.OutputParent); readErr != nil || len(entries) != 0 {
				t.Fatalf("failed collection left private-run residue: entries=%v err=%v", entries, readErr)
			}
		})
	}
}

func TestPackerRequestValidationRejectsUnsafeTransport(t *testing.T) {
	base := testPackerRequest(t, t.TempDir())
	for _, test := range []struct {
		name string
		edit func(*PackerBuildRequest)
		code nkderr.Code
	}{
		{name: "no timeout", edit: func(request *PackerBuildRequest) { request.Timeout = 0 }, code: nkderr.CodeInvalidCatalog},
		{name: "project traversal", edit: func(request *PackerBuildRequest) { request.ProjectPath = "../packer" }, code: nkderr.CodeInvalidCatalog},
		{name: "build list", edit: func(request *PackerBuildRequest) { request.BuildName = "qemu.a,qemu.b" }, code: nkderr.CodeInvalidCatalog},
		{name: "build display control", edit: func(request *PackerBuildRequest) { request.BuildName = "qemu.\u202enode" }, code: nkderr.CodeInvalidCatalog},
		{name: "architecture display control", edit: func(request *PackerBuildRequest) { request.Architecture = "arm\u200b64" }, code: nkderr.CodeInvalidCatalog},
		{name: "accelerator absent", edit: func(request *PackerBuildRequest) { request.Accelerator = "" }, code: nkderr.CodeInvalidCatalog},
		{name: "accelerator unadmitted", edit: func(request *PackerBuildRequest) { request.Accelerator = "host" }, code: nkderr.CodeInvalidCatalog},
		{name: "partial SSH mapping", edit: func(request *PackerBuildRequest) { request.SSHPrivateKeyVariable = "ssh_private_key_file" }, code: nkderr.CodeInvalidCatalog},
		{name: "host SSH mapping unsupported", edit: func(request *PackerBuildRequest) {
			request.SSHPrivateKeyVariable = "ssh_private_key_file"
			request.SSHPublicKeyVariable = "ssh_public_key_file"
		}, code: nkderr.CodeEngineCapability},
		{name: "artifact traversal", edit: func(request *PackerBuildRequest) { request.ArtifactPath = "../node.qcow2" }, code: nkderr.CodeInvalidCatalog},
		{name: "manifest unpinned", edit: func(request *PackerBuildRequest) { request.MaterialManifestSHA256 = "latest" }, code: nkderr.CodeUnpinnedInput},
		{name: "material traversal", edit: func(request *PackerBuildRequest) { request.Materials[0].Path = "../component.deb" }, code: nkderr.CodeInvalidCatalog},
		{name: "material unpinned", edit: func(request *PackerBuildRequest) { request.Materials[0].SHA256 = "latest" }, code: nkderr.CodeUnpinnedInput},
		{name: "material path duplicate", edit: func(request *PackerBuildRequest) {
			request.Materials = append(request.Materials, request.Materials[0])
		}, code: nkderr.CodeInvalidCatalog},
		{name: "material parent file conflict", edit: func(request *PackerBuildRequest) {
			request.Materials = append(request.Materials, PackerMaterialInput{
				Path: "materials", SHA256: request.Materials[0].SHA256, Content: request.Materials[0].Content,
			})
		}, code: nkderr.CodeInvalidCatalog},
		{name: "reserved collision", edit: func(request *PackerBuildRequest) { request.Variables[0].Name = "architecture" }, code: nkderr.CodeInvalidCatalog},
		{name: "delimiter in name", edit: func(request *PackerBuildRequest) { request.Variables[0].Name = "architecture=other" }, code: nkderr.CodeInvalidCatalog},
		{name: "control in value", edit: func(request *PackerBuildRequest) { request.Variables[0].Value = "one\ntwo" }, code: nkderr.CodeInvalidCatalog},
		{name: "too many variables", edit: func(request *PackerBuildRequest) {
			request.Variables = make([]PackerVariable, packertransport.MaxPublicVariables+1)
		}, code: nkderr.CodeInvalidCatalog},
		{name: "variable argv too large", edit: func(request *PackerBuildRequest) {
			request.Variables = make([]PackerVariable, 17)
			for index := range request.Variables {
				request.Variables[index] = PackerVariable{
					Name:  fmt.Sprintf("variable_%d", index),
					Value: strings.Repeat("x", packertransport.MaxVariableValueBytes),
				}
			}
		}, code: nkderr.CodeInvalidCatalog},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := base
			request.Variables = append([]PackerVariable(nil), base.Variables...)
			request.Materials = append([]PackerMaterialInput(nil), base.Materials...)
			test.edit(&request)
			if code := packerErrorCode(validatePackerBuildRequest(request)); code != test.code {
				t.Fatalf("code = %q, want %q", code, test.code)
			}
		})
	}
}

func TestPackerToolchainCloseAcceptsZeroValue(t *testing.T) {
	var toolchain PackerToolchain
	if err := toolchain.Close(); err != nil {
		t.Fatal(err)
	}
	if err := toolchain.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestPackerArtifactRejectsChangedAndClosedContent(t *testing.T) {
	raw := []byte("artifact bytes\n")
	path := filepath.Join(t.TempDir(), "artifact")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	artifact := &PackerArtifact{
		file: file, digest: FileDigest([]byte("different bytes\n")), size: int64(len(raw)),
	}
	if _, err := artifact.WriteToContext(t.Context(), io.Discard); packerErrorCode(err) != nkderr.CodeVerifyFailed {
		t.Fatalf("changed artifact error = %v", err)
	}
	if err := artifact.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := artifact.WriteToContext(t.Context(), io.Discard); packerErrorCode(err) != nkderr.CodeVerifyFailed {
		t.Fatalf("closed artifact error = %v", err)
	}
}

func TestPackerToolchainCloseWaitsForActiveRunAndBlocksNewRuns(t *testing.T) {
	toolchain, _ := testPackerToolchain(t)
	const activeRuns = 4
	type activeRun struct {
		projectDir string
		request    PackerBuildRequest
	}
	type runResult struct {
		index    int
		artifact *PackerArtifact
		err      error
	}
	runs := make([]activeRun, activeRuns)
	runDone := make(chan runResult, activeRuns)
	for index := range runs {
		projectDir := t.TempDir()
		if err := os.WriteFile(filepath.Join(projectDir, "block-validate"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		runs[index] = activeRun{projectDir: projectDir, request: testPackerRequest(t, projectDir)}
		go func(index int) {
			run := runs[index]
			artifact, err := runPacker(context.Background(), toolchain,
				testMaterializedPackerSource(run.projectDir), run.request)
			runDone <- runResult{index: index, artifact: artifact, err: err}
		}(index)
	}
	releaseActiveRuns := func() error {
		var errs []error
		for _, run := range runs {
			errs = append(errs, os.WriteFile(filepath.Join(run.projectDir, "release-validate"), nil, 0o600))
		}
		return errors.Join(errs...)
	}
	released := false
	defer func() {
		if !released {
			_ = releaseActiveRuns()
		}
	}()
	for _, run := range runs {
		waitForPackerMarker(t, run.projectDir, "validate-started")
	}

	closeDone := make(chan error, 1)
	go func() { closeDone <- toolchain.Close() }()
	waitForPackerCloseWriter(t, toolchain)
	select {
	case err := <-closeDone:
		t.Fatalf("Close returned before %d active runs completed: %v", activeRuns, err)
	default:
	}

	blockedProject := t.TempDir()
	blockedRequest := testPackerRequest(t, blockedProject)
	blockedRequest.Timeout = 100 * time.Millisecond
	if artifact, err := runPacker(context.Background(), toolchain, testMaterializedPackerSource(blockedProject), blockedRequest); artifact != nil || packerErrorCode(err) != nkderr.CodeTimeout {
		t.Fatalf("run racing with Close = %#v, %v; want timeout", artifact, err)
	}
	if err := releaseActiveRuns(); err != nil {
		t.Fatal(err)
	}
	released = true
	for range runs {
		select {
		case result := <-runDone:
			if result.err != nil || result.artifact == nil {
				t.Fatalf("active run %d = %#v, %v", result.index, result.artifact, result.err)
			}
			if err := result.artifact.Close(); err != nil {
				t.Fatal(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("active Packer runs did not finish")
		}
	}
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not finish after active run")
	}
	closedArtifact, closedErr := runPacker(context.Background(), toolchain,
		testMaterializedPackerSource(blockedProject), blockedRequest)
	if closedArtifact != nil || packerErrorCode(closedErr) != nkderr.CodeEngineCapability {
		t.Fatalf("run after Close = %#v, %v; want %s", closedArtifact, closedErr, nkderr.CodeEngineCapability)
	}
}

func waitForPackerCloseWriter(t *testing.T, toolchain *PackerToolchain) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if !toolchain.mu.TryRLock() {
			return
		}
		toolchain.mu.RUnlock()
		if time.Now().After(deadline) {
			t.Fatal("Packer toolchain Close did not begin waiting for active runs")
		}
		time.Sleep(time.Millisecond)
	}
}

func waitForPackerMarker(t *testing.T, projectDir, name string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(projectDir, name)); err == nil {
			return
		} else if !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("Packer marker %q was not created", name)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestPackerDriverRequiresPreparedSourceAndRejectsPathSymlinks(t *testing.T) {
	toolchain, _ := testPackerToolchain(t)
	projectDir := t.TempDir()
	request := testPackerRequest(t, projectDir)
	if artifact, err := runPacker(t.Context(), toolchain, nil, request); artifact != nil || packerErrorCode(err) != nkderr.CodeEngineCapability {
		t.Fatalf("nil prepared source result = %#v, %v", artifact, err)
	}

	realProject := filepath.Join(projectDir, "real")
	if err := os.Mkdir(realProject, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realProject, filepath.Join(projectDir, "linked")); err != nil {
		t.Fatal(err)
	}
	request.ProjectPath = "linked"
	if artifact, err := runPacker(t.Context(), toolchain, testMaterializedPackerSource(projectDir), request); artifact != nil || packerErrorCode(err) != nkderr.CodeInvalidCatalog {
		t.Fatalf("symlinked project result = %#v, %v", artifact, err)
	}
	if err := os.Mkdir(filepath.Join(realProject, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	request.ProjectPath = filepath.Join("linked", "nested")
	if artifact, err := runPacker(t.Context(), toolchain, testMaterializedPackerSource(projectDir), request); artifact != nil || packerErrorCode(err) != nkderr.CodeInvalidCatalog {
		t.Fatalf("intermediate-symlinked project result = %#v, %v", artifact, err)
	}

	request = testPackerRequest(t, projectDir)
	realParent := t.TempDir()
	linkedParent := filepath.Join(t.TempDir(), "linked-output")
	if err := os.Symlink(realParent, linkedParent); err != nil {
		t.Fatal(err)
	}
	request.OutputParent = linkedParent
	if code := packerErrorCode(validatePackerBuildRequest(request)); code != nkderr.CodeEngineCapability {
		t.Fatalf("symlinked output parent code = %q", code)
	}

	request = testPackerRequest(t, projectDir)
	realManifest := request.MaterialManifestPath
	linkedManifest := filepath.Join(t.TempDir(), "materials.json")
	if err := os.Symlink(realManifest, linkedManifest); err != nil {
		t.Fatal(err)
	}
	request.MaterialManifestPath = linkedManifest
	if artifact, err := runPacker(t.Context(), toolchain, testMaterializedPackerSource(projectDir), request); artifact != nil || packerErrorCode(err) != nkderr.CodeUnverifiedRef {
		t.Fatalf("symlinked manifest result = %#v, %v", artifact, err)
	}

	request = testPackerRequest(t, projectDir)
	request.MaterialManifestSHA256 = FileDigest([]byte("different manifest"))
	if artifact, err := runPacker(t.Context(), toolchain, testMaterializedPackerSource(projectDir), request); artifact != nil || packerErrorCode(err) != nkderr.CodeUnverifiedRef {
		t.Fatalf("wrong manifest digest result = %#v, %v", artifact, err)
	}

	request = testPackerRequest(t, projectDir)
	if err := request.Materials[0].Content.Close(); err != nil {
		t.Fatal(err)
	}
	if artifact, err := runPacker(t.Context(), toolchain, testMaterializedPackerSource(projectDir), request); artifact != nil || packerErrorCode(err) != nkderr.CodeUnverifiedRef {
		t.Fatalf("closed material capability result = %#v, %v", artifact, err)
	}

	request = testPackerRequest(t, projectDir)
	request.Materials[0].SHA256 = FileDigest([]byte("different material"))
	if artifact, err := runPacker(t.Context(), toolchain, testMaterializedPackerSource(projectDir), request); artifact != nil || packerErrorCode(err) != nkderr.CodeUnverifiedRef {
		t.Fatalf("wrong material digest result = %#v, %v", artifact, err)
	}
}

func TestPackerToolchainPinsPluginClosureAndFailsClosed(t *testing.T) {
	requireLinuxPackerExecution(t)
	packerPath, packerDigest := writeFakePacker(t)
	pluginRoot := t.TempDir()
	pluginPath := filepath.Join("github.com", "hashicorp", "qemu", "packer-plugin-qemu")
	pluginBytes, err := os.ReadFile(packerPath)
	if err != nil {
		t.Fatal(err)
	}
	fullPluginPath := filepath.Join(pluginRoot, pluginPath)
	if err := os.MkdirAll(filepath.Dir(fullPluginPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fullPluginPath, pluginBytes, 0o700); err != nil {
		t.Fatal(err)
	}
	toolchain, err := OpenPackerToolchain(t.Context(), packerPath, packerDigest, nil, pluginRoot, []PackerPluginFile{{
		Path: pluginPath, SHA256: FileDigest(pluginBytes), Executable: true,
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer toolchain.Close()
	if toolchain.pluginsDir == pluginRoot {
		t.Fatal("toolchain retained mutable plugin source root")
	}
	if err := os.WriteFile(fullPluginPath, []byte("mutated source\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := verifyPackerToolchain(t.Context(), toolchain); err != nil {
		t.Fatalf("source mutation changed private plugin snapshot: %v", err)
	}

	privatePlugin := filepath.Join(toolchain.pluginsDir, pluginPath)
	if err := os.Chmod(privatePlugin, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(privatePlugin, []byte("tampered private plugin\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if code := packerErrorCode(verifyPackerToolchain(t.Context(), toolchain)); code != nkderr.CodeUnverifiedRef {
		t.Fatalf("tampered private plugin code = %q", code)
	}
}

func TestPackerToolchainPinsHostToolClosureForPrivatePATH(t *testing.T) {
	requireLinuxPackerExecution(t)
	packerPath, packerDigest := writeFakePacker(t)
	hostToolPath, hostToolDigest := writeFakePackerHostTool(t)
	toolchain, err := OpenPackerToolchain(t.Context(), packerPath, packerDigest, []PackerHostTool{{
		Name: "nkd-test-helper", Path: hostToolPath, SHA256: hostToolDigest,
	}}, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer toolchain.Close()

	if err := os.WriteFile(hostToolPath, []byte("#!/bin/sh\nprintf 'replaced-host-tool\\n'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := verifyPackerToolchain(t.Context(), toolchain); err != nil {
		t.Fatalf("source mutation changed held host-tool closure: %v", err)
	}

	projectDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(projectDir, "require-host-tool"), []byte("required\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	request := testPackerRequest(t, projectDir)
	artifact, err := runPacker(t.Context(), toolchain, testMaterializedPackerSource(projectDir), request)
	if err != nil {
		t.Fatal(err)
	}
	if err := artifact.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestPackerHostToolDescriptorSurvivesGoSubprocessExec(t *testing.T) {
	requireLinuxPackerExecution(t)
	executablePath, executableDigest := writeFakePacker(t)
	hostToolPath, hostToolDigest := writeFakePackerHostTool(t)
	toolchain, err := OpenPackerToolchain(t.Context(), executablePath, executableDigest, []PackerHostTool{{
		Name: "nkd-test-helper", Path: hostToolPath, SHA256: hostToolDigest,
	}}, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer toolchain.Close()
	hostPath := filepath.Join(t.TempDir(), "bin")
	if err := os.Mkdir(hostPath, 0o700); err != nil {
		t.Fatal(err)
	}
	restoreOwnerWriteForCleanup(t, hostPath)
	if err := materializePackerHostPath(hostPath, toolchain.hostTools); err != nil {
		t.Fatal(err)
	}
	if err := runPackerCommand(t.Context(), toolchain, t.TempDir(), []string{
		"NKD_PACKER_GO_HOST_TOOL_HELPER=1",
		"PATH=" + hostPath,
	}, []string{"host-tool-check"}, io.Discard, io.Discard, "testing Go host-tool inheritance"); err != nil {
		t.Fatal(err)
	}
}

func TestPackerHostPathTamperingFailsClosed(t *testing.T) {
	requireLinuxPackerExecution(t)
	packerPath, packerDigest := writeFakePacker(t)
	hostToolPath, hostToolDigest := writeFakePackerHostTool(t)
	toolchain, err := OpenPackerToolchain(t.Context(), packerPath, packerDigest, []PackerHostTool{{
		Name: "nkd-test-helper", Path: hostToolPath, SHA256: hostToolDigest,
	}}, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer toolchain.Close()

	for _, test := range []struct {
		name   string
		tamper func(string) error
	}{
		{name: "writable root", tamper: func(root string) error { return os.Chmod(root, 0o700) }},
		{name: "extra entry", tamper: func(root string) error {
			if err := os.Chmod(root, 0o700); err != nil {
				return err
			}
			if err := os.Symlink("/proc/self/fd/1", filepath.Join(root, "extra")); err != nil {
				return err
			}
			return os.Chmod(root, 0o500)
		}},
		{name: "undeclared entry", tamper: func(root string) error {
			if err := os.Chmod(root, 0o700); err != nil {
				return err
			}
			if err := os.Remove(filepath.Join(root, "nkd-test-helper")); err != nil {
				return err
			}
			if err := os.Symlink("/proc/self/fd/1", filepath.Join(root, "other")); err != nil {
				return err
			}
			return os.Chmod(root, 0o500)
		}},
		{name: "changed target", tamper: func(root string) error {
			if err := os.Chmod(root, 0o700); err != nil {
				return err
			}
			entry := filepath.Join(root, "nkd-test-helper")
			if err := os.Remove(entry); err != nil {
				return err
			}
			if err := os.Symlink("/proc/self/fd/999999", entry); err != nil {
				return err
			}
			return os.Chmod(root, 0o500)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			hostPath := filepath.Join(t.TempDir(), "bin")
			if err := os.Mkdir(hostPath, 0o700); err != nil {
				t.Fatal(err)
			}
			restoreOwnerWriteForCleanup(t, hostPath)
			if err := materializePackerHostPath(hostPath, toolchain.hostTools); err != nil {
				t.Fatal(err)
			}
			if err := test.tamper(hostPath); err != nil {
				t.Fatal(err)
			}
			if code := packerErrorCode(verifyPackerHostPath(hostPath, toolchain.hostTools)); code != nkderr.CodeUnverifiedRef {
				t.Fatalf("tampered PATH code = %q", code)
			}
		})
	}
}

func restoreOwnerWriteForCleanup(t *testing.T, path string) {
	t.Helper()
	t.Cleanup(func() {
		if err := os.Chmod(path, 0o700); err != nil && !os.IsNotExist(err) {
			t.Errorf("restoring owner-write permission on %q for cleanup: %v", path, err)
		}
	})
}

func TestPackerToolchainRejectsInvalidHostToolPins(t *testing.T) {
	packerPath, packerDigest := writeFakePacker(t)
	hostToolPath, hostToolDigest := writeFakePackerHostTool(t)
	tests := []struct {
		name  string
		tools []PackerHostTool
		code  nkderr.Code
	}{
		{name: "non-basename", tools: []PackerHostTool{{Name: "bin/qemu", Path: hostToolPath, SHA256: hostToolDigest}}, code: nkderr.CodeInvalidCatalog},
		{name: "relative source", tools: []PackerHostTool{{Name: "qemu", Path: "qemu", SHA256: hostToolDigest}}, code: nkderr.CodeEngineCapability},
		{name: "unpinned", tools: []PackerHostTool{{Name: "qemu", Path: hostToolPath, SHA256: "latest"}}, code: nkderr.CodeUnpinnedInput},
		{name: "duplicate name", tools: []PackerHostTool{
			{Name: "qemu", Path: hostToolPath, SHA256: hostToolDigest},
			{Name: "qemu", Path: hostToolPath, SHA256: hostToolDigest},
		}, code: nkderr.CodeInvalidCatalog},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := OpenPackerToolchain(t.Context(), packerPath, packerDigest, test.tools, "", nil); packerErrorCode(err) != test.code {
				t.Fatalf("error = %v, want code %q", err, test.code)
			}
		})
	}
}

func TestPackerClosureCountAndSizeBoundsFailClosed(t *testing.T) {
	packerPath, packerDigest := writeFakePacker(t)
	t.Run("host-tool count", func(t *testing.T) {
		tools := make([]PackerHostTool, maxPackerHostTools+1)
		if _, err := OpenPackerToolchain(t.Context(), packerPath, packerDigest, tools, "", nil); packerErrorCode(err) != nkderr.CodeInvalidCatalog {
			t.Fatalf("host-tool count error = %v", err)
		}
	})
	t.Run("plugin count", func(t *testing.T) {
		plugins := make([]PackerPluginFile, maxPackerPluginFiles+1)
		if _, err := OpenPackerToolchain(t.Context(), packerPath, packerDigest, nil, "", plugins); packerErrorCode(err) != nkderr.CodeInvalidCatalog {
			t.Fatalf("plugin count error = %v", err)
		}
	})
	t.Run("material count", func(t *testing.T) {
		request := testPackerRequest(t, t.TempDir())
		request.Materials = make([]PackerMaterialInput, maxPackerMaterialFiles+1)
		if code := packerErrorCode(validatePackerBuildRequest(request)); code != nkderr.CodeInvalidCatalog {
			t.Fatalf("material count code = %q", code)
		}
	})

	maxInt64 := int64(^uint64(0) >> 1)
	for _, test := range []struct {
		name                   string
		total, size, file, all int64
		wantTotal              int64
		wantOK                 bool
	}{
		{name: "within bounds", total: 4, size: 6, file: 6, all: 10, wantTotal: 10, wantOK: true},
		{name: "individual bound", size: 7, file: 6, all: 10},
		{name: "aggregate bound", total: 5, size: 6, file: 6, all: 10},
		{name: "negative size", size: -1, file: 6, all: 10},
		{name: "arithmetic overflow", total: maxInt64, size: 1, file: maxInt64, all: maxInt64},
	} {
		t.Run("size "+test.name, func(t *testing.T) {
			got, ok := addPackerBoundedSize(test.total, test.size, test.file, test.all)
			if got != test.wantTotal || ok != test.wantOK {
				t.Fatalf("addPackerBoundedSize() = %d/%v, want %d/%v", got, ok, test.wantTotal, test.wantOK)
			}
		})
	}
}

func TestPackerToolchainRejectsNonELFExecutableFormats(t *testing.T) {
	requireLinuxPackerExecution(t)
	packerPath, packerDigest := writeFakePacker(t)
	nonELFRoot := t.TempDir()
	nonELFBytes := []byte("MZ-not-an-elf-executable\n")
	nonELFPath := filepath.Join(nonELFRoot, "not-elf")
	if err := os.WriteFile(nonELFPath, nonELFBytes, 0o700); err != nil {
		t.Fatal(err)
	}
	nonELFDigest := FileDigest(nonELFBytes)
	for _, test := range []struct {
		name string
		open func() error
	}{
		{name: "Packer", open: func() error {
			_, err := OpenPackerToolchain(t.Context(), nonELFPath, nonELFDigest, nil, "", nil)
			return err
		}},
		{name: "host tool", open: func() error {
			_, err := OpenPackerToolchain(t.Context(), packerPath, packerDigest, []PackerHostTool{{
				Name: "not-elf", Path: nonELFPath, SHA256: nonELFDigest,
			}}, "", nil)
			return err
		}},
		{name: "plugin", open: func() error {
			_, err := OpenPackerToolchain(t.Context(), packerPath, packerDigest, nil, nonELFRoot, []PackerPluginFile{{
				Path: "not-elf", SHA256: nonELFDigest, Executable: true,
			}})
			return err
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := test.open(); packerErrorCode(err) != nkderr.CodeEngineCapability {
				t.Fatalf("non-ELF error = %v", err)
			}
		})
	}
}

func TestPackerToolchainRejectsInvalidPinsAndPluginSymlinks(t *testing.T) {
	packerPath, packerDigest := writeFakePacker(t)
	if _, err := OpenPackerToolchain(nil, packerPath, packerDigest, nil, "", nil); packerErrorCode(err) != nkderr.CodeUsage { //nolint:staticcheck // nil-context contract
		t.Fatalf("nil context error = %v", err)
	}
	if _, err := OpenPackerToolchain(t.Context(), packerPath, "latest", nil, "", nil); packerErrorCode(err) != nkderr.CodeUnpinnedInput {
		t.Fatalf("unpinned executable error = %v", err)
	}
	if _, err := OpenPackerToolchain(t.Context(), filepath.Base(packerPath), packerDigest, nil, "", nil); packerErrorCode(err) != nkderr.CodeEngineCapability {
		t.Fatalf("ambient executable name error = %v", err)
	}
	if _, err := OpenPackerToolchain(t.Context(), packerPath, packerDigest, nil, "", []PackerPluginFile{
		{Path: "plugin", SHA256: FileDigest([]byte("plugin\n"))},
		{Path: "plugin/checksum", SHA256: FileDigest([]byte("checksum\n"))},
	}); packerErrorCode(err) != nkderr.CodeInvalidCatalog {
		t.Fatalf("plugin parent-file conflict error = %v", err)
	}
	if runtime.GOOS != "linux" {
		if _, err := OpenPackerToolchain(t.Context(), packerPath, packerDigest, nil, "", nil); packerErrorCode(err) != nkderr.CodeEngineCapability {
			t.Fatalf("unsupported host error = %v", err)
		}
		return
	}
	scriptRoot := t.TempDir()
	scriptBytes := []byte("#!/bin/sh\nexit 0\n")
	scriptPath := filepath.Join(scriptRoot, "script")
	if err := os.WriteFile(scriptPath, scriptBytes, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenPackerToolchain(t.Context(), scriptPath, FileDigest(scriptBytes), nil, "", nil); packerErrorCode(err) != nkderr.CodeEngineCapability {
		t.Fatalf("script Packer executable error = %v", err)
	}
	if _, err := OpenPackerToolchain(t.Context(), packerPath, packerDigest, []PackerHostTool{{
		Name: "script", Path: scriptPath, SHA256: FileDigest(scriptBytes),
	}}, "", nil); packerErrorCode(err) != nkderr.CodeEngineCapability {
		t.Fatalf("script host-tool error = %v", err)
	}
	if _, err := OpenPackerToolchain(t.Context(), packerPath, packerDigest, nil, scriptRoot, []PackerPluginFile{{
		Path: "script", SHA256: FileDigest(scriptBytes), Executable: true,
	}}); packerErrorCode(err) != nkderr.CodeEngineCapability {
		t.Fatalf("script plugin executable error = %v", err)
	}

	pluginRoot := t.TempDir()
	realPlugin := filepath.Join(pluginRoot, "real-plugin")
	if err := os.WriteFile(realPlugin, []byte("plugin\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realPlugin, filepath.Join(pluginRoot, "plugin-link")); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenPackerToolchain(t.Context(), packerPath, packerDigest, nil, pluginRoot, []PackerPluginFile{{
		Path: "plugin-link", SHA256: FileDigest([]byte("plugin\n")), Executable: true,
	}}); packerErrorCode(err) != nkderr.CodeUnverifiedRef {
		t.Fatalf("symlinked plugin error = %v", err)
	}
	if _, err := OpenPackerToolchain(t.Context(), packerPath, packerDigest, nil, pluginRoot, []PackerPluginFile{{
		Path: "real-plugin", SHA256: FileDigest([]byte("other\n")), Executable: true,
	}}); packerErrorCode(err) != nkderr.CodeUnverifiedRef {
		t.Fatalf("wrong plugin digest error = %v", err)
	}
}

func TestPackerProjectDescriptorSurvivesPathReplacement(t *testing.T) {
	requireLinuxPackerExecution(t)
	root := t.TempDir()
	project := filepath.Join(root, "project")
	if err := os.Mkdir(project, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "identity"), []byte("held\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, handle, err := openPackerProjectDirectory(root, "project")
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Close()
	descriptorPath := fmt.Sprintf("/proc/%d/fd/%d", os.Getpid(), handle.Fd())

	moved := filepath.Join(root, "moved")
	if err := os.Rename(project, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(project, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "identity"), []byte("replacement\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(descriptorPath, "identity"))
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "held\n" {
		t.Fatalf("descriptor path followed replacement bytes %q", raw)
	}
}

func TestClosedPackerToolchainFailsBeforeRequestWork(t *testing.T) {
	toolchain, _ := testPackerToolchain(t)
	if err := toolchain.Close(); err != nil {
		t.Fatal(err)
	}
	request := testPackerRequest(t, t.TempDir())
	if artifact, err := runPacker(t.Context(), toolchain, testMaterializedPackerSource(t.TempDir()), request); artifact != nil || packerErrorCode(err) != nkderr.CodeEngineCapability {
		t.Fatalf("closed toolchain result = %#v, %v", artifact, err)
	}
}
