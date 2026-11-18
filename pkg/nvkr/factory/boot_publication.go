// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"math"
	"path/filepath"
	"reflect"
	"slices"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

const BootPlanSchemaVersion = "nvkr.dev/run-plan/v7"
const ArtifactBootPreparation = "boot-preparation"

// BootPublicationPlan records independently admitted phase allowances. Exact
// selection and destinations remain in the same Plan's Environment. These are
// descriptions, never counters, observed copies or restorable receipts.
type BootPublicationPlan struct {
	Destinations []BootDestinationPlan `json:"destinations"`
}

type BootDestinationPlan struct {
	Name                 string `json:"name"`
	ControlObjects       int    `json:"controlObjects"`
	PreparationReadBytes int64  `json:"preparationReadBytes"`
	FinalReadBytes       int64  `json:"finalReadBytes"`
}

func bootSelectedNames(selection definition.BootSelection) []string {
	names := make([]string, 0, len(selection.Disks)+len(selection.Imagers))
	for _, disk := range selection.Disks {
		names = append(names, disk.Output)
	}
	for _, imager := range selection.Imagers {
		names = append(names, imager.Output)
	}
	slices.Sort(names)
	return names
}

func deriveBootPublicationPlan(plan *Plan) (*BootPublicationPlan, error) {
	if plan.Environment.Publication == nil || plan.Environment.Publication.BootSelection == nil {
		return nil, nil
	}
	selection := plan.Environment.Publication.BootSelection
	if err := selection.ValidateOutputs(plan.Definition.Outputs); err != nil {
		return nil, err
	}
	names := bootSelectedNames(*selection)
	// Original roots each have index/manifest/config and two proof bundles.
	// Each selected output adds manifest/config and descriptor proofs; reserve
	// both aggregate and local copies, without assuming byte sharing. Native
	// disk/executable outputs have one build-output SBOM per selected component,
	// each with three control blobs in each of its two roots.
	controls := 5*(len(names)+1) + 6*len(names)
	for _, output := range plan.LifecycleOutputs {
		if slices.Contains(names, output.Name) {
			controls += 6 * len(output.Components)
		}
	}
	result := &BootPublicationPlan{}
	for _, destination := range plan.Environment.Publication.ObjectStores {
		if destination.Mode != definition.ObjectStoreBootMode {
			continue
		}
		minimum, err := delivery.BootPhaseMinimumReadBytes(*selection, destination)
		if err != nil {
			return nil, err
		}
		if minimum > math.MaxInt64/2 {
			return nil, e.New(e.CodeEngineCapability, "boot phase cannot reserve bounded retry reads")
		}
		entry := BootDestinationPlan{Name: destination.Name, ControlObjects: controls, PreparationReadBytes: 2 * minimum, FinalReadBytes: 2 * minimum}
		request := bootPhaseRequest(*selection, destination, entry, false)
		if err := request.Validate(); err != nil {
			return nil, err
		}
		result.Destinations = append(result.Destinations, entry)
	}
	if len(result.Destinations) == 0 {
		return nil, e.New(e.CodeEngineCapability, "boot Plan requires selective destinations")
	}
	return result, nil
}

func bootPhaseRequest(selection definition.BootSelection, destination definition.ObjectStoreDestination, bounds BootDestinationPlan, final bool) delivery.BootPhaseRequest {
	limits := destination.EffectiveTransfer()
	reads := bounds.PreparationReadBytes
	if final {
		reads = bounds.FinalReadBytes
	}
	s3, serving := *destination.S3, *destination.Serving
	destination.S3, destination.Serving = &s3, &serving
	return delivery.BootPhaseRequest{Selection: selection.Clone(), Destination: destination, ReadBytes: reads,
		Limits: delivery.BootTransferLimits{ControlObjects: bounds.ControlObjects,
			Storage: delivery.MirrorLimits{MaxObjectBytes: int64(limits.MaxObjectBytes), MaxTotalBytes: int64(limits.MaxTotalBytes), MaxObjects: int(limits.MaxObjects)}}}
}

func (plan Plan) validateBootPublicationPlan() error {
	want, err := deriveBootPublicationPlan(&plan)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(want, plan.BootPublication) || (want != nil) != (plan.SchemaVersion == BootPlanSchemaVersion) {
		return e.New(e.CodePipelineDrift, "boot phase bounds differ from the complete compiled selection")
	}
	return nil
}

func addBootPublicationDataflow(plan *Plan, destination definition.ObjectStoreDestination, runsOn string, nodes map[string]Node, artifacts map[string]Artifact) error {
	selection := plan.Environment.Publication.BootSelection
	if selection == nil {
		return e.New(e.CodePipelineDrift, "boot destination has no complete selection")
	}
	target := "object-" + destination.Name
	inputs := []string{"tool/" + plan.Definition.CosignTool}
	for _, name := range bootSelectedNames(*selection) {
		route := outputPublicationRoute(name)
		for _, prefix := range []string{"metadata/", "publication/", "signature/", "predicate/", "verification/"} {
			input := prefix + route
			if artifacts[input].ID == "" {
				return e.New(e.CodePipelineDrift, "boot preparation lacks authenticated output input %q", input)
			}
			inputs = append(inputs, input)
		}
	}
	prepare, prepared := "prepare-boot/"+destination.Name, "boot-preparation/"+destination.Name
	nodes[prepare] = Node{ID: prepare, Kind: NodePublish, Target: target, Inputs: uniqueSorted(inputs), Outputs: []string{prepared}, RunsOn: runsOn, Network: ir.NetworkDefault}
	artifacts[prepared] = Artifact{ID: prepared, Name: destination.Name, Kind: ArtifactBootPreparation, Producer: prepare, Target: target,
		Path: filepath.ToSlash(filepath.Join(".nvkr", "factory", "boot-preparations", destination.Name+".json"))}
	assembly := nodes["package/release"]
	if assembly.Kind != NodeAssembleRelease {
		return e.New(e.CodePipelineDrift, "boot copy requires reference-only aggregate assembly")
	}
	assembly.Inputs = uniqueSorted(append(assembly.Inputs, prepared))
	nodes[assembly.ID] = assembly
	final, completion := "publish-boot/"+destination.Name, "object-publication/"+destination.Name
	inputs = []string{prepared, "publication/release", "signature/release", "predicate/release", "verification/release", "tool/" + plan.Definition.CosignTool}
	for _, input := range inputs {
		if artifacts[input].ID == "" {
			return e.New(e.CodePipelineDrift, "final boot publication lacks authenticated aggregate input %q", input)
		}
	}
	nodes[final] = Node{ID: final, Kind: NodePublish, Target: target, Inputs: uniqueSorted(inputs), Outputs: []string{completion}, RunsOn: runsOn, Network: ir.NetworkDefault}
	artifacts[completion] = Artifact{ID: completion, Name: destination.Name, Kind: ArtifactObjectPublication, Producer: final, Target: target,
		Path: filepath.ToSlash(filepath.Join(".nvkr", "factory", "object-publications", destination.Name+".json"))}
	return nil
}

// BootPhaseForNode rederives the whole graph before returning detached limits
// for one exact preparation or final publication operation.
func BootPhaseForNode(plan *Plan, nodeID string) (delivery.BootPhaseRequest, bool, error) {
	if plan == nil {
		return delivery.BootPhaseRequest{}, false, e.New(e.CodeUsage, "boot phase requires a Plan")
	}
	if err := plan.Validate(); err != nil {
		return delivery.BootPhaseRequest{}, false, err
	}
	if plan.BootPublication != nil {
		for _, bounds := range plan.BootPublication.Destinations {
			final := nodeID == "publish-boot/"+bounds.Name
			if !final && nodeID != "prepare-boot/"+bounds.Name {
				continue
			}
			for _, destination := range plan.Environment.Publication.ObjectStores {
				if destination.Name == bounds.Name {
					return bootPhaseRequest(*plan.Environment.Publication.BootSelection, destination, bounds, final), final, nil
				}
			}
		}
	}
	return delivery.BootPhaseRequest{}, false, e.New(e.CodePipelineDrift, "node is not an exact compiled boot phase")
}

// Keep runtime admission closed until both lifecycle phases and inventory-only
// completion are wired. Static compilation does not enable a publication lane.
func admitBootPublicationExecution(plan *Plan) error {
	if plan.BootPublication != nil {
		return e.New(e.CodeEngineCapability, "selective boot execution requires the complete lifecycle publisher")
	}
	return nil
}
