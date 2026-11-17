// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
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
			var index githubScriptIndex
			if err := json.Unmarshal(bundle.Files[githubScriptIndexPath(plan)], &index); err != nil {
				t.Fatal(err)
			}
			scriptPath := testNamedStorePath(t, "scripts/kubelet/build", []byte(script), ".sh")
			found, worker := false, false
			for jobID, job := range index.Jobs {
				workflow := string(bundle.Files[job.Workflow])
				if !strings.Contains(workflow, "(job "+jobID+")") || !strings.Contains(workflow, jobID+":") {
					t.Fatal("job missing navigation pointer", jobID)
				}
				worker = worker || job.Workflow != bundle.WorkflowPath
				for _, operation := range job.Operations {
					if !bytes.Contains(operation.Command, []byte(scriptPath)) {
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
			raw, err := os.ReadFile(filepath.Join(root, index.StoreIndex))
			if err != nil {
				t.Fatal(err)
			}
			var variants map[string][]planStoreVariant
			if err := json.Unmarshal(raw, &variants); err != nil {
				t.Fatal(err)
			}
			matched := false
			for _, variant := range variants["scripts/kubelet/build"] {
				if PlanStoreDirectory+"/"+variant.File == scriptPath && len(variant.Bundles) == 1 && variant.Bundles[0] == filepath.Base(filepath.Dir(bundle.PlanPath)) {
					matched = true
				}
			}
			if !matched {
				t.Fatal("script has no bundle-user link")
			}
			// Navigation is covered by closure drift, but is not consulted by Plan loads.
			writeGeneratedFixture(t, filepath.Join(root, githubScriptIndexPath(plan)), []byte("{}\n"))
			if _, err := LoadPlanContext(t.Context(), filepath.Join(root, bundle.PlanPath)); err != nil {
				t.Fatal(err)
			}
			assertFactoryErrorCode(t, CheckGeneratedBundle(t.Context(), root, bundle), e.CodePipelineDrift)
		})
	}
}
