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
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/NVIDIA/nke-recipes/internal/ownedcmd"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/internal/heldfile"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/pipeline"
)

const (
	LocalRunSchemaVersion = "nkd.dev/factory-local-run/v1"
	localWorkspaceMarker  = ".nkd-factory-workspace.json"
	localPlanPath         = ".runner/plan.json"
)

// LocalRunOptions configures one local execution of a compiled factory plan.
// Workspace must be a repository-relative child of .nkd; this lets the runner
// preserve exact source receipts across reruns while safely replacing only outputs
// owned by this module. Publication is opt-in because it is the sole consequential
// remote mutation in the plan.
type LocalRunOptions struct {
	Workspace    string
	RunnerSource string
	Jobs         int
	Runtime      string
	Go           string
	Publish      bool
	DryRun       bool
	Stdout       io.Writer
	Stderr       io.Writer
	producers    *pipeline.NodeSession
}

// LocalNodeResult is a deterministic, unqualified execution observation.
type LocalNodeResult struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Status string `json:"status"` // ok | cached | skipped | dry-run
}

// LocalRunResult records what this process completed. It is deliberately not a
// producer, validation-vector, release, or clearance receipt.
type LocalRunResult struct {
	SchemaVersion string            `json:"schemaVersion"`
	Qualification EvidenceTier      `json:"qualification"`
	PlanDigest    string            `json:"planDigest"`
	Workspace     string            `json:"workspace"`
	Nodes         []LocalNodeResult `json:"nodes"`
}

type localRunDeps struct {
	prepareRunner func(context.Context, *Plan, LocalRunOptions, []string) (map[string]string, error)
	executeNode   func(context.Context, *Plan, Node, LocalRunOptions, map[string]string) error
}

// RunLocal executes the Plan's artifact graph in dependency order. Every producer
// uses the same exact-node execution operation as hosted Runs; utility work runs
// in its pinned container. Source receipts remain reusable across local attempts.
func RunLocal(ctx context.Context, plan *Plan, options LocalRunOptions) (*LocalRunResult, error) {
	options = withLocalDefaults(options)
	if err := AdmitLocal(ctx, plan, options); err != nil {
		return nil, err
	}
	return runLocal(ctx, plan, options, productionLocalRunDeps())
}

// AdmitLocal verifies every capability needed by a local execution before the
// caller reserves a workspace or creates output. It deliberately performs no
// filesystem mutation and does not build a runner.
func AdmitLocal(ctx context.Context, plan *Plan, options LocalRunOptions) error {
	options = withLocalDefaults(options)
	if err := admitLocalPlan(ctx, plan, options); err != nil {
		return err
	}
	if options.DryRun {
		return nil
	}
	return admitLocalRunners(ctx, plan, options, localRunnerPlatforms(topologicalFactoryNodes(plan.Graph)))
}

func withLocalDefaults(options LocalRunOptions) LocalRunOptions {
	if options.Workspace == "" {
		options.Workspace = filepath.ToSlash(filepath.Join(".nkd", "factory"))
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
	return options
}

func productionLocalRunDeps() localRunDeps {
	return localRunDeps{
		prepareRunner: prepareLocalRunners,
		executeNode:   executeLocalFactoryNode,
	}
}

func runLocal(ctx context.Context, plan *Plan, options LocalRunOptions, deps localRunDeps) (*LocalRunResult, error) {
	if err := admitLocalPlan(ctx, plan, options); err != nil {
		return nil, err
	}
	ordered := topologicalFactoryNodes(plan.Graph)
	planDigest, err := plan.Digest()
	if err != nil {
		return nil, err
	}
	result := &LocalRunResult{SchemaVersion: LocalRunSchemaVersion,
		Qualification: TierLocalExecution, PlanDigest: planDigest,
		Workspace: filepath.ToSlash(options.Workspace)}
	if options.DryRun {
		for _, node := range ordered {
			status := "dry-run"
			if requiresPublication(node.Kind) && !options.Publish {
				status = "skipped"
			}
			result.Nodes = append(result.Nodes, LocalNodeResult{ID: node.ID, Kind: node.Kind, Status: status})
			fmt.Fprintf(options.Stdout, "[%s] %s (%s)\n", status, node.ID, node.Kind)
		}
		return result, nil
	}
	if deps.prepareRunner == nil || deps.executeNode == nil {
		return nil, e.New(e.CodeInternal, "factory local executor is incomplete")
	}
	options.producers, err = pipeline.NewNodeSession(ctx)
	if err != nil {
		return nil, err
	}
	defer options.producers.Close()
	if err := ensureLocalWorkspace(ctx, options.Workspace, planDigest); err != nil {
		return nil, err
	}
	platforms := localRunnerPlatforms(ordered)
	runners, err := deps.prepareRunner(ctx, plan, options, platforms)
	if err != nil {
		return nil, err
	}

	// One scheduling pass covers producers and utility work. A batch contains only
	// ready operations; cancel and join the whole batch before returning failure.
	status := make(map[string]string, len(ordered))
	var outputMu sync.Mutex
	options.Stdout = lockedLocalWriter{mu: &outputMu, Writer: options.Stdout}
	options.Stderr = lockedLocalWriter{mu: &outputMu, Writer: options.Stderr}
	for len(status) < len(ordered) {
		var ready []Node
		for _, node := range ordered {
			if status[node.ID] != "" {
				continue
			}
			if requiresPublication(node.Kind) && !options.Publish {
				status[node.ID] = "skipped"
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
		batchErrors := make([]error, len(ready))
		var batch sync.WaitGroup
		for index, node := range ready {
			batch.Go(func() {
				results[index], batchErrors[index] = runLocalNode(batchCtx, plan, node, options, runners, deps.executeNode)
				if batchErrors[index] != nil {
					cancel()
				}
			})
		}
		batch.Wait()
		cancel()
		if err := ctx.Err(); err != nil {
			return nil, e.Wrap(e.CodeTimeout, err, "local Run canceled")
		}
		// A canceled sibling can sort before the operation that failed. Preserve
		// the causal failure instead of reporting the cancellation it triggered.
		for _, err := range batchErrors {
			var coded *e.Error
			if err != nil && (!errors.As(err, &coded) || coded.Code != e.CodeTimeout) {
				return nil, err
			}
		}
		for _, err := range batchErrors {
			if err != nil {
				return nil, err
			}
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
	return result, nil
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
	execute func(context.Context, *Plan, Node, LocalRunOptions, map[string]string) error) (LocalNodeResult, error) {
	result := LocalNodeResult{ID: node.ID, Kind: node.Kind}
	if err := ctx.Err(); err != nil {
		return result, e.Wrap(e.CodeTimeout, err, "local node canceled")
	}
	cached, err := reusableFactoryTool(ctx, plan, node, options.Workspace)
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
	if err := execute(ctx, plan, node, options, runners); err != nil {
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

func admitLocalPlan(ctx context.Context, plan *Plan, options LocalRunOptions) error {
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
	if err := rejectUnsupportedPlanRequirements(plan, "local factory execution"); err != nil {
		return err
	}
	if len(plan.Environment.SourceAccess) != 0 {
		return e.New(e.CodeEngineCapability,
			"local execution does not support Environment.sourceAccess authentication")
	}
	if err := validateLocalWorkspace(options.Workspace, plan); err != nil {
		return err
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
	if _, err := exec.LookPath(options.Runtime); err != nil {
		return e.Wrap(e.CodeEngineCapability, err, "finding container runtime %s", options.Runtime)
	}
	if _, err := exec.LookPath(options.Go); err != nil {
		return e.Wrap(e.CodeEngineCapability, err, "finding Go compiler %s", options.Go)
	}
	if _, err := ownedcmd.Output(ctx, options.Runtime, "version", "--format", "{{.Server.Version}}"); err != nil {
		return classifyContext(ctx, e.Wrap(e.CodeEngineCapability, err, "contacting local container runtime"))
	}
	if _, err := generatorLinkerFlags(plan.GeneratorVersion); err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(options.RunnerSource, "go.mod")); err != nil {
		return e.Wrap(e.CodeEngineCapability, err, "runner source %s is not an nkdctl checkout", options.RunnerSource)
	}
	return nil
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
		output := filepath.Join(options.Workspace, ".runner", "nkd-exec-"+arch)
		if err := os.MkdirAll(filepath.Dir(output), 0o700); err != nil {
			return nil, e.Wrap(e.CodeInternal, err, "creating local runner directory")
		}
		stage, err := os.CreateTemp(filepath.Dir(output), ".nkd-exec-*")
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
		cmd := exec.CommandContext(ctx, options.Go, "build", "-trimpath", "-ldflags", linkerFlags,
			"-o", absoluteOutput, "./cmd/nkdctl")
		cmd.Dir = options.RunnerSource
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+arch)
		cmd.Stdout, cmd.Stderr = options.Stdout, options.Stderr
		if err := ownedcmd.Run(ctx, cmd); err != nil {
			return nil, classifyContext(ctx, e.Wrap(e.CodeEngineCapability, err, "building linux/%s factory runner", arch))
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

func executeLocalFactoryNode(ctx context.Context, plan *Plan, node Node, options LocalRunOptions, runners map[string]string) error {
	if node.IRNode != "" {
		relocated, err := relocateFactoryPlan(plan, options.Workspace)
		if err != nil {
			return err
		}
		run := (pipeline.Local{}).ExecNode
		if options.producers != nil {
			run = options.producers.ExecNode
		}
		return executeProducer(ctx, relocated, node, options.Runtime, options.Jobs, options.Stdout, options.Stderr, run)
	}

	if node.Network == ir.NetworkNone {
		if _, ok := runners[node.Platform]; !ok {
			return e.New(e.CodeEngineCapability, "factory node %s has no linux runner for %s", node.ID, node.Platform)
		}
		workspace, err := filepath.Abs(options.Workspace)
		if err != nil {
			return err
		}
		arch := strings.TrimPrefix(node.Platform, "linux/")
		containerRunner := "/workspace/.nkd/factory/.runner/nkd-exec-" + arch
		name := "nkd-factory-" + sanitizeLocalNodeID(node.ID) + "-" + strconv.Itoa(os.Getpid())
		args := []string{"run", "--rm", "--name", name, "--network", "none", "--platform", node.Platform,
			"-e", "HOME=/tmp"}
		if user := localContainerUser(); user != "" {
			args = append(args, "--user", user)
		}
		args = append(args, "-v", workspace+":/workspace/.nkd/factory", "-w", "/workspace",
			node.Container, containerRunner, "factory", "execute-node",
			"--plan", "/workspace/.nkd/factory/"+localPlanPath, "--node", node.ID,
			"--workspace", ".nkd/factory")
		cmd := exec.CommandContext(ctx, options.Runtime, args...)
		cmd.Stdout, cmd.Stderr = options.Stdout, options.Stderr
		runErr := ownedcmd.Run(ctx, cmd)
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_, _ = ownedcmd.CombinedOutput(cleanupCtx, options.Runtime, "rm", "-f", name)
		cancel()
		if runErr != nil {
			return classifyContext(ctx, runErr)
		}
		return nil
	}
	relocated, err := relocateFactoryPlan(plan, options.Workspace)
	if err != nil {
		return err
	}
	return ExecuteNode(ctx, relocated, node.ID, options.Stdout, options.Stderr)
}

func relocateFactoryPlan(plan *Plan, workspace string) (*Plan, error) {
	// Capture the compiled identity BEFORE any path is rewritten, and carry it onto the
	// relocated copy. Without this the plan's digest would silently become a function of
	// the caller's --workspace, and every attestation minted from a non-default workspace
	// would name a plan digest that nothing else in the system can reproduce.
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
	if err := relocated.Validate(); err != nil {
		return nil, err
	}
	relocated.identityDigest = identity
	return &relocated, nil
}

func relocatedFactoryPath(path, workspace string) (string, error) {
	prefix := filepath.ToSlash(filepath.Join(".nkd", "factory"))
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
		!strings.HasPrefix(clean, ".nkd/") || clean == ".nkd/factory/.." || strings.Contains(clean, "../") {
		return e.New(e.CodeUsage, "factory workspace %q must be a clean repository-relative child of .nkd", workspace)
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

func topologicalFactoryNodes(graph ArtifactGraph) []Node {
	byID := make(map[string]Node, len(graph.Nodes))
	producer := make(map[string]string, len(graph.Artifacts))
	for _, node := range graph.Nodes {
		byID[node.ID] = node
	}
	for _, artifact := range graph.Artifacts {
		producer[artifact.ID] = artifact.Producer
	}
	indegree := make(map[string]int, len(graph.Nodes))
	children := make(map[string][]string, len(graph.Nodes))
	for _, node := range graph.Nodes {
		parents := map[string]bool{}
		for _, input := range node.Inputs {
			parent := producer[input]
			if !parents[parent] {
				parents[parent] = true
				indegree[node.ID]++
				children[parent] = append(children[parent], node.ID)
			}
		}
	}
	var ready []string
	for id := range byID {
		if indegree[id] == 0 {
			ready = append(ready, id)
		}
	}
	sort.Strings(ready)
	ordered := make([]Node, 0, len(byID))
	for len(ready) > 0 {
		id := ready[0]
		ready = ready[1:]
		ordered = append(ordered, byID[id])
		for _, child := range children[id] {
			indegree[child]--
			if indegree[child] == 0 {
				ready = append(ready, child)
				sort.Strings(ready)
			}
		}
	}
	return ordered
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
