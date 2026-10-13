// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
)

// RunGitHubNode executes exactly one admitted Plan operation. Host-side reporting
// includes utility-container launch failures. Carrier preparation is deliberately
// separate so it never shares a source fetch's step credentials.
func RunGitHubNode(ctx context.Context, plan *Plan, nodeID string, options GitHubStepOptions) error {
	return runGitHubNode(ctx, plan, nodeID, options, executeGitHubNode)
}

func runGitHubNode(ctx context.Context, plan *Plan, nodeID string, options GitHubStepOptions,
	execute func(context.Context, *Plan, Node, GitHubStepOptions) error) error {
	if err := admitGitHubPlan(ctx, plan); err != nil {
		return err
	}
	nodes, _ := planIndex(plan)
	node, ok := nodes[nodeID]
	if !ok {
		return e.New(e.CodeInvalidCatalog, "GitHub Plan has no node %q", nodeID)
	}
	node, err := plan.GitHubExecutionFor(node)
	if err != nil {
		return err
	}
	return githubProgress(ctx, options, githubNodeLabel(plan, node), githubNodeDetails(plan, node), func(progress *githubStepProgress) error {
		if len(githubSourceEnvironment(plan, node)) > 0 {
			progress.phase("check source credential")
			if os.Getenv(sourceTokenEnv) == "" {
				return e.New(e.CodeFetchFailed, "source credential is empty")
			}
		}
		progress.phase("execute " + node.ID)
		if err := execute(ctx, plan, node, progress.options); err != nil {
			return err
		}
		for _, output := range node.Outputs {
			progress.log("Produced: " + output)
		}
		return nil
	})
}

func executeGitHubNode(ctx context.Context, plan *Plan, node Node, options GitHubStepOptions) error {
	if len(plan.Environment.GitHubRunners) > 0 {
		if err := admitGitHubNodePlatform(plan, node, runtime.GOOS+"/"+runtime.GOARCH); err != nil {
			return err
		}
		// Admit utility containers on the host; their isolated child has no daemon.
		// Producer container admission remains owned by ExecuteNode.
		if node.IRNode == "" && node.Container != "" {
			if err := checkNodeContainerPlatform(ctx, plan, node, pipeline.CheckContainerPlatform); err != nil {
				return err
			}
		}
	}
	if node.Kind == NodeFetch {
		return executeGitHubSourceFetch(ctx, plan, node.ID, options.Stdout, options.Stderr)
	}
	if node.IRNode != "" || node.Container == "" {
		return ExecuteNode(ctx, plan, node.ID, options.Stdout, options.Stderr)
	}
	bounded, cancel, err := githubUtilityContext(ctx, plan, node.ID)
	if err != nil {
		return err
	}
	defer cancel()
	ctx = bounded
	// The generated hosted layout derives from the admitted Environment. Bind its live Plan to the caller's
	// admitted object before launch and require the inner helper to check it again.
	layout := githubBundleLayout(plan.Environment.GitHubBundle)
	diskPlan, err := LoadPlanContext(ctx, layout.planPath)
	if err != nil {
		return err
	}
	expected, err := plan.Digest()
	if err != nil {
		return err
	}
	observed, err := diskPlan.Digest()
	if err != nil {
		return err
	}
	if observed != expected {
		return e.New(e.CodePipelineDrift, "utility Plan differs from the admitted GitHub Plan")
	}
	root, err := filepath.Abs(".")
	if err != nil {
		return e.Wrap(e.CodeEngineCapability, err, "resolve GitHub utility workspace")
	}
	location := utilityRunnerLocation{runtime: plan.Environment.RuntimeBinary(), mountSource: root, mountTarget: "/workspace",
		helper: "/workspace/nvkr-exec", plan: layout.planPath, planDigest: expected,
		publicationRunContext: PerOutputPublication(plan) && node.PackageKind == PackageOCI}
	if location.runtime == "docker" {
		location.user, location.home = localContainerUser(), true
	}
	err = executeUtilityNode(ctx, node, location, options.Stdout, options.Stderr)
	return githubUtilityError(ctx, err)
}

// githubUtilityContext bounds a hosted utility by its existing provider stage
// budget. An earlier caller deadline still wins; this is not a measurement of
// the job's remaining time. Local and producer-owned deadlines are unchanged.
func githubUtilityContext(ctx context.Context, plan *Plan, nodeID string) (context.Context, context.CancelFunc, error) {
	stages, _, err := githubStages(plan)
	if err != nil {
		return nil, nil, err
	}
	for _, stage := range stages {
		for _, node := range stage.Nodes {
			if node.ID != nodeID {
				continue
			}
			minutes, err := githubStageTimeoutMinutes(stage)
			if err != nil {
				return nil, nil, err
			}
			bounded, cancel := context.WithTimeout(ctx, time.Duration(minutes)*time.Minute)
			return bounded, cancel, nil
		}
	}
	return nil, nil, e.New(e.CodeInvalidCatalog, "GitHub utility node has no execution stage")
}

// githubStepExitError retains the provider runtime's process status without
// making it an NVKR public error allocation. Its cause still has a stable code.
// Only this package can issue one; arbitrary errors cannot override CLI status.
type githubStepExitError struct {
	status int
	cause  error
}

func (err *githubStepExitError) Error() string { return err.cause.Error() }
func (err *githubStepExitError) Unwrap() error { return err.cause }

// GitHubStepExitCode preserves a hosted runtime's exact primary process status
// unless the owning operation classified cancellation or a deadline as timeout.
// That outer classification uses the frozen ADR-03 status even when the retained
// cause includes a child process status. CLI adapters may call this
// without allowing caller-defined error types to choose arbitrary exit statuses.
func GitHubStepExitCode(err error) int {
	var coded *e.Error
	if errors.As(err, &coded) && coded.Code == e.CodeTimeout {
		return e.ExitCode(err)
	}
	var step *githubStepExitError
	if errors.As(err, &step) {
		return step.status
	}
	return e.ExitCode(err)
}

func githubUtilityError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return e.Wrap(e.CodeTimeout, ctxErr, "GitHub utility execution canceled")
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		status := exit.ExitCode()
		if status < 0 {
			if wait, ok := exit.Sys().(syscall.WaitStatus); ok && wait.Signaled() {
				status = 128 + int(wait.Signal())
			}
		}
		if status < 1 || status > 255 {
			return e.Wrap(e.CodeEngineCapability, err, "GitHub utility process has no exit status")
		}
		code := e.CodeEngineCapability
		for _, candidate := range e.AllCodes {
			if e.ExitCode(e.New(candidate, "")) == status {
				code = candidate
				break
			}
		}
		return &githubStepExitError{status: status, cause: e.Wrap(code, err, "GitHub utility execution failed")}
	}
	var coded *e.Error
	if errors.As(err, &coded) {
		return err
	}
	return e.Wrap(e.CodeEngineCapability, err, "GitHub utility runtime could not execute")
}

// admitGitHubPlan is shared by all hosted step operations. It precedes logs,
// summary writes, carrier inspection, subprocesses and output creation.
func admitGitHubPlan(ctx context.Context, plan *Plan) error {
	if ctx == nil {
		return e.New(e.CodeUsage, "GitHub execution requires a context")
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "GitHub execution canceled")
	}
	if plan == nil {
		return e.New(e.CodeUsage, "GitHub execution requires a Plan")
	}
	if err := plan.Validate(); err != nil {
		return err
	}
	if err := rejectUnsupportedRuntimeStorage(plan, "GitHub execution"); err != nil {
		return err
	}
	if err := admitGitHubRequirements(plan, "GitHub execution"); err != nil {
		return err
	}
	if plan.DefinitionPath == "" || plan.EnvironmentPath == "" {
		return e.New(e.CodeEngineCapability, "GitHub execution requires repository-relative definition and environment paths")
	}
	if _, err := githubCarrierPattern(plan.Graph.Artifacts); err != nil {
		return err
	}
	return nil
}

// VerifyPlanDigest binds a cross-process helper handoff to the exact admitted
// complete Plan. An expected identity is a selection, never execution authority.
func VerifyPlanDigest(plan *Plan, expected string) error {
	if plan == nil {
		return e.New(e.CodeUsage, "Plan digest verification requires a Plan")
	}
	actual, err := plan.Digest()
	if err != nil {
		return err
	}
	if expected == "" || actual != expected {
		return e.New(e.CodePipelineDrift, "execution Plan does not match its expected digest")
	}
	return nil
}
