package factory

import (
	"context"
	"sort"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/material"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/pipeline"
)

// diskMaterialCoverage describes the inventory scope, not an evidence lane.
type diskMaterialCoverage string

// diskMaterialCoverageStagedOutputs is deliberately narrower than complete
// acquisition coverage. Neither private source vendoring nor downloads made by
// a network-enabled producer or its guest are inventoried by these observations.
const diskMaterialCoverageStagedOutputs diskMaterialCoverage = "staged-producer-outputs"

// diskMaterialProduction describes how the observed output was supplied, without
// exposing the IR's execution-node vocabulary to the trace's consumers.
type diskMaterialProduction string

const (
	diskMaterialProductionBuilt    diskMaterialProduction = "built"
	diskMaterialProductionAcquired diskMaterialProduction = "acquired"
)

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

// diskMaterialMember describes one observed producer output, including the disk
// root. Kind is its output kind; Production distinguishes build from acquisition.
// A member's archive digest is not an installed-file or package identity.
type diskMaterialMember struct {
	Producer   string
	Component  string
	Kind       string
	Production diskMaterialProduction
	Digest     string
	Size       int64
}

// diskMaterialEdge describes one exact consumed input. ManifestDigest identifies
// the serialized manifest actually supplied to Consumer, including any explicit
// producer-owned projection. Producer names are scoped to the Subject's output.
type diskMaterialEdge struct {
	Consumer       string
	Binding        string
	Producer       string
	ManifestDigest string
}

// diskMaterialTraceDescription is a non-authoritative view for inspection and
// diagnostics. It has no wire schema or receipt constructor. Coverage never
// asserts the complete fetched/transitive software closure or disk composition.
type diskMaterialTraceDescription struct {
	OutputName string
	Subject    DiskPayloadObservation
	Members    []diskMaterialMember
	Edges      []diskMaterialEdge
	Coverage   diskMaterialCoverage
	// Only build invocations are listed. Source-fetch vendoring and guest
	// acquisitions are outside this staged-output trace even when this is empty.
	UntracedNetworkBuilds []string
}

// diskMaterialTrace is a factory-issued, process-local observation of a live
// disk Run's staged producer graph. Public descriptions cannot reconstruct it.
// It is not a signed provenance receipt, full-disk SBOM, or publication gate.
type diskMaterialTrace struct {
	issued      bool
	description diskMaterialTraceDescription
}

// ValidateSubject rejects unissued observations and substitution to another
// named output, Plan, logical Build, or delivered QCOW2. Two outputs may share
// Build and payload identities without sharing their invocation observations.
// It checks immutable subject identity, not the issuing Run's continued
// lifetime or a unique attempt. It remains valid after that owner closes and
// must never substitute for a still-held disk capability or execution receipt.
func (trace *diskMaterialTrace) ValidateSubject(outputName, planDigest, buildDigest, diskDigest string) error {
	if trace == nil || !trace.issued || trace.description.OutputName != outputName || trace.description.Subject.PlanDigest != planDigest ||
		trace.description.Subject.BuildDigest != buildDigest || trace.description.Subject.Digest != diskDigest {
		return e.New(e.CodeVerifyFailed, "disk material trace does not bind the exact output, Plan, Build and payload")
	}
	return nil
}

// Description returns an independent view, not authority. Mutating it cannot
// alter this observation or satisfy a later subject check.
func (trace *diskMaterialTrace) Description() diskMaterialTraceDescription {
	if trace == nil || !trace.issued {
		return diskMaterialTraceDescription{}
	}
	description := trace.description
	description.Subject.Components = append([]string(nil), description.Subject.Components...)
	description.Members = append([]diskMaterialMember(nil), description.Members...)
	description.Edges = append([]diskMaterialEdge(nil), description.Edges...)
	description.UntracedNetworkBuilds = append([]string(nil), description.UntracedNetworkBuilds...)
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
	if len(instruction.Materials) == 0 {
		// Packer still receives an empty manifest, but producer results record no
		// manifest identity when the Plan declares no inputs (pipeline contract).
		if receipt.manifestDigest != "" || (materials != nil && len(materials.observations) != 0) {
			return e.New(e.CodeVerifyFailed, "producer %q recorded undeclared material inputs", node.ID)
		}
	} else if materials != nil {
		if materials.manifest == nil || receipt.manifestDigest != materials.manifest.Digest() ||
			len(materials.observations) != len(instruction.Materials) {
			return e.New(e.CodeVerifyFailed, "producer %q did not record its actual material handoff", node.ID)
		}
		if err := materials.manifest.AssertUnchangedContext(ctx); err != nil {
			return e.Wrap(e.CodeVerifyFailed, err, "rechecking consumed material manifest")
		}
		receipt.materials = append([]material.Observation(nil), materials.observations...)
	} else {
		return e.New(e.CodeVerifyFailed, "producer %q has no live material handoff", node.ID)
	}
	if instruction.BuilderType != ir.BuilderVMImage {
		// This independent output/result recheck is intentionally additional I/O,
		// including for large ISO inputs. Consumer-held hashes cannot prove the
		// successful producer's own output/result pairing at recording time.
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
	if owner.diskClosed || owner.diskClosing {
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

// observeDiskMaterials composes only retained live invocation observations. It
// independently streams the held QCOW2 and follows the Plan's exact material
// graph, refusing any missing producer or digest disagreement. Reopening result
// JSON cannot create a missing consumption observation. Guest/network and private
// fetched-software inventories remain explicit gaps even when this graph matches.
func (owner *LocalRunResult) observeDiskMaterials(ctx context.Context, plan *Plan,
	expected LifecycleOutputPlan,
) (*diskMaterialTrace, error) {
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
	return owner.observeDiskMaterialsWhileHeld(ctx, plan, expected)
}

// observeDiskMaterialsWhileHeld requires the caller to own the disk lifetime
// permit. Inspection uses it to keep tracing and scanning in one live handoff.
func (owner *LocalRunResult) observeDiskMaterialsWhileHeld(ctx context.Context, plan *Plan,
	expected LifecycleOutputPlan,
) (*diskMaterialTrace, error) {
	owner.diskMu.Lock()
	if owner.diskClosed || owner.diskClosing {
		owner.diskMu.Unlock()
		return nil, e.New(e.CodeGateUnmet, "disk material tracing requires an open local execution record")
	}
	if owner.SchemaVersion != LocalRunSchemaVersion || owner.Qualification != TierLocalExecution {
		owner.diskMu.Unlock()
		return nil, e.New(e.CodeVerifyFailed, "disk material tracing requires the local execution schema and tier")
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
	description := diskMaterialTraceDescription{OutputName: expected.Name, Subject: subject, Coverage: diskMaterialCoverageStagedOutputs}
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
		var production diskMaterialProduction
		switch node.Kind {
		case ir.KindBuild:
			production = diskMaterialProductionBuilt
		case ir.KindPull:
			production = diskMaterialProductionAcquired
		default:
			// Exhaustiveness guard: the producer map currently admits only build and pull nodes.
			return e.New(e.CodeVerifyFailed, "disk material producer %q has unsupported production kind %q", id, node.Kind)
		}
		seen[id] = true
		description.Members = append(description.Members, diskMaterialMember{Producer: id, Component: node.Component,
			Kind: node.OutputType, Production: production, Digest: receipt.digest, Size: receipt.size})
		if node.Kind == ir.KindBuild && node.Network != ir.NetworkNone {
			description.UntracedNetworkBuilds = append(description.UntracedNetworkBuilds, id)
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
			description.Edges = append(description.Edges, diskMaterialEdge{Consumer: id, Binding: binding.Binding,
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
	components := make([]string, 0, len(description.Members))
	for _, member := range description.Members {
		components = append(components, member.Component)
	}
	sort.Strings(components)
	if !equalFactoryStrings(components, expected.Components) {
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
	sort.Strings(description.UntracedNetworkBuilds)
	return &diskMaterialTrace{issued: true, description: description}, nil
}
