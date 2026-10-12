// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"maps"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
)

func TestValidateGitHubBundleRejectsInspectionDiagnosticUploadTampering(t *testing.T) {
	for _, platform := range []string{"linux/amd64", "linux/arm64"} {
		t.Run(platform, func(t *testing.T) {
			plan := arcTestPlan(t, platform)
			bundle, err := RenderGitHub(t.Context(), plan)
			if err != nil {
				t.Fatal(err)
			}
			if err := ValidateGitHubBundle(t.Context(), plan, bundle); err != nil {
				t.Fatalf("unmodified diagnostic upload was rejected: %v", err)
			}
			original := bundle.Files[GitHubWorkflowPath]
			header := []byte("      - name: Retain inspection diagnostics\n")
			if bytes.Count(original, header) != 1 {
				t.Fatal("fixture must have exactly one diagnostic upload")
			}
			start := bytes.Index(original, header)
			end := bytes.Index(original[start+len(header):], []byte("      - name:"))
			if end < 0 {
				t.Fatal("fixture must retain required output steps after diagnostics")
			}
			diagnostic := original[start : start+len(header)+end]
			var diagnosticPath, diagnosticName, payloadPath string
			document := decodeGitHubWorkflow(t, original)
			for _, job := range document.Jobs {
				for _, step := range job.Steps {
					if step.Name == "Retain inspection diagnostics" {
						diagnosticPath, diagnosticName = step.With["path"], step.With["name"]
						for _, upload := range job.Steps {
							if upload.Name != "Upload failed Packer diagnostics" && upload.Uses == step.Uses && upload.With["if-no-files-found"] == "error" {
								payloadPath = upload.With["path"]
								break
							}
						}
					}
				}
			}
			if diagnosticPath == "" || diagnosticName == "" || payloadPath == "" {
				t.Fatal("fixture lacks distinct diagnostic and required payload uploads")
			}
			for _, test := range []struct {
				name, old, replacement string
				wholeWorkflow          bool
			}{
				{"wildcard path", yamlSingle(diagnosticPath), yamlSingle(".nvkr/diagnostics/inspection/*.jsonl"), false},
				{"carrier path", yamlSingle(diagnosticPath), yamlSingle(payloadPath), false},
				{"artifact name", yamlSingle(diagnosticName), yamlSingle("unexpected-diagnostics"), false},
				{"step name", "Retain inspection diagnostics", "Upload extra evidence", false},
				{"retention", "retention-days: 1", "retention-days: 2", false},
				{"condition", "if: ${{ always() }}", "if: ${{ success() }}", false},
				{"credential", "        with:\n", "        env:\n          NVKR_CREDENTIAL: '${{ secrets.UNRELATED }}'\n        with:\n", false},
				{"continue on error", "continue-on-error: true", "continue-on-error: false", false},
				{"unbounded upload", "        timeout-minutes: 5\n", "", false},
				{"longer upload", "timeout-minutes: 5", "timeout-minutes: 6", false},
				{"required payload warning", "path: " + yamlSingle(payloadPath) + "\n          if-no-files-found: error", "path: " + yamlSingle(payloadPath) + "\n          if-no-files-found: warn", true},
				{"removed diagnostics", string(diagnostic), "", false},
				{"extra diagnostics", string(diagnostic), string(diagnostic) + string(diagnostic), false},
			} {
				t.Run(test.name, func(t *testing.T) {
					input := diagnostic
					if test.wholeWorkflow {
						input = original
					}
					if bytes.Count(input, []byte(test.old)) != 1 {
						t.Fatal("mutation must select exactly one original descriptor field")
					}
					mutated := bytes.Replace(input, []byte(test.old), []byte(test.replacement), 1)
					workflow := mutated
					if !test.wholeWorkflow {
						workflow = bytes.Replace(original, diagnostic, mutated, 1)
					}
					if bytes.Equal(workflow, original) {
						t.Fatal("mutation did not change workflow bytes")
					}
					changed := *bundle
					changed.Files = maps.Clone(bundle.Files)
					changed.Files[GitHubWorkflowPath] = workflow
					// Authenticate the hostile workflow's new bytes so rejection
					// exercises semantic admission, not a stale file digest.
					changed.WorkflowDigest = pipeline.FileDigest(workflow)
					assertFactoryErrorCode(t, ValidateGitHubBundle(t.Context(), plan, &changed), e.CodeEngineCapability)
				})
			}
		})
	}
}
