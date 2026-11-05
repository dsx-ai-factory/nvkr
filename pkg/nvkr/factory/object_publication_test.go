// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"context"
	"io"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func objectPublicationPlan(t *testing.T, perOutput bool) (*Plan, *Plan) {
	t.Helper()
	base := referenceTestPlan(t)
	if !perOutput {
		base = signedTestPlan(t, cosignSigningProfile(), ghcrTarget())
	}
	environment := base.Environment.Clone()
	environment.APIVersion = definition.EnvironmentObjectStoresAPIVersion
	environment.Publication.ObjectStores = []definition.ObjectStoreDestination{{Name: "customer", Provider: "s3", S3: &definition.S3ObjectStoreDestination{Region: "us-east-1", Bucket: "synthetic-artifacts", Prefix: "products/nvke", ExpectedBucketOwner: "123456789012", RoleRef: "github:S3_PUBLISH_ROLE"}}}
	stack, err := catalog.ApplyStack(base.CatalogSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := Compile(t.Context(), base.Definition, environment, stack, base.GeneratorVersion)
	if err != nil {
		t.Fatal(err)
	}
	plan.DefinitionPath, plan.EnvironmentPath = base.DefinitionPath, base.EnvironmentPath
	return base, plan
}

func TestObjectPublicationUsesOrdinaryRequiredGraph(t *testing.T) {
	for _, perOutput := range []bool{false, true} {
		t.Run(map[bool]string{false: "aggregate", true: "per-output"}[perOutput], func(t *testing.T) {
			base, plan := objectPublicationPlan(t, perOutput)
			if !reflect.DeepEqual(base.Builds, plan.Builds) || !reflect.DeepEqual(base.LifecycleOutputs, plan.LifecycleOutputs) || !reflect.DeepEqual(base.Routes, plan.Routes) {
				t.Fatal("object destination changed producer or original publication identity")
			}
			nodes, artifacts := planIndex(plan)
			node := nodes["publish-object/customer"]
			if node.Kind != NodePublish || plan.Targets[node.Target].Kind != TargetObject || !slices.Contains(node.Inputs, "verification/release") {
				t.Fatal("object publication bypassed original aggregate verification")
			}
			if !finalizerArtifact(plan, artifacts[node.Outputs[0]]) {
				t.Fatal("required object publication report is absent from finalization")
			}
			bundle, err := RenderGitHub(t.Context(), plan)
			if err != nil {
				t.Fatal(err)
			}
			document := decodeGitHubWorkflow(t, expandedGitHubWorkflow(t, bundle))
			_, jobs, err := githubStages(plan)
			if err != nil {
				t.Fatal(err)
			}
			job := document.Jobs[jobs[node.ID]]
			if job.Permissions["id-token"] != "write" || job.Permissions["packages"] != "read" || job.Env["NVKR_S3_ROLE_ARN"] != "${{ vars['S3_PUBLISH_ROLE'] }}" {
				t.Fatal("object publisher lacks exact workload/source-read scope")
			}
			if !slices.Contains(job.Needs, jobs[artifacts["verification/release"].Producer]) {
				t.Fatal("provider job does not depend on original verification")
			}
			for id, other := range document.Jobs {
				if id != jobs[node.ID] && other.Env["NVKR_S3_ROLE_ARN"] != "" {
					t.Fatal("workload role escaped the object publisher")
				}
			}
			weakened := *plan
			weakened.Graph.Nodes = slices.Clone(plan.Graph.Nodes)
			for i := range weakened.Graph.Nodes {
				if weakened.Graph.Nodes[i].ID == node.ID {
					weakened.Graph.Nodes[i].Inputs = slices.DeleteFunc(slices.Clone(node.Inputs), func(id string) bool { return id == "verification/release" })
				}
			}
			assertFactoryErrorCode(t, weakened.Validate(), e.CodePipelineDrift)
		})
	}
}

func TestObjectPublicationBoundsAdmitBeforeCatalogIO(t *testing.T) {
	_, plan := objectPublicationPlan(t, true)
	limits := plan.Environment.Publication.ObjectStores[0].EffectiveTransfer()
	if err := (delivery.MirrorLimits{MaxObjectBytes: int64(limits.MaxObjectBytes), MaxTotalBytes: int64(limits.MaxTotalBytes), MaxObjects: int(limits.MaxObjects)}).Validate(); err != nil {
		t.Fatal("authored defaults exceed the byte-owner's admitted capabilities", err)
	}
	for _, tc := range []struct {
		name string
		edit func(*definition.ObjectStoreDestination)
		code e.Code
	}{
		{"object bytes", func(d *definition.ObjectStoreDestination) { d.Transfer.MaxObjectBytes = 64<<30 + 1 }, e.CodeEngineCapability},
		{"object count", func(d *definition.ObjectStoreDestination) { d.Transfer.MaxObjects = 32769 }, e.CodeEngineCapability},
		{"endpoint path", func(d *definition.ObjectStoreDestination) { d.S3.Endpoint = "https://example.test/" }, e.CodeInvalidCatalog},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := plan.Definition
			d.Catalog.Base = "missing-catalog"
			environment := plan.Environment.Clone()
			tc.edit(&environment.Publication.ObjectStores[0])
			compiled, err := CompileDocuments(t.Context(), "definition.yaml", d, "environment.yaml", environment, "test")
			assertFactoryErrorCode(t, err, tc.code)
			if compiled != nil {
				t.Fatal("unsupported destination returned a Plan")
			}
		})
	}
}

func TestObjectPublicationBridgePreservesHostedAdmissionAndFailure(t *testing.T) {
	for _, scenario := range []string{"success", "publisher failure", "canceled", "wrong node"} {
		t.Run(scenario, func(t *testing.T) {
			_, plan := objectPublicationPlan(t, true)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			id := "publish-object/customer"
			want := e.Code("")
			if scenario == "canceled" {
				cancel()
				want = e.CodeTimeout
			}
			if scenario == "wrong node" {
				id = "sign/release"
				want = e.CodePipelineDrift
			}
			var log bytes.Buffer
			called := false
			err := RunGitHubObjectPublicationNode(ctx, plan, id, GitHubStepOptions{Stdout: &log, Stderr: &log}, func(_ context.Context, got *Plan, node Node, _ GitHubStepOptions) error {
				called = true
				if got != plan || node.ID != "publish-object/customer" {
					t.Fatal("dispatch changed frozen operation")
				}
				if scenario == "publisher failure" {
					return e.New(e.CodeUnavailable, "synthetic provider failure")
				}
				return nil
			})
			if scenario == "publisher failure" {
				want = e.CodeUnavailable
			}
			if want != "" {
				assertFactoryErrorCode(t, err, want)
			} else if err != nil {
				t.Fatal(err)
			}
			if called != (scenario == "success" || scenario == "publisher failure") {
				t.Fatal("hosted admission dispatched invalid operation")
			}
			if strings.Contains(log.String(), "Produced:") != (scenario == "success") {
				t.Fatal("publisher failure was reported as successful output")
			}
		})
	}
}

func TestObjectPublicationFailurePreventsRunFinalization(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("hosted carrier restoration is Linux-only")
	}
	for _, status := range []string{"success", "failure", "skipped", "missing"} {
		t.Run(status, func(t *testing.T) {
			_, plan := objectPublicationPlan(t, true)
			_, nodeJobs, err := githubStages(plan)
			if err != nil {
				t.Fatal(err)
			}
			jobs, err := githubFinalizerJobs(plan, nodeJobs)
			if err != nil {
				t.Fatal(err)
			}
			results := map[string]any{}
			for _, job := range jobs {
				results[job] = map[string]string{"result": "success"}
			}
			publisher := nodeJobs["publish-object/customer"]
			if status == "missing" {
				delete(results, publisher)
			} else {
				results[publisher] = map[string]string{"result": status}
			}
			t.Chdir(t.TempDir())
			err = recoverGitHubArtifacts(t.Context(), plan, carrierTestJSON(t, results), GitHubStepOptions{Stdout: io.Discard, Stderr: io.Discard}, func(context.Context, Artifact) (string, error) { return "restored:success", nil }, writeGitHubRestoreOutcome)
			if status == "missing" {
				assertFactoryErrorCode(t, err, e.CodeInvalidCatalog)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			err = ValidateHostedRestores(t.Context(), ".nvkr/factory", plan)
			if status == "success" {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				assertFactoryErrorCode(t, err, e.CodeProvenanceMissing)
			}
		})
	}
}
