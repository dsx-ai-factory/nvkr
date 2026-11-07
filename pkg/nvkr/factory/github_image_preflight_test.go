// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"slices"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/imagepreflight"
)

func TestGitHubImagePreflightGatesProducerFanout(t *testing.T) {
	for _, mode := range []string{"flat", "partitioned", "runtime-partitioned"} {
		t.Run(mode, func(t *testing.T) {
			plan := privateImageTestPlan(t, ProductionRegistryWriteCredential)
			if mode != "flat" {
				plan.Environment.GitHubExecutionPartitions = true
			}
			if mode == "runtime-partitioned" {
				plan = runtimeTransportPlan(t, plan)
			}
			var err error
			plan.EnvironmentDigest, err = plan.Environment.Digest()
			if err != nil {
				t.Fatal(err)
			}
			bundle, err := RenderGitHub(t.Context(), plan)
			if err != nil {
				t.Fatal(err)
			}
			placements, err := githubPreflightSubjects(plan)
			if err != nil {
				t.Fatal(err)
			}
			if len(placements) < 2 {
				t.Fatal("fixture must span hosted and ARC placement")
			}
			projection, err := githubNeedsForPlan(t.Context(), plan)
			if err != nil {
				t.Fatal(err)
			}
			stages, _, err := githubStages(plan)
			if err != nil {
				t.Fatal(err)
			}
			preflightIDs, err := githubImagePreflightJobs(plan)
			if err != nil {
				t.Fatal(err)
			}
			for _, stage := range stages {
				for _, id := range preflightIDs {
					if !slices.Contains(projection.Full[stage.ID], id) {
						t.Fatalf("stage %s bypasses %s", stage.ID, id)
					}
					if stage.executionCondition() == executionAfterCompletion && !slices.Contains(projection.Reduced[stage.ID], id) {
						t.Fatal("completion stage lost explicit reader-success dependency")
					}
				}
			}
			seen := map[string]bool{}
			for _, document := range githubEmittedWorkflows(t, plan, bundle) {
				for id, job := range document.Jobs {
					if !strings.HasPrefix(id, "preflight-images-") {
						continue
					}
					seen[id] = true
					if _, ok := placements[job.RunsOn]; !ok {
						t.Fatal("probe uses a different placement")
					}
					checked, probed := -1, -1
					for index, step := range job.Steps {
						if strings.Contains(step.Run, "check-runtime-inputs") {
							checked = index
						}
						if strings.Contains(step.Run, "factory github preflight-images ") {
							probed = index
							if step.If != "" || step.ContinueOnError || step.TimeoutMinutes != 3 {
								t.Fatal("preflight can be skipped or is unbounded")
							}
							if step.Env[githubImageToken] != "" && (step.Env[githubImageToken] != "${{ secrets.NVKR_REGISTRY_READ }}" || step.Env[githubImageUser] != "${{ vars.NVKR_REGISTRY_READ_USER }}" || len(step.Env) != 2) {
								t.Fatal("wrong preflight reader")
							}
						}
					}
					if probed < 0 || (mode == "runtime-partitioned" && (checked < 0 || probed <= checked)) {
						t.Fatal("probe did not follow final runtime Plan validation")
					}
				}
			}
			if len(seen) != len(placements) {
				t.Fatal("partition lost a placement probe")
			}
		})
	}
}

func TestGitHubImagePreflightSelectsFinalPlanAndOnlyReader(t *testing.T) {
	before := runtimeTransportPlan(t, privateImageTestPlan(t, ProductionRegistryWriteCredential))
	after := recompileGitHubStabilityPlan(t, before, func(in *githubStabilityInputs) {
		component := in.catalog.Components["image"]
		for platform, image := range component.ToolchainImages {
			component.ToolchainImages[platform] = strings.Split(image, "@")[0] + "@sha256:" + strings.Repeat("f", 64)
		}
		in.catalog.Components["image"] = component
	})
	beforeDigest, err := before.Digest()
	if err != nil {
		t.Fatal(err)
	}
	afterDigest, err := after.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if beforeDigest == afterDigest {
		t.Fatal("fixture did not refresh Plan")
	}
	t.Setenv(githubImageUser, "synthetic-reader")
	t.Setenv(githubImageToken, "synthetic-reader-token")
	t.Setenv("GITHUB_TOKEN", "synthetic-source-token")
	t.Setenv("NVKR_REGISTRY_WRITE", "synthetic-writer-token")
	forbidGenerationSeam(t)
	_, err = preflightGitHubImages(leakedProjectionSelection(t.Context()), after, "arc-amd64-kvm", func(ctx context.Context, digest string, subjects []imagepreflight.Subject, resolve imagepreflight.CredentialResolver) (*imagepreflight.Report, error) {
		if digest != afterDigest {
			t.Fatal("probe bound the committed Plan instead of final Plan")
		}
		found := false
		for _, subject := range subjects {
			if strings.Contains(subject.Image, "/packer-") {
				found = true
				if !strings.HasSuffix(subject.Image, strings.Repeat("f", 64)) {
					t.Fatal("stale image selected")
				}
				credential, err := resolve(ctx, subject.Route)
				if err != nil || credential.Username != "synthetic-reader" || credential.Token != "synthetic-reader-token" {
					t.Fatal("probe used a credential fallback", err)
				}
			}
		}
		if !found {
			t.Fatal("missing refreshed image")
		}
		return &imagepreflight.Report{PlanDigest: digest}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = PreflightGitHubImages(t.Context(), after, "unadmitted-runner")
	assertFactoryErrorCode(t, err, e.CodeEngineCapability)
}

func TestGitHubImagePreflightCompletionCondition(t *testing.T) {
	plan := privateImageTestPlan(t, ProductionRegistryWriteCredential)
	ids, err := githubImagePreflightJobs(plan)
	if err != nil {
		t.Fatal(err)
	}
	source := githubYAMLMap("if", githubYAMLString(githubCompletionCondition(plan, ids)), "steps", githubYAMLMap())
	job, err := projectGitHubPartitionJob(plan, "verification", source, ids, nil, executionAfterCompletion)
	if err != nil {
		t.Fatal(err)
	}
	condition := githubYAMLValue(job, "if")
	for _, id := range ids {
		if !strings.Contains(condition, "fromJSON(inputs.dependencies)['"+id+"'].result == 'success'") {
			t.Fatalf("completion job bypasses reader gate: %s", condition)
		}
	}
}
