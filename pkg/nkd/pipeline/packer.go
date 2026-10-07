// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/NVIDIA/nke-recipes/internal/securepath"
	"github.com/NVIDIA/nke-recipes/internal/transportarchive"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/internal/heldfile"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/internal/packertransport"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/internal/safetext"
)

const (
	// MaxVMImagePackerInputBytes bounds the complete authenticated input carrier
	// consumed by direct-Packer execution. The exact carrier includes source,
	// materials, manifest, runtime configuration, and archive overhead and is
	// measured before Docker creates a volume or container.
	MaxVMImagePackerInputBytes int64 = transportarchive.MaxArchiveBytes
	// packerInputCarrierReserveBytes leaves room for the outer Docker-copy tar's
	// one regular-file header, block padding, end markers, and future standard
	// library tar metadata. Both the authenticated inner closure and its outer
	// carrier are still measured before Docker creates mutable state.
	packerInputCarrierReserveBytes int64 = 1 << 20
	maxPackerInputClosureBytes           = MaxVMImagePackerInputBytes - packerInputCarrierReserveBytes

	maxPackerExecutableBytes       int64 = 512 << 20
	maxPackerHostToolBytes         int64 = 512 << 20
	maxPackerHostToolClosureBytes  int64 = 4 << 30
	maxPackerHostTools                   = 256
	maxPackerPluginBytes           int64 = 512 << 20
	maxPackerPluginClosureBytes    int64 = 4 << 30
	maxPackerPluginFiles                 = 256
	maxPackerBuildNameBytes              = 256
	maxPackerPathBytes                   = 4096
	maxPackerValidationOutputBytes       = 1 << 20
)

// PackerPluginFile pins one file in the pre-realized Packer plugin closure.
// Executable distinguishes plugin binaries from their checksum files. The
// private plugin tree contains exactly this manifest and nothing discovered from
// an ambient Packer home.
type PackerPluginFile struct {
	Path       string
	SHA256     string
	Executable bool
}

// PackerHostTool pins one PATH-resolved executable in the pre-realized VM
// toolchain closure. Name is the exact basename Packer or its plugins invoke;
// Path is an absolute resolver-owned source path and is never placed on PATH.
type PackerHostTool struct {
	Name   string
	Path   string
	SHA256 string
}

type packerHostTool struct {
	name string
	file *heldfile.File
}

// stagedPackerInputs adds the deterministic transport-archive observation used
// only by the direct-Packer container adapter.
type stagedPackerInputs struct {
	*stagedBuildMaterialInputs
	inputArchiveDigest string
}

// PackerToolchain is a process-local capability for one exact Packer executable,
// PATH-resolved host-tool closure, and plugin closure. Its fields stay private
// so a pathname or digest string alone cannot authorize execution.
type PackerToolchain struct {
	mu         sync.RWMutex
	executable *heldfile.File
	hostTools  []packerHostTool
	pluginsDir string
	plugins    []privateFileSnapshot
	closed     bool
}

// OpenPackerToolchain snapshots the digest-pinned Packer executable, every
// PATH-resolved host tool, and every digest-pinned plugin file into private
// storage. Executable inputs must be Linux ELF files; authenticating their
// dynamic loader and shared-library closure remains an outer realized-runtime
// responsibility. executablePath and host-tool paths must be exact absolute
// paths supplied by the authenticated toolchain resolver; this boundary never
// searches ambient PATH. pluginSourceRoot is ignored only when plugins is
// empty; an empty manifest produces an explicit empty PACKER_PLUGIN_PATH rather
// than falling back to ambient discovery.
func OpenPackerToolchain(ctx context.Context, executablePath, executableDigest string, hostTools []PackerHostTool, pluginSourceRoot string, plugins []PackerPluginFile) (*PackerToolchain, error) {
	if ctx == nil {
		return nil, e.New(e.CodeUsage, "opening a Packer toolchain requires a non-nil context")
	}
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "opening a Packer toolchain")
	}
	if !canonical.IsDigest(executableDigest) {
		return nil, e.New(e.CodeUnpinnedInput,
			"Packer requires an exact lowercase sha256 executable digest, got %q", executableDigest)
	}
	if !validCleanAbsolutePath(executablePath) {
		return nil, e.New(e.CodeEngineCapability,
			"Packer executable must be an exact clean absolute path")
	}
	if len(plugins) > maxPackerPluginFiles {
		return nil, e.New(e.CodeInvalidCatalog,
			"Packer plugin closure has %d files, exceeding the %d-file bound", len(plugins), maxPackerPluginFiles)
	}
	pluginPins, err := validatePackerPluginManifest(plugins)
	if err != nil {
		return nil, err
	}
	hostToolPins, err := validatePackerHostToolManifest(hostTools)
	if err != nil {
		return nil, err
	}
	if err := heldfile.RequireExecutableSupport(); err != nil {
		return nil, e.Wrap(e.CodeEngineCapability, err, "holding Packer executable by descriptor")
	}
	executable, err := heldfile.OpenContext(ctx, executablePath, heldfile.Options{
		Label:          "packer",
		Executable:     true,
		MaxBytes:       maxPackerExecutableBytes,
		ExpectedDigest: executableDigest,
	})
	if err != nil {
		if ctx.Err() != nil {
			return nil, e.Wrap(e.CodeTimeout, ctx.Err(), "capturing pinned Packer executable")
		}
		if errors.Is(err, heldfile.ErrUnsupported) {
			return nil, e.Wrap(e.CodeEngineCapability, err, "holding Packer executable by descriptor")
		}
		return nil, e.Wrap(e.CodeUnverifiedRef, err, "capturing pinned Packer executable %q", executablePath)
	}
	if err := requireELFExecutable(executable, "Packer executable"); err != nil {
		_ = executable.Close()
		return nil, err
	}
	heldHostTools, err := holdPackerHostTools(ctx, hostToolPins)
	if err != nil {
		_ = executable.Close()
		return nil, err
	}
	pluginsDir, snapshots, err := snapshotPackerPlugins(ctx, pluginSourceRoot, pluginPins)
	if err != nil {
		_ = executable.Close()
		_ = closePackerHostTools(heldHostTools)
		return nil, err
	}
	return &PackerToolchain{
		executable: executable,
		hostTools:  heldHostTools,
		pluginsDir: pluginsDir,
		plugins:    snapshots,
	}, nil
}

// Close releases the held executable and host-tool closure and removes the
// private plugin closure. It waits for the complete validate/build/collect
// lifetime of every active call; new calls remain context-cancelable while a
// close is waiting. Close is idempotent.
func (toolchain *PackerToolchain) Close() error {
	if toolchain == nil {
		return nil
	}
	toolchain.mu.Lock()
	defer toolchain.mu.Unlock()
	if toolchain.closed {
		return nil
	}
	toolchain.closed = true
	var closeErr error
	if toolchain.executable != nil {
		closeErr = toolchain.executable.Close()
	}
	toolchain.executable = nil
	hostToolCloseErr := closePackerHostTools(toolchain.hostTools)
	toolchain.hostTools = nil
	cleanupErr := removePrivateTree(toolchain.pluginsDir)
	toolchain.pluginsDir = ""
	if err := errors.Join(closeErr, hostToolCloseErr, cleanupErr); err != nil {
		return e.Wrap(e.CodeInternal, err, "closing Packer toolchain")
	}
	return nil
}

// PackerVariable is the shared public name=value transport type. These
// bindings must never contain secrets; the pinned Packer executable remains
// authoritative for HCL variable semantics.
type PackerVariable = packertransport.Variable

// PackerMaterialInput is retained as the direct-Packer API name. Both ordinary
// OCI and Packer producers consume the same held material capability.
type PackerMaterialInput = BuildMaterialInput

// PackerBuildRequest contains only runtime values. Catalog and material-graph
// owners lower their typed declarations into this boundary; the driver does not
// import catalog types or interpret HCL.
type PackerBuildRequest struct {
	ProjectPath string
	BuildName   string

	ArchitectureVariable string
	Architecture         string
	AcceleratorVariable  string
	Accelerator          string

	MaterialManifestVariable string
	MaterialManifestPath     string
	MaterialManifestSHA256   string
	Materials                []PackerMaterialInput

	OutputDirectoryVariable   string
	OutputParent              string
	BuildLogDirectoryVariable string
	HTTPDirectoryVariable     string
	SSHPrivateKeyVariable     string
	SSHPublicKeyVariable      string

	Variables    []PackerVariable
	ArtifactPath string
	Timeout      time.Duration
	Stdout       io.Writer
	Stderr       io.Writer
}

// packerBuildContract is the driver-independent invocation shape shared by
// held-host and containerized Packer. Runtime-only fields such as host output
// directories and deadlines stay with the driver that owns them.
type packerBuildContract struct {
	projectPath               string
	buildName                 string
	architectureVariable      string
	architecture              string
	acceleratorVariable       string
	accelerator               string
	materialManifestVariable  string
	materialManifestPath      string
	materialManifestSHA256    string
	materials                 []PackerMaterialInput
	outputDirectoryVariable   string
	buildLogDirectoryVariable string
	httpDirectoryVariable     string
	sshPrivateKeyVariable     string
	sshPublicKeyVariable      string
	variables                 []PackerVariable
	artifactPath              string
}

func packerContractFromBuildRequest(request PackerBuildRequest) packerBuildContract {
	return packerBuildContract{
		projectPath: request.ProjectPath, buildName: request.BuildName,
		architectureVariable: request.ArchitectureVariable, architecture: request.Architecture,
		acceleratorVariable: request.AcceleratorVariable, accelerator: request.Accelerator,
		materialManifestVariable: request.MaterialManifestVariable,
		materialManifestPath:     request.MaterialManifestPath, materialManifestSHA256: request.MaterialManifestSHA256,
		materials: request.Materials, outputDirectoryVariable: request.OutputDirectoryVariable,
		buildLogDirectoryVariable: request.BuildLogDirectoryVariable, httpDirectoryVariable: request.HTTPDirectoryVariable,
		sshPrivateKeyVariable: request.SSHPrivateKeyVariable, sshPublicKeyVariable: request.SSHPublicKeyVariable,
		variables: request.Variables, artifactPath: request.ArtifactPath,
	}
}

// PackerArtifact owns the exact regular-file inode collected from the private
// output tree. The pathname is removed before runPacker returns, so later path
// substitution cannot change these bytes.
type PackerArtifact struct {
	mu           sync.Mutex
	file         *os.File
	digest       string
	size         int64
	acquisitions *heldfile.File
}

func (artifact *PackerArtifact) Digest() string {
	if artifact == nil {
		return ""
	}
	return artifact.digest
}

func (artifact *PackerArtifact) Size() int64 {
	if artifact == nil {
		return 0
	}
	return artifact.size
}

// WriteToContext copies the collected artifact while rechecking its exact digest.
func (artifact *PackerArtifact) WriteToContext(ctx context.Context, destination io.Writer) (int64, error) {
	if ctx == nil {
		return 0, e.New(e.CodeUsage, "reading a Packer artifact requires a non-nil context")
	}
	if artifact == nil || destination == nil {
		return 0, e.New(e.CodeUsage, "a Packer artifact and destination are required")
	}
	if err := ctx.Err(); err != nil {
		return 0, e.Wrap(e.CodeTimeout, err, "reading collected Packer artifact")
	}
	artifact.mu.Lock()
	defer artifact.mu.Unlock()
	if artifact.file == nil {
		return 0, e.New(e.CodeVerifyFailed, "collected Packer artifact is closed")
	}
	if _, err := artifact.file.Seek(0, io.SeekStart); err != nil {
		return 0, e.Wrap(e.CodeVerifyFailed, err, "rewinding collected Packer artifact")
	}
	hash := sha256.New()
	size, err := copyPipelineContext(ctx, io.MultiWriter(destination, hash), artifact.file)
	if err != nil {
		if ctx.Err() != nil {
			return 0, e.Wrap(e.CodeTimeout, ctx.Err(), "reading collected Packer artifact")
		}
		return 0, e.Wrap(e.CodeVerifyFailed, err, "reading collected Packer artifact")
	}
	got := "sha256:" + hex.EncodeToString(hash.Sum(nil))
	if size != artifact.size || got != artifact.digest {
		return 0, e.New(e.CodeVerifyFailed,
			"collected Packer artifact changed: got %s/%d, want %s/%d",
			got, size, artifact.digest, artifact.size)
	}
	if _, err := artifact.file.Seek(0, io.SeekStart); err != nil {
		return 0, e.Wrap(e.CodeVerifyFailed, err, "rewinding collected Packer artifact after read")
	}
	return size, nil
}

// Close releases the collected artifact. It is idempotent.
func (artifact *PackerArtifact) Close() error {
	if artifact == nil {
		return nil
	}
	artifact.mu.Lock()
	defer artifact.mu.Unlock()
	if artifact.file == nil {
		return nil
	}
	err := errors.Join(artifact.file.Close(), artifact.acquisitions.Close())
	artifact.file = nil
	artifact.acquisitions = nil
	if err != nil {
		return e.Wrap(e.CodeInternal, err, "closing collected Packer artifact")
	}
	return nil
}

// runPacker validates then builds one exact source-owned Packer project. The
// unexported materializedSource capability is issued only after the existing
// fetch-receipt, source-copy, patch, and re-hash boundary succeeds. A raw project
// pathname is intentionally not an authority-bearing entry point. The driver does
// not run `packer init`, parse HCL, generate HCL, or consult ambient Packer state.
// Its private state, PATH, and environment are not a kernel sandbox: network
// denial, local-exec and filesystem confinement, credential isolation, and VM
// host admission remain responsibilities of the outer authenticated execution
// adapter. OutputParent is therefore a runner-owned private directory. This
// driver is not connected to public VM-image lowering until those gates exist.
func runPacker(ctx context.Context, toolchain *PackerToolchain, source *materializedSource, request PackerBuildRequest) (result *PackerArtifact, returnErr error) {
	if ctx == nil {
		return nil, e.New(e.CodeUsage, "running Packer requires a non-nil context")
	}
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "running Packer")
	}
	if toolchain == nil {
		return nil, e.New(e.CodeEngineCapability, "an admitted Packer toolchain is required")
	}
	if request.Timeout <= 0 {
		return nil, e.New(e.CodeInvalidCatalog, "Packer build requires a positive executor timeout")
	}
	runCtx, cancel := context.WithTimeout(ctx, request.Timeout)
	defer cancel()
	if err := lockPackerToolchain(runCtx, toolchain); err != nil {
		return nil, err
	}
	defer toolchain.mu.RUnlock()
	if toolchain.closed || toolchain.executable == nil || toolchain.pluginsDir == "" {
		return nil, e.New(e.CodeEngineCapability, "the admitted Packer toolchain is closed or incomplete")
	}
	if source == nil || source.Dir == "" || !canonical.IsDigest(source.InputDigest) {
		return nil, e.New(e.CodeEngineCapability,
			"Packer execution requires an authenticated materialized source capability")
	}
	// Authenticate the complete toolchain capability before request validation
	// touches caller-named directories or source paths.
	if err := verifyPackerToolchain(runCtx, toolchain); err != nil {
		return nil, err
	}
	if err := validatePackerBuildRequest(request); err != nil {
		return nil, err
	}
	_, projectHandle, err := openPackerProjectDirectory(source.Dir, request.ProjectPath)
	if err != nil {
		return nil, e.Wrap(e.CodeInvalidCatalog, err, "resolving Packer project in authenticated source")
	}
	defer func() { _ = projectHandle.Close() }()
	// Execute from the held directory inode rather than resolving the source
	// pathname again in os/exec. The Linux held-executable boundary already
	// requires procfs; naming our own descriptor through the parent PID keeps the
	// cwd stable even if a mutable ancestor is renamed before child start.
	projectDir := fmt.Sprintf("/proc/%d/fd/%d", os.Getpid(), projectHandle.Fd())

	manifest, err := heldfile.OpenContext(runCtx, request.MaterialManifestPath, heldfile.Options{
		Label:          "packer-material-manifest",
		MaxBytes:       maxBuildMaterialManifestBytes,
		ExpectedDigest: request.MaterialManifestSHA256,
	})
	if err != nil {
		if runCtx.Err() != nil {
			return nil, e.Wrap(e.CodeTimeout, runCtx.Err(), "capturing Packer material manifest")
		}
		return nil, e.Wrap(e.CodeUnverifiedRef, err, "capturing Packer material manifest")
	}
	defer func() { _ = manifest.Close() }()

	runRoot, err := os.MkdirTemp(request.OutputParent, ".nkd-packer-")
	if err != nil {
		return nil, e.Wrap(e.CodeBuildFailed, err, "creating private Packer execution root")
	}
	if err := os.Chmod(runRoot, 0o700); err != nil {
		_ = os.RemoveAll(runRoot)
		return nil, e.Wrap(e.CodeBuildFailed, err, "sealing private Packer execution root")
	}
	defer func() {
		if err := removePrivateTree(runRoot); err != nil && returnErr == nil {
			if result != nil {
				_ = result.Close()
				result = nil
			}
			returnErr = e.Wrap(e.CodeInternal, err, "removing private Packer execution root")
		}
	}()

	outputDir := filepath.Join(runRoot, "output")
	manifestPath := filepath.Join(runRoot, "inputs", "material-manifest.json")
	stateDirs := []string{
		outputDir,
		filepath.Dir(manifestPath),
		filepath.Join(runRoot, "bin"),
		filepath.Join(runRoot, "home"),
		filepath.Join(runRoot, "config"),
		filepath.Join(runRoot, "cache"),
		filepath.Join(runRoot, "tmp"),
	}
	for _, directory := range stateDirs {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return nil, e.Wrap(e.CodeBuildFailed, err, "creating private Packer state")
		}
		if err := os.Chmod(directory, 0o700); err != nil {
			return nil, e.Wrap(e.CodeBuildFailed, err, "sealing private Packer state")
		}
	}
	hostPath := filepath.Join(runRoot, "bin")
	if err := materializePackerHostPath(hostPath, toolchain.hostTools); err != nil {
		return nil, err
	}
	if err := materializeBuildMaterialManifest(runCtx, manifest, manifestPath); err != nil {
		return nil, err
	}
	stagedInputs, err := stageBuildMaterialInputs(runCtx, filepath.Dir(manifestPath), request.MaterialManifestSHA256, request.Materials)
	if err != nil {
		return nil, err
	}
	configPath := filepath.Join(runRoot, "config", "packer.json")
	if err := os.WriteFile(configPath, []byte("{}\n"), 0o400); err != nil {
		return nil, e.Wrap(e.CodeBuildFailed, err, "creating empty private Packer configuration")
	}
	bindings := append([]PackerVariable(nil), request.Variables...)
	bindings = append(bindings,
		PackerVariable{Name: request.MaterialManifestVariable, Value: manifestPath},
		PackerVariable{Name: request.OutputDirectoryVariable, Value: outputDir},
	)
	attemptDirectories := []struct {
		name string
		path string
	}{
		{request.BuildLogDirectoryVariable, filepath.Join(runRoot, "logs")},
		{request.HTTPDirectoryVariable, filepath.Join(runRoot, "http")},
	}
	for _, directory := range attemptDirectories {
		if directory.name == "" {
			continue
		}
		if err := os.MkdirAll(directory.path, 0o700); err != nil {
			return nil, e.Wrap(e.CodeBuildFailed, err, "creating private Packer attempt directory")
		}
		bindings = append(bindings, PackerVariable{Name: directory.name, Value: directory.path})
	}
	bindings = append(bindings,
		PackerVariable{Name: request.ArchitectureVariable, Value: request.Architecture},
		PackerVariable{Name: request.AcceleratorVariable, Value: request.Accelerator})
	sort.Slice(bindings, func(i, j int) bool { return bindings[i].Name < bindings[j].Name })
	variableArgs := make([]string, 0, len(bindings)*2)
	for _, binding := range bindings {
		variableArgs = append(variableArgs, "-var", binding.Name+"="+binding.Value)
	}
	baseArgs := []string{"-only=" + request.BuildName}
	baseArgs = append(baseArgs, variableArgs...)
	baseArgs = append(baseArgs, ".")
	environment := packerEnvironment(runRoot, hostPath, toolchain.pluginsDir, configPath)
	stdout := request.Stdout
	if stdout == nil {
		stdout = io.Discard
	}
	stderr := request.Stderr
	if stderr == nil {
		stderr = io.Discard
	}
	outputMu := &sync.Mutex{}
	stdout = &synchronizedPackerWriter{mu: outputMu, destination: stdout}
	stderr = &synchronizedPackerWriter{mu: outputMu, destination: stderr}

	validateArgs := append([]string{"validate"}, baseArgs...)
	validationStdout := newPackerValidationOutput(stdout)
	validationStderr := newPackerValidationOutput(stderr)
	if err := runPackerCommand(runCtx, toolchain, projectDir, environment, validateArgs,
		validationStdout, validationStderr, "validating Packer project"); err != nil {
		return nil, err
	}
	// Packer 1.14 can return exit zero after an exact -only selector matches no
	// build. Its warning is followed by the ordinary success line, so exit status
	// alone is not admission. Fail closed on every warning/error/log diagnostic
	// and accept only Packer's one clean validation result.
	if !validationStdout.exact("The configuration is valid.") || !validationStderr.empty() {
		return nil, e.New(e.CodeBuildFailed,
			"Packer validation emitted diagnostics or did not confirm one clean configuration")
	}
	if err := verifyPackerToolchain(runCtx, toolchain); err != nil {
		return nil, err
	}
	if err := verifyPackerHostPath(hostPath, toolchain.hostTools); err != nil {
		return nil, err
	}
	if err := stagedInputs.verify(runCtx); err != nil {
		return nil, err
	}
	buildArgs := append([]string{"build", "-on-error=cleanup"}, baseArgs...)
	if err := runPackerCommand(runCtx, toolchain, projectDir, environment, buildArgs, stdout, stderr, "building Packer artifact"); err != nil {
		return nil, err
	}
	if err := verifyPackerToolchain(runCtx, toolchain); err != nil {
		return nil, err
	}
	if err := verifyPackerHostPath(hostPath, toolchain.hostTools); err != nil {
		return nil, err
	}
	if err := stagedInputs.verify(runCtx); err != nil {
		return nil, err
	}

	artifactFile, _, err := openRegularBeneath(outputDir, request.ArtifactPath)
	if err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err,
			"collecting declared Packer artifact %q", request.ArtifactPath)
	}
	if err := artifactFile.Chmod(0o400); err != nil {
		_ = artifactFile.Close()
		return nil, e.Wrap(e.CodeVerifyFailed, err, "sealing declared Packer artifact permissions")
	}
	if err := artifactFile.Sync(); err != nil {
		_ = artifactFile.Close()
		return nil, e.Wrap(e.CodeVerifyFailed, err, "syncing declared Packer artifact")
	}
	before, err := artifactFile.Stat()
	if err != nil {
		_ = artifactFile.Close()
		return nil, e.Wrap(e.CodeVerifyFailed, err, "restating declared Packer artifact")
	}
	digest, size, _, err := hashOpenRegularContext(runCtx, artifactFile, before)
	if err != nil {
		_ = artifactFile.Close()
		if runCtx.Err() != nil {
			return nil, e.Wrap(e.CodeTimeout, runCtx.Err(), "hashing declared Packer artifact")
		}
		return nil, e.Wrap(e.CodeVerifyFailed, err, "hashing declared Packer artifact")
	}
	if _, err := artifactFile.Seek(0, io.SeekStart); err != nil {
		_ = artifactFile.Close()
		return nil, e.Wrap(e.CodeVerifyFailed, err, "rewinding declared Packer artifact")
	}
	return &PackerArtifact{file: artifactFile, digest: digest, size: size}, nil
}

func validatePackerHostToolManifest(tools []PackerHostTool) ([]PackerHostTool, error) {
	if len(tools) > maxPackerHostTools {
		return nil, e.New(e.CodeInvalidCatalog,
			"Packer host-tool closure has %d files, exceeding the %d-file bound", len(tools), maxPackerHostTools)
	}
	pins := append([]PackerHostTool(nil), tools...)
	sort.Slice(pins, func(i, j int) bool { return pins[i].Name < pins[j].Name })
	for index, tool := range pins {
		if !validPackerHostToolName(tool.Name) {
			return nil, e.New(e.CodeInvalidCatalog,
				"Packer host-tool name %q is not one exact executable basename", tool.Name)
		}
		if !validCleanAbsolutePath(tool.Path) {
			return nil, e.New(e.CodeEngineCapability,
				"Packer host-tool %q must have an exact clean absolute source path", tool.Name)
		}
		if !canonical.IsDigest(tool.SHA256) {
			return nil, e.New(e.CodeUnpinnedInput,
				"Packer host-tool %q has no exact lowercase sha256 digest", tool.Name)
		}
		if index > 0 && pins[index-1].Name == tool.Name {
			return nil, e.New(e.CodeInvalidCatalog,
				"Packer host-tool closure repeats executable name %q", tool.Name)
		}
	}
	return pins, nil
}

func holdPackerHostTools(ctx context.Context, pins []PackerHostTool) ([]packerHostTool, error) {
	tools := make([]packerHostTool, 0, len(pins))
	cleanup := func(cause error) ([]packerHostTool, error) {
		_ = closePackerHostTools(tools)
		return nil, cause
	}
	var total int64
	for _, pin := range pins {
		tool, err := heldfile.OpenContext(ctx, pin.Path, heldfile.Options{
			Label:          "packer-host-" + pin.Name,
			Executable:     true,
			MaxBytes:       maxPackerHostToolBytes,
			ExpectedDigest: pin.SHA256,
		})
		if err != nil {
			if ctx.Err() != nil {
				return cleanup(e.Wrap(e.CodeTimeout, ctx.Err(), "capturing pinned Packer host-tool %q", pin.Name))
			}
			if errors.Is(err, heldfile.ErrUnsupported) {
				return cleanup(e.Wrap(e.CodeEngineCapability, err, "holding Packer host-tool %q by descriptor", pin.Name))
			}
			return cleanup(e.Wrap(e.CodeUnverifiedRef, err, "capturing pinned Packer host-tool %q", pin.Name))
		}
		if err := requireELFExecutable(tool, "Packer host-tool "+pin.Name); err != nil {
			_ = tool.Close()
			return cleanup(err)
		}
		nextTotal, ok := addPackerBoundedSize(total, tool.Size(), maxPackerHostToolBytes, maxPackerHostToolClosureBytes)
		if !ok {
			_ = tool.Close()
			return cleanup(e.New(e.CodeEngineCapability,
				"Packer host-tool closure exceeds the %d-byte bound", maxPackerHostToolClosureBytes))
		}
		total = nextTotal
		tools = append(tools, packerHostTool{name: pin.Name, file: tool})
	}
	return tools, nil
}

func closePackerHostTools(tools []packerHostTool) error {
	errs := make([]error, 0, len(tools))
	for _, tool := range tools {
		if tool.file != nil {
			errs = append(errs, tool.file.Close())
		}
	}
	return errors.Join(errs...)
}

func requireELFExecutable(reader io.ReaderAt, label string) error {
	var magic [4]byte
	read, err := reader.ReadAt(magic[:], 0)
	if err != nil && !errors.Is(err, io.EOF) {
		return e.Wrap(e.CodeUnverifiedRef, err, "inspecting %s executable format", label)
	}
	if read != len(magic) || !bytes.Equal(magic[:], []byte("\x7fELF")) {
		return e.New(e.CodeEngineCapability, "%s is not an admitted Linux ELF executable", label)
	}
	return nil
}

func addPackerBoundedSize(total, size, maxFile, maxTotal int64) (int64, bool) {
	return addBoundedSize(total, size, maxFile, maxTotal)
}

func validPackerHostToolName(name string) bool {
	return validPackerTransport(name, 255, false) && name != "." && name != ".." &&
		filepath.Base(name) == name && !strings.ContainsAny(name, `/\\`)
}

func materializePackerHostPath(root string, tools []packerHostTool) error {
	for index, tool := range tools {
		if tool.file == nil || tool.file.OSFile() == nil {
			return e.New(e.CodeEngineCapability,
				"Packer host-tool %q is closed or incomplete", tool.name)
		}
		target, err := heldfile.ChildPath(index + 1)
		if err != nil {
			return e.Wrap(e.CodeEngineCapability, err,
				"addressing held Packer host-tool %q", tool.name)
		}
		if err := os.Symlink(target, filepath.Join(root, tool.name)); err != nil {
			return e.Wrap(e.CodeInternal, err,
				"materializing private PATH entry for Packer host-tool %q", tool.name)
		}
	}
	if err := os.Chmod(root, 0o500); err != nil {
		return e.Wrap(e.CodeInternal, err, "sealing private Packer host-tool PATH")
	}
	return verifyPackerHostPath(root, tools)
}

func verifyPackerHostPath(root string, tools []packerHostTool) error {
	info, err := os.Lstat(root)
	if err != nil {
		return e.Wrap(e.CodeUnverifiedRef, err,
			"private Packer host-tool PATH is missing, replaced, or writable")
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o500 {
		return e.New(e.CodeUnverifiedRef,
			"private Packer host-tool PATH is missing, replaced, or writable")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return e.Wrap(e.CodeUnverifiedRef, err, "reading private Packer host-tool PATH")
	}
	if len(entries) != len(tools) {
		return e.New(e.CodeUnverifiedRef,
			"private Packer host-tool PATH has %d entries, want %d", len(entries), len(tools))
	}
	expected := make(map[string]string, len(tools))
	for index, tool := range tools {
		target, pathErr := heldfile.ChildPath(index + 1)
		if pathErr != nil {
			return e.Wrap(e.CodeEngineCapability, pathErr,
				"addressing held Packer host-tool %q", tool.name)
		}
		expected[tool.name] = target
	}
	for _, entry := range entries {
		target, ok := expected[entry.Name()]
		if !ok || entry.Type()&os.ModeSymlink == 0 {
			return e.New(e.CodeUnverifiedRef,
				"private Packer host-tool PATH contains undeclared entry %q", entry.Name())
		}
		actual, readErr := os.Readlink(filepath.Join(root, entry.Name()))
		if readErr != nil {
			return e.Wrap(e.CodeUnverifiedRef, readErr,
				"private Packer host-tool PATH entry %q changed", entry.Name())
		}
		if actual != target {
			return e.New(e.CodeUnverifiedRef,
				"private Packer host-tool PATH entry %q changed", entry.Name())
		}
	}
	return nil
}

func validatePackerPluginManifest(plugins []PackerPluginFile) ([]PackerPluginFile, error) {
	pins := append([]PackerPluginFile(nil), plugins...)
	sort.Slice(pins, func(i, j int) bool { return pins[i].Path < pins[j].Path })
	paths := make(map[string]bool, len(pins))
	for index, plugin := range pins {
		if !validPackerRelativePath(plugin.Path) {
			return nil, e.New(e.CodeInvalidCatalog,
				"Packer plugin file %q is not a clean relative path", plugin.Path)
		}
		if !canonical.IsDigest(plugin.SHA256) {
			return nil, e.New(e.CodeUnpinnedInput,
				"Packer plugin file %q has no exact lowercase sha256 digest", plugin.Path)
		}
		if index > 0 && pins[index-1].Path == plugin.Path {
			return nil, e.New(e.CodeInvalidCatalog,
				"Packer plugin closure repeats path %q", plugin.Path)
		}
		paths[plugin.Path] = true
	}
	for _, plugin := range pins {
		pluginPath := plugin.Path
		for parent := filepath.Dir(pluginPath); parent != "."; parent = filepath.Dir(parent) {
			if paths[parent] {
				return nil, e.New(e.CodeInvalidCatalog,
					"Packer plugin path %q conflicts with parent file %q", pluginPath, parent)
			}
		}
	}
	return pins, nil
}

func snapshotPackerPlugins(ctx context.Context, sourceRoot string, pins []PackerPluginFile) (string, []privateFileSnapshot, error) {
	if len(pins) > 0 {
		if _, err := requireCleanAbsoluteDirectory(sourceRoot, "Packer plugin source root"); err != nil {
			return "", nil, err
		}
	}
	privateRoot, err := os.MkdirTemp("", "nkd-packer-plugins-")
	if err != nil {
		return "", nil, e.Wrap(e.CodeInternal, err, "creating private Packer plugin root")
	}
	cleanup := func(cause error) (string, []privateFileSnapshot, error) {
		_ = removePrivateTree(privateRoot)
		return "", nil, cause
	}
	if err := os.Chmod(privateRoot, 0o700); err != nil {
		return cleanup(e.Wrap(e.CodeInternal, err, "sealing private Packer plugin root"))
	}
	snapshots := make([]privateFileSnapshot, 0, len(pins))
	var total int64
	for _, pin := range pins {
		if err := ctx.Err(); err != nil {
			return cleanup(e.Wrap(e.CodeTimeout, err, "snapshotting Packer plugin closure"))
		}
		source, before, err := openRegularBeneath(sourceRoot, pin.Path)
		if err != nil {
			return cleanup(e.Wrap(e.CodeUnverifiedRef, err, "opening pinned Packer plugin file %q", pin.Path))
		}
		nextTotal, ok := addPackerBoundedSize(total, before.Size(), maxPackerPluginBytes, maxPackerPluginClosureBytes)
		if !ok {
			_ = source.Close()
			return cleanup(e.New(e.CodeEngineCapability,
				"Packer plugin file %q exceeds the authenticated closure size bound", pin.Path))
		}
		total = nextTotal
		destination := filepath.Join(privateRoot, pin.Path)
		if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
			_ = source.Close()
			return cleanup(e.Wrap(e.CodeInternal, err, "creating private Packer plugin directory"))
		}
		writer, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			_ = source.Close()
			return cleanup(e.Wrap(e.CodeInternal, err, "creating private Packer plugin file %q", pin.Path))
		}
		hash := sha256.New()
		written, copyErr := copyPipelineContext(ctx, io.MultiWriter(writer, hash), source)
		after, statErr := source.Stat()
		closeSourceErr := source.Close()
		mode := os.FileMode(0o400)
		if pin.Executable {
			mode = 0o500
		}
		chmodErr := writer.Chmod(mode)
		syncErr := writer.Sync()
		closeWriterErr := writer.Close()
		got := "sha256:" + hex.EncodeToString(hash.Sum(nil))
		if err := errors.Join(copyErr, statErr, closeSourceErr, syncErr, chmodErr, closeWriterErr); err != nil {
			return cleanup(e.Wrap(e.CodeUnverifiedRef, err, "snapshotting Packer plugin file %q", pin.Path))
		}
		if !os.SameFile(before, after) || written != before.Size() || after.Size() != before.Size() ||
			!after.ModTime().Equal(before.ModTime()) || after.Mode() != before.Mode() || got != pin.SHA256 {
			return cleanup(e.New(e.CodeUnverifiedRef,
				"Packer plugin file %q does not match its exact manifest", pin.Path))
		}
		if pin.Executable {
			captured, _, openErr := openRegularBeneath(privateRoot, pin.Path)
			if openErr != nil {
				return cleanup(e.Wrap(e.CodeUnverifiedRef, openErr,
					"opening captured Packer plugin executable %q", pin.Path))
			}
			formatErr := requireELFExecutable(captured, "Packer plugin "+pin.Path)
			closeErr := captured.Close()
			if formatErr != nil {
				return cleanup(formatErr)
			}
			if closeErr != nil {
				return cleanup(e.Wrap(e.CodeInternal, closeErr,
					"closing captured Packer plugin executable %q", pin.Path))
			}
		}
		snapshots = append(snapshots, privateFileSnapshot{Path: pin.Path, SHA256: pin.SHA256, Mode: mode})
	}
	if err := sealPrivateInputDirectories(privateRoot); err != nil {
		return cleanup(e.Wrap(e.CodeInternal, err, "sealing private Packer plugin closure"))
	}
	if err := verifyPrivateFileSnapshot(ctx, privateRoot, snapshots, "Packer plugin"); err != nil {
		return cleanup(err)
	}
	return privateRoot, snapshots, nil
}

func verifyPackerToolchain(ctx context.Context, toolchain *PackerToolchain) error {
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "verifying Packer toolchain")
	}
	if err := toolchain.executable.AssertUnchangedContext(ctx); err != nil {
		if ctx.Err() != nil {
			return e.Wrap(e.CodeTimeout, ctx.Err(), "verifying held Packer executable")
		}
		return e.Wrap(e.CodeUnverifiedRef, err, "verifying held Packer executable")
	}
	for _, tool := range toolchain.hostTools {
		if tool.file == nil {
			return e.New(e.CodeUnverifiedRef,
				"held Packer host-tool %q is missing", tool.name)
		}
		if err := tool.file.AssertUnchangedContext(ctx); err != nil {
			if ctx.Err() != nil {
				return e.Wrap(e.CodeTimeout, ctx.Err(), "verifying held Packer host-tool %q", tool.name)
			}
			return e.Wrap(e.CodeUnverifiedRef, err, "verifying held Packer host-tool %q", tool.name)
		}
	}
	return verifyPrivateFileSnapshot(ctx, toolchain.pluginsDir, toolchain.plugins, "Packer plugin")
}

func validatePackerBuildRequest(request PackerBuildRequest) error {
	if request.Timeout <= 0 {
		return e.New(e.CodeInvalidCatalog, "Packer build requires a positive executor timeout")
	}
	if _, err := requireCleanAbsoluteDirectory(request.OutputParent, "Packer output parent"); err != nil {
		return err
	}
	return validatePackerBuildContract(packerContractFromBuildRequest(request), false)
}

func validatePackerBuildContract(contract packerBuildContract, allowSSHKeyPaths bool) error {
	if !validPackerProjectPath(contract.projectPath) {
		return e.New(e.CodeInvalidCatalog, "Packer project path must be clean and relative to authenticated source")
	}
	if !validPackerTransport(contract.buildName, maxPackerBuildNameBytes, false) ||
		strings.ContainsAny(contract.buildName, ",*?[]\\") {
		return e.New(e.CodeInvalidCatalog, "Packer build name must select one exact build")
	}
	if !validPackerTransport(contract.architecture, packertransport.MaxVariableValueBytes, false) {
		return e.New(e.CodeInvalidCatalog, "Packer architecture value is not transport-safe")
	}
	if (contract.sshPrivateKeyVariable == "") != (contract.sshPublicKeyVariable == "") {
		return e.New(e.CodeInvalidCatalog,
			"Packer requires both SSH key variable names or neither")
	}
	if contract.sshPrivateKeyVariable != "" && !allowSSHKeyPaths {
		return e.New(e.CodeEngineCapability,
			"host Packer cannot realize attempt-local SSH key path variables")
	}
	if contract.accelerator != "kvm" && contract.accelerator != "tcg" {
		return e.New(e.CodeInvalidCatalog, "Packer accelerator must be selected by VM runtime admission")
	}
	if !canonical.IsDigest(contract.materialManifestSHA256) {
		return e.New(e.CodeUnpinnedInput, "Packer material manifest requires an exact lowercase sha256 digest")
	}
	if !validCleanAbsolutePath(contract.materialManifestPath) {
		return e.New(e.CodeInvalidCatalog, "Packer material manifest path must be clean and absolute")
	}
	if len(contract.materials) > maxBuildMaterialFiles {
		return e.New(e.CodeInvalidCatalog,
			"Packer build has %d materials, exceeding the %d-file bound", len(contract.materials), maxBuildMaterialFiles)
	}
	materialPaths := make(map[string]bool, len(contract.materials))
	orderedMaterialPaths := make([]string, 0, len(contract.materials))
	var materialBytes int64
	for _, material := range contract.materials {
		if !validPackerRelativePath(material.Path) || material.Path == "material-manifest.json" {
			return e.New(e.CodeInvalidCatalog,
				"Packer material path %q is not a usable manifest-relative path", material.Path)
		}
		if materialPaths[material.Path] {
			return e.New(e.CodeInvalidCatalog, "Packer material path %q is duplicated", material.Path)
		}
		if !canonical.IsDigest(material.SHA256) {
			return e.New(e.CodeUnpinnedInput,
				"Packer material %q requires an exact lowercase sha256 digest", material.Path)
		}
		if material.Content == nil || material.Content.Digest() != material.SHA256 {
			return e.New(e.CodeUnverifiedRef,
				"Packer material %q lacks the exact authenticated held bytes", material.Path)
		}
		nextMaterialBytes, ok := addPackerBoundedSize(materialBytes, material.Content.Size(),
			MaxBuildMaterialBytes, maxBuildMaterialTotalBytes)
		if !ok {
			return e.New(e.CodeEngineCapability,
				"Packer material %q exceeds the authenticated closure size bound", material.Path)
		}
		materialBytes = nextMaterialBytes
		materialPaths[material.Path] = true
		orderedMaterialPaths = append(orderedMaterialPaths, material.Path)
	}
	sort.Strings(orderedMaterialPaths)
	for _, materialPath := range orderedMaterialPaths {
		for parent := filepath.Dir(materialPath); parent != "."; parent = filepath.Dir(parent) {
			if materialPaths[parent] {
				return e.New(e.CodeInvalidCatalog,
					"Packer material path %q conflicts with parent file %q", materialPath, parent)
			}
		}
	}
	if !validPackerRelativePath(contract.artifactPath) {
		return e.New(e.CodeInvalidCatalog, "Packer artifact path must be clean and relative to its private output directory")
	}
	reserved := []string{
		contract.architectureVariable,
		contract.acceleratorVariable,
		contract.materialManifestVariable,
		contract.outputDirectoryVariable,
	}
	for _, optional := range []string{
		contract.buildLogDirectoryVariable,
		contract.httpDirectoryVariable,
		contract.sshPrivateKeyVariable,
		contract.sshPublicKeyVariable,
	} {
		if optional != "" {
			reserved = append(reserved, optional)
		}
	}
	if err := packertransport.ValidateVariables(reserved, contract.variables); err != nil {
		return e.Wrap(e.CodeInvalidCatalog, err, "validating Packer variable transport")
	}
	return nil
}

func validPackerTransport(value string, maxBytes int, allowEmpty bool) bool {
	return safetext.Valid(value, maxBytes, allowEmpty)
}

func validPackerRelativePath(path string) bool {
	return validCleanRelativePath(path, maxPackerPathBytes)
}

func validPackerProjectPath(path string) bool {
	return path == "." || validPackerRelativePath(path)
}

func validCleanAbsolutePath(path string) bool {
	return validPackerTransport(path, maxPackerPathBytes, false) && filepath.IsAbs(path) && filepath.Clean(path) == path
}

func requireCleanAbsoluteDirectory(path, label string) (string, error) {
	if !validCleanAbsolutePath(path) {
		return "", e.New(e.CodeEngineCapability, "%s must be a clean absolute path", label)
	}
	directory, _, err := securepath.OpenDirectory(path)
	if err != nil {
		return "", e.Wrap(e.CodeEngineCapability, err, "%s is not a real directory", label)
	}
	if err := directory.Close(); err != nil {
		return "", e.Wrap(e.CodeInternal, err, "closing %s", label)
	}
	return path, nil
}

func openPackerProjectDirectory(root, relative string) (string, *os.File, error) {
	if !validCleanAbsolutePath(root) || !validPackerProjectPath(relative) {
		return "", nil, fmt.Errorf("project root and path must be clean")
	}
	current, _, err := securepath.OpenDirectory(root)
	if err != nil {
		return "", nil, err
	}
	if relative == "." {
		return root, current, nil
	}
	for _, part := range strings.Split(relative, string(filepath.Separator)) {
		next, _, openErr := securepath.OpenChild(current, part, true)
		_ = current.Close()
		if openErr != nil {
			return "", nil, openErr
		}
		current = next
	}
	return filepath.Join(root, relative), current, nil
}

func packerEnvironment(runRoot, hostPath, pluginsDir, configPath string) []string {
	return []string{
		"CHECKPOINT_DISABLE=1",
		"HOME=" + filepath.Join(runRoot, "home"),
		"PACKER_CACHE_DIR=" + filepath.Join(runRoot, "cache"),
		"PACKER_CONFIG=" + configPath,
		"PACKER_CONFIG_DIR=" + filepath.Join(runRoot, "config"),
		"PACKER_NO_COLOR=1",
		"PACKER_PLUGIN_PATH=" + pluginsDir,
		"PATH=" + hostPath,
		"TMPDIR=" + filepath.Join(runRoot, "tmp"),
		"XDG_CACHE_HOME=" + filepath.Join(runRoot, "cache"),
		"XDG_CONFIG_HOME=" + filepath.Join(runRoot, "config"),
	}
}

type packerValidationOutput struct {
	destination io.Writer
	buffer      bytes.Buffer
	seen        bool
	overflow    bool
}

type synchronizedPackerWriter struct {
	mu          *sync.Mutex
	destination io.Writer
}

func (writer *synchronizedPackerWriter) Write(raw []byte) (int, error) {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	return writer.destination.Write(raw)
}

func newPackerValidationOutput(destination io.Writer) *packerValidationOutput {
	if destination == nil {
		destination = io.Discard
	}
	return &packerValidationOutput{destination: destination}
}

func (output *packerValidationOutput) Write(raw []byte) (int, error) {
	if len(raw) > 0 {
		output.seen = true
	}
	remaining := maxPackerValidationOutputBytes - output.buffer.Len()
	if remaining > 0 {
		captured := len(raw)
		if captured > remaining {
			captured = remaining
		}
		_, _ = output.buffer.Write(raw[:captured])
	}
	if len(raw) > remaining {
		output.overflow = true
	}
	return output.destination.Write(raw)
}

func (output *packerValidationOutput) exact(want string) bool {
	return output != nil && !output.overflow && strings.TrimSpace(output.buffer.String()) == want
}

func (output *packerValidationOutput) empty() bool {
	return output != nil && !output.seen
}

func runPackerCommand(ctx context.Context, toolchain *PackerToolchain, projectDir string, environment, args []string, stdout, stderr io.Writer, operation string) error {
	hostToolDescriptors := make([]heldfile.Descriptor, 0, len(toolchain.hostTools))
	for _, tool := range toolchain.hostTools {
		hostToolDescriptors = append(hostToolDescriptors, tool.file)
	}
	err := heldfile.RunContext(ctx, toolchain.executable, args, heldfile.RunOptions{
		Stdout: stdout,
		Stderr: stderr,
		Env:    environment,
		Dir:    projectDir,
	}, hostToolDescriptors...)
	if err != nil {
		if ctx.Err() != nil {
			return e.Wrap(e.CodeTimeout, ctx.Err(), "%s", operation)
		}
		if errors.Is(err, heldfile.ErrUnsupported) {
			return e.Wrap(e.CodeEngineCapability, err, "%s", operation)
		}
		return e.Wrap(e.CodeBuildFailed, err, "%s", operation)
	}
	return nil
}

func lockPackerToolchain(ctx context.Context, toolchain *PackerToolchain) error {
	for {
		if toolchain.mu.TryRLock() {
			return nil
		}
		timer := time.NewTimer(5 * time.Millisecond)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return e.Wrap(e.CodeTimeout, ctx.Err(), "waiting for admitted Packer toolchain")
		case <-timer.C:
		}
	}
}
