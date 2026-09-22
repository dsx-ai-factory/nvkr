package pipeline

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/execution"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/internal/heldfile"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/internal/safetext"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
)

type nodeSessionPackerRuntime struct {
	ready   chan struct{}
	path    string
	runtime *heldRuntimeExecutable
	err     error
}

// VMImagePackerExecutor is a process-local, session-owned admission for one
// exact set of Plan nodes, one held Docker executable, one daemon identity, and
// their already-local digest-pinned toolchain images. It produces unqualified
// local execution results only; it is not provider allocation, pipeline
// attestation, or qualification evidence.
type VMImagePackerExecutor struct {
	mu      sync.Mutex
	active  sync.WaitGroup
	closed  bool
	session *nodeSessionState
	runtime packerContainerRuntime
	nodes   map[string][]byte
}

// VMImagePackerSelection binds one exact pipeline node to its validated
// factory-Plan subject. Subject is routing identity only; the factory Plan and
// VM runtime admission remain the authority for execution.
type VMImagePackerSelection struct {
	Subject string
	Node    ir.Node
}

// VMImagePackerRun carries process-local capabilities and ordinary log/source
// routing for one already-admitted Plan node. Manifest and material bytes must
// be package-held observations derived from that Plan; paths and digest strings
// do not authorize inputs at this boundary.
type VMImagePackerRun struct {
	SourceRoot string
	Manifest   *heldfile.File
	Materials  []PackerMaterialInput
	Runtime    *execution.LocalVMRuntime
	Stdout     io.Writer
	Stderr     io.Writer
}

// AdmitVMImagePacker performs the runtime and toolchain-image admission phase.
// It performs no source, material, workspace, output, container, or VM work.
// The returned executor remains valid only until NodeSession.Close.
func (handle *NodeSession) AdmitVMImagePacker(ctx context.Context, selections []VMImagePackerSelection, runtimeName string) (*VMImagePackerExecutor, error) {
	if err := requireRunContext(ctx, "VM-image Packer admission"); err != nil {
		return nil, err
	}
	if handle == nil || handle.state == nil {
		return nil, e.New(e.CodeEngineCapability, "node execution session is not open")
	}
	if len(selections) == 0 {
		return nil, e.New(e.CodeUsage, "VM-image Packer admission requires selected Plan nodes")
	}
	if runtimeName == "" {
		runtimeName = "docker"
	}
	ordered := append([]VMImagePackerSelection(nil), selections...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Subject < ordered[j].Subject })
	bound := make(map[string][]byte, len(ordered))
	for index, selection := range ordered {
		if !safetext.Valid(selection.Subject, 4096, false) || strings.TrimSpace(selection.Subject) != selection.Subject {
			return nil, e.New(e.CodeInvalidCatalog, "VM-image Packer admission has an unsafe Plan subject")
		}
		if index > 0 && selection.Subject == ordered[index-1].Subject {
			return nil, e.New(e.CodeInvalidCatalog, "VM-image Packer admission repeats subject %q", selection.Subject)
		}
		node := selection.Node
		if err := node.Validate(); err != nil {
			return nil, err
		}
		if node.BuilderType != ir.BuilderVMImage || node.Packer == nil || node.ExecutionBinding == nil {
			return nil, e.New(e.CodeEngineCapability,
				"node %q is not a bound direct-Packer VM-image producer", node.ID)
		}
		if node.Disk == nil || node.Disk.Encryption != "none" {
			return nil, e.New(e.CodeEngineCapability,
				"node %q requests encrypted disk execution without a typed secret channel", node.ID)
		}
		raw, err := canonical.Marshal(node)
		if err != nil {
			return nil, err
		}
		bound[selection.Subject] = raw
	}

	session := handle.state
	session.mu.Lock()
	if session.closed {
		session.mu.Unlock()
		return nil, e.New(e.CodeEngineCapability, "node execution session is closed")
	}
	if err := session.ctx.Err(); err != nil {
		session.mu.Unlock()
		return nil, e.Wrap(e.CodeTimeout, err, "node execution session canceled")
	}
	session.active.Add(1)
	session.mu.Unlock()
	defer session.active.Done()
	callCtx, cancel := context.WithCancel(session.ctx)
	stop := context.AfterFunc(ctx, cancel)
	defer func() { stop(); cancel() }()

	runtimePath, err := resolveExecutionRuntime(runtimeName)
	if err != nil {
		return nil, err
	}
	admitted, err := session.admitPackerRuntime(callCtx, runtimePath)
	if err != nil {
		return nil, err
	}
	containerRuntime := heldPackerContainerRuntime{runtime: admitted}
	if err := observePackerRuntimeFreshness(callCtx, containerRuntime); err != nil {
		return nil, err
	}
	for _, selection := range ordered {
		if err := observeLocalPackerImage(callCtx, containerRuntime, selection.Node); err != nil {
			return nil, err
		}
	}
	return &VMImagePackerExecutor{
		session: session, runtime: heldPackerContainerRuntime{runtime: admitted}, nodes: bound,
	}, nil
}

func (session *nodeSessionState) admitPackerRuntime(ctx context.Context, runtimePath string) (*heldRuntimeExecutable, error) {
	session.mu.Lock()
	entry := session.packerRuntime
	if entry == nil {
		entry = &nodeSessionPackerRuntime{ready: make(chan struct{}), path: runtimePath}
		session.packerRuntime = entry
		session.mu.Unlock()

		held, err := snapshotRuntimeExecutableContext(ctx, runtimePath) //nolint:staticcheck // Linux authority path.
		if err == nil {
			held.identity, err = observeHeldRuntimeIdentity(ctx, held)
		}
		if err != nil && held != nil && held.capability != nil {
			_ = held.capability.Close()
			held = nil
		}
		entry.runtime, entry.err = held, err
		close(entry.ready)
	} else {
		if entry.path != runtimePath {
			session.mu.Unlock()
			return nil, e.New(e.CodeEngineCapability,
				"node execution session is already bound to a different container runtime")
		}
		session.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, e.Wrap(e.CodeTimeout, ctx.Err(), "waiting for container runtime admission")
		case <-entry.ready:
		}
	}
	if entry.err != nil {
		return nil, entry.err
	}
	if entry.runtime == nil || entry.runtime.capability == nil || !canonical.IsDigest(entry.runtime.identity) {
		return nil, e.New(e.CodeEngineCapability, "node execution session has no admitted container runtime")
	}
	return entry.runtime, nil
}

func (executor *VMImagePackerExecutor) requiresExactNode(subject string, node ir.Node) error {
	if executor == nil || executor.session == nil || executor.runtime == nil {
		return e.New(e.CodeEngineCapability, "VM-image Packer executor is absent")
	}
	want, ok := executor.nodes[subject]
	if !ok {
		return e.New(e.CodeEngineCapability, "VM-image Packer executor does not admit subject %q", subject)
	}
	got, err := canonical.Marshal(node)
	if err != nil {
		return err
	}
	if !bytes.Equal(got, want) {
		return e.New(e.CodeEngineCapability, "VM-image Packer subject %q changed after admission", subject)
	}
	return nil
}

// Run executes one exact admitted direct-Packer node. Runtime, daemon, image,
// source and input authority stay owned by the NodeSession and held capabilities;
// the result is deliberately unqualified pipeline evidence.
func (executor *VMImagePackerExecutor) Run(
	ctx context.Context,
	subject string,
	node ir.Node,
	input VMImagePackerRun,
) (artifact *PackerArtifact, result *ArtifactResult, returnErr error) {
	if err := requireRunContext(ctx, "VM-image Packer execution"); err != nil {
		return nil, nil, err
	}
	if err := executor.requiresExactNode(subject, node); err != nil {
		return nil, nil, err
	}
	if input.Manifest == nil || input.Runtime == nil {
		return nil, nil, e.New(e.CodeEngineCapability,
			"VM-image Packer execution requires held manifest and VM runtime capabilities")
	}
	if node.Disk == nil || node.Disk.Encryption != "none" {
		return nil, nil, e.New(e.CodeEngineCapability,
			"VM-image Packer execution does not admit encrypted disks without a typed secret channel")
	}

	executor.mu.Lock()
	if executor.closed {
		executor.mu.Unlock()
		return nil, nil, e.New(e.CodeEngineCapability, "VM-image Packer executor is closed")
	}
	executor.active.Add(1)
	executor.mu.Unlock()
	defer executor.active.Done()

	options := withDefaults(RunOptions{
		Ctx: ctx, SourceRoot: input.SourceRoot, Launch: true,
		Stdout: input.Stdout, Stderr: input.Stderr,
	})
	options.progress = &progressSink{w: options.Stdout}
	root, err := filepath.Abs(options.SourceRoot)
	if err != nil {
		return nil, nil, e.Wrap(e.CodeBuildFailed, err, "resolving prepared Packer source owner")
	}
	session := executor.session
	session.mu.Lock()
	if session.closed {
		session.mu.Unlock()
		return nil, nil, e.New(e.CodeEngineCapability, "node execution session is closed")
	}
	if err := session.ctx.Err(); err != nil {
		session.mu.Unlock()
		return nil, nil, e.Wrap(e.CodeTimeout, err, "node execution session canceled")
	}
	cache := session.sources[root]
	if cache == nil {
		cache = newPreparedSourceCache()
		session.sources[root] = cache
	}
	options.sources = cache
	session.active.Add(1)
	session.mu.Unlock()
	defer session.active.Done()
	runCtx, cancel := context.WithCancel(session.ctx)
	stop := context.AfterFunc(ctx, cancel)
	defer func() { stop(); cancel() }()
	options.Ctx = runCtx

	if err := observePackerRuntimeFreshness(runCtx, executor.runtime); err != nil {
		return nil, nil, err
	}
	if err := observeLocalPackerImage(runCtx, executor.runtime, node); err != nil {
		return nil, nil, err
	}
	workspace, err := input.Runtime.HoldWorkspace()
	if err != nil {
		return nil, nil, err
	}
	kvmEnabled, err := resolvedPackerKVM(node)
	if err != nil {
		return nil, nil, err
	}
	var kvm *os.File
	if kvmEnabled {
		kvm, err = input.Runtime.HoldKVM()
		if err != nil {
			return nil, nil, err
		}
	}
	manifest, err := input.Manifest.ReadAllContext(runCtx, maxPackerManifestBytes)
	if err != nil {
		return nil, nil, e.Wrap(e.CodeUnverifiedRef, err, "reading held Packer material manifest")
	}
	if err := input.Manifest.AssertUnchangedContext(runCtx); err != nil {
		return nil, nil, e.Wrap(e.CodeUnverifiedRef, err, "rechecking held Packer material manifest")
	}
	source, err := materializeSource(node, options)
	if err != nil {
		return nil, nil, err
	}
	sharedSource := node.SourceReadOnly && node.WriteDir != ""
	if !sharedSource && source.cleanup != nil {
		defer source.cleanup()
	}

	bindings := packerContainerBindings{
		packerVersion: node.Packer.PackerVersion, acceleratorVariable: node.Packer.AcceleratorVariable,
		buildLogDirectoryVariable: node.Packer.BuildLogDirectoryVariable,
		httpDirectoryVariable:     node.Packer.HTTPDirectoryVariable,
		sshPrivateKeyVariable:     node.Packer.SSHPrivateKeyVariable,
		sshPublicKeyVariable:      node.Packer.SSHPublicKeyVariable,
	}
	artifact, err = runPackerContainer(runCtx, packerContainerRunRequest{
		runtime: executor.runtime, source: source,
		node: node, manifest: manifest, digest: input.Manifest.Digest(), materials: input.Materials,
		workspace: workspace, kvm: kvm, bindings: bindings,
		stdout: options.Stdout, stderr: options.Stderr,
	})
	if err != nil {
		return nil, nil, err
	}
	result = &ArtifactResult{
		Component: node.Component, OutputType: node.OutputType,
		Qualification: ArtifactUnqualified, Toolchain: node.Toolchain,
		InputDigest: source.InputDigest, BuilderType: node.BuilderType,
		EngineClass: node.BuilderType, Path: node.ArtifactPath,
		SHA256: artifact.Digest(), Size: artifact.Size(),
	}
	return artifact, result, nil
}

// Close invalidates the executor after all active calls finish. The owning
// NodeSession retains and later closes the runtime and source capabilities.
func (executor *VMImagePackerExecutor) Close() error {
	if executor == nil {
		return nil
	}
	executor.mu.Lock()
	executor.closed = true
	executor.mu.Unlock()
	executor.active.Wait()
	return nil
}
