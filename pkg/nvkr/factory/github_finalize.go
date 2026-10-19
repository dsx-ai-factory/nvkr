// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"fmt"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

const githubDiskBuildFinalizerTimeoutMinutes = 120

// Finalization includes bootstrap, bounded carrier acquisition/restoration and
// authentication of the complete release. It cannot share the 15-minute utility
// budget: acquisition alone permits twenty minutes. Keep a bounded job lifetime
// with room for release verification and the final Run/handoff uploads.
const githubReleaseFinalizerTimeoutMinutes = 120

// Build-only finalization restores the complete raw disk/material/source closure.
// Keep its storage on the one disk output's explicitly selected ARC pool. A
// mixed-output closure has no admitted aggregate scratch-capacity contract.
func githubDiskBuildFinalizerRunner(plan *Plan) (string, error) {
	if !GitHubDiskBuildOnly(plan) || len(plan.LifecycleOutputs) != 1 || plan.LifecycleOutputs[0].Kind != PackageDiskImage || len(plan.Builds) != 1 {
		return "", e.New(e.CodeEngineCapability, "GitHub build-only disk finalization requires exactly one disk output")
	}
	var runner string
	for _, node := range plan.Builds[0].Graph.Nodes {
		if node.BuilderType != ir.BuilderVMImage || node.ExecutionBinding == nil {
			continue
		}
		binding := node.ExecutionBinding
		if runner != "" || binding.Class.RunsOn == "" || binding.Class.RunsOn != githubARCRunner(string(binding.Class.HostPlatform)) {
			return "", e.New(e.CodeEngineCapability, "GitHub build-only disk finalization requires one admitted native ARC root")
		}
		runner = binding.Class.RunsOn
	}
	if runner == "" {
		return "", e.New(e.CodeEngineCapability, "GitHub build-only disk finalization has no admitted native ARC root")
	}
	return runner, nil
}

func githubFinalizerTimeoutMinutes(plan *Plan) int {
	if GitHubDiskBuildOnly(plan) {
		return githubDiskBuildFinalizerTimeoutMinutes
	}
	return githubReleaseFinalizerTimeoutMinutes
}

// writeGitHubFinalizerRestoreSteps emits the complete carrier download and restore
// sequence for the lifecycle finalizer. The private transport projection fetches
// the exact brace set serially and merges uniquely named carrier files into one
// directory. Missing pattern members are left for recovery, so every expected artifact reaches
// the per-producer status logic and the finalizer can persist a failed Run.
func writeGitHubFinalizerRestoreSteps(out *bytes.Buffer, plan *Plan, nodeJobs map[string]string) error {
	if out == nil || plan == nil {
		return e.New(e.CodeUsage, "GitHub finalizer restore rendering requires output and plan")
	}
	if len(plan.Graph.Artifacts) == 0 {
		return e.New(e.CodeInvalidCatalog, "GitHub lifecycle finalizer requires at least one carrier")
	}

	actions, err := resolveGitHubActions(plan.Actions)
	if err != nil {
		return err
	}
	for _, artifact := range plan.Graph.Artifacts {
		if nodeJobs[artifact.Producer] == "" {
			return e.New(e.CodeInvalidCatalog, "GitHub finalizer artifact %q has no producer job", artifact.ID)
		}
	}
	var carriers []Artifact
	for _, artifact := range plan.Graph.Artifacts {
		if finalizerArtifact(plan, artifact) {
			carriers = append(carriers, artifact)
		}
	}
	pattern, err := githubCarrierPattern(carriers)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "      - name: Download artifacts and evidence\n        uses: %s\n        continue-on-error: true\n        with:\n          pattern: %s\n          path: .nvkr/carriers\n          merge-multiple: 'true'\n", actions.Download, yamlSingle(pattern))
	fmt.Fprintf(out, "      - name: Recover planned carriers\n        id: recover-artifacts\n        env:\n          NVKR_JOB_RESULTS: ${{ toJSON(needs) }}\n        run: %s\n", githubRunnerCommand(plan, "recover", "", ""))
	return nil
}
