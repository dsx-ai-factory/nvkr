// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/dsx-ai-factory/nvkr/internal/buildactivity"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/execution"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/safetext"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

type nodeSessionPackerRuntime struct {
	ready         chan struct{}
	path          string
	storageDriver definition.PodmanStorageDriver
	runtime       *heldRuntimeExecutable
	err           error
}

// VMImagePackerExecutor is a process-local, session-owned admission for one
// exact set of Plan nodes, one held Docker executable, one daemon identity, and
// their already-local digest-pinned toolchain images. It produces unqualified
// local execution results only; it is not provider allocation, pipeline
// attestation, or qualification evidence.
type VMImagePackerExecutor struct {
	mu                 sync.Mutex
	active             sync.WaitGroup
	closed             bool
	session            *nodeSessionState
	runtime            packerContainerRuntime
	nodes              map[string][]byte
	activityOperations map[string]string
}

// VMImagePackerSelection binds one exact pipeline node to its validated
// factory-Plan subject. Subject is routing identity only; the factory Plan and
// VM runtime admission remain the authority for execution.
type VMImagePackerSelection struct {
	Subject string
	Node    ir.Node
	// Descriptive operation label frozen with the exact selected subject. It
	// does not change producer identity or authorize execution.
	ActivityOperation string
}

// VMImagePackerRun carries process-local capabilities and ordinary log/source
// routing for one already-admitted Plan node. Manifest and material bytes must
// be package-held observations derived from that Plan; paths and digest strings
// do not authorize inputs at this boundary.
type VMImagePackerRun struct {
	SourceRoot  string
	Manifest    *heldfile.File
	Materials   []PackerMaterialInput
	Runtime     *execution.LocalVMRuntime
	Diagnostics *PackerDiagnosticSink
	// Activity is optional metadata-only presentation, not execution evidence.
	Activity *buildactivity.Sink
	Stdout   io.Writer
	Stderr   io.Writer
}

// AdmitVMImagePacker performs the runtime and toolchain-image admission phase.
// It performs no source, material, workspace, output, container, or VM work.
// The returned executor remains valid only until NodeSession.Close.
func (handle *NodeSession) AdmitVMImagePacker(
	ctx context.Context,
	selections []VMImagePackerSelection,
	networkAdmission *VMImagePackerNetworkAdmission,
	runtimeName string,
) (*VMImagePackerExecutor, error) {
	if err := requireRunContext(ctx, "VM-image Packer admission"); err != nil {
		return nil, err
	}
	if runtime.GOOS != "linux" {
		return nil, e.New(e.CodeEngineCapability, "containerized Packer execution requires Linux")
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
	activityOperations := make(map[string]string, len(ordered))
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
		if err := requireVMImagePackerNetworkAdmission(networkAdmission, selection); err != nil {
			return nil, err
		}
		raw, err := canonical.Marshal(node)
		if err != nil {
			return nil, err
		}
		bound[selection.Subject] = raw
		if selection.ActivityOperation != "" {
			if (buildactivity.Record{Version: buildactivity.Version, Operation: selection.ActivityOperation}).Validate() != nil {
				return nil, e.New(e.CodeUsage, "unsupported descriptive Packer operation label")
			}
			activityOperations[selection.Subject] = selection.ActivityOperation
		}
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
	storageDriver := definition.PodmanStorageDriver("")
	if networkAdmission != nil {
		storageDriver = networkAdmission.storageDriver
	}
	admitted, err := session.admitPackerRuntimeStorage(callCtx, runtimePath, storageDriver)
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
		activityOperations: activityOperations,
		session:            session, runtime: heldPackerContainerRuntime{runtime: admitted}, nodes: bound,
	}, nil
}

func (session *nodeSessionState) admitPackerRuntime(ctx context.Context, runtimePath string) (*heldRuntimeExecutable, error) {
	return session.admitPackerRuntimeStorage(ctx, runtimePath, "")
}

func (session *nodeSessionState) admitPackerRuntimeStorage(ctx context.Context, runtimePath string, storageDriver definition.PodmanStorageDriver) (*heldRuntimeExecutable, error) {
	if err := storageDriver.Validate(filepath.Base(runtimePath)); err != nil {
		return nil, err
	}
	session.mu.Lock()
	entry := session.packerRuntime
	if entry == nil {
		entry = &nodeSessionPackerRuntime{ready: make(chan struct{}), path: runtimePath, storageDriver: storageDriver}
		session.packerRuntime = entry
		session.mu.Unlock()

		held, err := snapshotRuntimeExecutableContext(ctx, runtimePath) //nolint:staticcheck // Linux authority path.
		if err == nil {                                                 //nolint:staticcheck // Linux authority path; the non-Linux snapshot always fails.
			held.storageDriver = storageDriver
			held.identity, err = observeHeldRuntimeIdentity(ctx, held)
		}
		if err != nil && held != nil && held.capability != nil {
			_ = held.close()
			held = nil
		}
		entry.runtime, entry.err = held, err
		close(entry.ready)
	} else {
		if entry.path != runtimePath || entry.storageDriver != storageDriver {
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
	if input.Manifest == nil || input.Runtime == nil || input.Diagnostics == nil {
		return nil, nil, e.New(e.CodeEngineCapability,
			"VM-image Packer execution requires held manifest, VM runtime and diagnostic capabilities")
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
	var runCtx context.Context
	var cancel context.CancelFunc
	if deadline, ok := ctx.Deadline(); ok {
		runCtx, cancel = context.WithDeadline(session.ctx, deadline)
	} else {
		runCtx, cancel = context.WithCancel(session.ctx)
	}
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
	manifest, err := input.Manifest.ReadAllContext(runCtx, maxBuildMaterialManifestBytes)
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
		node: node, subject: subject, manifest: manifest, digest: input.Manifest.Digest(), materials: input.Materials,
		workspace: workspace, kvm: kvm, diagnostics: input.Diagnostics, bindings: bindings,
		activity: input.Activity, activityOperation: executor.activityOperations[subject],
		stdout: options.Stdout, stderr: options.Stderr,
	})
	if err != nil {
		return nil, nil, err
	}
	materialManifestDigest := ""
	if len(node.Materials) != 0 {
		materialManifestDigest = input.Manifest.Digest()
	}
	result = &ArtifactResult{
		Component: node.Component, OutputType: node.OutputType,
		Qualification: ArtifactUnqualified, Toolchain: node.Toolchain,
		InputDigest: source.InputDigest, MaterialManifestDigest: materialManifestDigest, BuilderType: node.BuilderType,
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
