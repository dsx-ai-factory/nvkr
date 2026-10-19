// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"encoding/json"
	"slices"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestGitHubDependencyProjectionAdmission(t *testing.T) {
	plan, projection, _ := collectedRecoveryPlan(t)
	baseline := githubWorkflowDocument{Jobs: map[string]githubJob{}}
	for id, needs := range projection.Reduced {
		baseline.Jobs[id] = githubJob{Needs: slices.Clone(needs)}
	}
	for id, members := range projection.Collectors {
		baseline.Jobs[id] = githubResultCollector(plan, id, members)
	}
	finalizer := baseline.Jobs[githubFinalizerJobID]
	finalizer.Steps = []githubStep{githubCollectedRunEndSummary(len(projection.Full[githubFinalizerJobID]))}
	baseline.Jobs[githubFinalizerJobID] = finalizer
	collectorID := sortedKeys(projection.Collectors)[0]
	stageID := ""
	for _, id := range sortedKeys(projection.Full) {
		if id != githubFinalizerJobID && len(projection.Reduced[id]) > 0 && len(projection.Full[id]) > len(projection.Reduced[id]) {
			stageID = id
			break
		}
	}
	if stageID == "" {
		t.Fatal("fixture requires a reduced execution dependency list")
	}
	for _, test := range []struct {
		name   string
		code   e.Code
		mutate func(*githubWorkflowDocument)
	}{
		{name: "exact projection"},
		{name: "missing producer edge", code: e.CodeInvalidCatalog, mutate: func(doc *githubWorkflowDocument) {
			job := doc.Jobs[stageID]
			job.Needs = job.Needs[1:]
			doc.Jobs[stageID] = job
		}},
		{name: "unreduced producer edges", code: e.CodeInvalidCatalog, mutate: func(doc *githubWorkflowDocument) {
			job := doc.Jobs[stageID]
			job.Needs = slices.Clone(projection.Full[stageID])
			doc.Jobs[stageID] = job
		}},
		{name: "missing collector", code: e.CodeInvalidCatalog, mutate: func(doc *githubWorkflowDocument) {
			delete(doc.Jobs, collectorID)
		}},
		{name: "duplicate collector member", code: e.CodeInvalidCatalog, mutate: func(doc *githubWorkflowDocument) {
			job := doc.Jobs[collectorID]
			job.Needs = append(job.Needs, job.Needs[0])
			doc.Jobs[collectorID] = job
		}},
		{name: "collector token", code: e.CodeEngineCapability, mutate: func(doc *githubWorkflowDocument) {
			job := doc.Jobs[collectorID]
			job.Permissions["contents"] = "read"
			doc.Jobs[collectorID] = job
		}},
		{name: "collector condition", code: e.CodeEngineCapability, mutate: func(doc *githubWorkflowDocument) {
			job := doc.Jobs[collectorID]
			job.If = "${{ always() }}"
			doc.Jobs[collectorID] = job
		}},
		{name: "collector command", code: e.CodeEngineCapability, mutate: func(doc *githubWorkflowDocument) {
			job := doc.Jobs[collectorID]
			job.Steps[0].Run = "printf success"
			doc.Jobs[collectorID] = job
		}},
		{name: "collector fabricated results", code: e.CodeEngineCapability, mutate: func(doc *githubWorkflowDocument) {
			job := doc.Jobs[collectorID]
			job.Outputs["results"] = "{}"
			doc.Jobs[collectorID] = job
		}},
		{name: "ordinary job outputs", code: e.CodeEngineCapability, mutate: func(doc *githubWorkflowDocument) {
			job := doc.Jobs[stageID]
			job.Outputs = map[string]string{"results": "success"}
			doc.Jobs[stageID] = job
		}},
		{name: "aggregate collector success summary", code: e.CodeEngineCapability, mutate: func(doc *githubWorkflowDocument) {
			job := doc.Jobs[githubFinalizerJobID]
			job.Steps[len(job.Steps)-1].Env["NVKR_JOB_RESULTS"] = "${{ join(needs.*.result, ' ') }}"
			doc.Jobs[githubFinalizerJobID] = job
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw, err := json.Marshal(baseline)
			if err != nil {
				t.Fatal(err)
			}
			var document githubWorkflowDocument
			if err := json.Unmarshal(raw, &document); err != nil {
				t.Fatal(err)
			}
			// Empty collector permissions are an explicit provider restriction.
			for id := range projection.Collectors {
				job := document.Jobs[id]
				job.Permissions = map[string]string{}
				document.Jobs[id] = job
			}
			if test.mutate != nil {
				test.mutate(&document)
			}
			err = admitGitHubNeeds(t.Context(), plan, &document)
			if test.code != "" {
				assertFactoryErrorCode(t, err, test.code)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(document.Jobs) != len(projection.Full) {
				t.Fatal("logical admission retained collector execution slots")
			}
			for id, want := range projection.Full {
				if !slices.Equal(document.Jobs[id].Needs, want) {
					t.Fatalf("logical prerequisites changed for %s", id)
				}
			}
		})
	}
}
