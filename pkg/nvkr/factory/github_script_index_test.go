// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestGitHubScriptIndexConnectsJobsToExactCommands(t *testing.T) {
	const script = "printf 'first\n'\nprintf 'last'" // Deliberately no final newline.
	base := recompileGitHubStabilityPlan(t, githubRuntimePlanFixture(t), func(in *githubStabilityInputs) {
		component := in.catalog.Components["kubelet"]
		component.BuildCommand = []string{"sh", "-c", script}
		in.catalog.Components["kubelet"] = component
	})
	for _, partitioned := range []bool{false, true} {
		t.Run(map[bool]string{false: "flat", true: "partitioned"}[partitioned], func(t *testing.T) {
			plan := recompileGitHubStabilityPlan(t, base, func(in *githubStabilityInputs) { in.environment.GitHubExecutionPartitions = partitioned })
			previous := githubForcePartitionFallback
			githubForcePartitionFallback = partitioned
			t.Cleanup(func() { githubForcePartitionFallback = previous })
			before := mustCanonical(t, plan)
			bundle, err := RenderGitHub(t.Context(), plan)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, mustCanonical(t, plan)) {
				t.Fatal("navigation changed Plan identity")
			}
			jobs, err := githubScriptNavigation(plan, bundle.Files)
			if err != nil {
				t.Fatal(err)
			}
			scriptPath := testNamedStorePath(t, "scripts/kubelet/build", []byte(script), ".sh")
			found, worker := false, false
			for jobID, job := range jobs {
				workflow := string(bundle.Files[job.Workflow])
				comment := "    # Producer commands: " + PlanStoreIndexPath + " (job " + jobID + ")"
				if !strings.Contains(workflow, "\n  "+jobID+":\n"+comment+"\n") || strings.Count(workflow, comment+"\n") != 1 {
					t.Fatal("job must have exactly one navigation pointer immediately below its key", jobID)
				}
				worker = worker || job.Workflow != bundle.WorkflowPath
				for _, operation := range job.Operations {
					if !slices.Contains(operation.Scripts, scriptPath) {
						continue
					}
					found = true
					if operation.Producer == "" || operation.Step == "" || !bytes.Contains(bundle.Files[operation.Record], []byte(scriptPath)) || string(bundle.Files[scriptPath]) != script {
						t.Fatal("job lost exact producer/script binding", operation)
					}
				}
			}
			if !found || worker != partitioned {
				t.Fatal("fixture omitted script or partition routing", found, worker)
			}
			root := t.TempDir()
			if err := WriteGeneratedBundle(t.Context(), root, bundle); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(filepath.Join(root, PlanStoreIndexPath))
			if err != nil {
				t.Fatal(err)
			}
			var index planStoreIndexDocument
			if err := json.Unmarshal(raw, &index); err != nil {
				t.Fatal(err)
			}
			for jobID, job := range jobs {
				variants := index.Jobs[jobID]
				if len(variants) != 1 || !reflect.DeepEqual(variants[0].Operations, job.Operations) || len(variants[0].Workflows) != 1 || variants[0].Workflows[0].File != job.Workflow {
					t.Fatal("shared navigation differs from the emitted job", jobID)
				}
			}
			if len(index.Jobs) != len(jobs) {
				t.Fatal("shared navigation changed job membership")
			}
			matched := false
			for _, variant := range index.Objects["scripts/kubelet/build"] {
				if PlanStoreDirectory+"/"+variant.File == scriptPath && len(variant.Bundles) == 1 && variant.Bundles[0] == filepath.Base(filepath.Dir(bundle.PlanPath)) {
					matched = true
				}
			}
			if !matched {
				t.Fatal("script has no bundle-user link")
			}
			// The object inventory can remain valid while the selected job mapping
			// drifts. Both failures are descriptive and must not block Plan loads.
			for _, damage := range []string{"objects", "jobs"} {
				t.Run(damage, func(t *testing.T) {
					changed := index
					if damage == "objects" {
						changed.Objects = map[string][]planStoreVariant{}
					} else {
						changed.Jobs = map[string][]planStoreJobVariant{}
					}
					raw, err := treeJSON(changed)
					if err != nil {
						t.Fatal(err)
					}
					writeGeneratedFixture(t, filepath.Join(root, PlanStoreIndexPath), raw)
					if _, err := LoadPlanContext(t.Context(), filepath.Join(root, bundle.PlanPath)); err != nil {
						t.Fatal(err)
					}
					assertFactoryErrorCode(t, CheckGeneratedBundle(t.Context(), root, bundle), e.CodePipelineDrift)
				})
			}
		})
	}
}
