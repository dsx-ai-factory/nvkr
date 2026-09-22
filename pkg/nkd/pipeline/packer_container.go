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
	"strings"
	"time"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
)

const (
	packerContainerInputRoot    = "/nkd/input"
	packerContainerWorkRoot     = "/nkd/work"
	packerContainerManifestPath = packerContainerInputRoot + "/inputs/material-manifest.json"
	packerContainerOutputPath   = packerContainerWorkRoot + "/output"
	packerContainerBuildLogPath = packerContainerWorkRoot + "/build-logs"
	packerContainerHTTPPath     = packerContainerWorkRoot + "/http-data"
	packerContainerPluginPath   = "/nkd/toolchain/packer/plugins"
	packerContainerPATH         = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
	// Open the transported tar once, authenticate that held inode, then extract
	// the same inode. The fixed script is part of NKD's runtime contract; only
	// the canonical expected digest and fixed container path are arguments.
	packerInputExtractScript = `exec 3<"$2"
printf '%s  %s\n' "$1" /proc/self/fd/3 | sha256sum -c - >/dev/null
tar --no-same-owner --delay-directory-restore -xf /proc/self/fd/3 -C /nkd/input`
	packerVolumeBootstrapScript = `chown -R "$1:$2" /nkd/input /nkd/work`
)

type packerContainerRuntime interface {
	Endpoint() string
	AdmittedIdentity() (string, string)
	Run(context.Context, io.Reader, io.Writer, io.Writer, ...string) error
	Cleanup(context.Context, time.Duration, io.Writer, io.Writer, ...string) error
}

type heldPackerContainerRuntime struct{ runtime *heldRuntimeExecutable }

func (runtime heldPackerContainerRuntime) Endpoint() string {
	if runtime.runtime == nil {
		return ""
	}
	return runtime.runtime.endpoint
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
	runtime   packerContainerRuntime
	source    *materializedSource
	node      ir.Node
	manifest  []byte
	digest    string
	materials []PackerMaterialInput
	workspace *os.File
	kvm       *os.File
	bindings  packerContainerBindings
	stdout    io.Writer
	stderr    io.Writer
}

type packerContainerCreateRequest struct {
	name          string
	image         string
	platform      string
	input         string
	inputReadOnly bool
	work          string
	project       string
	packerArgs    []string
	labels        []string
	kvm           *os.File
	entrypoint    string
	user          string
	groupAdd      string
	allowChown    bool
	network       string
}

func packerContainerCreateArgs(request packerContainerCreateRequest) []string {
	network := request.network
	if network == "" {
		network = ir.NetworkNone
	}
	args := []string{
		"create", "--name", request.name, "--pull=never",
		"--platform", request.platform, "--network=" + network, "--read-only",
		"--cap-drop=ALL", "--security-opt=no-new-privileges", "--no-healthcheck", "--hostname=nkd-packer",
	}
	if request.allowChown {
		args = append(args, "--cap-add=CHOWN")
	}
	user := request.user
	if user == "" {
		user = fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid())
	}
	args = append(args, "--user="+user)
	if request.groupAdd != "" {
		args = append(args, "--group-add="+request.groupAdd)
	}
	for _, label := range request.labels {
		args = append(args, "--label", label)
	}
	inputMount := "type=volume,src=" + request.input + ",dst=" + packerContainerInputRoot
	if request.inputReadOnly {
		inputMount += ",readonly"
	}
	entrypoint := request.entrypoint
	if entrypoint == "" {
		entrypoint = "packer"
	}
	args = append(args,
		"--mount", inputMount,
		"--mount", "type=volume,src="+request.work+",dst="+packerContainerWorkRoot+",volume-nocopy",
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
		args = append(args, "--device", fmt.Sprintf("/proc/%d/fd/%d:/dev/kvm:rw", os.Getpid(), request.kvm.Fd()))
	}
	args = append(args, request.image)
	return append(args, request.packerArgs...)
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
	wantImageID := ""
	wantManifestDigest := ""
	if canonical.IsDigest(node.Toolchain) {
		wantImageID = node.Toolchain
	} else {
		var err error
		wantManifestDigest, err = imageManifestDigest(node.Toolchain)
		if err != nil {
			return e.Wrap(e.CodeInvalidCatalog, err, "node %q toolchain", node.ID)
		}
	}
	var stdout, stderr bytes.Buffer
	if err := runtime.Run(ctx, nil, &stdout, &stderr,
		"image", "inspect", "--platform", node.ToolchainPlatform, "--format", "{{json .}}", node.Toolchain); err != nil {
		return runtimeCommandError(ctx, err, stderr.String(), e.CodeEngineCapability,
			"inspecting already-local Packer toolchain for node %q", node.ID)
	}
	var inspection packerImageInspection
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &inspection); err != nil {
		return e.Wrap(e.CodeEngineCapability, err, "decoding local Packer toolchain inspection for node %q", node.ID)
	}
	wantOS, wantArch, ok := strings.Cut(node.ToolchainPlatform, "/")
	if !ok || inspection.OS != wantOS || inspection.Architecture != wantArch {
		return e.New(e.CodePreflightFailed,
			"local Packer toolchain platform %s/%s differs from selected platform %q",
			inspection.OS, inspection.Architecture, node.ToolchainPlatform)
	}
	if inspection.Config == nil {
		return e.New(e.CodePreflightFailed,
			"local Packer toolchain inspection omitted image configuration")
	}
	if len(inspection.Config.Volumes) != 0 {
		return e.New(e.CodePreflightFailed,
			"local Packer toolchain declares inherited writable volumes")
	}
	pathEntries := 0
	for _, variable := range inspection.Config.Env {
		name, _, ok := strings.Cut(variable, "=")
		if !ok || name != "PATH" {
			return e.New(e.CodePreflightFailed,
				"local Packer toolchain declares inherited environment outside PATH")
		}
		pathEntries++
	}
	if pathEntries > 1 {
		return e.New(e.CodePreflightFailed,
			"local Packer toolchain declares PATH more than once")
	}
	if wantImageID != "" {
		if inspection.ID != wantImageID {
			return e.New(e.CodePreflightFailed,
				"runtime selected local Packer toolchain image ID %q for node %q, want %q",
				inspection.ID, node.ID, wantImageID)
		}
		return nil
	}
	for _, reference := range inspection.RepoDigests {
		if _, digest, ok := strings.Cut(reference, "@"); ok && digest == wantManifestDigest {
			return nil
		}
	}
	return e.New(e.CodePreflightFailed,
		"runtime did not report selected Packer toolchain manifest %s for node %q", wantManifestDigest, node.ID)
}

func validatePackerContainerManifest(manifest []byte, digest string) error {
	if len(manifest) == 0 || len(manifest) > int(maxPackerManifestBytes) || !canonical.IsDigest(digest) {
		return e.New(e.CodeUnpinnedInput, "Packer material manifest requires bounded bytes and an exact digest")
	}
	got := fmt.Sprintf("sha256:%x", sha256.Sum256(manifest))
	if got != digest {
		return e.New(e.CodeUnverifiedRef, "Packer material manifest bytes do not match %s", digest)
	}
	return nil
}
