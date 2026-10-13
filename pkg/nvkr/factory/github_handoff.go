// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"fmt"
)

const (
	githubQualificationHandoffPath = ".nvkr/qualification/handoff.json"
	githubQualificationHandoffName = "nvkr-qualification-${{ github.run_id }}-${{ github.run_attempt }}"
)

func githubQualificationHandoffEnabled(plan *Plan) bool {
	return plan.Environment.Signing != nil && plan.Environment.Signing.Mode == SigningModeCosign
}

func githubQualificationHandoffStep(actions githubActionRefs) githubStep {
	return githubStep{
		Name: "Upload complete qualification handoff", Uses: actions.Upload,
		If: "${{ success() }}", TimeoutMinutes: 5,
		With: map[string]string{
			"name": githubQualificationHandoffName, "path": githubQualificationHandoffPath,
			"include-hidden-files": "true", "if-no-files-found": "error",
		},
	}
}

func writeGitHubQualificationHandoffStep(out *bytes.Buffer, actions githubActionRefs) {
	step := githubQualificationHandoffStep(actions)
	fmt.Fprintf(out, "      - name: %s\n        uses: %s\n        if: %s\n        timeout-minutes: %d\n        with:\n          name: %s\n          path: %s\n          include-hidden-files: 'true'\n          if-no-files-found: error\n",
		step.Name, step.Uses, step.If, step.TimeoutMinutes, yamlSingle(step.With["name"]), step.With["path"])
}
