// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"slices"
	"sync"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sbom"
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
	if plan == nil || owner == nil || (runtime == nil && !diskBuildProvenance(plan)) {
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
	// The root bypasses executeProducer to retain its opaque held capability.
	// Persist its descriptive record from that same live owner for downstream
	// exact-subject validation; caller-supplied JSON cannot create this result.
	observation, producerResult, err := owner.inspectDiskLifecycleMetadata(ctx, plan, expected)
	if err != nil {
		return err
	}
	record := artifacts["result/"+expected.Name+"/"+observation.Component]
	if record.Kind != "producer-result" || record.Path == "" {
		return e.New(e.CodePipelineDrift, "disk publication lacks its exact producer record slot")
	}
	raw, err := canonical.Marshal(producerResult)
	if err != nil {
		return err
	}
	if err := writeFactoryFile(ctx, record.Path, raw, 0o644); err != nil {
		return err
	}
	if diskBuildProvenance(plan) {
		if err := owner.packageDiskBuildProvenance(ctx, plan, expected, output); err != nil {
			return err
		}
	} else {
		receipt, err := owner.ReconcileDiskEvidence(ctx, plan, expected, runtime)
		if err != nil {
			return err
		}
		if _, _, err := owner.PackageReconciledDisk(ctx, plan, expected, runtime, receipt, output); err != nil {
			return err
		}
	}
	// The package/evidence no-replace commit succeeded. Commit wins over
	// subsequent cancellation; Close waits for this reservation to finish.
	owner.recordCommittedHostedDiskPublication(output.Producer, evidence.Producer)
	return nil
}

func (owner *LocalRunResult) recordCommittedHostedDiskPublication(output, evidence string) {
	owner.diskMu.Lock()
	defer owner.diskMu.Unlock()
	owner.Nodes = append(owner.Nodes,
		LocalNodeResult{ID: evidence, Kind: NodeSBOM, Status: "ok"},
		LocalNodeResult{ID: output, Kind: NodePackage, Status: "ok"})
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
	owner.diskPublicationWait.Add(1)
	var once sync.Once
	return func() {
		once.Do(func() {
			owner.diskMu.Lock()
			delete(owner.diskPublicationPending, output)
			owner.diskMu.Unlock()
			owner.diskPublicationWait.Done()
		})
	}, nil
}
