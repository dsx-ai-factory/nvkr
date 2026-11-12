// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"fmt"
	"strconv"
)

const (
	githubQualificationHandoffPath = ".nvkr/qualification/handoff.json"
	githubQualificationHandoffName = "nvkr-qualification-${{ github.run_id }}-${{ github.run_attempt }}"
)

func githubQualificationHandoffEnabled(plan *Plan) bool {
	return AdmitQualificationHandoff(plan) == nil
}

func githubQualificationHandoffStep(plan *Plan, actions githubActionRefs) githubStep {
	return githubStep{
		Name: "Upload complete qualification handoff", Uses: actions.Upload,
		If: "${{ success() }}", TimeoutMinutes: 5,
		With: map[string]string{
			"name": githubQualificationHandoffName, "path": githubQualificationHandoffPath,
			"include-hidden-files": "true", "if-no-files-found": "error",
			"retention-days": strconv.Itoa(plan.GitHubArtifactRetentionDays()),
		},
	}
}

func writeGitHubQualificationHandoffStep(out *bytes.Buffer, plan *Plan, actions githubActionRefs) {
	step := githubQualificationHandoffStep(plan, actions)
	fmt.Fprintf(out, "      - name: %s\n        uses: %s\n        if: %s\n        timeout-minutes: %d\n        with:\n          name: %s\n          path: %s\n          include-hidden-files: 'true'\n          if-no-files-found: error\n          retention-days: %s\n",
		step.Name, step.Uses, step.If, step.TimeoutMinutes, yamlSingle(step.With["name"]), step.With["path"], step.With["retention-days"])
}
