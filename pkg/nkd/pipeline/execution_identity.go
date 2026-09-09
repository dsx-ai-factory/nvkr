package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/nhensley/nkdctl/pkg/nkd/canonical"
	e "github.com/nhensley/nkdctl/pkg/nkd/errors"
	"github.com/nhensley/nkdctl/pkg/nkd/internal/heldfile"
	"github.com/nhensley/nkdctl/pkg/nkd/ir"
	"github.com/nhensley/nkdctl/pkg/nkd/profile"
)

// LocalContainerSandboxMode is the exact isolation contract exercised by Local's
// container adapter. It is deliberately distinct from ExecutionSandboxMode, which
// is the runner-owned Landlock/seccomp policy used by direct exec-node builds.
const LocalContainerSandboxMode = "oci-runtime/network=none+source=ro+private-output=rw/v1"

const qualifiedRuntimeEndpoint = "unix:///var/run/docker.sock"

// ExecutionObservationRequest contains only independently measurable execution
// inputs. Expected profile values are intentionally absent, so a production observer
// cannot accidentally turn a declaration into an observation.
type ExecutionObservationRequest struct {
	RuntimeExecutable string
	BuildNodes        []ir.Node
	runnerExecutable  *heldfile.File // sealed snapshot of the mapped runner; local adapter only
	runtimeExecutable *heldRuntimeExecutable
}

// ExecutionObserver is the stage-two I/O seam. Implementations inspect the actual
// runner, runtime, toolchain images, and sandbox that will execute selected nodes.
// profile.VerifyExecution remains the sole comparison/receipt authority.
type ExecutionObserver interface {
	ObserveExecution(context.Context, ExecutionObservationRequest) (profile.ExecutionObservation, error)
}

// QualifiedExecution requests ADR-07 stage two. A nil request means an explicitly
// unqualified development run; it can produce diagnostics but never a qualified
// report or validation vector.
type QualifiedExecution struct {
	Profile     profile.BuilderProfile
	HostReceipt profile.HostPreflightReceipt
	Observer    ExecutionObserver
}

type verifiedExecution struct {
	profile        profile.BuilderProfile
	receipt        profile.ExecutionReceipt
	nodes          []ir.Node
	runtime        *heldRuntimeExecutable
	runtimeCleanup func()
}

// heldRuntimeExecutable is an executable capability, not a pathname. On Linux it
// owns a sealed anonymous file descriptor and every qualified runtime subprocess is
// execve'd through an inherited descriptor referring to that exact inode.
type heldRuntimeExecutable struct {
	capability  *heldfile.File
	endpoint    string
	environment []string
}

func (runtime *heldRuntimeExecutable) run(ctx context.Context, stdout, stderr io.Writer, args ...string) error {
	if runtime == nil || runtime.capability == nil || runtime.capability.OSFile() == nil {
		return e.New(e.CodeEngineCapability, "container runtime capability is incomplete")
	}
	return heldfile.RunContext(ctx, runtime.capability, args, heldfile.RunOptions{
		Stdout: stdout, Stderr: stderr,
		Env: append([]string(nil), runtime.environment...),
	})
}

// runCleanup uses a short grace period inside the reaper's independently bounded
// cleanup context. Cleanup still offers SIGTERM first, but an unresponsive runtime
// client cannot extend cancellation by the normal long-running command grace.
func (runtime *heldRuntimeExecutable) runCleanup(ctx context.Context, terminationGrace time.Duration,
	stdout, stderr io.Writer, args ...string,
) error {
	if runtime == nil || runtime.capability == nil || runtime.capability.OSFile() == nil {
		return e.New(e.CodeEngineCapability, "container runtime capability is incomplete")
	}
	return heldfile.RunContext(ctx, runtime.capability, args, heldfile.RunOptions{
		Stdout: stdout, Stderr: stderr,
		Env:              append([]string(nil), runtime.environment...),
		TerminationGrace: terminationGrace,
	})
}

func (execution *verifiedExecution) cleanupRuntime() {
	if execution != nil && execution.runtimeCleanup != nil {
		execution.runtimeCleanup()
		execution.runtimeCleanup = nil
	}
}

// LocalExecutionObserver measures the local container adapter. It hashes the exact
// running executable and resolved runtime binary, asks the runtime for its actual
// implementation/version document, materializes each digest-pinned image, inspects
// its RepoDigests, and probes Nix inside the selected image when required.
//
// It never receives the expected builder profile; comparisons happen later in the
// profile module. Missing runtime evidence therefore fails rather than being filled
// from a declaration.
type LocalExecutionObserver struct{}

// localMappedRunnerObserver is intentionally private: only the stock local adapter
// may ask the orchestrator for a held snapshot of this process's mapped executable.
// Provider observers must measure their own runner rather than consume a pathname
// supplied by the caller.
type localMappedRunnerObserver interface{ requiresMappedRunnerSnapshot() }

func (LocalExecutionObserver) requiresMappedRunnerSnapshot() {}

func (LocalExecutionObserver) ObserveExecution(ctx context.Context, request ExecutionObservationRequest) (profile.ExecutionObservation, error) {
	if ctx == nil {
		return profile.ExecutionObservation{}, e.New(e.CodeUsage, "local execution observation requires a non-nil context")
	}
	if err := ctx.Err(); err != nil {
		return profile.ExecutionObservation{}, e.Wrap(e.CodeTimeout, err, "local execution observation canceled")
	}
	if len(request.BuildNodes) == 0 {
		return profile.ExecutionObservation{}, e.New(e.CodePreflightFailed, "local execution observation requires selected build nodes")
	}
	if request.runnerExecutable == nil {
		return profile.ExecutionObservation{}, e.New(e.CodeEngineCapability,
			"local execution observation has no held snapshot of the mapped runner executable")
	}
	if err := request.runnerExecutable.AssertUnchangedContext(ctx); err != nil {
		return profile.ExecutionObservation{}, mapCancellation(ctx,
			e.Wrap(e.CodeEngineCapability, err, "validating sealed mapped runner snapshot"),
			"validating sealed mapped runner snapshot")
	}
	runnerDigest := request.runnerExecutable.Digest()
	if request.runtimeExecutable == nil {
		return profile.ExecutionObservation{}, e.New(e.CodeEngineCapability,
			"local execution observation has no held container-runtime capability")
	}
	runtimeDigest, err := observeHeldRuntimeIdentity(ctx, request.runtimeExecutable)
	if err != nil {
		return profile.ExecutionObservation{}, err
	}
	observation := profile.ExecutionObservation{
		RunnerDigest: runnerDigest, RunnerOrigin: profile.RunnerPreexisting,
		RuntimeIdentity: runtimeDigest,
	}
	nodes := append([]ir.Node(nil), request.BuildNodes...)
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].ID < nodes[j].ID })
	for _, node := range nodes {
		if err := ctx.Err(); err != nil {
			return profile.ExecutionObservation{}, e.Wrap(e.CodeTimeout, err, "local execution observation canceled")
		}
		toolchainDigest, err := observeHeldContainerImage(ctx, request.runtimeExecutable, node)
		if err != nil {
			return profile.ExecutionObservation{}, err
		}
		identity := profile.NodeExecutionObservation{
			NodeID: node.ID, ToolchainDigest: toolchainDigest,
			SandboxMode: LocalContainerSandboxMode,
		}
		switch node.BuilderType {
		case "", ir.BuilderOCIHermetic:
			identity.EngineClass = ir.EngineOCIHermetic
		case ir.BuilderNix:
			// The current local adapter can inspect the Nix image and executable, but it
			// cannot yet prove that the declared flake.lock closure was realized and is
			// the exact offline closure the command will consume. Copying
			// node.FlakeLockDigest here would turn an expectation into evidence.
			return profile.ExecutionObservation{}, e.New(e.CodeEngineCapability,
				"qualified Nix node %q requires a realized-closure observation adapter; local execution cannot yet measure it", node.ID)
		default:
			return profile.ExecutionObservation{}, e.New(e.CodeBuilderNotProvided,
				"local execution observer cannot measure builder %q for node %q", node.BuilderType, node.ID)
		}
		observation.Nodes = append(observation.Nodes, identity)
	}
	return observation, nil
}

type runtimeIdentityDocument struct {
	SchemaVersion    string          `json:"schemaVersion"`
	ExecutableDigest string          `json:"executableDigest"`
	Endpoint         string          `json:"endpoint"`
	ServerID         string          `json:"serverId"`
	Version          json.RawMessage `json:"version"`
}

func observeHeldRuntimeIdentity(ctx context.Context, runtime *heldRuntimeExecutable) (string, error) {
	if runtime == nil || runtime.capability == nil || runtime.capability.Digest() == "" {
		return "", e.New(e.CodeEngineCapability, "container runtime capability is incomplete")
	}
	raw, err := heldExecutionOutput(ctx, runtime, "version", "--format", "{{json .}}")
	if err != nil {
		return "", e.Wrap(e.CodeEngineCapability, err, "observing held container runtime identity")
	}
	serverRaw, err := heldExecutionOutput(ctx, runtime, "info", "--format", "{{json .ID}}")
	if err != nil {
		return "", e.Wrap(e.CodeEngineCapability, err, "observing container runtime server identity")
	}
	var serverID string
	if err := json.Unmarshal(bytes.TrimSpace(serverRaw), &serverID); err != nil || strings.TrimSpace(serverID) == "" {
		return "", e.New(e.CodeEngineCapability,
			"container runtime returned no canonical server identity")
	}
	return runtimeIdentityFromOutput(runtime.capability.Digest(), runtime.endpoint, serverID, raw)
}

func runtimeIdentityFromOutput(executableDigest, endpoint, serverID string, raw []byte) (string, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return "", e.New(e.CodeEngineCapability, "container runtime returned no canonicalizable version identity")
	}
	if err := canonical.ValidateJSONDocument(raw); err != nil {
		return "", e.Wrap(e.CodeEngineCapability, err,
			"container runtime returned an ambiguous version identity")
	}
	var value any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&value); err != nil {
		return "", e.Wrap(e.CodeEngineCapability, err, "decoding container runtime identity")
	}
	version, err := canonical.Marshal(value)
	if err != nil {
		return "", err
	}
	document := runtimeIdentityDocument{
		SchemaVersion: "nkd.runtime-identity/v1", ExecutableDigest: executableDigest,
		Endpoint: endpoint, ServerID: serverID, Version: version,
	}
	return canonical.Digest(canonical.KindRuntimeIdentity, document)
}

func observeContainerImage(ctx context.Context, runtimePath string, node ir.Node) (string, error) {
	return observeContainerImageWith(ctx, node, func(args ...string) ([]byte, error) {
		return executionOutput(ctx, runtimePath, args...)
	})
}

func observeHeldContainerImage(ctx context.Context, runtime *heldRuntimeExecutable, node ir.Node) (string, error) {
	if runtime == nil || runtime.capability == nil {
		return "", e.New(e.CodeEngineCapability, "container runtime capability is incomplete")
	}
	return observeContainerImageWith(ctx, node, func(args ...string) ([]byte, error) {
		return heldExecutionOutput(ctx, runtime, args...)
	})
}

func observeContainerImageWith(ctx context.Context, node ir.Node, output func(...string) ([]byte, error)) (string, error) {
	if strings.HasPrefix(node.Toolchain, "sha256:") {
		if validateSHA256(node.Toolchain) != nil {
			return "", e.New(e.CodeInvalidCatalog,
				"node %q toolchain image ID is not a canonical digest", node.ID)
		}
		raw, err := output("image", "inspect", "--format", "{{json .Id}}", node.Toolchain)
		if err != nil {
			return "", e.Wrap(e.CodeEngineCapability, err,
				"inspecting local toolchain image ID for node %q", node.ID)
		}
		var observedID string
		if err := json.Unmarshal(bytes.TrimSpace(raw), &observedID); err != nil {
			return "", e.Wrap(e.CodeEngineCapability, err,
				"decoding local toolchain image ID for node %q", node.ID)
		}
		if observedID != node.Toolchain {
			return "", e.New(e.CodePreflightFailed,
				"runtime selected local toolchain image ID %q for node %q, want %q",
				observedID, node.ID, node.Toolchain)
		}
		return observedID, nil
	}
	pullArgs := []string{"pull"}
	if node.Platform != "" {
		pullArgs = append(pullArgs, "--platform", node.Platform)
	}
	pullArgs = append(pullArgs, node.Toolchain)
	if _, err := output(pullArgs...); err != nil {
		return "", e.Wrap(e.CodeEngineCapability, err, "materializing pinned toolchain for node %q", node.ID)
	}
	raw, err := output("image", "inspect", "--format", "{{json .RepoDigests}}", node.Toolchain)
	if err != nil {
		return "", e.Wrap(e.CodeEngineCapability, err, "inspecting toolchain identity for node %q", node.ID)
	}
	var refs []string
	if err := json.Unmarshal(bytes.TrimSpace(raw), &refs); err != nil {
		return "", e.Wrap(e.CodeEngineCapability, err, "decoding toolchain identity for node %q", node.ID)
	}
	want, err := imageManifestDigest(node.Toolchain)
	if err != nil {
		return "", e.Wrap(e.CodeInvalidCatalog, err, "node %q toolchain", node.ID)
	}
	for _, ref := range refs {
		if _, digest, ok := strings.Cut(ref, "@"); ok && digest == want {
			return digest, nil
		}
	}
	return "", e.New(e.CodePreflightFailed,
		"runtime did not report selected toolchain manifest %s for node %q", want, node.ID)
}

func executionOutput(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := execCmd(RunOptions{Ctx: ctx}, name, args...)
	return commandOutput(ctx, cmd)
}

func heldExecutionOutput(ctx context.Context, runtime *heldRuntimeExecutable, args ...string) ([]byte, error) {
	if runtime == nil || runtime.capability == nil {
		return nil, e.New(e.CodeEngineCapability, "container runtime capability is incomplete")
	}
	var stdout, stderr bytes.Buffer
	if err := runtime.run(ctx, &stdout, &stderr, args...); err != nil {
		if ctx.Err() != nil {
			return nil, e.Wrap(e.CodeTimeout, ctx.Err(), "execution observation canceled")
		}
		return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

func commandOutput(ctx context.Context, cmd *exec.Cmd) ([]byte, error) {
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := runOwnedCommand(ctx, cmd); err != nil {
		if ctx.Err() != nil {
			return nil, e.Wrap(e.CodeTimeout, ctx.Err(), "execution observation canceled")
		}
		return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

func imageManifestDigest(ref string) (string, error) {
	_, digest, ok := strings.Cut(ref, "@")
	if !ok || strings.Contains(digest, "@") || validateSHA256(digest) != nil {
		return "", fmt.Errorf("image %q is not pinned by one canonical manifest digest", ref)
	}
	return digest, nil
}

func resolveExecutionRuntime(runtimeBinary string) (runtimePath string, err error) {
	resolved, resolveErr := exec.LookPath(runtimeBinary)
	if resolveErr != nil {
		return "", e.Wrap(e.CodeEngineCapability, resolveErr, "resolving container runtime %s", runtimeBinary)
	}
	return resolved, nil
}

func qualifySelectedExecution(ctx context.Context, request *QualifiedExecution, runtimeBinary string, nodes []ir.Node, executeLocally bool) (*verifiedExecution, string, error) {
	if request == nil {
		return nil, runtimeBinary, nil
	}
	if request.Observer == nil {
		return nil, runtimeBinary, e.New(e.CodePreflightFailed, "qualified execution requires an execution observer")
	}
	profileSnapshot := cloneProfile(request.Profile)
	nodeSnapshot := cloneBuildNodes(nodes)
	runtimePath, err := resolveExecutionRuntime(runtimeBinary)
	if err != nil {
		return nil, runtimeBinary, err
	}
	var runtimeSnapshot *heldRuntimeExecutable
	runtimeCleanup := func() {}
	_, localObserver := request.Observer.(localMappedRunnerObserver)
	if executeLocally || localObserver {
		runtimeSnapshot, err = snapshotRuntimeExecutableContext(ctx, runtimePath) //nolint:staticcheck // Linux implementation is runtime-dependent.
		// On unsupported hosts the build-tagged implementation always refuses;
		// Linux returns a capability or a runtime-dependent error.
		//nolint:staticcheck
		if err != nil {
			return nil, runtimeBinary, err
		}
		runtimeCleanup = func() { _ = runtimeSnapshot.capability.Close() }
	}
	var (
		runnerSnapshot *heldfile.File
		runnerCleanup  = func() {}
	)
	if localObserver {
		runnerSnapshot, runnerCleanup, err = snapshotMappedRunnerExecutableContext(ctx) //nolint:staticcheck // Linux implementation is runtime-dependent.
		// On unsupported hosts the build-tagged implementation always refuses;
		// Linux returns a capability or a runtime-dependent error.
		//nolint:staticcheck
		if err != nil {
			runtimeCleanup()
			return nil, runtimeBinary, err
		}
	}
	cleanup := func() {
		runnerCleanup()
		runtimeCleanup()
	}
	observed, err := request.Observer.ObserveExecution(ctx, ExecutionObservationRequest{
		RuntimeExecutable: runtimePath, BuildNodes: cloneBuildNodes(nodeSnapshot),
		runnerExecutable: runnerSnapshot, runtimeExecutable: runtimeSnapshot,
	})
	if err != nil {
		cleanup()
		return nil, runtimeBinary, err
	}
	receipt, err := profile.VerifyExecution(ctx, request.HostReceipt, profileSnapshot, nodeSnapshot, observed)
	if err != nil {
		cleanup()
		return nil, runtimeBinary, err
	}
	if err := receipt.RequireFor(ctx, profileSnapshot, nodeSnapshot); err != nil {
		cleanup()
		return nil, runtimeBinary, err
	}
	return &verifiedExecution{
		profile: profileSnapshot, receipt: receipt, nodes: nodeSnapshot,
		runtime: runtimeSnapshot, runtimeCleanup: cleanup,
	}, runtimePath, nil
}

func cloneProfile(in profile.BuilderProfile) profile.BuilderProfile {
	out := in
	out.Provides = append([]string(nil), in.Provides...)
	out.Fields = append([]profile.Field(nil), in.Fields...)
	return out
}

func cloneBuildNodes(in []ir.Node) []ir.Node {
	out := make([]ir.Node, len(in))
	for i, node := range in {
		out[i] = node
		out[i].Needs = append([]string(nil), node.Needs...)
		out[i].VendorCmd = append([]string(nil), node.VendorCmd...)
		out[i].Env = append([]ir.EnvVar(nil), node.Env...)
		out[i].Patches = append([]ir.PatchRef(nil), node.Patches...)
		out[i].Command = append([]string(nil), node.Command...)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func observedNodeIdentity(execution *verifiedExecution, nodeID string) (profile.VerifiedNodeIdentity, bool) {
	if execution == nil {
		return profile.VerifiedNodeIdentity{}, false
	}
	for _, identity := range execution.receipt.NodeIdentities() {
		if identity.NodeID == nodeID {
			return identity, true
		}
	}
	return profile.VerifiedNodeIdentity{}, false
}
