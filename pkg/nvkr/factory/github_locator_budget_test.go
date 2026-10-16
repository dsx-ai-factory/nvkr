// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"fmt"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestGitHubLocatorBudgetIncludesEveryProviderUpload(t *testing.T) {
	actions := testActionRefs(t, githubTestPlan(t))
	for _, count := range []int{maxGitHubLocatorArtifacts - 1, maxGitHubLocatorArtifacts, maxGitHubLocatorArtifacts + 1} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			// Include uploads outside ordinary producer outputs. The same name
			// cannot justify undercounting separate provider operations.
			upload := githubStep{Uses: actions.Upload, With: map[string]string{"name": "same-name"}}
			document := githubWorkflowDocument{Jobs: map[string]githubJob{
				"build-runner":       {Steps: []githubStep{upload}},
				"nvkr-lifecycle-run": {Steps: []githubStep{upload}},
				"native-diagnostics": {Steps: []githubStep{{Uses: actions.Upload, If: "${{ failure() }}", ContinueOnError: true}}},
			}}
			producer := githubJob{Steps: []githubStep{{Uses: actions.Download}, {Run: "echo upload-artifact"}}}
			for range count - 3 {
				producer.Steps = append(producer.Steps, upload)
			}
			document.Jobs["producer"] = producer
			err := validateGitHubLocatorBudget(document, actions)
			if count > maxGitHubLocatorArtifacts {
				assertFactoryErrorCode(t, err, e.CodeEngineCapability)
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}
