// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"encoding/json"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"unicode"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
)

func TestPlanTreeLoadIndependentOfWorkflowRendering(t *testing.T) {
	plan := githubRuntimePlanFixture(t)
	for _, mutation := range []string{"different renderer", "declared extra", "missing workflow"} {
		t.Run(mutation, func(t *testing.T) {
			root := t.TempDir()
			expected := writeGitHubRuntimeBaseline(t, root, plan)
			switch mutation {
			case "different renderer":
				changed, err := RenderGitHub(t.Context(), plan)
				if err != nil {
					t.Fatal(err)
				}
				changed.Files[changed.WorkflowPath] = append(changed.Files[changed.WorkflowPath], []byte("# another renderer\n")...)
				changed.WorkflowDigest = pipeline.FileDigest(changed.Files[changed.WorkflowPath])
				if err := WriteGeneratedBundle(t.Context(), root, changed); err != nil {
					t.Fatal(err)
				}
			case "declared extra":
				changed, err := RenderGitHub(t.Context(), plan)
				if err != nil {
					t.Fatal(err)
				}
				changed.Files[path.Dir(changed.PlanPath)+"/unexpected.json"] = []byte("{}")
				if err := WriteGeneratedBundle(t.Context(), root, changed); err != nil {
					t.Fatal(err)
				}
			case "missing workflow":
				if err := os.Rename(filepath.Join(root, expected.WorkflowPath), filepath.Join(root, "saved-workflow")); err != nil {
					t.Fatal(err)
				}
			}
			loaded, err := LoadPlanContext(t.Context(), filepath.Join(root, expected.PlanPath))
			if err != nil {
				t.Fatal("Plan inspection coupled to workflow bytes", err)
			}
			if !bytes.Equal(mustCanonical(t, loaded), mustCanonical(t, plan)) {
				t.Fatal("Plan changed")
			}
			assertFactoryErrorCode(t, CheckGeneratedBundle(t.Context(), root, expected), e.CodePipelineDrift)
		})
	}
}

func TestPlanTreeRelativeAndLoneRoot(t *testing.T) {
	plan := githubRuntimePlanFixture(t)
	root := t.TempDir()
	bundle := writeGitHubRuntimeBaseline(t, root, plan)
	for _, relative := range []string{".", ".github", path.Dir(bundle.PlanPath)} {
		t.Run(relative, func(t *testing.T) {
			working := filepath.Join(root, filepath.FromSlash(relative))
			t.Chdir(working)
			filename, err := filepath.Rel(working, filepath.Join(root, bundle.PlanPath))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := LoadPlanContext(t.Context(), filename); err != nil {
				t.Fatal(err)
			}
		})
	}
	lone := filepath.Join(t.TempDir(), "plan.json")
	if err := os.WriteFile(lone, bundle.Files[bundle.PlanPath], 0644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadPlanContext(t.Context(), lone)
	assertFactoryErrorCode(t, err, e.CodePipelineDrift)
	if !strings.Contains(err.Error(), "export with run plan --json") {
		t.Fatal("missing recovery guidance", err)
	}
}

func TestPlanTreeDiagnosticsEscapeUntrustedNames(t *testing.T) {
	plan := githubRuntimePlanFixture(t)
	for _, control := range []string{"\x1b]0;title\a", "\t", "\u202e"} {
		t.Run("manifest", func(t *testing.T) {
			root := t.TempDir()
			bundle := writeGitHubRuntimeBaseline(t, root, plan)
			bundle.Files[path.Dir(bundle.PlanPath)+"/steps/"+control+"/step.json"] = []byte("{}")
			_, raw, err := ManifestFor(bundle)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, bundle.ManifestPath), raw, 0644); err != nil {
				t.Fatal(err)
			}
			_, err = LoadPlanContext(t.Context(), filepath.Join(root, bundle.PlanPath))
			assertFactoryErrorCode(t, err, e.CodePipelineDrift)
			if strings.ContainsFunc(err.Error(), func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) }) {
				t.Fatal("raw terminal controls in diagnostic")
			}
		})
	}
	t.Run("step key", func(t *testing.T) {
		planPath := githubLayoutFor(plan).planPath
		files, err := renderPlanTree(t.Context(), plan, planPath)
		if err != nil {
			t.Fatal(err)
		}
		var root planFields
		if err := json.Unmarshal(files[planPath], &root); err != nil {
			t.Fatal(err)
		}
		var builds []planFields
		if err := json.Unmarshal(root["builds"], &builds); err != nil {
			t.Fatal(err)
		}
		const key = "\x1b[31mfield"
		builds[0][key] = json.RawMessage(`true`)
		root["builds"] = mustCanonical(t, builds)
		files[planPath], err = treeJSON(root)
		if err != nil {
			t.Fatal(err)
		}
		stepPath := path.Join(path.Dir(planPath), "steps", plan.Builds[0].Name, "step.json")
		var step planFields
		if err := json.Unmarshal(files[stepPath], &step); err != nil {
			t.Fatal(err)
		}
		step[key] = json.RawMessage(`true`)
		files[stepPath], err = treeJSON(step)
		if err != nil {
			t.Fatal(err)
		}
		_, _, err = joinPlanTree(t.Context(), planPath, files)
		assertFactoryErrorCode(t, err, e.CodePipelineDrift)
		if strings.ContainsFunc(err.Error(), unicode.IsControl) {
			t.Fatal("raw terminal control in field diagnostic")
		}
	})
}
