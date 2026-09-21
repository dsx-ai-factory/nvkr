package factory

import (
	"path/filepath"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
)

// Artifact kinds produced by the signing lane. They are records ABOUT a
// publication, never the publication itself, so they are deliberately not
// packageable inputs.
const (
	ArtifactSigningRecord       = "signing-record"
	ArtifactProvenancePredicate = "provenance-predicate"
	ArtifactVerificationRecord  = "verification-record"
)

// addSigningDataflow appends the two post-publication nodes of one OCI route when
// the delivery configures signing. The edges are pure dataflow, not ordering
// hints: sign consumes the PUBLISHED reference (so it cannot run before publish)
// together with the routed layout (so it can re-derive the digest it is about to
// bind instead of trusting the reference file), and verify-publication consumes
// the reference and the signing record (so it cannot run before signing).
func addSigningDataflow(plan *Plan, runsOn string, route Route,
	packageInput, referenceID string, nodes map[string]Node, artifacts map[string]Artifact) error {
	signing := plan.Environment.Signing
	if signing == nil {
		return nil
	}
	if plan.Targets[route.Target].Kind != TargetOCI {
		return e.New(e.CodeEngineCapability,
			"delivery route %q cannot be signed: only OCI registry targets have a signing adapter", route.Name)
	}
	signInputs := []string{packageInput, referenceID}
	verifyInputs := []string{referenceID, "signature/" + route.Name}
	if signing.Mode == SigningModeCosign {
		// The pinned cosign executable is DATA this node consumes; declaring it as an
		// input is what makes a generated pipeline transport it to the signing job
		// instead of assuming an ambient binary on the runner.
		tool := "tool/" + plan.Definition.CosignTool
		if _, ok := artifacts[tool]; !ok {
			return e.New(e.CodeTrustPolicyMissing,
				"delivery signing cosignTool %q has no acquired tool artifact", plan.Definition.CosignTool)
		}
		signInputs = append(signInputs, tool)
		verifyInputs = append(verifyInputs, tool)
	}
	signID := NodeSign + "/" + route.Name
	nodes[signID] = Node{ID: signID, Kind: NodeSign, Target: route.Target,
		Inputs: uniqueSorted(signInputs), Network: ir.NetworkDefault, RunsOn: runsOn,
		Outputs: uniqueSorted([]string{"signature/" + route.Name, "predicate/" + route.Name})}
	artifacts["signature/"+route.Name] = Artifact{ID: "signature/" + route.Name, Name: route.Name,
		Kind: ArtifactSigningRecord, MediaType: "application/vnd.nkd.signing-record.v1+json",
		Producer: signID, Target: route.Target,
		Path: filepath.ToSlash(filepath.Join(".nkd", "factory", "signatures", route.Name+".json"))}
	artifacts["predicate/"+route.Name] = Artifact{ID: "predicate/" + route.Name, Name: route.Name,
		Kind: ArtifactProvenancePredicate, MediaType: "application/vnd.in-toto.provenance.v1+json",
		Producer: signID, Target: route.Target,
		Path: filepath.ToSlash(filepath.Join(".nkd", "factory", "predicates", route.Name+".json"))}
	verifyID := NodeVerifyPublication + "/" + route.Name
	nodes[verifyID] = Node{ID: verifyID, Kind: NodeVerifyPublication, Target: route.Target,
		Inputs: uniqueSorted(verifyInputs), Network: ir.NetworkDefault, RunsOn: runsOn,
		Outputs: []string{"verification/" + route.Name}}
	artifacts["verification/"+route.Name] = Artifact{ID: "verification/" + route.Name, Name: route.Name,
		Kind: ArtifactVerificationRecord, MediaType: "application/vnd.nkd.verification-record.v1+json",
		Producer: verifyID, Target: route.Target,
		Path: filepath.ToSlash(filepath.Join(".nkd", "factory", "verifications", route.Name+".json"))}
	return nil
}

// validateSigningGraph binds the emitted signing lane to the delivery policy that
// authorizes it: a plan cannot carry a sign or verify-publication node without a
// signing profile, cannot sign a non-OCI target, and cannot publish to a signing
// delivery without also signing that route. Node presence is authority-bearing
// here — an executor dispatches on it — so it is checked at the plan boundary
// rather than trusted from whoever produced the plan file.
func (plan Plan) validateSigningGraph() error {
	signed := map[string]bool{}
	published := map[string]bool{}
	for _, node := range plan.Graph.Nodes {
		switch node.Kind {
		case NodePublish:
			published[node.Target] = true
			continue
		case NodeSign:
			signed[node.Target] = true
		case NodeVerifyPublication, NodeAttestEvidence:
		default:
			continue
		}
		if plan.Environment.Signing == nil {
			return e.New(e.CodeTrustPolicyMissing,
				"factory plan node %q has no delivery signing profile authorizing it", node.ID)
		}
		if plan.Targets[node.Target].Kind != TargetOCI {
			return e.New(e.CodeEngineCapability,
				"factory plan node %q signs target %q, which is not an OCI registry", node.ID, node.Target)
		}
	}
	if plan.Environment.Signing == nil {
		return nil
	}
	for target := range published {
		if !signed[target] {
			return e.New(e.CodeTrustPolicyMissing,
				"factory plan publishes to signing target %q without a sign node", target)
		}
	}
	return nil
}
