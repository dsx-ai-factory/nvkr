// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"maps"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
)

func TestGitHubUploadsHaveBoundedRetention(t *testing.T) {
	for _, test := range []struct {
		name        string
		plan        func(*testing.T) *Plan
		handoffDays string
		diagnostics bool
	}{
		{"development", githubDevelopmentTestPlan, "1", false},
		{"signed development", githubDevelopmentPublicationTestPlan, "7", false},
		{"hosted", githubTestPlan, "7", false},
		{"per-output compact workflow", referenceTestPlan, "7", false},
		{"native disk amd64", func(t *testing.T) *Plan { return arcTestPlan(t, "linux/amd64") }, "7", true},
		{"native disk arm64", func(t *testing.T) *Plan { return arcTestPlan(t, "linux/arm64") }, "7", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			plan := test.plan(t)
			bundle, err := RenderGitHub(t.Context(), plan)
			if err != nil {
				t.Fatal(err)
			}
			document := decodeGitHubWorkflow(t, bundle.Files[bundle.WorkflowPath])
			counts := map[string]int{}
			for jobID, job := range document.Jobs {
				for _, step := range job.Steps {
					if step.Uses != plan.Actions["upload-artifact"].Uses() {
						continue
					}
					kind, want := "carrier", test.handoffDays
					switch {
					case step.With["name"] == "nvkr-lifecycle-run":
						kind, want = "record", "7"
					case step.With["path"] == "nvkr-exec":
						kind = "helper"
					case step.Name == "Retain inspection diagnostics" || step.Name == "Upload failed Packer diagnostics":
						kind, want = "diagnostic", "1"
					}
					counts[kind]++
					if step.With["retention-days"] != want {
						t.Fatalf("%s/%s retention = %q, want %s", jobID, step.Name, step.With["retention-days"], want)
					}
					if kind != "diagnostic" && step.With["if-no-files-found"] != "error" {
						t.Fatalf("%s/%s permits a missing required upload", jobID, step.Name)
					}
				}
			}
			if counts["helper"] == 0 || counts["carrier"] == 0 || counts["record"] != 1 {
				t.Fatalf("missing required upload families: %v", counts)
			}
			if test.diagnostics && counts["diagnostic"] != 2 {
				t.Fatalf("disk fixture omits diagnostic uploads: %v", counts)
			}
		})
	}
}

func TestValidateGitHubBundleRejectsUploadRetentionTampering(t *testing.T) {
	for _, test := range []struct {
		name string
		plan func(*testing.T) *Plan
	}{
		{"production", githubTestPlan},
		{"development", githubDevelopmentTestPlan},
	} {
		t.Run(test.name, func(t *testing.T) {
			testGitHubRetentionTampering(t, test.plan(t))
		})
	}
}

func testGitHubRetentionTampering(t *testing.T, plan *Plan) {
	t.Helper()
	bundle, err := RenderGitHub(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	original := bundle.Files[bundle.WorkflowPath]
	document := decodeGitHubWorkflow(t, original)
	// Select each upload family from the admitted, noncompacted rendering so a
	// mutation changes only that retention field, preserving the remaining bytes.
	steps := map[string]githubStep{}
	for _, jobID := range sortedKeys(document.Jobs) {
		for _, step := range document.Jobs[jobID].Steps {
			if step.Uses != plan.Actions["upload-artifact"].Uses() {
				continue
			}
			kind := "carrier"
			if step.With["name"] == "nvkr-lifecycle-run" {
				kind = "record"
			} else if step.With["path"] == "nvkr-exec" {
				kind = "helper"
			}
			if _, found := steps[kind]; !found {
				steps[kind] = step
			}
		}
	}
	for _, kind := range []string{"helper", "carrier", "record"} {
		step, ok := steps[kind]
		if !ok {
			t.Fatalf("fixture omits %s upload", kind)
		}
		header := "      - name: " + yamlSingle(step.Name) + "\n"
		if kind == "record" {
			header = "      - name: Upload final Run record\n"
		}
		if bytes.Count(original, []byte(header)) != 1 {
			t.Fatalf("fixture must contain one %s upload", kind)
		}
		start := bytes.Index(original, []byte(header))
		retention := []byte("          retention-days: " + step.With["retention-days"] + "\n")
		end := bytes.Index(original[start:], retention)
		if end < 0 {
			t.Fatalf("fixture omits %s retention", kind)
		}
		upload := original[start : start+end+len(retention)]
		for _, mutation := range []struct{ name, value string }{
			{"removed", ""},
			{"repository default", "0"},
			{"long-lived", "90"},
		} {
			t.Run(kind+"/"+mutation.name, func(t *testing.T) {
				replacement := ""
				if mutation.value != "" {
					replacement = "          retention-days: " + mutation.value + "\n"
				}
				changedUpload := bytes.Replace(upload, retention, []byte(replacement), 1)
				workflow := bytes.Replace(original, upload, changedUpload, 1)
				changed := *bundle
				changed.Files = maps.Clone(bundle.Files)
				changed.Files[bundle.WorkflowPath] = workflow
				// Rehash the changed workflow to exercise semantic admission, not
				// stale exact-blob integrity metadata.
				changed.WorkflowDigest = pipeline.FileDigest(workflow)
				err := ValidateGitHubBundle(t.Context(), plan, &changed)
				assertFactoryErrorCode(t, err, e.CodeEngineCapability)
				if !strings.Contains(err.Error(), "exact admitted rendering") {
					t.Fatalf("retention mutation reached the wrong admission check: %v", err)
				}
			})
		}
	}
}
