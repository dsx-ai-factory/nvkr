// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/provenance"
)

// localProducerProvenance describes the original local producer's execution.
// It is unsigned evidence under the existing unattested local builder identity,
// never a hosted workflow, signing receipt, reproduction or release capability.
// The producer records its own actual time window; caller Run fields are unused.
func (receipt *DebianOutputReceipt) localProducerProvenance(ctx context.Context) ([]byte, DebianOutputObservation, error) {
	var zero DebianOutputObservation
	if receipt == nil || receipt.state == nil || receipt.state.owner == nil {
		return nil, zero, e.New(e.CodeProvenanceMissing, "local Debian provenance requires an original terminal receipt")
	}
	// Freeze the opaque facade before original admission can block.
	frozen := *receipt
	state := frozen.state.owner
	var plan Plan
	if err := canonical.UnmarshalExact(state.plan, &plan); err != nil {
		return nil, zero, e.Wrap(e.CodeVerifyFailed, err, "reading original Debian provenance Plan")
	}
	var output LifecycleOutputPlan
	if err := canonical.UnmarshalExact(frozen.state.output, &output); err != nil {
		return nil, zero, e.Wrap(e.CodeVerifyFailed, err, "reading original Debian provenance output")
	}
	owned, finish, err := state.begin(ctx, state.original, &plan, output)
	if err != nil {
		return nil, zero, err
	}
	defer finish()
	observation, err := frozen.DescribeContext(owned)
	if err != nil {
		return nil, zero, err
	}
	state.mu.Lock()
	completion, exists := state.completions[observation.Subject]
	state.mu.Unlock()
	if !exists {
		return nil, zero, e.New(e.CodeProvenanceMissing, "local Debian provenance has no original producer completion")
	}
	run, err := provenance.NewLocalRunContext(observation.PlanDigest, completion.startedOn, completion.finishedOn)
	if err != nil {
		return nil, zero, err
	}
	dependencies := resolvedDependencies(&plan)
	dependencies = append(dependencies, provenance.Dependency{URI: "nvkr:prepared-source",
		Digest: map[string]string{"sha256": strings.TrimPrefix(observation.InputDigest, "sha256:")}})
	// The complete Plan remains the invocation identity and retains ADR-09's
	// weakest aggregate build type. Exact named output membership is separate.
	selected := plan
	selected.Builds = nil
	for _, build := range plan.Builds {
		if build.Name == output.Name && build.BuildDigest == output.BuildDigest {
			selected.Builds = append(selected.Builds, build)
		}
	}
	if len(selected.Builds) != 1 {
		return nil, zero, e.New(e.CodeVerifyFailed, "local Debian provenance changes its named Build")
	}
	predicate := provenance.ProductionPredicate(run, provenance.ProductionInputs{
		BuildType: productionBuildType(&plan), PlanDigest: observation.PlanDigest,
		RunPlanReference: plan.Name, DefinitionDigest: plan.DefinitionDigest,
		EnvironmentDigest: plan.EnvironmentDigest, CatalogDigest: plan.CatalogDigest,
		GeneratorVersion: plan.GeneratorVersion, SigningMode: provenance.SigningModeDryRun,
		ResolvedDependencies: dependencies,
	})
	raw, err := provenance.MarshalProduction(predicate)
	if err != nil {
		return nil, zero, err
	}
	if err := owned.Err(); err != nil {
		return nil, zero, e.Wrap(e.CodeTimeout, err, "composing canceled local Debian provenance")
	}
	return raw, observation, nil
}
