// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"reflect"
	"slices"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// Admit the exact provider projection before restoring logical dependencies for
// the existing artifact/credential validator. Missing edges are never repaired
// from a caller's workflow: the complete emitted map comes from the Plan.
func admitGitHubNeeds(ctx context.Context, plan *Plan, document *githubWorkflowDocument) error {
	projection, err := githubNeedsForPlan(ctx, plan)
	if err != nil {
		return err
	}
	if len(document.Jobs) != len(projection.Full)+len(projection.Collectors) {
		return e.New(e.CodeInvalidCatalog, "GitHub workflow changes its projected job set")
	}
	for _, id := range sortedKeys(document.Jobs) {
		job := document.Jobs[id]
		want, ok := projection.Reduced[id]
		if !ok || !slices.Equal(job.Needs, want) {
			return e.New(e.CodeInvalidCatalog, "GitHub job %q changes its projected dependencies", id)
		}
		if members, collector := projection.Collectors[id]; collector {
			if !reflect.DeepEqual(job, githubResultCollector(plan, id, members)) {
				return e.New(e.CodeEngineCapability, "GitHub result collector %q changes its closed interface", id)
			}
			continue
		}
		if len(job.Outputs) != 0 {
			return e.New(e.CodeEngineCapability, "GitHub execution job %q exposes unapproved outputs", id)
		}
	}
	if len(projection.Collectors) != 0 {
		finalizer := document.Jobs["nvkr-lifecycle-run"]
		expected := len(projection.Full["nvkr-lifecycle-run"])
		last := len(finalizer.Steps) - 1
		if last < 0 || !reflect.DeepEqual(finalizer.Steps[last], githubCollectedRunEndSummary(expected)) {
			return e.New(e.CodeEngineCapability, "GitHub closing summary must count exact collected producer results")
		}
		finalizer.Steps[last] = githubRunEndSummary(expected)
		document.Jobs["nvkr-lifecycle-run"] = finalizer
	}
	for id := range projection.Collectors {
		delete(document.Jobs, id)
	}
	for id, needs := range projection.Full {
		job := document.Jobs[id]
		job.Needs = slices.Clone(needs)
		document.Jobs[id] = job
	}
	return nil
}
