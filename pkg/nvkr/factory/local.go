// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sort"
	"strings"
	"sync"

	"github.com/dsx-ai-factory/nvkr/internal/buildactivity"
	"github.com/dsx-ai-factory/nvkr/internal/ownedcmd"
	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	authored "github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/progress"
)

const (
	LocalRunSchemaVersion = "nvkr.dev/factory-local-run/v1"
	localWorkspaceMarker  = ".nvkr-factory-workspace.json"
	localPlanPath         = ".runner/plan.json"
	nvkrRunnerPackage     = "github.com/dsx-ai-factory/nvkr/cmd/nvkr"
)

// LocalRunOptions configures one local execution of a compiled factory plan.
// Workspace must be a repository-relative child of .nvkr; this lets the runner
// preserve exact source receipts across reruns while safely replacing only outputs
// owned by this module. Publication is opt-in because it is the sole consequential
// remote mutation in the plan.
type LocalRunOptions struct {
	// CompileInputs is canonical descriptive input transport. Factory replays it
	// against the Plan and retains it only after workspace ownership is established.
	CompileInputs string
	Workspace     string
	RunnerSource  string
	Jobs          int
	Runtime       string
	Go            string
	Publish       bool
	DryRun        bool
	Stdout        io.Writer
	Stderr        io.Writer
	Activity      *buildactivity.Sink // Optional descriptive log metadata only.
	Progress      *progress.Monitor   // Optional descriptive latest state only.
	// Diagnostics is an opaque lifecycle-issued attempt-directory capability.
	// A path or serialized description cannot substitute for it.
	Diagnostics   *pipeline.PackerDiagnosticSink
	producers     *pipeline.NodeSession
	vmResolver    vmDriverResolver
	vmDrivers     vmDriverExecutor
	vmAdmissions  map[string]vmAdmission
	materialOwner *LocalRunResult
	nativeGo      map[string]*pipeline.NativeGoToolchain
}

// LocalNodeResult is a deterministic, unqualified execution observation.
type LocalNodeResult struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Status string `json:"status"` // ok | cached | skipped | dry-run
}

// LocalRunResult records what this process completed. It is deliberately not a
// producer, validation-vector, release, or clearance receipt. Callers must Close
// it after consuming any held disk observation.
type LocalRunResult struct {
	SchemaVersion string            `json:"schemaVersion"`
	Qualification EvidenceTier      `json:"qualification"`
	PlanDigest    string            `json:"planDigest"`
	Workspace     string            `json:"workspace"`
	Nodes         []LocalNodeResult `json:"nodes"`

	diskMu                 sync.Mutex
	diskPermit             chan struct{}
	diskClosed             bool
	diskClosing            bool
	diskInspectionCancel   context.CancelFunc
	diskPublicationPending map[string]bool
	diskPublicationWait    sync.WaitGroup
	diskCompletions        []diskProducerCompletion
	materialConsumptions   map[string]producerConsumption
}

// QualificationLane returns the evidence lane recorded by this local execution.
// It is descriptive until the lifecycle binds the independently observed
// outputs into its Run.
func (result *LocalRunResult) QualificationLane() string {
	if result == nil {
		return ""
	}
	return result.Qualification.String()
}

type localRunDeps struct {
	admitVMBuilds func(context.Context, *Plan, string) (map[string]vmAdmission, error)
	admitRunners  func(context.Context, *Plan, LocalRunOptions, []string) error
	prepareRunner func(context.Context, *Plan, LocalRunOptions, []string) (map[string]string, error)
	executeNode   func(context.Context, *Plan, Node, LocalRunOptions, map[string]string) error
	executeDisk   diskProducerCallback
	makeDisk      func(*Plan, *LocalRunOptions) diskProducerCallback
}

type localProducerSessionCloser interface {
	Close()
}

// RunLocal executes the Plan's artifact graph in dependency order. Every producer
// uses the same exact-node execution operation as hosted Runs; utility work runs
// in its pinned container. Source receipts remain reusable across local attempts.
// The caller owns the returned result and must Close it after observation.
func RunLocal(ctx context.Context, plan *Plan, options LocalRunOptions) (*LocalRunResult, error) {
	options = withLocalDefaults(options)
	if hasLocalVMBuild(plan) && !options.DryRun {
		return runLocal(ctx, plan, options, productionLocalRunDeps())
	}
	if err := AdmitLocal(ctx, plan, options); err != nil {
		return nil, err
	}
	return runLocal(ctx, plan, options, productionLocalRunDeps())
}

// AdmitLocal verifies every workspace-independent capability before the caller
// reserves a workspace or creates output. A non-dry-run VM Plan defers only the
// held-workspace, capacity, and KVM checks to RunLocal. It performs no
// filesystem mutation and does not build a runner.
func AdmitLocal(ctx context.Context, plan *Plan, options LocalRunOptions) error {
	options = withLocalDefaults(options)
	if err := admitLocalPlanBase(ctx, plan); err != nil {
		return err
	}
	if err := validateLocalWorkspace(options.Workspace, plan); err != nil {
		return err
	}
	if options.DryRun {
		return nil
	}
	if err := admitLocalDiskPublication(plan); err != nil {
		return err
	}
	toolchains, err := admitLocalNativeGo(ctx, plan)
	if err != nil {
		return err
	}
	defer closeLocalNativeGo(toolchains)
	vmBuild := hasLocalVMBuild(plan)
	if vmBuild {
		for _, selected := range plan.Graph.Nodes {
			if selected.Kind != NodeBuild || selected.IRNode == "" {
				continue
			}
			_, instruction, err := plan.producerInstruction(selected)
			if err != nil {
				return err
			}
			if instruction.BuilderType != ir.BuilderVMImage {
				continue
			}
			_, native := plan.Environment.Runtime.NativeToolchains[instruction.Toolchain]
			if err := validateLocalPackerHost(instruction, native); err != nil {
				return err
			}
		}
	}
	platforms := localRunnerPlatforms(topologicalFactoryNodes(plan.Graph))
	if vmBuild && len(platforms) == 0 {
		return admitLocalRuntime(ctx, options)
	}
	return admitLocalRunners(ctx, plan, options, platforms)
}

func withLocalDefaults(options LocalRunOptions) LocalRunOptions {
	if options.Workspace == "" {
		options.Workspace = filepath.ToSlash(filepath.Join(".nvkr", "factory"))
	}
	if options.RunnerSource == "" {
		options.RunnerSource = "."
	}
	if options.Jobs <= 0 {
		options.Jobs = 1
	}
	if options.Runtime == "" {
		options.Runtime = "docker"
	}
	if options.Go == "" {
		options.Go = "go"
	}
	if options.Stdout == nil {
		options.Stdout = io.Discard
	}
	if options.Stderr == nil {
		options.Stderr = io.Discard
	}
	if options.vmResolver == nil {
		options.vmResolver = localVMPackerResolver{}
	}
	return options
}

func productionLocalRunDeps() localRunDeps {
	return localRunDeps{
		admitVMBuilds: admitLocalVMBuilds,
		admitRunners:  admitLocalRunners,
		prepareRunner: prepareLocalRunners,
		executeNode:   executeLocalFactoryNode,
		makeDisk:      admittedLocalDiskProducer,
	}
}

func runLocal(ctx context.Context, plan *Plan, options LocalRunOptions, deps localRunDeps) (
	result *LocalRunResult,
	returnErr error,
) {
	if err := admitLocalPlanBase(ctx, plan); err != nil {
		return nil, err
	}
	if err := validateLocalWorkspace(options.Workspace, plan); err != nil {
		return nil, err
	}
	if !options.DryRun {
		var err error
		options.nativeGo, err = admitLocalNativeGo(ctx, plan)
		if err != nil {
			return nil, err
		}
		defer closeLocalNativeGo(options.nativeGo)
	}
	if !options.DryRun {
		if err := admitLocalDiskPublication(plan); err != nil {
			return nil, err
		}
	}
	if !options.DryRun && hasLocalVMBuild(plan) {
		if err := validateVMDriverPlan(plan); err != nil {
			return nil, err
		}
	}
	if deps.executeDisk == nil && deps.makeDisk != nil {
		deps.executeDisk = deps.makeDisk(plan, &options)
	}
	if !options.DryRun && requiresDirectPackerDiskAdapter(plan) && deps.executeDisk == nil {
		return nil, e.New(e.CodeEngineCapability,
			"local factory execution has no held-output direct-Packer adapter")
	}
	ordered := topologicalFactoryNodes(plan.Graph)
	planDigest, err := plan.Digest()
	if err != nil {
		return nil, err
	}
	if !options.Progress.Matches("", planDigest) {
		options.Progress = nil
	}
	progressMode := "local"
	if options.DryRun {
		progressMode = "dry-run"
	}
	options.Progress.Start(progressMode)
	result = &LocalRunResult{SchemaVersion: LocalRunSchemaVersion,
		Qualification: TierLocalExecution, PlanDigest: planDigest,
		Workspace: filepath.ToSlash(options.Workspace)}
	owner := result
	if hasLocalVMBuild(plan) {
		options.materialOwner = owner
	}
	completed := false
	defer func() {
		returnErr = finishLocalRun(owner, options.producers, completed, returnErr)
		if returnErr != nil {
			result = nil
		}
	}()
	if options.DryRun {
		for _, node := range ordered {
			status := "dry-run"
			if requiresPublication(node.Kind) && !options.Publish {
				status = "skipped"
			}
			result.Nodes = append(result.Nodes, LocalNodeResult{ID: node.ID, Kind: node.Kind, Status: status})
			if status == "skipped" {
				options.Progress.SetOperation(node.ID, "skipped", "")
			}
			fmt.Fprintf(options.Stdout, "[%s] %s (%s)\n", status, node.ID, node.Kind)
		}
		completed = true
		return result, nil
	}
	if err := validateLocalCompileInputs(ctx, plan, options.CompileInputs); err != nil {
		return nil, err
	}
	if deps.prepareRunner == nil || deps.executeNode == nil {
		return nil, e.New(e.CodeInternal, "factory local executor is incomplete")
	}
	var outputMu sync.Mutex
	options.Stdout = lockedLocalWriter{mu: &outputMu, Writer: options.Stdout}
	options.Stderr = lockedLocalWriter{mu: &outputMu, Writer: options.Stderr}
	options.producers, err = pipeline.NewNodeSession(ctx)
	if err != nil {
		return nil, err
	}
	if hasLocalVMBuild(plan) {
		if options.vmResolver == nil {
			return nil, e.New(e.CodeEngineCapability, "factory local VM driver resolver is unavailable")
		}
		relocated, relocateErr := relocateFactoryPlan(plan, options.Workspace)
		if relocateErr != nil {
			return nil, relocateErr
		}
		options.vmDrivers, err = options.vmResolver.Admit(ctx, relocated, options.producers, vmDriverRunConfig{
			Runtime: options.Runtime, Workspace: options.Workspace, Stdout: options.Stdout, Stderr: options.Stderr,
			Diagnostics: options.Diagnostics, Activity: options.Activity,
		})
		if err != nil {
			return nil, err
		}
		if options.vmDrivers == nil {
			return nil, e.New(e.CodeEngineCapability, "factory local VM driver resolver returned no executor")
		}
		defer func() {
			if closeErr := options.vmDrivers.Close(); closeErr != nil {
				completed = false
				returnErr = joinFactoryCleanupError(returnErr, closeErr,
					"closing local VM driver executor")
			}
		}()
	}
	if err := ensureLocalWorkspace(ctx, options.Workspace, planDigest); err != nil {
		return nil, err
	}
	if err := retainLocalCompileInputs(ctx, options.Workspace, options.CompileInputs); err != nil {
		return nil, err
	}
	platforms := localRunnerPlatforms(ordered)
	if hasLocalVMBuild(plan) {
		if deps.admitVMBuilds == nil || deps.admitRunners == nil {
			return nil, e.New(e.CodeInternal, "factory local VM admission is incomplete")
		}
		relocated, relocateErr := relocateFactoryPlan(plan, options.Workspace)
		if relocateErr != nil {
			return nil, relocateErr
		}
		options.vmAdmissions, err = deps.admitVMBuilds(ctx, relocated, options.Workspace)
		if err != nil {
			return nil, err
		}
		defer func() {
			if closeErr := closeLocalVMAdmissions(options.vmAdmissions); closeErr != nil {
				completed = false
				returnErr = joinFactoryCleanupError(returnErr, closeErr,
					"closing local VM runtime admissions")
			}
		}()
		if err := deps.admitRunners(ctx, plan, options, platforms); err != nil {
			return nil, err
		}
	}
	runners, err := deps.prepareRunner(ctx, plan, options, platforms)
	if err != nil {
		return nil, err
	}

	// One scheduling pass covers producers and utility work. A batch contains only
	// ready operations; cancel and join the whole batch before returning failure.
	status := make(map[string]string, len(ordered))
	options.Progress.SetPhase("execution")
	for len(status) < len(ordered) {
		var ready []Node
		for _, node := range ordered {
			if status[node.ID] != "" {
				continue
			}
			if requiresPublication(node.Kind) && !options.Publish {
				status[node.ID] = "skipped"
				options.Progress.SetOperation(node.ID, "skipped", "")
				result.Nodes = append(result.Nodes, LocalNodeResult{ID: node.ID, Kind: node.Kind, Status: "skipped"})
				continue
			}
			if failedLocalDependency(plan.Graph, node, status) == "" {
				ready = append(ready, node)
				if len(ready) == options.Jobs {
					break
				}
			}
		}
		if len(ready) == 0 {
			if len(status) == len(ordered) {
				break
			}
			return nil, e.New(e.CodeBuildFailed, "local Run has no operation with complete dependencies")
		}
		batchCtx, cancel := context.WithCancel(ctx)
		results := make([]LocalNodeResult, len(ready))
		var firstError error
		var firstFailure sync.Once
		var batch sync.WaitGroup
		for index, node := range ready {
			batch.Go(func() {
				var nodeErr error
				results[index], nodeErr = runLocalNode(batchCtx, plan, node, options, runners, result, deps)
				if nodeErr != nil {
					// Record the initiating error before canceling siblings. A real
					// operation timeout and the cancellations it triggers may carry
					// the same code, so neither sort order nor code identifies cause.
					firstFailure.Do(func() {
						firstError = nodeErr
						cancel()
					})
				}
			})
		}
		batch.Wait()
		cancel()
		if err := ctx.Err(); err != nil {
			return nil, e.Wrap(e.CodeTimeout, err, "local Run canceled")
		}
		if firstError != nil {
			return nil, firstError
		}
		for _, completed := range results {
			status[completed.ID] = completed.Status
			result.Nodes = append(result.Nodes, completed)
		}
	}

	sort.Slice(result.Nodes, func(i, j int) bool { return result.Nodes[i].ID < result.Nodes[j].ID })
	raw, err := canonical.Marshal(result)
	if err != nil {
		return nil, err
	}
	if err := writeFactoryFile(ctx, filepath.Join(options.Workspace, "run.json"), raw, 0o644); err != nil {
		return nil, err
	}
	completed = true
	return result, nil
}

// Compilation can describe disk publication before the grouped live executor
// is available. Reject its package operation before runtime, source or workspace
// work; raw disk execution and static Plan inspection remain supported.
func admitLocalDiskPublication(plan *Plan) error {
	for _, node := range plan.Graph.Nodes {
		if node.Kind == NodePackage && node.PackageKind == PackageDiskImage {
			return e.New(e.CodeEngineCapability,
				"local disk publication requires the live grouped inspection and packaging executor")
		}
	}
	return nil
}

func admittedLocalDiskProducer(plan *Plan, options *LocalRunOptions) diskProducerCallback {
	return func(
		ctx context.Context,
		selected Node,
		instruction ir.Node,
	) (*pipeline.PackerArtifact, *pipeline.ArtifactResult, error) {
		if plan == nil || options == nil || selected.ID == "" ||
			selected.IRNode != instruction.ID || instruction.Packer == nil {
			return nil, nil, e.New(e.CodeEngineCapability,
				"local direct-Packer disk execution is incomplete")
		}
		relocated, err := relocateFactoryPlan(plan, options.Workspace)
		if err != nil {
			return nil, nil, err
		}
		admission := options.vmAdmissions[selected.ID]
		if admission == nil {
			return nil, nil, e.New(e.CodeEngineCapability,
				"local VM node %q has no pre-execution runtime admission", selected.ID)
		}
		return dispatchAdmittedVMProducer(ctx, admission, relocated, selected, options.vmDrivers, options.materialOwner)
	}
}

// finishLocalRun releases held disk payloads before their producer session on
// failure. A successful result retains its payload capabilities for lifecycle
// observation; only the producer session is closed before returning it.
func finishLocalRun(result *LocalRunResult, producers localProducerSessionCloser, completed bool, returnErr error) error {
	if !completed {
		if closeErr := result.Close(); closeErr != nil {
			code := e.CodeInternal
			var coded *e.Error
			if errors.As(returnErr, &coded) {
				code = coded.Code
			}
			returnErr = e.Wrap(code, errors.Join(returnErr, closeErr),
				"local Run failed and closing disk results also failed")
		}
	}
	if producers != nil {
		producers.Close()
	}
	return returnErr
}

// The same lock covers stdout and stderr because callers may share one writer.
type lockedLocalWriter struct {
	mu *sync.Mutex
	io.Writer
}

func (w lockedLocalWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.Writer.Write(p)
}

func runLocalNode(ctx context.Context, plan *Plan, node Node, options LocalRunOptions, runners map[string]string,
	owner *LocalRunResult, deps localRunDeps,
) (result LocalNodeResult, returnErr error) {
	result = LocalNodeResult{ID: node.ID, Kind: node.Kind}
	defer func() {
		state, code := "completed", ""
		if returnErr != nil {
			state, code = "failed", string(e.CodeInternal)
			var coded *e.Error
			if errors.As(returnErr, &coded) {
				code = string(coded.Code)
			}
			if code == string(e.CodeTimeout) {
				state = "canceled"
			}
		} else if result.Status == "cached" {
			state = "cached"
		}
		options.Progress.SetOperation(node.ID, state, code)
	}()
	if err := ctx.Err(); err != nil {
		return result, e.Wrap(e.CodeTimeout, err, "local node canceled")
	}
	directPacker, err := isDirectPackerDiskNode(plan, node)
	if err != nil {
		return result, err
	}
	// A pathname cache cannot recreate the held capability needed by terminal
	// disk observation. The direct-Packer owner must return a fresh authenticated
	// result even when it internally reuses immutable work.
	cached := false
	if !directPacker {
		cached, err = reusableFactoryTool(ctx, plan, node, options.Workspace)
	}
	if err != nil {
		return result, err
	}
	if cached {
		result.Status = "cached"
		fmt.Fprintf(options.Stdout, "[cached] %s\n", node.ID)
		return result, nil
	}
	// Producer operations own safe replacement and reusable source receipts.
	if node.IRNode == "" {
		if err := resetFactoryNodeOutputs(plan, node, options.Workspace); err != nil {
			return result, err
		}
	}
	fmt.Fprintf(options.Stdout, "[start] %s (%s)\n", node.ID, node.Kind)
	options.Progress.SetOperation(node.ID, "running", "")
	if directPacker {
		err = populateDiskProducerCompletion(ctx, plan, node.ID, owner, deps.executeDisk)
	} else if node.Kind == NodeVerify {
		_, artifacts := planIndex(plan)
		if plan.hasCompiledDiskTarget(node.BuildTarget, artifacts) {
			var relocated *Plan
			relocated, err = relocateFactoryPlan(plan, options.Workspace)
			if err == nil {
				_, relocatedArtifacts := planIndex(relocated)
				err = executeHeldDiskObservation(ctx, relocated, node, relocatedArtifacts, owner)
			}
		} else if deps.executeNode == nil {
			err = e.New(e.CodeInternal, "factory local node executor is incomplete")
		} else {
			err = deps.executeNode(ctx, plan, node, options, runners)
		}
	} else if deps.executeNode == nil {
		err = e.New(e.CodeInternal, "factory local node executor is incomplete")
	} else {
		err = deps.executeNode(ctx, plan, node, options, runners)
	}
	if err != nil {
		code := e.CodeBuildFailed
		var coded *e.Error
		if errors.As(err, &coded) {
			code = coded.Code
		} else if ctx.Err() != nil {
			code = e.CodeTimeout
		}
		return result, e.Wrap(code, err, "factory node %s", node.ID)
	}
	result.Status = "ok"
	fmt.Fprintf(options.Stdout, "[done] %s\n", node.ID)
	return result, nil
}

func requiresDirectPackerDiskAdapter(plan *Plan) bool {
	if plan == nil {
		return false
	}
	for _, build := range plan.Builds {
		if build.Graph == nil {
			continue
		}
		for _, instruction := range build.Graph.Nodes {
			if instruction.Packer != nil {
				return true
			}
		}
	}
	return false
}

func isDirectPackerDiskNode(plan *Plan, node Node) (bool, error) {
	if plan == nil || node.IRNode == "" {
		return false, nil
	}
	_, instruction, err := plan.producerInstruction(node)
	if err != nil {
		return false, err
	}
	return instruction.Packer != nil, nil
}

func admitLocalPlanBase(ctx context.Context, plan *Plan) error {
	if ctx == nil {
		return e.New(e.CodeUsage, "local execution requires a context")
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "local execution canceled")
	}
	if plan == nil {
		return e.New(e.CodeUsage, "local execution requires a plan")
	}
	if err := plan.Validate(); err != nil {
		return err
	}
	if err := rejectUnsupportedRuntimeStorage(plan, "local factory execution"); err != nil {
		return err
	}
	if err := rejectUnsupportedLocalPlanRequirements(plan, "local factory execution"); err != nil {
		return err
	}
	if PerOutputPublication(plan) {
		return e.New(e.CodeEngineCapability, "per-output publication requires GitHub execution")
	}
	if len(plan.Environment.GitHubRunners) > 0 {
		return e.New(e.CodeEngineCapability, "local execution does not support Environment.githubRunners")
	}
	if plan.Environment.GitHubDevelopment {
		return e.New(e.CodeEngineCapability, "local execution does not support Environment.githubDevelopment")
	}
	if len(plan.Environment.GitHubDispatchBranches) > 0 {
		return e.New(e.CodeEngineCapability, "local execution does not support Environment.githubDispatchBranches")
	}
	if len(plan.Environment.SourceAccess) != 0 {
		return e.New(e.CodeEngineCapability,
			"local execution does not support Environment.sourceAccess authentication")
	}
	return nil
}

func admitLocalRunners(ctx context.Context, plan *Plan, options LocalRunOptions, platforms []string) error {
	if len(platforms) == 0 {
		return nil
	}
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		return e.New(e.CodeEngineCapability, "factory local execution requires Linux or Darwin process ownership")
	}
	if err := admitLocalRuntime(ctx, options); err != nil {
		return err
	}
	if requiresLocalRunnerCompilation(platforms) {
		if _, err := exec.LookPath(options.Go); err != nil {
			return e.Wrap(e.CodeEngineCapability, err, "finding Go compiler %s", options.Go)
		}
		if _, err := generatorLinkerFlags(plan.GeneratorVersion); err != nil {
			return err
		}
		if _, err := os.Stat(filepath.Join(options.RunnerSource, "go.mod")); err != nil {
			return e.Wrap(e.CodeEngineCapability, err, "runner source %s is not an nvkr checkout", options.RunnerSource)
		}
	}
	return nil
}

func admitLocalRuntime(ctx context.Context, options LocalRunOptions) error {
	if _, err := exec.LookPath(options.Runtime); err != nil {
		return e.Wrap(e.CodeEngineCapability, err, "finding container runtime %s", options.Runtime)
	}
	if _, err := ownedcmd.Output(ctx, options.Runtime, "version", "--format", "{{.Server.Version}}"); err != nil {
		return classifyContext(ctx, e.Wrap(e.CodeEngineCapability, err, "contacting local container runtime"))
	}
	return nil
}

func requiresLocalRunnerCompilation(platforms []string) bool {
	for _, platform := range platforms {
		if runtime.GOOS != "linux" || platform != "linux/"+runtime.GOARCH || !currentProcessIsNVKRRunner() {
			return true
		}
	}
	return false
}

func currentProcessIsNVKRRunner() bool {
	info, ok := debug.ReadBuildInfo()
	return isNVKRRunnerBuild(info, ok)
}

func isNVKRRunnerBuild(info *debug.BuildInfo, ok bool) bool {
	if !ok || info == nil || info.Path != nvkrRunnerPackage {
		return false
	}
	for _, setting := range info.Settings {
		if setting.Key == "CGO_ENABLED" {
			return setting.Value == "0"
		}
	}
	return false
}

func prepareLocalRunners(ctx context.Context, plan *Plan, options LocalRunOptions, platforms []string) (map[string]string, error) {
	if len(platforms) == 0 {
		return map[string]string{}, nil
	}
	linkerFlags, err := generatorLinkerFlags(plan.GeneratorVersion)
	if err != nil {
		return nil, err
	}
	runners := make(map[string]string, len(platforms))
	for _, platform := range platforms {
		arch := strings.TrimPrefix(platform, "linux/")
		output := filepath.Join(options.Workspace, ".runner", "nvkr-exec-"+arch)
		if err := os.MkdirAll(filepath.Dir(output), 0o700); err != nil {
			return nil, e.Wrap(e.CodeInternal, err, "creating local runner directory")
		}
		stage, err := os.CreateTemp(filepath.Dir(output), ".nvkr-exec-*")
		if err != nil {
			return nil, e.Wrap(e.CodeInternal, err, "staging local factory runner")
		}
		stagePath := stage.Name()
		if err := stage.Close(); err != nil {
			_ = os.Remove(stagePath)
			return nil, err
		}
		defer func() { _ = os.Remove(stagePath) }()
		absoluteOutput, err := filepath.Abs(stagePath)
		if err != nil {
			return nil, err
		}
		if runtime.GOOS == "linux" && platform == "linux/"+runtime.GOARCH && currentProcessIsNVKRRunner() {
			if err := copyCurrentRunner(ctx, absoluteOutput); err != nil {
				return nil, err
			}
		} else {
			cmd := exec.CommandContext(ctx, options.Go, "build", "-trimpath", "-ldflags", linkerFlags,
				"-o", absoluteOutput, "./cmd/nvkr")
			cmd.Dir = options.RunnerSource
			cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+arch)
			cmd.Stdout, cmd.Stderr = options.Stdout, options.Stderr
			if err := ownedcmd.Run(ctx, cmd); err != nil {
				return nil, classifyContext(ctx, e.Wrap(e.CodeEngineCapability, err, "building linux/%s factory runner", arch))
			}
		}
		if err := os.Chmod(stagePath, 0o555); err != nil {
			return nil, e.Wrap(e.CodeInternal, err, "protecting local factory runner")
		}
		built, err := os.Open(stagePath)
		if err != nil {
			return nil, err
		}
		if err := built.Sync(); err != nil {
			_ = built.Close()
			return nil, err
		}
		if err := built.Close(); err != nil {
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			return nil, e.Wrap(e.CodeTimeout, err, "publishing local factory runner canceled")
		}
		if err := os.Rename(stagePath, output); err != nil {
			return nil, e.Wrap(e.CodeInternal, err, "publishing local factory runner")
		}
		directory, err := os.Open(filepath.Dir(output))
		if err != nil {
			return nil, err
		}
		if err := directory.Sync(); err != nil {
			_ = directory.Close()
			return nil, err
		}
		if err := directory.Close(); err != nil {
			return nil, err
		}
		runners[platform] = output
	}
	raw, err := canonical.Marshal(plan)
	if err != nil {
		return nil, err
	}
	if err := writeFactoryFile(ctx, filepath.Join(options.Workspace, filepath.FromSlash(localPlanPath)), raw, 0o444); err != nil {
		return nil, err
	}
	return runners, nil
}

func copyCurrentRunner(ctx context.Context, destination string) error {
	source, err := os.Open("/proc/self/exe")
	if err != nil {
		return e.Wrap(e.CodeEngineCapability, err, "opening mapped factory runner")
	}
	defer source.Close()

	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_TRUNC, 0)
	if err != nil {
		return e.Wrap(e.CodeInternal, err, "opening staged factory runner")
	}
	_, copyErr := copyContext(ctx, output, source)
	closeErr := output.Close()
	if copyErr != nil {
		return e.Wrap(e.CodeInternal, copyErr, "copying mapped factory runner")
	}
	if closeErr != nil {
		return e.Wrap(e.CodeInternal, closeErr, "closing staged factory runner")
	}
	return nil
}

func executeLocalFactoryNode(ctx context.Context, plan *Plan, node Node, options LocalRunOptions, runners map[string]string) error {
	if node.IRNode != "" {
		run := (pipeline.Local{}).ExecNode
		if options.producers != nil {
			run = options.producers.ExecNode
		}
		return executeLocalProducer(ctx, plan, node, options, run)
	}

	// Terminal observation reopens and hashes the local producer's host bytes;
	// it executes no producer code. Use the same filesystem view as collection,
	// rather than a VM bind-mount view after private BuildKit output promotion.
	if node.Network == ir.NetworkNone && node.Kind != NodeVerify {
		if _, ok := runners[node.Platform]; !ok {
			return e.New(e.CodeEngineCapability, "factory node %s has no linux runner for %s", node.ID, node.Platform)
		}
		workspace, err := filepath.Abs(options.Workspace)
		if err != nil {
			return err
		}
		arch := strings.TrimPrefix(node.Platform, "linux/")
		containerRunner := "/workspace/.nvkr/factory/.runner/nvkr-exec-" + arch
		planDigest := ""
		if hasLocalVMBuild(plan) {
			planDigest, err = plan.Digest()
			if err != nil {
				return err
			}
		}
		return executeUtilityNode(ctx, node, utilityRunnerLocation{
			runtime: options.Runtime, mountSource: workspace, mountTarget: "/workspace/.nvkr/factory",
			helper: containerRunner, plan: "/workspace/.nvkr/factory/" + localPlanPath, planDigest: planDigest,
			user: localContainerUser(), home: true,
		}, options.Stdout, options.Stderr)
	}
	relocated, err := relocateFactoryPlan(plan, options.Workspace)
	if err != nil {
		return err
	}
	return ExecuteNode(ctx, relocated, node.ID, options.Stdout, options.Stderr)
}

func executeLocalProducer(ctx context.Context, plan *Plan, node Node, options LocalRunOptions,
	run func(ir.Node, pipeline.RunOptions) (*pipeline.ArtifactResult, error),
) error {
	relocated, err := relocateFactoryPlan(plan, options.Workspace)
	if err != nil {
		return err
	}
	var owner *LocalRunResult
	for _, output := range plan.LifecycleOutputs {
		if output.Name == node.BuildTarget && output.Kind == authored.OutputDiskImage {
			owner = options.materialOwner
		}
	}
	invoke := func(instruction ir.Node, settings pipeline.RunOptions) (*pipeline.ArtifactResult, error) {
		if instruction.BuilderType == ir.BuilderNativeGo {
			settings.NativeGo = options.nativeGo[instruction.Toolchain]
		}
		return run(instruction, settings)
	}
	return executeProducer(ctx, relocated, node, options.Runtime, options.Jobs, options.Stdout, options.Stderr, invoke, owner)
}

func hasLocalVMBuild(plan *Plan) bool {
	if plan == nil {
		return false
	}
	for _, node := range plan.Graph.Nodes {
		if node.Kind != NodeBuild || node.IRNode == "" {
			continue
		}
		_, instruction, err := plan.producerInstruction(node)
		if err == nil && instruction.BuilderType == ir.BuilderVMImage {
			return true
		}
	}
	return false
}

func admitLocalVMBuilds(ctx context.Context, plan *Plan, workspacePath string) (
	admissions map[string]vmAdmission,
	returnErr error,
) {
	workspace, _, err := securepath.OpenDirectory(workspacePath)
	if err != nil {
		return nil, e.Wrap(e.CodeEngineCapability, err, "holding local VM workspace")
	}
	defer func() {
		if closeErr := workspace.Close(); closeErr != nil {
			admissionCloseErr := closeLocalVMAdmissions(admissions)
			admissions = nil
			returnErr = joinFactoryCleanupError(returnErr, errors.Join(closeErr, admissionCloseErr),
				"closing local VM workspace handle")
		}
	}()
	admissions = make(map[string]vmAdmission)
	for _, node := range topologicalFactoryNodes(plan.Graph) {
		if node.Kind != NodeBuild || node.IRNode == "" {
			continue
		}
		_, instruction, err := plan.producerInstruction(node)
		if err != nil {
			return nil, joinFactoryCleanupError(err, closeLocalVMAdmissions(admissions),
				"closing partial local VM admissions")
		}
		if instruction.BuilderType != ir.BuilderVMImage {
			continue
		}
		admission, err := AdmitLocalVMBuild(ctx, plan, node.ID, workspace)
		if err != nil {
			return nil, joinFactoryCleanupError(err, closeLocalVMAdmissions(admissions),
				"closing partial local VM admissions")
		}
		admissions[node.ID] = admission
	}
	return admissions, nil
}

func closeLocalVMAdmissions(admissions map[string]vmAdmission) error {
	var closeErrs []error
	for _, id := range sortedKeys(admissions) {
		if admission := admissions[id]; admission != nil {
			closeErrs = append(closeErrs, admission.Close())
		}
	}
	return errors.Join(closeErrs...)
}

func joinFactoryCleanupError(primary, cleanup error, operation string) error {
	if cleanup == nil {
		return primary
	}
	code := e.CodeInternal
	var coded *e.Error
	if errors.As(primary, &coded) {
		code = coded.Code
	}
	return e.Wrap(code, errors.Join(primary, cleanup), "%s", operation)
}

func relocateFactoryPlan(plan *Plan, workspace string) (*Plan, error) {
	// Capture the compiled identity BEFORE any path is rewritten so attestations do
	// not depend on the caller's --workspace (see Plan.identityDigest).
	identity, err := plan.IdentityDigest()
	if err != nil {
		return nil, err
	}
	raw, err := canonical.Marshal(plan)
	if err != nil {
		return nil, err
	}
	var relocated Plan
	if err := canonical.UnmarshalExact(raw, &relocated); err != nil {
		return nil, err
	}
	for index := range relocated.Graph.Artifacts {
		path, err := relocatedFactoryPath(relocated.Graph.Artifacts[index].Path, workspace)
		if err != nil {
			return nil, err
		}
		relocated.Graph.Artifacts[index].Path = filepath.ToSlash(path)
	}
	relocated.workspace = workspace
	if err := relocated.Validate(); err != nil {
		return nil, err
	}
	relocated.identityDigest = identity
	return &relocated, nil
}

func relocatedFactoryPath(path, workspace string) (string, error) {
	prefix := filepath.ToSlash(filepath.Join(".nvkr", "factory"))
	if path != prefix && !strings.HasPrefix(path, prefix+"/") {
		return "", e.New(e.CodePipelineDrift, "factory artifact path %q is outside %s", path, prefix)
	}
	relative := strings.TrimPrefix(strings.TrimPrefix(path, prefix), "/")
	if relative == "" {
		return filepath.Clean(workspace), nil
	}
	return filepath.Join(workspace, filepath.FromSlash(relative)), nil
}

func validateLocalWorkspace(workspace string, plan *Plan) error {
	clean := filepath.ToSlash(filepath.Clean(workspace))
	if workspace == "" || filepath.IsAbs(workspace) || clean != workspace ||
		!strings.HasPrefix(clean, ".nvkr/") || clean == ".nvkr/factory/.." || strings.Contains(clean, "../") {
		return e.New(e.CodeUsage, "factory workspace %q must be a clean repository-relative child of .nvkr", workspace)
	}
	for _, artifact := range plan.Graph.Artifacts {
		if _, err := relocatedFactoryPath(artifact.Path, workspace); err != nil {
			return err
		}
	}
	return nil
}

type localWorkspaceRecord struct {
	SchemaVersion string `json:"schemaVersion"`
	PlanDigest    string `json:"planDigest"`
}

func ensureLocalWorkspace(ctx context.Context, workspace, planDigest string) error {
	current := ""
	for _, part := range strings.Split(filepath.ToSlash(workspace), "/") {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			if err := os.Mkdir(current, 0o700); err != nil {
				return e.Wrap(e.CodeImmutableViolation, err, "creating factory workspace %s", current)
			}
			continue
		}
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return e.New(e.CodeImmutableViolation, "factory workspace component %s is not a real directory", current)
		}
	}
	marker := filepath.Join(workspace, localWorkspaceMarker)
	if held, err := heldfile.OpenContext(ctx, marker, heldfile.Options{Label: "factory-workspace-marker", MaxBytes: 4096}); err == nil {
		raw, readErr := held.ReadAllContext(ctx, 4096)
		closeErr := held.Close()
		if readErr != nil || closeErr != nil {
			return e.Wrap(e.CodeImmutableViolation, errors.Join(readErr, closeErr), "reading factory workspace marker")
		}
		var record localWorkspaceRecord
		if canonical.UnmarshalExact(raw, &record) != nil || record.SchemaVersion != LocalRunSchemaVersion {
			return e.New(e.CodeImmutableViolation, "factory workspace %s has an invalid ownership marker", workspace)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return e.Wrap(e.CodeImmutableViolation, err, "reading factory workspace marker")
	} else {
		entries, readErr := os.ReadDir(workspace)
		if readErr != nil {
			return e.Wrap(e.CodeImmutableViolation, readErr, "reading factory workspace")
		}
		if len(entries) != 0 {
			return e.New(e.CodeImmutableViolation, "factory workspace %s is nonempty and not owned by local execution", workspace)
		}
	}
	record := localWorkspaceRecord{SchemaVersion: LocalRunSchemaVersion, PlanDigest: planDigest}
	raw, err := canonical.Marshal(record)
	if err != nil {
		return err
	}
	return writeFactoryFile(ctx, marker, raw, 0o600)
}

func resetFactoryNodeOutputs(plan *Plan, node Node, workspace string) error {
	artifacts := make(map[string]Artifact, len(plan.Graph.Artifacts))
	for _, artifact := range plan.Graph.Artifacts {
		artifacts[artifact.ID] = artifact
	}
	for _, output := range node.Outputs {
		path, err := relocatedFactoryPath(artifacts[output].Path, workspace)
		if err != nil {
			return err
		}
		if err := os.RemoveAll(path); err != nil {
			return e.Wrap(e.CodeImmutableViolation, err, "resetting factory output %s", output)
		}
	}
	return nil
}

func reusableFactoryTool(ctx context.Context, plan *Plan, node Node, workspace string) (bool, error) {
	if node.Kind != NodeAcquire || node.IRNode != "" || node.ExecutableDigest == "" || len(node.Outputs) != 1 {
		return false, nil
	}
	_, artifacts := planIndex(plan)
	path, err := relocatedFactoryPath(artifacts[node.Outputs[0]].Path, workspace)
	if err != nil {
		return false, err
	}
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	digest, err := digestFactoryPath(ctx, path)
	if err != nil || digest != node.ExecutableDigest {
		return false, nil
	}
	return true, nil
}

func localRunnerPlatforms(nodes []Node) []string {
	set := map[string]bool{}
	for _, node := range nodes {
		if node.IRNode == "" && node.Network == ir.NetworkNone {
			set[node.Platform] = true
		}
	}
	return sortedKeys(set)
}

func failedLocalDependency(graph ArtifactGraph, node Node, status map[string]string) string {
	producer := map[string]string{}
	for _, artifact := range graph.Artifacts {
		producer[artifact.ID] = artifact.Producer
	}
	for _, input := range node.Inputs {
		parent := producer[input]
		if status[parent] != "ok" && status[parent] != "cached" {
			return parent
		}
	}
	return ""
}

// requiresPublication reports whether a node kind is part of the opt-in remote
// lane. Publication is the run's only consequential remote mutation, and signing
// and verification are ABOUT a publication: they consume the reference publish
// wrote and contact the same registry, so without --publish there is nothing for
// them to be about. Grouping them here keeps one definition of "remote" for both
// the dry-run listing and the executing loop.
func requiresPublication(kind string) bool {
	switch kind {
	case NodePublish, NodeSign, NodeVerifyPublication, NodeAttestEvidence:
		return true
	default:
		return false
	}
}

func sanitizeLocalNodeID(value string) string {
	value = strings.NewReplacer("/", "-", ".", "-", "_", "-").Replace(value)
	if len(value) > 48 {
		value = value[:48]
	}
	return value
}
