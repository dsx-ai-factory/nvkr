// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/buildactivity"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

const (
	packerContainerInputRoot    = "/nvkr/input"
	packerContainerWorkRoot     = "/nvkr/work"
	packerContainerManifestPath = packerContainerInputRoot + "/inputs/material-manifest.json"
	packerContainerOutputPath   = packerContainerWorkRoot + "/output"
	packerContainerBuildLogPath = packerContainerWorkRoot + "/build-logs"
	packerContainerHTTPPath     = packerContainerWorkRoot + "/http-data"
	// In-image ABI of the digest-pinned closure, not a caller namespace alias.
	packerContainerPluginPath = "/nkd/toolchain/packer/plugins"
	packerContainerPATH       = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
	// Open the transported tar once, authenticate that held inode, then extract
	// the same inode. The fixed script is part of NVKR's runtime contract; only
	// the canonical expected digest and fixed container path are arguments.
	packerInputExtractScript = `exec 3<"$2"
printf '%s  %s\n' "$1" /proc/self/fd/3 | sha256sum -c - >/dev/null
install -d -m 0700 "$3/config" "$3/inputs" "$3/source"
tar --no-same-owner --no-overwrite-dir -xf /proc/self/fd/3 -C "$3"
chown -R "$4:$5" "$3" "$6"`
	// Docker resolves devices in its daemon namespace, not the client's procfs.
	// Check the projected kernel/device pair in the same container that will exec
	// Packer. No source-owned command runs before these checks. Status 97 is
	// reserved for projection failures in this guarded build invocation.
	packerKVMProjectionScript = `test "$(cat /proc/sys/kernel/random/boot_id)" = "$1" || {
  echo 'KVM projection belongs to a different kernel boot' >&2; exit 97;
}
test -c "$3" && test "$(stat -L -c '%t:%T' "$3")" = "$2" || {
  echo 'KVM projection differs from the admitted character device' >&2; exit 97;
}
shift 3
exec "$@"`
)

// packerKVMProjection is issued from the already-admitted held device. It binds
// Docker's fixed device path to the caller's kernel boot and device numbers.
// It does not claim transfer of the open descriptor or qualified consumption.
type packerKVMProjection struct {
	device       *os.File
	bootID       string
	deviceNumber string
}

type packerContainerRuntime interface {
	Endpoint() string
	RuntimeName() string
	AdmittedIdentity() (string, string)
	Run(context.Context, io.Reader, io.Writer, io.Writer, ...string) error
	Cleanup(context.Context, time.Duration, io.Writer, io.Writer, ...string) error
}

type heldPackerContainerRuntime struct{ runtime *heldRuntimeExecutable }

// This private owner operation is not a caller-provided cleanup permission.
// The bounded activity stop invokes it only after a runtime client fails to
// join, before releasing the enclosing session's active-call ownership.
func (runtime heldPackerContainerRuntime) retainUnjoinedPackerActivity() {
	if runtime.runtime != nil {
		runtime.runtime.unjoinedPackerActivity.Store(true)
	}
}

func (runtime heldPackerContainerRuntime) Endpoint() string {
	if runtime.runtime == nil {
		return ""
	}
	return runtime.runtime.endpoint
}

func (runtime heldPackerContainerRuntime) RuntimeName() string {
	if runtime.runtime == nil {
		return ""
	}
	return runtime.runtime.name
}

func (runtime heldPackerContainerRuntime) AdmittedIdentity() (string, string) {
	if runtime.runtime == nil || runtime.runtime.capability == nil {
		return "", ""
	}
	return runtime.runtime.capability.Digest(), runtime.runtime.identity
}

func (runtime heldPackerContainerRuntime) Run(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, args ...string) error {
	if runtime.runtime == nil {
		return e.New(e.CodeEngineCapability, "container runtime capability is incomplete")
	}
	return runtime.runtime.runIO(ctx, stdin, stdout, stderr, args...)
}

func (runtime heldPackerContainerRuntime) Cleanup(ctx context.Context, grace time.Duration, stdout, stderr io.Writer, args ...string) error {
	if runtime.runtime == nil {
		return e.New(e.CodeEngineCapability, "container runtime capability is incomplete")
	}
	return runtime.runtime.runCleanup(ctx, grace, stdout, stderr, args...)
}

// packerContainerBindings are copied from the exact validated Plan node by the
// package-owned NodeSession adapter. They are never inferred from HCL.
type packerContainerBindings struct {
	packerVersion             string
	acceleratorVariable       string
	buildLogDirectoryVariable string
	httpDirectoryVariable     string
	sshPrivateKeyVariable     string
	sshPublicKeyVariable      string
}

type packerContainerRunRequest struct {
	runtime           packerContainerRuntime
	source            *materializedSource
	node              ir.Node
	subject           string
	manifest          []byte
	digest            string
	materials         []PackerMaterialInput
	workspace         *os.File
	kvm               *os.File
	diagnostics       *PackerDiagnosticSink
	activity          *buildactivity.Sink
	activityOperation string
	bindings          packerContainerBindings
	stdout            io.Writer
	stderr            io.Writer
	identity          packerContainerIdentity
}

type packerContainerIdentity struct {
	passwdPath string
	groupPath  string
}

type packerContainerCreateRequest struct {
	name          string
	image         string
	platform      string
	input         string
	inputReadOnly bool
	work          string
	workReadOnly  bool
	project       string
	packerArgs    []string
	labels        []string
	kvm           *os.File
	kvmProjection *packerKVMProjection
	entrypoint    string
	user          string
	groupAdd      string
	allowChown    bool
	allowDAC      bool
	network       string
	identity      packerContainerIdentity
}

func packerContainerCreateArgs(runtimeName string, request packerContainerCreateRequest) []string {
	network := request.network
	if network == "" {
		network = ir.NetworkNone
	}
	if runtimeName == "docker" && network == ir.NetworkDefault {
		network = "bridge"
	}
	args := []string{
		"create", "--name", request.name, "--pull=never",
		"--platform", request.platform, "--network=" + network, "--read-only",
		"--cap-drop=ALL", "--security-opt=no-new-privileges", "--no-healthcheck", "--hostname=nvkr-packer",
	}
	if request.allowChown {
		args = append(args, "--cap-add=CHOWN")
	}
	if request.allowDAC {
		args = append(args, "--cap-add=DAC_OVERRIDE")
	}
	user := request.user
	if user == "" {
		uid, gid := os.Getuid(), os.Getgid()
		user = fmt.Sprintf("%d:%d", uid, gid)
		// Consumer processes retain the invoking numeric identity. OpenSSH also
		// requires that identity to resolve through getpwuid(3). Podman can inject
		// exact public identity rows directly. Docker receives attempt-owned public
		// identity projections prepared before any consumer container is created.
		if runtimeName == "podman" {
			args = append(args,
				fmt.Sprintf("--passwd-entry=nvkr:x:%d:%d:NVKR Builder:%s/home:/bin/sh", uid, gid, packerContainerWorkRoot),
				fmt.Sprintf("--group-entry=nvkr:x:%d:", gid),
			)
		} else {
			args = append(args,
				"--mount", "type=bind,src="+request.identity.passwdPath+",dst=/etc/passwd,readonly",
				"--mount", "type=bind,src="+request.identity.groupPath+",dst=/etc/group,readonly",
			)
		}
	}
	args = append(args, "--user="+user)
	if request.groupAdd != "" {
		args = append(args, "--group-add="+request.groupAdd)
	}
	for _, label := range request.labels {
		args = append(args, "--label", label)
	}
	entrypoint := request.entrypoint
	if entrypoint == "" {
		entrypoint = "packer"
	}
	commandArgs := request.packerArgs
	if request.kvmProjection != nil {
		commandArgs = append([]string{"-ec", packerKVMProjectionScript, "nvkr-kvm-projection",
			request.kvmProjection.bootID, request.kvmProjection.deviceNumber, "/dev/kvm", entrypoint}, commandArgs...)
		entrypoint = "sh"
	}
	switch runtimeName {
	case "docker":
		inputMount := "type=volume,src=" + request.input + ",dst=" + packerContainerInputRoot + ",volume-nocopy"
		if request.inputReadOnly {
			inputMount += ",readonly"
		}
		if request.input != "" {
			args = append(args, "--mount", inputMount)
		}
		workMount := "type=volume,src=" + request.work + ",dst=" + packerContainerWorkRoot + ",volume-nocopy"
		if request.workReadOnly {
			workMount += ",readonly"
		}
		args = append(args, "--mount", workMount)
	case "podman":
		// Podman exposes named-volume copy control through --volume rather
		// than Docker's --mount volume-nocopy option. Both source values are
		// internally generated named volumes, never caller-provided paths.
		inputOptions := "nocopy"
		if request.inputReadOnly {
			inputOptions += ",ro"
		}
		if request.input != "" {
			args = append(args, "--volume", request.input+":"+packerContainerInputRoot+":"+inputOptions)
		}
		workOptions := "nocopy"
		if request.workReadOnly {
			workOptions += ",ro"
		}
		args = append(args, "--volume", request.work+":"+packerContainerWorkRoot+":"+workOptions)
	}
	args = append(args,
		"--workdir", request.project,
		"--env", "CHECKPOINT_DISABLE=1",
		"--env", "HOME="+packerContainerWorkRoot+"/home",
		"--env", "PACKER_CACHE_DIR="+packerContainerWorkRoot+"/cache",
		"--env", "PACKER_CONFIG="+packerContainerInputRoot+"/config/packer.json",
		"--env", "PACKER_NO_COLOR=1",
		"--env", "PACKER_PLUGIN_PATH="+packerContainerPluginPath,
		"--env", "PATH="+packerContainerPATH,
		"--env", "TMPDIR="+packerContainerWorkRoot+"/tmp",
		"--entrypoint="+entrypoint,
	)
	if request.kvm != nil {
		devicePath := fmt.Sprintf("/proc/%d/fd/%d", os.Getpid(), request.kvm.Fd())
		if request.kvmProjection != nil {
			devicePath = "/dev/kvm"
		}
		args = append(args, "--device", devicePath+":/dev/kvm:rw")
	}
	args = append(args, request.image)
	return append(args, commandArgs...)
}

func packerContainerVariableArgs(node ir.Node, bindings packerContainerBindings, kvm bool) []string {
	variables := make([]PackerVariable, 0, len(node.Packer.Variables))
	for _, variable := range node.Packer.Variables {
		variables = append(variables, PackerVariable{Name: variable.Name, Value: variable.Value})
	}
	variables = append(variables,
		PackerVariable{Name: node.Packer.ArchitectureVariable, Value: packerArchitecture(node.Platform)},
		PackerVariable{Name: node.Packer.MaterialManifestVariable, Value: packerContainerManifestPath},
		PackerVariable{Name: node.Packer.OutputDirectoryVariable, Value: packerContainerOutputPath},
	)
	if bindings.acceleratorVariable != "" {
		accelerator := "tcg"
		if kvm {
			accelerator = "kvm"
		}
		variables = append(variables, PackerVariable{Name: bindings.acceleratorVariable, Value: accelerator})
	}
	optional := []PackerVariable{
		{Name: bindings.buildLogDirectoryVariable, Value: packerContainerBuildLogPath},
		{Name: bindings.httpDirectoryVariable, Value: packerContainerHTTPPath},
		{Name: bindings.sshPrivateKeyVariable, Value: packerContainerWorkRoot + "/ssh/id_ed25519"},
		{Name: bindings.sshPublicKeyVariable, Value: packerContainerWorkRoot + "/ssh/id_ed25519.pub"},
	}
	for _, variable := range optional {
		if variable.Name != "" {
			variables = append(variables, variable)
		}
	}
	sort.Slice(variables, func(i, j int) bool { return variables[i].Name < variables[j].Name })
	args := make([]string, 0, len(variables)*2)
	for _, variable := range variables {
		args = append(args, "-var", variable.Name+"="+variable.Value)
	}
	return args
}

func packerArchitecture(platform string) string {
	switch platform {
	case "linux/amd64":
		return "amd64"
	case "linux/arm64":
		return "arm64"
	default:
		return ""
	}
}

type packerImageInspection struct {
	ID           string   `json:"Id"`
	RepoDigests  []string `json:"RepoDigests"`
	OS           string   `json:"Os"`
	Architecture string   `json:"Architecture"`
	Config       *struct {
		Env     []string                   `json:"Env"`
		Volumes map[string]json.RawMessage `json:"Volumes"`
	} `json:"Config"`
}

func observeLocalPackerImage(ctx context.Context, runtime packerContainerRuntime, node ir.Node) error {
	_, err := observeLocalContainerImage(ctx, runtime, node.Toolchain, node.ToolchainPlatform, "local Packer toolchain for node "+strconv.Quote(node.ID))
	return err
}

// observeLocalContainerImage checks platform, inherited inputs and the exact
// selected pin once, returning that same validated runtime document to owners.
func observeLocalContainerImage(ctx context.Context, runtime packerContainerRuntime, image, platform, label string) (*packerImageInspection, error) {
	wantImageID := ""
	wantManifestDigest := ""
	if canonical.IsDigest(image) {
		wantImageID = image
	} else {
		var err error
		wantManifestDigest, err = imageManifestDigest(image)
		if err != nil {
			return nil, e.Wrap(e.CodeInvalidCatalog, err, "%s", label)
		}
	}
	var stdout, stderr bytes.Buffer
	args := []string{"image", "inspect"}
	if named, ok := runtime.(interface{ RuntimeName() string }); !ok || named.RuntimeName() != "podman" {
		args = append(args, "--platform", platform)
	}
	args = append(args, "--format", "{{json .}}", image)
	if err := runtime.Run(ctx, nil, &stdout, &stderr, args...); err != nil {
		return nil, runtimeCommandError(ctx, err, stderr.String(), e.CodeEngineCapability, "inspecting %s", label)
	}
	var inspection packerImageInspection
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &inspection); err != nil {
		return nil, e.Wrap(e.CodeEngineCapability, err, "decoding %s inspection", label)
	}
	wantOS, wantArch, ok := strings.Cut(platform, "/")
	if !ok || inspection.OS != wantOS || inspection.Architecture != wantArch {
		return nil, e.New(e.CodePreflightFailed, "%s platform %s/%s differs from selected platform %q", label, inspection.OS, inspection.Architecture, platform)
	}
	if inspection.Config == nil {
		return nil, e.New(e.CodePreflightFailed, "%s inspection omitted image configuration", label)
	}
	if len(inspection.Config.Volumes) != 0 {
		return nil, e.New(e.CodePreflightFailed, "%s declares inherited writable volumes", label)
	}
	pathEntries := 0
	for _, variable := range inspection.Config.Env {
		name, _, ok := strings.Cut(variable, "=")
		if !ok || name != "PATH" {
			return nil, e.New(e.CodePreflightFailed, "%s declares inherited environment outside PATH", label)
		}
		pathEntries++
	}
	if pathEntries > 1 {
		return nil, e.New(e.CodePreflightFailed, "%s declares PATH more than once", label)
	}
	if wantImageID != "" {
		observedImageID := normalizeLocalImageID(inspection.ID)
		if observedImageID != wantImageID {
			return nil, e.New(e.CodePreflightFailed, "runtime selected %s image ID %q, want %q", label, inspection.ID, wantImageID)
		}
		return &inspection, nil
	}
	for _, reference := range inspection.RepoDigests {
		if _, digest, ok := strings.Cut(reference, "@"); ok && digest == wantManifestDigest {
			return &inspection, nil
		}
	}
	return nil, e.New(e.CodePreflightFailed, "runtime did not report selected %s manifest %s", label, wantManifestDigest)
}

func normalizeLocalImageID(id string) string {
	if !canonical.IsDigest(id) && canonical.IsDigest("sha256:"+id) {
		return "sha256:" + id
	}
	return id
}

func validatePackerContainerManifest(manifest []byte, digest string) error {
	if len(manifest) == 0 || len(manifest) > int(maxBuildMaterialManifestBytes) || !canonical.IsDigest(digest) {
		return e.New(e.CodeUnpinnedInput, "Packer material manifest requires bounded bytes and an exact digest")
	}
	got := fmt.Sprintf("sha256:%x", sha256.Sum256(manifest))
	if got != digest {
		return e.New(e.CodeUnverifiedRef, "Packer material manifest bytes do not match %s", digest)
	}
	return nil
}
