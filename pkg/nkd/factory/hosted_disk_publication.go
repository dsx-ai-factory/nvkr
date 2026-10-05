package factory

import (
	"context"
	"slices"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/sbom"
)

// CompleteHostedDiskPublication constructs the planned disk wrapper and every
// component evidence slot while the grouped executor retains the original live
// owner. It performs no signing or network publication, and its node outcomes
// remain descriptions until authenticated by the existing hosted finalizer.
func CompleteHostedDiskPublication(ctx context.Context, plan *Plan, expected LifecycleOutputPlan,
	owner *LocalRunResult, runtime *sbom.DiskInspectionRuntime,
) error {
	if ctx == nil {
		return e.New(e.CodeUsage, "hosted disk completion requires a context")
	}
	if plan == nil || owner == nil || runtime == nil {
		return e.New(e.CodeProvenanceMissing, "hosted disk completion requires its original Plan, live owner and admitted inspector")
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "hosted disk completion canceled")
	}
	if plan.Qualification != TierPipelineAttested || plan.Environment.Publication == nil {
		return e.New(e.CodeEngineCapability, "hosted disk completion requires the production pipeline-attested route")
	}
	if err := plan.Validate(); err != nil {
		return err
	}
	nodes, artifacts := planIndex(plan)
	output := artifacts["package/"+expected.Name]
	if _, _, err := reconciledDiskPackageBinding(plan, expected, output); err != nil {
		return err
	}
	evidence := artifacts["sbom/"+expected.Name]
	evidenceNode, found := nodes[evidence.Producer]
	if !found || evidence.Kind != "sbom-set" || evidenceNode.Kind != NodeSBOM ||
		evidence.BuildTarget != expected.Name || !slices.Contains(evidenceNode.Outputs, evidence.ID) {
		return e.New(e.CodePipelineDrift, "hosted disk completion lacks its exact component evidence operation")
	}
	release, err := owner.reserveHostedDiskPublication(ctx, output.Producer, evidence.Producer)
	if err != nil {
		return err
	}
	defer release()
	receipt, err := owner.ReconcileDiskEvidence(ctx, plan, expected, runtime)
	if err != nil {
		return err
	}
	if _, _, err := owner.PackageReconciledDisk(ctx, plan, expected, runtime, receipt, output); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "hosted disk completion canceled before recording outcomes")
	}
	owner.diskMu.Lock()
	defer owner.diskMu.Unlock()
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "hosted disk completion canceled before recording outcomes")
	}
	if owner.diskClosed || owner.diskClosing {
		return e.New(e.CodeGateUnmet, "hosted disk completion requires its original open Run")
	}
	owner.Nodes = append(owner.Nodes,
		LocalNodeResult{ID: evidence.Producer, Kind: NodeSBOM, Status: "ok"},
		LocalNodeResult{ID: output.Producer, Kind: NodePackage, Status: "ok"})
	return nil
}

// Reserve the exact completion while inspection temporarily releases the Run's
// disk permit between operations. A second caller must not race the same package
// destination; distinct outputs can reserve their own slots independently.
func (owner *LocalRunResult) reserveHostedDiskPublication(ctx context.Context, output, evidence string) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "hosted disk completion canceled before reservation")
	}
	owner.diskMu.Lock()
	defer owner.diskMu.Unlock()
	if owner.diskClosed || owner.diskClosing {
		return nil, e.New(e.CodeGateUnmet, "hosted disk completion requires its original open Run")
	}
	for _, previous := range owner.Nodes {
		if previous.ID == output || previous.ID == evidence {
			return nil, e.New(e.CodeImmutableViolation, "hosted disk completion was already recorded")
		}
	}
	if owner.diskPublicationPending[output] {
		return nil, e.New(e.CodeImmutableViolation, "hosted disk completion is already in progress")
	}
	if owner.diskPublicationPending == nil {
		owner.diskPublicationPending = make(map[string]bool)
	}
	owner.diskPublicationPending[output] = true
	return func() {
		owner.diskMu.Lock()
		delete(owner.diskPublicationPending, output)
		owner.diskMu.Unlock()
	}, nil
}
