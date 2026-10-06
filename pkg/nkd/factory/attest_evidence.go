package factory

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"sort"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/delivery"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/sign"
)

const NodeAttestEvidence = "attest-sboms"

// bindEvidenceAttestation lowers hosted production evidence into an explicit
// pre-publication stage. Private raw SBOMs never become published deliverables.
func bindEvidenceAttestation(plan *Plan) error {
	if plan == nil {
		return e.New(e.CodeUsage, "evidence attestation requires a plan")
	}
	if len(plan.LifecycleOutputs) == 0 || plan.Environment.Signing == nil || plan.Environment.Signing.Mode != SigningModeCosign {
		return nil
	}
	for _, route := range plan.Routes {
		if PerOutputPublication(plan) && route.Name == "release" {
			continue
		}
		index := -1
		for i, a := range plan.Graph.Artifacts {
			if a.ID == route.Input {
				index = i
				break
			}
		}
		if index < 0 {
			return e.New(e.CodePipelineDrift, "release route has no package")
		}
		final := plan.Graph.Artifacts[index]
		prepared := final
		prepared.ID = "prepared/" + route.Name
		prepared.Path = filepath.ToSlash(filepath.Join(".nkd", "factory", "prepared", route.Name))
		stage := Node{ID: NodeAttestEvidence + "/" + route.Name, Kind: NodeAttestEvidence, Target: route.Target,
			Inputs: uniqueSorted([]string{prepared.ID, "tool/" + plan.Definition.CosignTool}), Outputs: []string{final.ID}, Network: ir.NetworkDefault}
		found := false
		for i, node := range plan.Graph.Nodes {
			if node.ID != final.Producer {
				continue
			}
			if node.Kind != NodePackage || len(node.Outputs) != 1 {
				return e.New(e.CodePipelineDrift, "SBOM preparation requires one package producer")
			}
			plan.Graph.Nodes[i].Outputs = []string{prepared.ID}
			stage.RunsOn = node.RunsOn
			found = true
		}
		if !found {
			return e.New(e.CodePipelineDrift, "release has no preparation node")
		}
		final.Producer = stage.ID
		plan.Graph.Artifacts[index] = final
		plan.Graph.Artifacts = append(plan.Graph.Artifacts, prepared)
		plan.Graph.Nodes = append(plan.Graph.Nodes, stage)
	}
	sort.Slice(plan.Graph.Nodes, func(i, j int) bool { return plan.Graph.Nodes[i].ID < plan.Graph.Nodes[j].ID })
	sort.Slice(plan.Graph.Artifacts, func(i, j int) bool { return plan.Graph.Artifacts[i].ID < plan.Graph.Artifacts[j].ID })
	return nil
}

func executeAttestEvidence(ctx context.Context, plan *Plan, node Node, artifacts map[string]Artifact, newSigner signerFor, stdout io.Writer) error {
	if plan.Environment.Signing == nil || plan.Environment.Signing.Mode != SigningModeCosign {
		return e.New(e.CodeEngineCapability, "SBOM attestations require production signing")
	}
	signer, err := newSigner(ctx, plan, node, artifacts)
	if err != nil {
		return err
	}
	attester, ok := signer.(sign.SBOMAttester)
	if !ok {
		return e.New(e.CodeEngineCapability, "signer has no SBOM attestation capability")
	}
	input, err := signNodeInput(node, artifacts, PackageOCI)
	if err != nil {
		return err
	}
	output, err := signNodeOutput(node, artifacts, PackageOCI)
	if err != nil {
		return err
	}
	result, err := delivery.AttestEvidence(ctx, input.Path, output.Path, attester.AttestSBOM)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Created release with subject-bound SBOM attestations: %s\n", result.RootDigest)
	return nil
}

func (plan Plan) validateEvidenceAttestationGraph() error {
	if len(plan.LifecycleOutputs) == 0 || plan.Environment.Signing == nil || plan.Environment.Signing.Mode != SigningModeCosign {
		return nil
	}
	nodes, artifacts := planIndex(&plan)
	for _, route := range plan.Routes {
		if PerOutputPublication(&plan) && route.Name == "release" {
			continue
		}
		output, ok := artifacts[route.Input]
		if !ok {
			return e.New(e.CodePipelineDrift, "attested route has no output")
		}
		node := nodes[output.Producer]
		if node.Kind != NodeAttestEvidence || node.Target != route.Target || len(node.Inputs) != 2 || len(node.Outputs) != 1 || node.Outputs[0] != output.ID {
			return e.New(e.CodePipelineDrift, "production route must consume its SBOM-attested package")
		}
		prepared, err := signNodeInput(node, artifacts, PackageOCI)
		if err != nil {
			return err
		}
		if nodes[prepared.Producer].Kind != NodePackage || !containsString(node.Inputs, "tool/"+plan.Definition.CosignTool) {
			return e.New(e.CodePipelineDrift, "SBOM attestation must consume preparation and the pinned signer")
		}
	}
	return nil
}
