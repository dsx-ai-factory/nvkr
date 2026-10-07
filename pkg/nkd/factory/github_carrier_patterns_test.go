// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/pipeline"
)

func TestGitHubCarrierPatternOrphanFailsSpecificAdmission(t *testing.T) {
	for _, target := range []string{"stage", "finalizer"} {
		t.Run(target, func(t *testing.T) {
			plan := githubTestPlan(t)
			bundle, err := RenderGitHub(t.Context(), plan)
			if err != nil {
				t.Fatal(err)
			}
			document := decodeGitHubWorkflow(t, bundle.Files[GitHubWorkflowPath])
			var jobID, pattern string
			for _, id := range sortedKeys(document.Jobs) {
				if (target == "finalizer") != (id == "nkd-lifecycle-run") {
					continue
				}
				for _, step := range document.Jobs[id].Steps {
					if step.With["pattern"] != "" {
						jobID, pattern = id, step.With["pattern"]
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
			orphan := "{" + strings.Trim(pattern, "{}") + ",nkd-orphan-no-uploader}"
			workflow := bytes.Replace(bundle.Files[GitHubWorkflowPath], []byte("pattern: "+yamlSingle(pattern)), []byte("pattern: "+yamlSingle(orphan)), 1)
			if bytes.Equal(workflow, bundle.Files[GitHubWorkflowPath]) {
				t.Fatal("pattern mutation did not affect rendered bytes")
			}
			mutated := *bundle
			mutated.Files = make(map[string][]byte, len(bundle.Files))
			for name, raw := range bundle.Files {
				mutated.Files[name] = raw
			}
			mutated.Files[GitHubWorkflowPath], mutated.WorkflowDigest = workflow, pipeline.FileDigest(workflow)
			err = ValidateGitHubBundle(t.Context(), plan, &mutated)
			wantMessage := fmt.Sprintf("GitHub job %q downloads a carrier pattern outside its admitted graph", jobID)
			var coded *e.Error
			if !errors.As(err, &coded) || coded.Code != e.CodeEngineCapability || coded.Msg != wantMessage {
				t.Fatalf("orphan pattern admission=%v; want %s %q", err, e.CodeEngineCapability, wantMessage)
			}
		})
	}
}
