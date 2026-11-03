// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/execution"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
	"golang.org/x/sys/unix"
)

const (
	packerContainerCleanupTimeout   = 20 * time.Minute
	packerContainerTerminationGrace = 2 * time.Second
)

func runPackerContainer(ctx context.Context, request packerContainerRunRequest) (artifact *PackerArtifact, returnErr error) {
	return runPackerContainerWithDiagnosticTimeout(ctx, request, packerContainerCleanupTimeout)
}

func runPackerContainerWithDiagnosticTimeout(ctx context.Context, request packerContainerRunRequest,
	diagnosticTimeout time.Duration,
) (artifact *PackerArtifact, returnErr error) {
	return runPackerContainerWithTimeouts(ctx, request, diagnosticTimeout, packerContainerCleanupTimeout)
}

func runPackerContainerWithTimeouts(ctx context.Context, request packerContainerRunRequest,
	diagnosticTimeout, observerTimeout time.Duration,
) (artifact *PackerArtifact, returnErr error) {
	if ctx == nil {
		return nil, e.New(e.CodeUsage, "running containerized Packer requires a non-nil context")
	}
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "running containerized Packer")
	}
	if _, ok := ctx.Deadline(); !ok {
		return nil, e.New(e.CodeEngineCapability, "containerized Packer requires the admitted executor deadline")
	}
	if request.runtime == nil || request.runtime.Endpoint() != qualifiedRuntimeEndpoint {
		return nil, e.New(e.CodeEngineCapability,
			"containerized Packer requires the held local container runtime endpoint")
	}
	if runtime.GOOS != "linux" {
		return nil, e.New(e.CodeEngineCapability, "containerized Packer execution requires Linux")
	}
	if err := ValidateVMImagePackerDiskExecution(request.node); err != nil {
		return nil, err
	}
	if err := request.node.Validate(); err != nil {
		return nil, err
	}
	if request.node.BuilderType != ir.BuilderVMImage || request.node.Packer == nil || request.node.ExecutionBinding == nil {
		return nil, e.New(e.CodeEngineCapability, "containerized Packer requires one bound VM-image Plan node")
	}
	if request.node.Packer.AcquisitionArchive != "" && request.node.Packer.AcquisitionArchive == request.node.ArtifactPath {
		return nil, e.New(e.CodeInvalidCatalog, "acquisition carrier cannot be the disk payload")
	}
	if request.source == nil || request.source.Dir == "" {
		return nil, e.New(e.CodeEngineCapability,
			"containerized Packer requires an authenticated materialized source capability")
	}
	kvmEnabled, err := resolvedPackerKVM(request.node)
	if err != nil {
		return nil, err
	}
	if err := validatePackerContainerRequest(request, kvmEnabled); err != nil {
		return nil, err
	}
	if err := validatePackerContainerKVM(request.runtime, request.kvm, kvmEnabled); err != nil {
		return nil, err
	}
	kvmProjection, err := observePackerKVMProjection(ctx, request.kvm,
		kvmEnabled && request.runtime.RuntimeName() == "docker")
	if err != nil {
		return nil, err
	}
	kvmGroup, err := packerKVMGroup(request.kvm, kvmEnabled)
	if err != nil {
		return nil, err
	}
	resources := &packerContainerResources{runtime: request.runtime}
	var diagnosticTarget *packerDiagnosticTarget
	defer func() {
		var diagnosticErr error
		if diagnosticTarget != nil {
			diagnosticErr = diagnosticTarget.close()
		}
		returnErr = errors.Join(returnErr, diagnosticErr)
		returnErr = finalizePackerContainerRun(&artifact, returnErr, resources.cleanup())
	}()
	diagnosticTarget, err = request.diagnostics.start(ctx, request.subject, request.manifest, request.digest)
	if err != nil {
		return nil, err
	}

	// Manifest retention precedes the first external runtime command. Fresh
	// daemon and image observations still happen before the first mutable Docker
	// object exists. They do not mint qualification; the outer verified execution
	// capability remains the authority for the held executable identity.
	if err := observePackerRuntimeFreshness(ctx, request.runtime); err != nil {
		return nil, err
	}
	if err := observeLocalPackerImage(ctx, request.runtime, request.node); err != nil {
		return nil, err
	}

	attempt, err := packerAttemptID()
	if err != nil {
		return nil, e.Wrap(e.CodeInternal, err, "allocating Packer attempt identity")
	}
	workspace, err := createPackerWorkspaceRoot(request.workspace, attempt)
	if err != nil {
		return nil, err
	}
	resources.workspace = workspace
	runRoot := workspace.path()
	if request.runtime.RuntimeName() == "docker" {
		request.identity, err = workspace.prepareDockerIdentity(ctx)
	} else {
		request.identity, err = preparePackerContainerIdentity(ctx, runRoot, request.runtime.RuntimeName())
	}
	if err != nil {
		return nil, err
	}
	inputArchive, workArchive, staged, err := preparePackerContainerTransport(ctx, runRoot, request)
	if err != nil {
		return nil, err
	}
	resources.held = append(resources.held, inputArchive, workArchive)

	nodeLabel := "nvkr.dev/node=" + request.node.ID
	attemptLabel := "nvkr.dev/attempt=" + attempt
	inputVolume := "nvkr-packer-" + attempt + "-input"
	workVolume := "nvkr-packer-" + attempt + "-work"
	for _, volume := range []struct{ name, role string }{{inputVolume, "input"}, {workVolume, "work"}} {
		resources.volumes = append(resources.volumes, volume.name)
		if err := createPackerVolume(ctx, request.runtime, volume.name,
			[]string{nodeLabel, attemptLabel, "nvkr.dev/role=" + volume.role}); err != nil {
			return nil, err
		}
	}

	stageName := "nvkr-packer-" + attempt + "-stage"
	stage := packerContainerCreateRequest{
		name: stageName, image: request.node.Toolchain, platform: request.node.ToolchainPlatform,
		input: inputVolume, work: workVolume, project: packerContainerWorkRoot,
		entrypoint: "sh", packerArgs: []string{
			"-ec", packerInputExtractScript, "nvkr-extract-input",
			strings.TrimPrefix(staged.inputArchiveDigest, "sha256:"), packerContainerWorkRoot + "/stage/input.tar",
			packerContainerInputRoot, strconv.Itoa(os.Getuid()), strconv.Itoa(os.Getgid()), packerContainerWorkRoot,
		},
		labels: []string{nodeLabel, attemptLabel, "nvkr.dev/role=stage"},
		user:   "0:0", allowChown: true, allowDAC: true,
	}
	resources.containers = append(resources.containers, stageName)
	if err := createPackerContainer(ctx, request.runtime, stage); err != nil {
		return nil, err
	}
	if err := copyArchiveToContainer(ctx, request.runtime, workArchive, stageName, packerContainerWorkRoot); err != nil {
		return nil, err
	}
	if err := copyArchiveToContainer(ctx, request.runtime, inputArchive, stageName, packerContainerWorkRoot+"/stage"); err != nil {
		return nil, err
	}
	if err := staged.verify(ctx); err != nil {
		return nil, err
	}
	// The fixed extraction stage owns both fresh volumes until it has verified
	// the archive and assigned their ownership to the invoking user. Its existing
	// DAC override permits traversal of sealed directories during ownership changes.
	if err := startPackerContainer(ctx, request.runtime, stageName, io.Discard, io.Discard); err != nil {
		return nil, e.Wrap(e.CodeUnverifiedRef, err,
			"pinned toolchain could not authenticate and extract the Packer input closure")
	}
	if err := resources.removeContainer(ctx, stageName); err != nil {
		return nil, err
	}
	// Staging has joined its transfers and authenticated the extracted volumes.
	// Release these transport-only copies before the VM needs memory; the factory
	// still owns the original material capabilities through consumption recording.
	// Final cleanup may close these again because heldfile.Close is idempotent.
	if err := errors.Join(inputArchive.Close(), workArchive.Close()); err != nil {
		return nil, e.Wrap(e.CodeInternal, err, "releasing authenticated Packer staging transports")
	}
	if err := preflightPackerVersion(ctx, request.runtime, resources, request.node,
		inputVolume, workVolume, nodeLabel, attemptLabel, attempt, request.bindings.packerVersion,
		request.identity); err != nil {
		return nil, err
	}
	if request.bindings.sshPrivateKeyVariable != "" {
		if err := generateAndVerifyPackerSSHKey(ctx, request.runtime, resources, request.node,
			inputVolume, workVolume, nodeLabel, attemptLabel, attempt, request.identity); err != nil {
			return nil, err
		}
	}

	baseArgs := packerContainerBaseArgs(request.node, request.bindings, kvmEnabled)
	project := path.Join(packerContainerInputRoot, "source", request.node.Packer.ProjectPath)

	validateName := "nvkr-packer-" + attempt + "-validate"
	validate := packerContainerCreateRequest{
		name: validateName, image: request.node.Toolchain, platform: request.node.ToolchainPlatform,
		input: inputVolume, inputReadOnly: true, work: workVolume, project: project,
		packerArgs: append([]string{"validate"}, baseArgs...),
		labels:     []string{nodeLabel, attemptLabel, "nvkr.dev/role=validate"},
		identity:   request.identity,
	}
	resources.containers = append(resources.containers, validateName)
	if err := createPackerContainer(ctx, request.runtime, validate); err != nil {
		return nil, err
	}
	validationStdout := newPackerValidationOutput(request.stdout)
	validationStderr := newPackerValidationOutput(request.stderr)
	if err := startPackerContainer(ctx, request.runtime, validateName, validationStdout, validationStderr); err != nil {
		return nil, err
	}
	if !validationStdout.exact("The configuration is valid.") || !validationStderr.empty() {
		return nil, e.New(e.CodeBuildFailed,
			"Packer validation emitted diagnostics or did not confirm one clean configuration")
	}
	if err := resources.removeContainer(ctx, validateName); err != nil {
		return nil, err
	}

	build := packerBuildContainerRequest(request, inputVolume, workVolume, project,
		nodeLabel, attemptLabel, attempt, kvmGroup, kvmEnabled)
	build.kvmProjection = kvmProjection
	buildName := build.name
	resources.containers = append(resources.containers, buildName)
	if err := createPackerContainer(ctx, request.runtime, build); err != nil {
		return nil, err
	}
	activity := startPackerActivity(ctx, request, resources, workVolume, "nvkr-packer-"+attempt+"-activity",
		[]string{nodeLabel, attemptLabel, "nvkr.dev/role=log-activity"})
	buildErr := startPackerBuildContainer(ctx, request.runtime, buildName, request.stdout, request.stderr, kvmProjection != nil)
	observerErr := activity.stopWithTimeout(ctx, observerTimeout)
	var recoveryErr error
	if activity.unjoined() {
		resources.mu.Lock()
		retained := resources.retainedObserver
		resources.mu.Unlock()
		recoveryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), packerContainerCleanupTimeout)
		recoveryErr = diagnosticTarget.recordObserverRetention(recoveryCtx, retained)
		cancel()
	}
	var diagnosticErr error
	if request.bindings.buildLogDirectoryVariable != "" {
		diagnosticCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), diagnosticTimeout)
		diagnosticErr = collectPackerContainerBuildLogs(diagnosticCtx, request.runtime, buildName,
			diagnosticTarget)
		cancel()
	}
	if diagnosticErr != nil && (errors.Is(diagnosticErr, errPackerDiagnosticsIncomplete) ||
		errors.Is(diagnosticErr, context.DeadlineExceeded)) {
		markerCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), packerContainerCleanupTimeout)
		markerErr := diagnosticTarget.recordIncomplete(markerCtx, diagnosticErr)
		cancel()
		if buildErr == nil && markerErr == nil {
			// Operational log loss is visible in the attempt, not a disk failure.
			diagnosticErr = nil
		} else {
			diagnosticErr = errors.Join(diagnosticErr, markerErr)
		}
	}
	if activity.unjoined() {
		// A live observer never authorizes artifact collection, but its stop
		// failure must not suppress bounded diagnostics from the build.
		return nil, errors.Join(buildErr, observerErr, recoveryErr, diagnosticErr)
	}
	// A joined observer's failed removal remains registered with resources.
	// The final reaper owns its retry and rejects the artifact if removal
	// still fails; a transient metadata cleanup error is not a build failure.
	if buildErr != nil {
		return nil, errors.Join(buildErr, diagnosticErr)
	}
	if diagnosticErr != nil {
		return nil, diagnosticErr
	}
	artifact, err = collectPackerContainerArtifact(ctx, request.runtime, buildName,
		path.Join(packerContainerOutputPath, path.Clean(request.node.ArtifactPath)), runRoot,
		request.node.Requirements.Resources.EphemeralStorageMiB)
	if err != nil {
		return nil, err
	}
	if name := request.node.Packer.AcquisitionArchive; name != "" {
		facts, err := execution.ObserveLocalHost(ctx, request.workspace)
		if err != nil {
			return artifact, err
		}
		budget := *request.node.Requirements.Resources
		artifact.acquisitions, err = collectPackerContainerAcquisitions(ctx, request.runtime, buildName,
			path.Join(packerContainerOutputPath, name), runRoot, budget, facts.Resources, artifact.Size())
		if err != nil {
			return artifact, err
		}
	}
	return artifact, nil
}

func packerBuildContainerRequest(request packerContainerRunRequest, inputVolume, workVolume, project,
	nodeLabel, attemptLabel, attempt, kvmGroup string, kvmEnabled bool,
) packerContainerCreateRequest {
	baseArgs := packerContainerBaseArgs(request.node, request.bindings, kvmEnabled)
	return packerContainerCreateRequest{
		name: "nvkr-packer-" + attempt + "-build", image: request.node.Toolchain,
		platform: request.node.ToolchainPlatform, input: inputVolume, inputReadOnly: true,
		work: workVolume, project: project,
		packerArgs: append([]string{"build", "-on-error=cleanup"}, baseArgs...),
		labels:     []string{nodeLabel, attemptLabel, "nvkr.dev/role=build"},
		kvm:        request.kvm, groupAdd: kvmGroup,
		network: request.node.Network, identity: request.identity,
	}
}

func packerContainerBaseArgs(node ir.Node, bindings packerContainerBindings, kvmEnabled bool) []string {
	variables := packerContainerVariableArgs(node, bindings, kvmEnabled)
	args := []string{"-only=" + node.Packer.BuildName}
	args = append(args, variables...)
	return append(args, ".")
}

func packerKVMGroup(device *os.File, enabled bool) (string, error) {
	if !enabled {
		if device != nil {
			return "", e.New(e.CodeEngineCapability, "Packer received KVM while its exact execution binding disables it")
		}
		return "", nil
	}
	if device == nil {
		return "", e.New(e.CodeEngineCapability, "Packer requires a held KVM device")
	}
	var stat unix.Stat_t
	if err := unix.Fstat(int(device.Fd()), &stat); err != nil {
		return "", e.Wrap(e.CodeEngineCapability, err, "observing admitted KVM device group")
	}
	return strconv.FormatUint(uint64(stat.Gid), 10), nil
}

func resolvedPackerKVM(node ir.Node) (bool, error) {
	found := false
	enabled := false
	for _, capability := range node.ExecutionBinding.Capabilities {
		if capability.Name != execution.CapabilityKVM {
			continue
		}
		if found {
			return false, e.New(e.CodeInvalidCatalog, "VM-image Plan node repeats its KVM resolution")
		}
		found, enabled = true, capability.Enabled
	}
	if !found {
		return false, e.New(e.CodeEngineCapability, "VM-image Plan node has no KVM resolution")
	}
	return enabled, nil
}

func observePackerRuntimeFreshness(ctx context.Context, runtime packerContainerRuntime) error {
	executableDigest, admittedIdentity := runtime.AdmittedIdentity()
	if !canonical.IsDigest(executableDigest) || !canonical.IsDigest(admittedIdentity) {
		return e.New(e.CodeEngineCapability, "container runtime has no admitted identity")
	}
	var versionOut, versionErr bytes.Buffer
	if err := runtime.Run(ctx, nil, &versionOut, &versionErr, "version", "--format", "{{json .}}"); err != nil {
		return runtimeCommandError(ctx, err, versionErr.String(), e.CodeEngineCapability,
			"re-observing held container runtime")
	}
	var infoOut, infoErr bytes.Buffer
	var serverID string
	if named, ok := runtime.(interface{ RuntimeName() string }); ok && named.RuntimeName() == "podman" {
		if err := runtime.Run(ctx, nil, &infoOut, &infoErr, "info", "--format", "{{json .}}"); err != nil {
			return runtimeCommandError(ctx, err, infoErr.String(), e.CodeEngineCapability,
				"re-observing Podman runtime server")
		}
		var err error
		serverID, err = podmanServerIdentity(infoOut.Bytes())
		if err != nil {
			return err
		}
	} else {
		if err := runtime.Run(ctx, nil, &infoOut, &infoErr, "info", "--format", "{{json .ID}}"); err != nil {
			return runtimeCommandError(ctx, err, infoErr.String(), e.CodeEngineCapability,
				"re-observing container runtime server")
		}
		if err := json.Unmarshal(bytes.TrimSpace(infoOut.Bytes()), &serverID); err != nil || strings.TrimSpace(serverID) == "" {
			return e.New(e.CodeEngineCapability, "container runtime returned no canonical server identity")
		}
	}
	observed, err := runtimeIdentityFromOutput(executableDigest, runtime.Endpoint(), serverID, versionOut.Bytes())
	if err != nil {
		return err
	}
	if observed != admittedIdentity {
		return e.New(e.CodeEngineCapability, "container runtime identity changed after admission")
	}
	return nil
}

func validatePackerContainerKVM(runtime packerContainerRuntime, kvm *os.File, enabled bool) error {
	if !enabled {
		if kvm != nil {
			return e.New(e.CodeEngineCapability, "Packer KVM device is present for a disabled Plan resolution")
		}
		return nil
	}
	return validateEnabledPackerContainerKVM(runtime, kvm)
}

func createPackerVolume(ctx context.Context, runtime packerContainerRuntime, name string, labels []string) error {
	args := []string{"volume", "create"}
	for _, label := range labels {
		args = append(args, "--label", label)
	}
	args = append(args, name)
	var stdout, stderr bytes.Buffer
	if err := runtime.Run(ctx, nil, &stdout, &stderr, args...); err != nil {
		return runtimeCommandError(ctx, err, stderr.String(), e.CodeBuildFailed, "creating container volume %q", name)
	}
	if strings.TrimSpace(stdout.String()) != name {
		return e.New(e.CodeBuildFailed, "container runtime did not confirm volume %q", name)
	}
	return nil
}

func createPackerContainer(ctx context.Context, runtime packerContainerRuntime, request packerContainerCreateRequest) error {
	if runtime.RuntimeName() == "docker" && request.kvm != nil && request.kvmProjection == nil {
		return e.New(e.CodeEngineCapability, "Docker KVM handoff requires an observed kernel/device projection")
	}
	if projection := request.kvmProjection; projection != nil &&
		(runtime.RuntimeName() != "docker" || request.kvm == nil || projection.device != request.kvm ||
			projection.bootID == "" || projection.deviceNumber == "") {
		return e.New(e.CodeEngineCapability, "Packer KVM projection does not bind the held device")
	}
	switch runtime.RuntimeName() {
	case "docker", "podman":
	default:
		return e.New(e.CodeEngineCapability,
			"Packer container %q requires the Docker or Podman runtime adapter", request.name)
	}
	switch request.network {
	case "":
		request.network = ir.NetworkNone
	case ir.NetworkNone, ir.NetworkDefault:
	default:
		return e.New(e.CodeEngineCapability,
			"Packer container %q requested unsupported network mode %q", request.name, request.network)
	}
	if runtime.RuntimeName() == "docker" && request.user == "" &&
		(request.identity.passwdPath == "" || request.identity.groupPath == "") {
		return e.New(e.CodeEngineCapability,
			"Packer container %q requires an attempt-owned Docker identity projection", request.name)
	}
	var stdout, stderr bytes.Buffer
	if err := runtime.Run(ctx, nil, &stdout, &stderr, packerContainerCreateArgs(runtime.RuntimeName(), request)...); err != nil {
		return runtimeCommandError(ctx, err, stderr.String(), e.CodeBuildFailed, "creating Packer container %q", request.name)
	}
	if strings.TrimSpace(stdout.String()) == "" {
		return e.New(e.CodeBuildFailed, "container runtime returned no ID for Packer container %q", request.name)
	}
	return nil
}

func startPackerContainer(ctx context.Context, runtime packerContainerRuntime, name string, stdout, stderr io.Writer) error {
	if stdout == nil {
		stdout = io.Discard
	}
	diagnostic := newPackerValidationOutput(stderr)
	if err := runtime.Run(ctx, nil, stdout, diagnostic, "start", "--attach", name); err != nil {
		return runtimeCommandError(ctx, err, diagnostic.buffer.String(), e.CodeBuildFailed,
			"running Packer container %q", name)
	}
	return nil
}

func startPackerBuildContainer(ctx context.Context, runtime packerContainerRuntime, name string,
	stdout, stderr io.Writer, guarded bool,
) error {
	err := startPackerContainer(ctx, runtime, name, stdout, stderr)
	var exit *exec.ExitError
	if guarded && ctx.Err() == nil && errors.As(err, &exit) && exit.ExitCode() == 97 {
		return e.Wrap(e.CodeEngineCapability, err, "Docker KVM projection was rejected before Packer")
	}
	return err
}

func preflightPackerVersion(ctx context.Context, runtime packerContainerRuntime,
	resources *packerContainerResources, node ir.Node, inputVolume, workVolume, nodeLabel, attemptLabel, attempt, version string,
	identity packerContainerIdentity,
) error {
	name := "nvkr-packer-" + attempt + "-version"
	request := packerContainerCreateRequest{
		name: name, image: node.Toolchain, platform: node.ToolchainPlatform,
		input: inputVolume, inputReadOnly: true, work: workVolume, project: packerContainerWorkRoot,
		packerArgs: []string{"version"}, labels: []string{nodeLabel, attemptLabel, "nvkr.dev/role=version"},
		identity: identity,
	}
	resources.containers = append(resources.containers, name)
	if err := createPackerContainer(ctx, runtime, request); err != nil {
		return err
	}
	var stdout, stderr bytes.Buffer
	if err := startPackerContainer(ctx, runtime, name, &stdout, &stderr); err != nil {
		return err
	}
	if strings.TrimSpace(stdout.String()) != "Packer v"+version || strings.TrimSpace(stderr.String()) != "" {
		return e.New(e.CodeEngineCapability,
			"pinned toolchain Packer version does not match declared version %q", version)
	}
	return resources.removeContainer(ctx, name)
}

func generateAndVerifyPackerSSHKey(ctx context.Context, runtime packerContainerRuntime,
	resources *packerContainerResources, node ir.Node, inputVolume, workVolume, nodeLabel, attemptLabel, attempt string,
	identity packerContainerIdentity,
) error {
	generateName := "nvkr-packer-" + attempt + "-ssh-generate"
	generate := packerContainerCreateRequest{
		name: generateName, image: node.Toolchain, platform: node.ToolchainPlatform,
		input: inputVolume, inputReadOnly: true, work: workVolume, project: packerContainerWorkRoot,
		entrypoint: "ssh-keygen", packerArgs: []string{"-q", "-t", "ed25519", "-N", "", "-f", packerContainerWorkRoot + "/ssh/id_ed25519"},
		labels:   []string{nodeLabel, attemptLabel, "nvkr.dev/role=ssh-generate"},
		identity: identity,
	}
	resources.containers = append(resources.containers, generateName)
	if err := createPackerContainer(ctx, runtime, generate); err != nil {
		return err
	}
	if err := startPackerContainer(ctx, runtime, generateName, io.Discard, io.Discard); err != nil {
		return e.Wrap(e.CodeEngineCapability, err, "generating attempt-local SSH key with pinned toolchain")
	}
	if err := resources.removeContainer(ctx, generateName); err != nil {
		return err
	}
	var fingerprints [2]string
	for index, key := range []string{"id_ed25519", "id_ed25519.pub"} {
		name := fmt.Sprintf("nvkr-packer-%s-ssh-verify-%d", attempt, index)
		verify := packerContainerCreateRequest{
			name: name, image: node.Toolchain, platform: node.ToolchainPlatform,
			input: inputVolume, inputReadOnly: true, work: workVolume, project: packerContainerWorkRoot,
			entrypoint: "ssh-keygen", packerArgs: []string{"-l", "-E", "sha256", "-f", packerContainerWorkRoot + "/ssh/" + key},
			labels:   []string{nodeLabel, attemptLabel, "nvkr.dev/role=ssh-verify"},
			identity: identity,
		}
		resources.containers = append(resources.containers, name)
		if err := createPackerContainer(ctx, runtime, verify); err != nil {
			return err
		}
		var output bytes.Buffer
		if err := startPackerContainer(ctx, runtime, name, &output, io.Discard); err != nil {
			return e.Wrap(e.CodeEngineCapability, err, "verifying attempt-local SSH key with pinned toolchain")
		}
		fingerprint, err := packerSSHKeyFingerprint(output.Bytes())
		if err != nil {
			return err
		}
		fingerprints[index] = fingerprint
		if err := resources.removeContainer(ctx, name); err != nil {
			return err
		}
	}
	if fingerprints[0] != fingerprints[1] {
		return e.New(e.CodeEngineCapability, "attempt-local SSH public and private keys do not match")
	}
	return nil
}

func packerAttemptID() (string, error) {
	var token [12]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(token[:]), nil
}

type packerContainerResources struct {
	runtime          packerContainerRuntime
	mu               sync.Mutex
	containers       []string
	privateObservers map[string]struct{}
	retainedObserver *packerActivityRetention
	volumes          []string
	held             []*heldfile.File
	workspace        *packerContainerWorkspace
}

func (resources *packerContainerResources) removeContainer(ctx context.Context, name string) error {
	if err := resources.runtime.Run(ctx, nil, io.Discard, io.Discard, "rm", "-fv", name); err != nil {
		return e.Wrap(e.CodeInternal, err, "removing container %q", name)
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "observing removal of container %q", name)
	}
	resources.mu.Lock()
	defer resources.mu.Unlock()
	for index, current := range resources.containers {
		if current == name {
			resources.containers = append(resources.containers[:index], resources.containers[index+1:]...)
			break
		}
	}
	return nil
}

func (resources *packerContainerResources) cleanup() error {
	return resources.cleanupWithTimeout(packerContainerCleanupTimeout)
}

func (resources *packerContainerResources) cleanupWithTimeout(timeout time.Duration) error {
	resources.mu.Lock()
	retained := resources.retainedObserver
	containers := append([]string(nil), resources.containers...)
	privateObservers := resources.privateObservers
	volumes := append([]string(nil), resources.volumes...)
	held := append([]*heldfile.File(nil), resources.held...)
	workspace := resources.workspace
	resources.containers, resources.volumes = nil, nil
	resources.privateObservers = nil
	resources.held, resources.workspace = nil, nil
	if retained != nil {
		// Only the observer's exact dependency closure must remain. Other
		// containers and the input volume still receive bounded reaping; the
		// completed staging archives are not inputs to either observer client.
		resources.containers = []string{retained.container}
		resources.privateObservers = map[string]struct{}{retained.container: {}}
		resources.volumes = []string{retained.work}
		resources.workspace = workspace
	}
	resources.mu.Unlock()
	var cleanupErrors []error
	if retained != nil {
		cleanupErrors = append(cleanupErrors, retained.err())
	}
	containerCtx, cancelContainers := context.WithTimeout(context.Background(), timeout)
	for index := len(containers) - 1; index >= 0; index-- {
		if retained != nil && containers[index] == retained.container {
			continue
		}
		if err := resources.runtime.Cleanup(containerCtx, packerContainerTerminationGrace, io.Discard, io.Discard,
			"rm", "-fv", containers[index]); err != nil {
			if _, private := privateObservers[containers[index]]; private {
				cleanupErrors = append(cleanupErrors, e.New(e.CodeInternal, "owned Packer log observer cleanup failed"))
			} else {
				cleanupErrors = append(cleanupErrors, err)
			}
		}
	}
	cancelContainers()
	// Volume removal gets its own bounded window so slow or hung container
	// teardown cannot consume the entire cleanup budget and strand volumes.
	volumeCtx, cancelVolumes := context.WithTimeout(context.Background(), timeout)
	for index := len(volumes) - 1; index >= 0; index-- {
		if retained != nil && volumes[index] == retained.work {
			continue
		}
		if err := resources.runtime.Cleanup(volumeCtx, packerContainerTerminationGrace, io.Discard, io.Discard,
			"volume", "rm", "-f", volumes[index]); err != nil {
			cleanupErrors = append(cleanupErrors, err)
		}
	}
	cancelVolumes()
	for _, file := range held {
		if err := file.Close(); err != nil {
			cleanupErrors = append(cleanupErrors, err)
		}
	}
	if retained == nil {
		if err := workspace.cleanup(); err != nil {
			cleanupErrors = append(cleanupErrors, err)
		}
	}
	return errors.Join(cleanupErrors...)
}

func runtimeCommandError(ctx context.Context, err error, stderr string, code e.Code, format string, args ...any) error {
	if ctx != nil && ctx.Err() != nil {
		return e.Wrap(e.CodeTimeout, errors.Join(err, ctx.Err()), format, args...)
	}
	if message := strings.TrimSpace(stderr); message != "" {
		err = fmt.Errorf("%w: %s", err, message)
	}
	return e.Wrap(code, err, format, args...)
}

func joinPackerContainerError(primary, cleanup error) error {
	var coded *e.Error
	if errors.As(primary, &coded) {
		return e.Wrap(coded.Code, errors.Join(primary, cleanup),
			"containerized Packer failed and cleanup also failed")
	}
	return e.Wrap(e.CodeInternal, errors.Join(primary, cleanup),
		"containerized Packer failed and cleanup also failed")
}

func finalizePackerContainerRun(artifact **PackerArtifact, primary, cleanup error) error {
	if cleanup == nil {
		if primary != nil && artifact != nil && *artifact != nil {
			closeErr := (*artifact).Close()
			*artifact = nil
			if closeErr != nil {
				return joinPackerContainerError(primary, closeErr)
			}
		}
		return primary
	}
	var artifactClose error
	if artifact != nil && *artifact != nil {
		// Resource reaping is part of the run contract. Do not return produced
		// bytes from a run that left its containers, volumes or private state in
		// an unknown state, even when the artifact itself was already verified.
		artifactClose = (*artifact).Close()
		*artifact = nil
	}
	cleanup = errors.Join(cleanup, artifactClose)
	if primary == nil {
		return e.Wrap(e.CodeInternal, cleanup, "reaping containerized Packer resources")
	}
	return joinPackerContainerError(primary, cleanup)
}
