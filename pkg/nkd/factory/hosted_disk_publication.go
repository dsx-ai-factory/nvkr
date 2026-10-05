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
	for _, previous := range owner.Nodes {
		if previous.ID == output.Producer || previous.ID == evidence.Producer {
			return e.New(e.CodeImmutableViolation, "hosted disk completion was already recorded")
		}
	}
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
	owner.Nodes = append(owner.Nodes,
		LocalNodeResult{ID: evidence.Producer, Kind: NodeSBOM, Status: "ok"},
		LocalNodeResult{ID: output.Producer, Kind: NodePackage, Status: "ok"})
	return nil
}
