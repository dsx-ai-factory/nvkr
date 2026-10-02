package factory

import (
	"context"
	"sort"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/material"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/pipeline"
)

// DiskMaterialCoverageStagedOutputs is deliberately narrower than complete
// acquisition coverage. Neither private source vendoring nor downloads made by
// a network-enabled producer or its guest are inventoried by these receipts.
const DiskMaterialCoverageStagedOutputs = "staged-producer-outputs"

// producerConsumption is retained only by a live local Run after an actual
// producer invocation succeeds. The material observations come from the held
// bytes handed to that invocation, never from a decoded producer-result file.
// It authenticates this local handoff, not installed content or qualification.
type producerConsumption struct {
	planDigest     string
	nodeID         string
	digest         string
	size           int64
	manifestDigest string
	materials      []material.Observation
}

// DiskMaterialMember describes one observed producer output, including the disk
// root. Kind is its output kind; Production distinguishes build from acquisition.
// A member's archive digest is not an installed-file or package identity.
type DiskMaterialMember struct {
	Producer   string
	Component  string
	Kind       string
	Production string
	Digest     string
	Size       int64
}

// DiskMaterialEdge describes one exact consumed input. ManifestDigest identifies
// the serialized manifest actually supplied to Consumer, including any explicit
// producer-owned projection. Producer names are scoped to the Subject's output.
type DiskMaterialEdge struct {
	Consumer       string
	Binding        string
	Producer       string
	ManifestDigest string
}

// DiskMaterialTraceDescription is a non-authoritative view for inspection and
// diagnostics. It has no wire schema or receipt constructor. Coverage never
// asserts the complete fetched/transitive software closure or disk composition.
type DiskMaterialTraceDescription struct {
	OutputName               string
	Subject                  DiskPayloadObservation
	Members                  []DiskMaterialMember
	Edges                    []DiskMaterialEdge
	Coverage                 string
	UntracedNetworkProducers []string
}

// DiskMaterialTrace is a factory-issued, process-local observation of a live
// disk Run's staged producer graph. Public descriptions cannot reconstruct it.
// It is not a signed provenance receipt, full-disk SBOM, or publication gate.
type DiskMaterialTrace struct {
	issued      bool
	description DiskMaterialTraceDescription
}

// ValidateSubject rejects unissued observations and substitution to another
// named output, Plan, logical Build, or delivered QCOW2. Two outputs may share
// Build and payload identities without sharing their invocation observations.
// It grants no completeness claim.
func (trace *DiskMaterialTrace) ValidateSubject(outputName, planDigest, buildDigest, diskDigest string) error {
	if trace == nil || !trace.issued || trace.description.OutputName != outputName || trace.description.Subject.PlanDigest != planDigest ||
		trace.description.Subject.BuildDigest != buildDigest || trace.description.Subject.Digest != diskDigest {
		return e.New(e.CodeVerifyFailed, "disk material trace does not bind the exact output, Plan, Build and payload")
	}
	return nil
}

// Description returns an independent view, not authority. Mutating it cannot
// alter this observation or satisfy a later subject check.
func (trace *DiskMaterialTrace) Description() DiskMaterialTraceDescription {
	if trace == nil || !trace.issued {
		return DiskMaterialTraceDescription{}
	}
	description := trace.description
	description.Subject.Components = append([]string(nil), description.Subject.Components...)
	description.Members = append([]DiskMaterialMember(nil), description.Members...)
	description.Edges = append([]DiskMaterialEdge(nil), description.Edges...)
	description.UntracedNetworkProducers = append([]string(nil), description.UntracedNetworkProducers...)
	return description
}

func (owner *LocalRunResult) recordProducerConsumption(ctx context.Context, plan *Plan, node Node,
	instruction ir.Node, result *pipeline.ArtifactResult, materials *observedProducerMaterials,
) error {
	if ctx == nil {
		return e.New(e.CodeUsage, "recording producer consumption requires a context")
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "recording producer consumption")
	}
	if owner == nil || plan == nil || result == nil {
		return e.New(e.CodeVerifyFailed, "producer consumption requires a successful live result")
	}
	planDigest, err := plan.IdentityDigest()
	if err != nil {
		return err
	}
	if planDigest != owner.PlanDigest || result.Component != instruction.Component || result.OutputType != instruction.OutputType {
		return e.New(e.CodeVerifyFailed, "producer consumption changes its Plan or output")
	}
	receipt := producerConsumption{planDigest: planDigest, nodeID: node.ID,
		digest: result.SHA256, size: result.Size, manifestDigest: result.MaterialManifestDigest}
	if materials != nil {
		if materials.manifest == nil || receipt.manifestDigest != materials.manifest.Digest() ||
			len(materials.observations) != len(instruction.Materials) {
			return e.New(e.CodeVerifyFailed, "producer %q did not record its actual material handoff", node.ID)
		}
		if err := materials.manifest.AssertUnchangedContext(ctx); err != nil {
			return e.Wrap(e.CodeVerifyFailed, err, "rechecking consumed material manifest")
		}
		receipt.materials = append([]material.Observation(nil), materials.observations...)
	} else if len(instruction.Materials) != 0 || receipt.manifestDigest != "" {
		return e.New(e.CodeVerifyFailed, "producer %q has no live material handoff", node.ID)
	}
	if instruction.BuilderType != ir.BuilderVMImage {
		_, artifacts := planIndex(plan)
		payload := artifacts["artifact/"+node.BuildTarget+"/"+instruction.Component]
		record := artifacts["result/"+node.BuildTarget+"/"+instruction.Component]
		observed, err := pipeline.AuthenticateResultForConsumption(ctx, instruction, payload.Path, record.Path)
		if err != nil {
			return err
		}
		if observed.SHA256 != receipt.digest || observed.Size != receipt.size {
			return e.New(e.CodeVerifyFailed, "producer %q changed its successful output", node.ID)
		}
	}
	owner.diskMu.Lock()
	defer owner.diskMu.Unlock()
	if owner.diskClosed {
		return e.New(e.CodeGateUnmet, "local disk execution result is closed")
	}
	if _, duplicate := owner.materialConsumptions[node.ID]; duplicate {
		return e.New(e.CodeVerifyFailed, "producer %q returned duplicate consumption observations", node.ID)
	}
	if owner.materialConsumptions == nil {
		owner.materialConsumptions = make(map[string]producerConsumption)
	}
	owner.materialConsumptions[node.ID] = receipt
	return nil
}

// ObserveDiskMaterials composes only retained live invocation observations. It
// independently streams the held QCOW2 and follows the Plan's exact material
// graph, refusing any missing producer or digest disagreement. Reopening result
// JSON cannot create a missing consumption observation. Guest/network and private
// fetched-software inventories remain explicit gaps even when this graph matches.
func (owner *LocalRunResult) ObserveDiskMaterials(ctx context.Context, plan *Plan,
	expected LifecycleOutputPlan,
) (*DiskMaterialTrace, error) {
	if ctx == nil {
		return nil, e.New(e.CodeUsage, "disk material tracing requires a context")
	}
	if owner == nil {
		return nil, e.New(e.CodeReleaseIncomplete, "disk material tracing requires a live local Run")
	}
	release, err := owner.acquireDiskPermit(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	owner.diskMu.Lock()
	if owner.diskClosed || owner.SchemaVersion != LocalRunSchemaVersion || owner.Qualification != TierLocalExecution {
		owner.diskMu.Unlock()
		return nil, e.New(e.CodeGateUnmet, "disk material tracing requires an open local execution record")
	}
	execution := diskExecutionState{PlanDigest: owner.PlanDigest, Nodes: append([]LocalNodeResult(nil), owner.Nodes...)}
	completions := append([]diskProducerCompletion(nil), owner.diskCompletions...)
	consumptions := make(map[string]producerConsumption, len(owner.materialConsumptions))
	for id, receipt := range owner.materialConsumptions {
		consumptions[id] = receipt
	}
	owner.diskMu.Unlock()
	subject, result, err := inspectDiskLifecyclePayload(ctx, plan, expected, execution, completions, nil, true)
	if err != nil {
		return nil, err
	}
	var build BuildPlan
	for _, candidate := range plan.Builds {
		if candidate.Name == expected.Name {
			build = candidate
		}
	}
	producers := make(map[string]ir.Node)
	for _, node := range build.Graph.Nodes {
		if node.Kind == ir.KindBuild || node.Kind == ir.KindPull {
			producers[node.ID] = node
		}
	}
	description := DiskMaterialTraceDescription{OutputName: expected.Name, Subject: subject, Coverage: DiskMaterialCoverageStagedOutputs}
	seen := make(map[string]bool)
	var visit func(string, string, int64) error
	visit = func(id, digest string, size int64) error {
		if err := ctx.Err(); err != nil {
			return e.Wrap(e.CodeTimeout, err, "tracing disk materials")
		}
		node, ok := producers[id]
		receipt, retained := consumptions[expected.Name+"/"+id]
		if !ok || !retained || receipt.nodeID != expected.Name+"/"+id || receipt.planDigest != subject.PlanDigest || receipt.digest != digest ||
			(size >= 0 && receipt.size != size) || receipt.size < 0 {
			return e.New(e.CodeVerifyFailed, "disk material producer %q has no exact retained invocation", id)
		}
		if seen[id] {
			return nil
		}
		seen[id] = true
		description.Members = append(description.Members, DiskMaterialMember{Producer: id, Component: node.Component,
			Kind: node.OutputType, Production: node.Kind, Digest: receipt.digest, Size: receipt.size})
		if node.Kind == ir.KindBuild && node.Network != ir.NetworkNone {
			description.UntracedNetworkProducers = append(description.UntracedNetworkProducers, id)
		}
		if len(receipt.materials) != len(node.Materials) {
			return e.New(e.CodeVerifyFailed, "producer %q consumption set differs from its Plan", id)
		}
		observations := make(map[string]material.Observation, len(receipt.materials))
		for _, observation := range receipt.materials {
			if _, duplicate := observations[observation.Binding]; duplicate {
				return e.New(e.CodeVerifyFailed, "producer %q repeats a consumed binding", id)
			}
			observations[observation.Binding] = observation
		}
		for _, binding := range node.Materials {
			observation, exists := observations[binding.Binding]
			producer := producers[binding.Producer]
			if !exists || observation.Component != binding.Component || observation.Kind != producer.OutputType ||
				"linux/"+observation.Architecture != node.Platform {
				return e.New(e.CodeVerifyFailed, "producer %q consumed a substituted material %q", id, binding.Binding)
			}
			description.Edges = append(description.Edges, DiskMaterialEdge{Consumer: id, Binding: binding.Binding,
				Producer: binding.Producer, ManifestDigest: receipt.manifestDigest})
			if err := visit(binding.Producer, observation.Digest, -1); err != nil {
				return err
			}
		}
		return nil
	}
	root, retained := consumptions[expected.Name+"/"+subject.Producer]
	if !retained || root.manifestDigest != result.MaterialManifestDigest {
		return nil, e.New(e.CodeVerifyFailed, "disk root has no exact consumed material manifest")
	}
	if err := visit(subject.Producer, subject.Digest, subject.Size); err != nil {
		return nil, err
	}
	if len(seen) != len(expected.Components) {
		return nil, e.New(e.CodeVerifyFailed, "disk material trace omits a selected producer")
	}
	sort.Slice(description.Members, func(i, j int) bool { return description.Members[i].Producer < description.Members[j].Producer })
	sort.Slice(description.Edges, func(i, j int) bool {
		left, right := description.Edges[i], description.Edges[j]
		if left.Consumer != right.Consumer {
			return left.Consumer < right.Consumer
		}
		return left.Binding < right.Binding
	})
	sort.Strings(description.UntracedNetworkProducers)
	return &DiskMaterialTrace{issued: true, description: description}, nil
}
