package factory

import (
	"context"
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
		if returnErr != nil && ctx.Err() != nil {
			returnErr = e.Wrap(e.CodeTimeout, returnErr, "ARC disk execution exhausted its bounded deadline")
		}
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
		pullCtx, stop := context.WithTimeout(ctx, 15*time.Minute)
		command := exec.CommandContext(pullCtx, plan.Environment.RuntimeBinary(), "pull", "--platform", root.Platform, image)
		command.Stdout, command.Stderr = options.Stdout, options.Stderr
		err := ownedcmd.Run(pullCtx, command)
		stop()
		if err != nil {
			return e.Wrap(e.CodeFetchFailed, err, "prepare exact ARC toolchain")
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
	defer func() {
		closed := diagnostics.Close()
		returnErr = joinFactoryCleanupError(returnErr, closed, "close ARC diagnostics")
		if returnErr != nil && closed == nil {
			// Only the sealed sink can issue this fixed, bounded route. Early
			// admission failure must not upload a preexisting workspace path.
			if retained := diagnostics.RetainedPath(); retained != "" {
				if output := os.Getenv("GITHUB_OUTPUT"); output != "" {
					_ = appendGitHubSummary(output, "packer_diagnostics="+retained+"\n")
				}
			}
		}
	}()
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
		err := githubProgress(nodeCtx, options, githubNodeLabel(plan, node), githubNodeDetails(plan, node), func(_ *githubStepProgress) error {
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
		})
		if err != nil && nodeCtx.Err() != nil {
			err = e.Wrap(e.CodeTimeout, err, "ARC disk producer %q exhausted its bounded deadline", node.ID)
		}
		stop()
		if err != nil {
			return err
		}
		owner.Nodes = append(owner.Nodes, LocalNodeResult{ID: node.ID, Kind: node.Kind, Status: "ok"})
	}
	for _, expected := range plan.LifecycleOutputs {
		if expected.Name == output {
			return CompleteHostedDiskPublication(ctx, plan, expected, owner, inspector)
		}
	}
	return e.New(e.CodePipelineDrift, "ARC output has no lifecycle binding")
}
