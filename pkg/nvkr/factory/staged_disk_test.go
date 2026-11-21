// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"sigs.k8s.io/yaml"
)

func TestStagedDiskReferenceBindsOutputAndAttempt(t *testing.T) {
	plan := developmentPublicationPlan(t, arcTestPlan(t, "linux/amd64"), "ghcr.io/example/nvkr/development")
	setDevelopmentRun(t)
	var route Route
	for _, candidate := range plan.Routes {
		if stagedDiskRoute(plan, candidate.Name) {
			route = candidate
		}
	}
	target, err := runtimeTarget(plan, route.Target)
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"exact", "sibling", "other attempt", "foreign registry", "tag"} {
		t.Run(scenario, func(t *testing.T) {
			ref := target.Ref + "@sha256:" + strings.Repeat("a", 64)
			code := e.CodeVerifyFailed
			switch scenario {
			case "sibling":
				ref = strings.Replace(ref, target.Ref, target.Ref+"-sibling", 1)
			case "other attempt":
				ref = strings.Replace(ref, "/attempts/1/", "/attempts/2/", 1)
			case "foreign registry":
				ref = strings.Replace(ref, "ghcr.io/", "registry.example.test/", 1)
			case "tag":
				ref, code = target.Ref+":latest", e.CodeUnpinnedSource
			}
			path := filepath.Join(t.TempDir(), "staged.txt")
			if err := os.WriteFile(path, []byte(ref+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			_, artifacts := planIndex(plan)
			artifact := artifacts[stagedDiskReferenceID(route.Name)]
			artifact.Path = path
			artifacts[artifact.ID] = artifact
			subject, _, err := stagedDiskSubject(t.Context(), plan, route, artifacts)
			if scenario == "exact" {
				if err != nil || subject.Ref != ref {
					t.Fatalf("exact reference rejected: %v", err)
				}
				return
			}
			assertFactoryErrorCode(t, err, code)
		})
	}
}

func TestStagedDiskDevelopmentGraph(t *testing.T) {
	for _, platform := range []string{"linux/amd64", "linux/arm64"} {
		t.Run(platform, func(t *testing.T) {
			production := arcTestPlan(t, platform)
			plan := developmentPublicationPlan(t, production, "ghcr.io/example/nvkr/development")
			output := plan.LifecycleOutputs[0].Name
			route := outputPublicationRoute(output)
			if stagedDiskRoute(production, route) || !stagedDiskRoute(plan, route) || stagedDiskRoute(plan, "release") {
				t.Fatal("staging escaped development disk route")
			}
			nodes, artifacts := planIndex(plan)
			staged := stagedDiskReferenceID(route)
			payload := "package/" + outputPublicationPackage(output)
			for _, kind := range []string{NodePublish, NodeAttestEvidence} {
				node := nodes[kind+"/"+route]
				if !slices.Contains(node.Inputs, staged) || slices.Contains(node.Inputs, payload) {
					t.Fatalf("release operation transports payload: %+v", node)
				}
			}
			_, jobs, err := githubStages(plan)
			if err != nil {
				t.Fatal(err)
			}
			if githubArtifactNeedsCarrier(plan, artifacts[payload], jobs) || !githubArtifactNeedsCarrier(plan, artifacts[staged], jobs) {
				t.Fatal("disk handoff is not reference-only")
			}
			if !githubMetadataKind(artifacts[staged].Kind) {
				t.Fatal("staged reference does not use Actions metadata")
			}
			bundle, err := RenderGitHub(t.Context(), plan)
			if err != nil {
				t.Fatal(err)
			}
			workflow := expandedGitHubWorkflow(t, bundle)
			if strings.Count(string(workflow), "factory github stage-disk ") != 1 || strings.Contains(string(workflow), githubArtifactName(payload)) {
				t.Fatal("workflow retains disk payload transport or duplicates stage push")
			}
		})
	}
}

func TestStagedDiskRejectsWorkflowTampering(t *testing.T) {
	for _, scenario := range []string{"missing", "writer", "OIDC", "early", "producer credential", "optional", "wrong ID", "wrong command"} {
		t.Run(scenario, func(t *testing.T) {
			plan := developmentPublicationPlan(t, arcTestPlan(t, "linux/amd64"), "ghcr.io/example/nvkr/development")
			bundle, err := RenderGitHub(t.Context(), plan)
			if err != nil {
				t.Fatal(err)
			}
			document := decodeGitHubWorkflow(t, expandedGitHubWorkflow(t, bundle))
			stages, _, err := githubStages(plan)
			if err != nil {
				t.Fatal(err)
			}
			var stage githubStage
			for _, candidate := range stages {
				if candidate.DiskOutput != "" {
					stage = candidate
				}
			}
			job := document.Jobs[stage.ID]
			push, run := -1, -1
			for i, step := range job.Steps {
				if step.ID == "stage-disk" {
					push = i
				}
				if step.ID == "run-disk" {
					run = i
				}
			}
			if push < 0 || run < 0 {
				t.Fatal("missing disk steps")
			}
			switch scenario {
			case "missing":
				job.Steps = append(job.Steps[:push], job.Steps[push+1:]...)
			case "writer":
				job.Steps[push].Env["NVKR_CREDENTIAL"] = "${{ secrets.NVKR_DEV_REGISTRY_WRITE }}"
			case "OIDC":
				job.Permissions = map[string]string{"id-token": "write"}
			case "early":
				pushStep := job.Steps[push]
				copy(job.Steps[run+1:push+1], job.Steps[run:push])
				job.Steps[run] = pushStep
			case "producer credential":
				job.Steps[run].Env = stagedDiskEnvironment()
			case "optional":
				job.Steps[push].ContinueOnError = true
			case "wrong ID":
				job.Steps[push].ID = "other-stage"
			case "wrong command":
				job.Steps[push].Run = "true"
			}
			document.Jobs[stage.ID] = job
			raw, err := yaml.Marshal(document)
			if err != nil {
				t.Fatal(err)
			}
			changed := unsharedGitHubTestBundle(t, plan, bundle, raw)
			err = ValidateGitHubBundle(t.Context(), plan, changed)
			assertFactoryErrorCode(t, err, e.CodeEngineCapability)
			if scenario == "early" && !strings.Contains(err.Error(), "disk stage push precedes producer completion") {
				t.Fatalf("ordering mutation failed an unrelated gate: %v", err)
			}
		})
	}
}
