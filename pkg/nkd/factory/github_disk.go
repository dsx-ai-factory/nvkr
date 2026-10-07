package factory

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"time"

	"github.com/NVIDIA/nke-recipes/internal/ownedcmd"
	"github.com/NVIDIA/nke-recipes/internal/securepath"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/pipeline"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/sbom"
)

// RunGitHubDisk keeps all material consumptions, the held QCOW2, inspection and
// packaging in one process under the original complete Plan. Only the resulting
// wrapper and evidence cross jobs; serialized descriptions never recreate receipts.
func RunGitHubDisk(ctx context.Context, plan *Plan, output string, options GitHubStepOptions) (returnErr error) {
	if err := admitGitHubPlan(ctx, plan); err != nil {
		return err
	}
	for _, key := range []string{sourceTokenEnv, githubImageToken, "NKD_CREDENTIAL", "ACTIONS_ID_TOKEN_REQUEST_TOKEN"} {
		if os.Getenv(key) != "" {
			return e.New(e.CodeEngineCapability, "ARC disk job contains an unrelated credential")
		}
	}
	if runtime.GOOS != "linux" {
		return e.New(e.CodeEngineCapability, "ARC disks require native Linux")
	}
	stages, _, err := githubStages(plan)
	if err != nil {
		return err
	}
	var stage githubStage
	for _, candidate := range stages {
		if candidate.DiskOutput == output && output != "" {
			stage = candidate
		}
	}
	if stage.DiskOutput == "" {
		return e.New(e.CodeEngineCapability, "no admitted ARC disk output %q", output)
	}
	if stage.Nodes[0].Platform != runtime.GOOS+"/"+runtime.GOARCH {
		return e.New(e.CodeEngineCapability, "ARC disk host architecture differs from the Plan")
	}
	ctx, cancel := context.WithTimeout(ctx, stage.diskExecutionBudget)
	defer cancel()
	defer func() {
		returnErr = classifyGitHubDiskError(returnErr, "ARC disk execution")
	}()
	workspace, _, err := securepath.OpenDirectory(".")
	if err != nil {
		return e.Wrap(e.CodeEngineCapability, err, "hold ARC workspace")
	}
	defer func() { returnErr = joinFactoryCleanupError(returnErr, workspace.Close(), "close ARC workspace") }()
	var root Node
	for _, node := range stage.Nodes {
		if node.Kind == NodeBuild {
			_, instruction, resolveErr := plan.producerInstruction(node)
			if resolveErr != nil {
				return resolveErr
			}
			if instruction.BuilderType == ir.BuilderVMImage {
				root = node
			}
		}
	}
	admission, err := AdmitLocalVMBuild(ctx, plan, root.ID, workspace)
	if err != nil {
		return err
	}
	defer func() { returnErr = joinFactoryCleanupError(returnErr, admission.Close(), "close ARC VM admission") }()
	_, instruction, err := plan.producerInstruction(root)
	if err != nil {
		return err
	}
	profile := plan.Environment.DiskInspectionProfiles[root.Platform]
	// Preparation follows host capability admission and precedes held image
	// admission. Consumers use the exact pinned reference, never a mutable tag.
	images := []string{instruction.Toolchain}
	if !diskBuildProvenance(plan) {
		images = append(images, profile.Image)
	}
	for _, image := range uniqueSorted(images) {
		if githubPrivateToolchain(plan, image) {
			// The isolated preparation step pulled this exact reference. Held
			// runtime admission below must still authenticate the local image.
			continue
		}
		if err := pullGitHubPublicImage(ctx, func(pullCtx context.Context) error {
			command := exec.CommandContext(pullCtx, plan.Environment.RuntimeBinary(), "pull", "--platform", root.Platform, image)
			command.Stdout, command.Stderr = options.Stdout, options.Stderr
			return ownedcmd.Run(pullCtx, command)
		}); err != nil {
			return err
		}
	}
	var inspector *sbom.DiskInspectionRuntime
	if !diskBuildProvenance(plan) {
		inspector, err = OpenDiskEvidenceRuntime(ctx, plan, plan.Environment.RuntimeBinary(), workspace)
		if err != nil {
			return err
		}
		defer func() { returnErr = joinFactoryCleanupError(returnErr, inspector.Close(), "close ARC inspector") }()
	}
	diagnostics, err := pipeline.NewPackerDiagnosticSink(ctx, workspace)
	if err != nil {
		return err
	}
	return coordinateGitHubDiskExecution(ctx, diagnostics, func(ctx context.Context) (returnErr error) {
		return executeAdmittedGitHubDisk(ctx, plan, stage, root, admission, inspector, diagnostics, options)
	})
}

// The dispatcher is private and entered only after complete Plan, credentials,
// native host and VM admission. It cannot replace the concrete diagnostic owner.
// Production cleanup inside dispatch finishes before the sink seals its route.
func coordinateGitHubDiskExecution(ctx context.Context, diagnostics *pipeline.PackerDiagnosticSink,
	dispatch func(context.Context) error,
) (returnErr error) {
	defer func() {
		returnErr = classifyGitHubDiskError(returnErr, "ARC disk execution")
		closed := diagnostics.Close()
		returnErr = joinFactoryCleanupError(returnErr, closed, "close ARC diagnostics")
		if returnErr != nil && closed == nil {
			if retained := diagnostics.RetainedPath(); retained != "" {
				returnErr = publishGitHubPackerDiagnosticRoute(returnErr, os.Getenv("GITHUB_OUTPUT"), retained)
			}
		}
	}()
	return dispatch(ctx)
}

func executeAdmittedGitHubDisk(ctx context.Context, plan *Plan, stage githubStage, root Node,
	admission vmAdmission, inspector *sbom.DiskInspectionRuntime, diagnostics *pipeline.PackerDiagnosticSink,
	options GitHubStepOptions,
) (returnErr error) {
	session, err := pipeline.NewNodeSession(ctx)
	if err != nil {
		return err
	}
	defer session.Close()
	executor, err := (localVMPackerResolver{}).Admit(ctx, plan, session, vmDriverRunConfig{
		NodeID: root.ID, Runtime: plan.Environment.RuntimeBinary(), Stdout: options.Stdout, Stderr: options.Stderr, Diagnostics: diagnostics,
	})
	if err != nil {
		return err
	}
	defer func() { returnErr = joinFactoryCleanupError(returnErr, executor.Close(), "close ARC Packer executor") }()
	digest, err := plan.IdentityDigest()
	if err != nil {
		return err
	}
	owner := &LocalRunResult{SchemaVersion: LocalRunSchemaVersion, Qualification: TierLocalExecution, PlanDigest: digest, Workspace: ".nkd/factory"}
	defer func() { returnErr = joinFactoryCleanupError(returnErr, owner.Close(), "close ARC disk owner") }()
	_, artifacts := planIndex(plan)
	return executeGitHubDiskNodes(ctx, plan, stage, owner, options, func(nodeCtx context.Context, node Node) error {
		switch {
		case node.ID == root.ID:
			return populateDiskProducerCompletion(nodeCtx, plan, node.ID, owner, func(ctx context.Context, selected Node, _ ir.Node) (*pipeline.PackerArtifact, *pipeline.ArtifactResult, error) {
				return dispatchAdmittedVMProducer(ctx, admission, plan, selected, executor, owner)
			})
		case node.IRNode != "":
			return executeProducer(nodeCtx, plan, node, plan.Environment.RuntimeBinary(), 1, options.Stdout, options.Stderr, session.ExecNode, owner)
		case node.Kind == NodeVerify:
			return executeHeldDiskObservation(nodeCtx, plan, node, artifacts, owner)
		default:
			return e.New(e.CodeEngineCapability, "unsupported grouped disk operation")
		}
	}, func(ctx context.Context) error {
		for _, expected := range plan.LifecycleOutputs {
			if expected.Name == stage.DiskOutput {
				return completeGitHubDiskPublication(ctx, plan, expected, owner, inspector, options)
			}
		}
		return e.New(e.CodePipelineDrift, "ARC output has no lifecycle binding")
	})
}

func executeGitHubDiskNodes(ctx context.Context, plan *Plan, stage githubStage, owner *LocalRunResult,
	options GitHubStepOptions, dispatch func(context.Context, Node) error, complete func(context.Context) error,
) error {
	for _, node := range stage.Nodes {
		if node.Kind == NodeSBOM || node.Kind == NodePackage {
			continue
		}
		nodeCtx := ctx
		stop := func() {}
		if node.Kind == NodeBuild || node.Kind == NodeAcquire {
			seconds, err := githubDiskProducerTimeoutSeconds(node)
			if err != nil {
				return err
			}
			nodeCtx, stop = context.WithTimeout(ctx, time.Duration(seconds)*time.Second)
		}
		var dispatchedErr error
		err := githubProgress(nodeCtx, options, githubNodeLabel(plan, node), githubNodeDetails(plan, node), func(_ *githubStepProgress) error {
			dispatchedErr = dispatch(nodeCtx, node)
			return dispatchedErr
		})
		// Progress reports cancellation of its context. Preserve a distinct
		// dispatcher failure when that cancellation only coincided with it.
		if dispatchedErr != nil {
			err = dispatchedErr
		}
		operation := "operation"
		if node.Kind == NodeBuild || node.Kind == NodeAcquire {
			operation = "producer"
		}
		err = classifyGitHubDiskError(err, fmt.Sprintf("ARC disk %s %q", operation, node.ID))
		stop()
		if err != nil {
			return err
		}
		owner.Nodes = append(owner.Nodes, LocalNodeResult{ID: node.ID, Kind: node.Kind, Status: "ok"})
	}
	return complete(ctx)
}

func classifyGitHubDiskError(err error, operation string) error {
	var coded *e.Error
	if err == nil || (errors.As(err, &coded) && coded.Code == e.CodeTimeout) {
		return err
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return e.Wrap(e.CodeTimeout, err, "%s exhausted its bounded deadline", operation)
	}
	if errors.Is(err, context.Canceled) {
		return e.Wrap(e.CodeTimeout, err, "%s canceled", operation)
	}
	return err
}

// The caller obtains retained only from the successfully sealed diagnostic sink.
// Output publication is descriptive, but a failed write must remain visible
// alongside the original producer error instead of silently skipping retention.
func publishGitHubPackerDiagnosticRoute(primary error, output, retained string) error {
	if primary == nil || output == "" || retained == "" {
		return primary
	}
	return joinFactoryCleanupError(primary, appendGitHubSummary(output, "packer_diagnostics="+retained+"\n"), "publish ARC Packer diagnostic route")
}

// Classify the active pull before releasing its deadline, so cleanup cancellation
// cannot turn an ordinary registry failure into a retryable timeout.
func pullGitHubPublicImage(ctx context.Context, run func(context.Context) error) error {
	ctx, stop := context.WithTimeout(ctx, 15*time.Minute)
	defer stop()
	if err := run(ctx); err != nil {
		return classifyContext(ctx, e.Wrap(e.CodeFetchFailed, err, "prepare exact ARC toolchain"))
	}
	return nil
}

func completeGitHubDiskPublication(ctx context.Context, plan *Plan, expected LifecycleOutputPlan,
	owner *LocalRunResult, inspector *sbom.DiskInspectionRuntime, options GitHubStepOptions,
) error {
	if err := CompleteHostedDiskPublication(ctx, plan, expected, owner, inspector); err != nil {
		return err
	}
	if !PerOutputPublication(plan) {
		return nil
	}
	node := planNode(plan, "package/"+outputPublicationPackage(expected.Name))
	if githubDiskOutput(plan, node) != expected.Name {
		return e.New(e.CodePipelineDrift, "ARC output has no admitted offline publication preparation")
	}
	_, artifacts := planIndex(plan)
	return githubProgress(ctx, options, githubNodeLabel(plan, node), githubNodeDetails(plan, node), func(_ *githubStepProgress) error {
		return executePackage(ctx, plan, node, artifacts, options.Stdout)
	})
}
