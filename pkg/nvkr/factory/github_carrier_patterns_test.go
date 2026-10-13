// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"sigs.k8s.io/yaml"
)

func TestGitHubCarrierPatternOrphanFailsSpecificAdmission(t *testing.T) {
	for _, target := range []string{"stage", "finalizer"} {
		t.Run(target, func(t *testing.T) {
			plan := githubTestPlan(t)
			bundle, err := RenderGitHub(t.Context(), plan)
			if err != nil {
				t.Fatal(err)
			}
			document := decodeGitHubWorkflow(t, expandedGitHubWorkflow(t, bundle))
			var jobID, pattern string
			selectedStep := -1
			for _, id := range sortedKeys(document.Jobs) {
				if (target == "finalizer") != (id == "nvkr-lifecycle-run") {
					continue
				}
				for index, step := range document.Jobs[id].Steps {
					if step.With["pattern"] != "" {
						jobID, pattern = id, step.With["pattern"]
						selectedStep = index
						break
					}
				}
				if pattern != "" {
					break
				}
			}
			if pattern == "" {
				t.Fatal("fixture has no selected download pattern")
			}
			orphan := "{" + strings.Trim(pattern, "{}") + ",nvkr-orphan-no-uploader}"
			document.Jobs[jobID].Steps[selectedStep].With["pattern"] = orphan
			raw, err := yaml.Marshal(document)
			if err != nil {
				t.Fatal(err)
			}
			mutated := relowerGitHubTestBundle(t, plan, bundle, raw)
			err = ValidateGitHubBundle(t.Context(), plan, mutated)
			wantMessage := fmt.Sprintf("GitHub job %q downloads a carrier pattern outside its admitted graph", jobID)
			var coded *e.Error
			if !errors.As(err, &coded) || coded.Code != e.CodeEngineCapability || coded.Msg != wantMessage {
				t.Fatalf("orphan pattern admission=%v; want %s %q", err, e.CodeEngineCapability, wantMessage)
			}
		})
	}
}
