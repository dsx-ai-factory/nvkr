// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"context"
	"io"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/cistore"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sign"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

func setDevelopmentRun(t *testing.T) {
	t.Helper()
	for key, value := range map[string]string{
		"GITHUB_ACTIONS": "true", "GITHUB_SERVER_URL": "https://github.com", "GITHUB_REPOSITORY": "example/nvkr",
		"GITHUB_WORKFLOW_REF": "example/nvkr/.github/workflows/nvkr-dev.yml@refs/heads/feature/pipeline",
		"GITHUB_REF":          "refs/heads/feature/pipeline", "GITHUB_EVENT_NAME": "workflow_dispatch",
		"GITHUB_RUN_ID": "12345", "GITHUB_RUN_ATTEMPT": "1",
	} {
		t.Setenv(key, value)
	}
}

func TestDevelopmentRuntimeTargetsAreAttemptScoped(t *testing.T) {
	plan := githubDevelopmentPublicationTestPlan(t)
	setDevelopmentRun(t)
	before, err := plan.Digest()
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ run, attempt string }{{"12345", "1"}, {"12345", "2"}, {"54321", "1"}} {
		t.Setenv("GITHUB_RUN_ID", test.run)
		t.Setenv("GITHUB_RUN_ATTEMPT", test.attempt)
		for name, original := range plan.Targets {
			target, err := runtimeTarget(plan, name)
			want := "ghcr.io/example/nvkr/development/runs/" + test.run + "/attempts/" + test.attempt + "/" + strings.TrimPrefix(original.Ref, "ghcr.io/example/nvkr/development/")
			if err != nil || target.Ref != want {
				t.Fatalf("target = %+v (%v), want %s", target, err, want)
			}
			if plan.Targets[name].Ref != original.Ref {
				t.Fatal("runtime projection mutated Plan")
			}
		}
	}
	after, err := plan.Digest()
	if err != nil || after != before {
		t.Fatalf("transport attempt changed Plan identity: %s (%v)", after, err)
	}
}

func TestDevelopmentOutputNamesAdmittedBeforeWork(t *testing.T) {
	base := githubDevelopmentPublicationTestPlan(t)
	setDevelopmentRun(t)
	for _, test := range []struct {
		name  string
		valid bool
	}{
		{"nvkectl-debug", true}, {"nvkectl_debug", true}, {"nvkectl.debug", true},
		{"nvkectl--debug", false}, {"nvkectl__debug", false}, {"nvkectl..debug", false}, {"nvkectl.-debug", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			definition := base.Definition.Clone()
			definition.Outputs = definition.Outputs[:1]
			definition.Outputs[0].Name = test.name
			if err := definition.Validate(); err != nil {
				t.Fatal(err)
			}
			if test.valid {
				plan, err := Compile(t.Context(), definition, base.Environment,
					&catalog.Stack{Catalog: base.CatalogSnapshot, Digest: base.CatalogDigest, Applied: base.AppliedOverlays}, base.GeneratorVersion)
				if err != nil {
					t.Fatal(err)
				}
				for target := range plan.Targets {
					if _, err := runtimeTarget(plan, target); err != nil {
						t.Fatal(err)
					}
				}
				return
			}
			definition.Catalog.Base = filepath.Join(t.TempDir(), "missing-catalog")
			plan, err := CompileDocuments(t.Context(), base.DefinitionPath, definition,
				base.EnvironmentPath, base.Environment, base.GeneratorVersion)
			assertFactoryErrorCode(t, err, e.CodeEngineCapability)
			if plan != nil {
				t.Fatal("unsupported publication output compiled a Plan")
			}
			// Complete-plan validation repeats the same admission at both execution
			// boundaries, even for a stale caller-authored Plan description.
			altered := *base
			altered.Definition = definition
			var log bytes.Buffer
			ran := false
			err = runGitHubNode(t.Context(), &altered, altered.Graph.Nodes[0].ID,
				GitHubStepOptions{Stdout: &log, Stderr: &log},
				func(context.Context, *Plan, Node, GitHubStepOptions) error { ran = true; return nil })
			assertFactoryErrorCode(t, err, e.CodeEngineCapability)
			assertFactoryErrorCode(t, ExecuteNode(t.Context(), &altered, altered.Graph.Nodes[0].ID, &log, &log), e.CodeEngineCapability)
			if ran || log.Len() != 0 {
				t.Fatal("unsupported output reached execution effects")
			}
		})
	}
}

func TestDevelopmentHostedAdmissionRejectsContextBeforeEffects(t *testing.T) {
	for _, test := range []struct {
		name, key, value string
		code             e.Code
	}{
		{"local", "GITHUB_ACTIONS", "", e.CodeEngineCapability},
		{"false marker", "GITHUB_ACTIONS", "false", e.CodeEngineCapability},
		{"other host", "GITHUB_SERVER_URL", "https://github.example.com", e.CodeEngineCapability},
		{"other repository", "GITHUB_REPOSITORY", "fork/nvkr", e.CodeEngineCapability},
		{"production workflow", "GITHUB_WORKFLOW_REF", "example/nvkr/.github/workflows/nvkr-run.yml@refs/heads/feature/pipeline", e.CodeEngineCapability},
		{"tag workflow", "GITHUB_WORKFLOW_REF", "example/nvkr/.github/workflows/nvkr-dev.yml@refs/tags/v1", e.CodeEngineCapability},
		{"other branch", "GITHUB_REF", "refs/heads/other", e.CodeEngineCapability},
		{"pull request", "GITHUB_EVENT_NAME", "pull_request", e.CodeEngineCapability},
		{"missing run", "GITHUB_RUN_ID", "", e.CodeProvenanceMissing},
		{"noncanonical run", "GITHUB_RUN_ID", "012345", e.CodeEngineCapability},
		{"zero attempt", "GITHUB_RUN_ATTEMPT", "0", e.CodeEngineCapability},
	} {
		t.Run(test.name, func(t *testing.T) {
			plan := githubDevelopmentPublicationTestPlan(t)
			setDevelopmentRun(t)
			t.Setenv(test.key, test.value)
			var log bytes.Buffer
			summary := filepath.Join(t.TempDir(), "summary")
			ran := false
			err := runGitHubNode(t.Context(), plan, plan.Graph.Nodes[0].ID,
				GitHubStepOptions{Stdout: &log, Stderr: &log, SummaryPath: summary},
				func(context.Context, *Plan, Node, GitHubStepOptions) error { ran = true; return nil })
			assertFactoryErrorCode(t, err, test.code)
			assertFactoryErrorCode(t, ExecuteNode(t.Context(), plan, plan.Graph.Nodes[0].ID, &log, &log), test.code)
			if ran || log.Len() != 0 {
				t.Fatal("unsupported development context caused effects")
			}
			if _, err := os.Stat(summary); !os.IsNotExist(err) {
				t.Fatal("unsupported development context wrote summary")
			}
		})
	}
}

func TestDevelopmentRejectsForeignSubjectsBeforeSigningOrRegistryReads(t *testing.T) {
	for _, operation := range []string{NodeSign, NodeVerifyPublication} {
		for _, repository := range []string{
			"ghcr.io/example/nvkr/m2-candidate/releases",
			"ghcr.io/example/nvkr/development/runs/12345/attempts/2/releases",
			"ghcr.io/example/nvkr/development/runs/54321/attempts/1/releases",
			"ghcr.io/example/nvkr/development/runs/12345/attempts/1/releases-worker",
		} {
			t.Run(operation+"/"+repository, func(t *testing.T) {
				plan := githubDevelopmentPublicationTestPlan(t)
				setDevelopmentRun(t)
				t.Setenv("NVKR_REGISTRY_USER", "dev-user")
				t.Setenv("NVKR_CREDENTIAL", "dev-credential")
				t.Chdir(t.TempDir())
				nodes, artifacts := planIndex(plan)
				writeReferenceFile(t, artifacts["publication/release"].Path, repository+"@sha256:"+strings.Repeat("a", 64))
				calls := 0
				newSigner := func(context.Context, *Plan, Node, map[string]Artifact) (sign.OCISigner, error) {
					calls++
					return &referenceSigner{}, nil
				}
				node := nodes[operation+"/release"]
				execute := executeSign
				if operation == NodeVerifyPublication {
					execute = executeVerifyPublication
				}
				err := execute(t.Context(), plan, node, artifacts, newSigner, io.Discard)
				assertFactoryErrorCode(t, err, e.CodeVerifyFailed)
				if calls != 0 {
					t.Fatal("foreign reference reached signer")
				}
				for _, id := range node.Outputs {
					if _, err := os.Stat(artifacts[id].Path); !os.IsNotExist(err) {
						t.Fatal("foreign reference wrote output")
					}
				}
			})
		}
	}
}

func TestDevelopmentMetadataAuthenticatesSameRunAttempts(t *testing.T) {
	plan, artifacts, signer := referenceMetadataFixtureForPlan(t, githubDevelopmentPublicationTestPlan(t))
	for _, test := range []struct {
		name, key, value string
		valid            bool
	}{
		{"same attempt", "GITHUB_RUN_ATTEMPT", "1", true},
		{"earlier attempt", "GITHUB_RUN_ATTEMPT", "2", true},
		{"other run", "GITHUB_RUN_ID", "12346", false},
		{"other branch", "GITHUB_WORKFLOW_REF", "example/nvkr/.github/workflows/nvkr-dev.yml@refs/heads/other", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			artifacts := maps.Clone(artifacts)
			out := artifacts["package/release"]
			out.Path = filepath.Join(t.TempDir(), "assembled")
			artifacts[out.ID] = out
			t.Setenv(test.key, test.value)
			if test.key == "GITHUB_WORKFLOW_REF" {
				t.Setenv("GITHUB_REF", "refs/heads/other")
			}
			node := planNode(plan, "package/release")
			err := executeReferenceRelease(t.Context(), plan, node, artifacts, referenceSignerFactory(signer))
			if !test.valid {
				assertFactoryErrorCode(t, err, e.CodeVerifyFailed)
				if _, err := os.Stat(artifacts[node.Outputs[0]].Path); !os.IsNotExist(err) {
					t.Fatal("stale metadata wrote release")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			inspection, err := delivery.InspectMetadata(t.Context(), artifacts[node.Outputs[0]].Path)
			if err != nil {
				t.Fatal(err)
			}
			for _, object := range inspection.Root.Objects {
				if object.Output == nil {
					continue
				}
				origin := object.Output.Publication
				if origin == nil || !strings.HasPrefix(origin.Ref, "ghcr.io/example/nvkr/development/runs/12345/attempts/1/releases-") {
					t.Fatalf("output origin lost development attempt: %+v", origin)
				}
			}
		})
	}
	t.Run("foreign metadata before publication", func(t *testing.T) {
		assertDevelopmentRejectsForeignMetadataBeforePublication(t, plan, artifacts)
	})
}

func assertDevelopmentRejectsForeignMetadataBeforePublication(t *testing.T, plan *Plan, artifacts map[string]Artifact) {
	t.Helper()
	t.Setenv("GITHUB_RUN_ID", "54321")
	t.Setenv("NVKR_REGISTRY_USER", "dev-user")
	t.Setenv("NVKR_CREDENTIAL", "dev-credential")
	t.Setenv(githubRepositoryTokenEnv, "repository-credential")
	route := outputPublicationRoute(plan.LifecycleOutputs[0].Name)
	node := planNode(plan, NodePublish+"/"+route)
	calls := 0
	original := remote.DefaultTransport
	remote.DefaultTransport = developmentRegistryTransport(func(*http.Request) (*http.Response, error) {
		calls++
		return nil, context.Canceled
	})
	t.Cleanup(func() { remote.DefaultTransport = original })
	err := executePublishWithAdmission(t.Context(), plan, node, artifacts, io.Discard,
		func(context.Context, string, []string, cistore.Options) error { return nil })
	assertFactoryErrorCode(t, err, e.CodeVerifyFailed)
	if calls != 0 {
		t.Fatal("stale development metadata reached registry")
	}
}

type developmentRegistryTransport func(*http.Request) (*http.Response, error)

func (transport developmentRegistryTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

func TestDevelopmentUtilityCarriesDispatchContextWithoutCredentials(t *testing.T) {
	plan, node, calls := githubRunnerRuntimeFixture(t, "exit 0\n")
	location := utilityRunnerLocation{runtime: plan.Environment.RuntimeBinary(), mountSource: "/fixture", mountTarget: "/workspace",
		helper: "/workspace/nvkr-exec", plan: GitHubDevelopmentPlanPath, publicationRunContext: true, developmentRunContext: true}
	setDevelopmentRun(t)
	t.Setenv("NVKR_CREDENTIAL", "must-not-cross")
	if err := executeUtilityNode(t.Context(), node, location, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"GITHUB_ACTIONS", "GITHUB_SERVER_URL", "GITHUB_REPOSITORY", "GITHUB_WORKFLOW_REF", "GITHUB_RUN_ID", "GITHUB_RUN_ATTEMPT", "GITHUB_EVENT_NAME", "GITHUB_REF"} {
		if !strings.Contains(string(raw), "-e\n"+key+"\n") {
			t.Fatalf("missing development identity %s", key)
		}
	}
	for _, forbidden := range []string{"GITHUB_TOKEN", "ACTIONS_ID_TOKEN", sourceTokenEnv, "NVKR_CREDENTIAL", "must-not-cross"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("credential crossed utility boundary: %s", forbidden)
		}
	}
}

// Exercise the failed-signing retry shape: publication remains in attempt 1,
// signing runs in attempt 3, and assembly also consumes attempt-2 outputs.
func TestDevelopmentRetrySignsOriginalPublicationAndAssemblesMixedOrigins(t *testing.T) {
	plan, artifacts, verifier := referenceMetadataFixtureForPlan(t, githubDevelopmentPublicationTestPlan(t), "1", "2")
	route := outputPublicationRoute(plan.LifecycleOutputs[0].Name)
	node := planNode(plan, NodeSign+"/"+route)
	original, err := readPublicationSubject(t.Context(), artifacts["publication/"+route].Path)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GITHUB_RUN_ATTEMPT", "3")
	for _, id := range node.Outputs {
		a := artifacts[id]
		a.Path = filepath.Join(t.TempDir(), "result.json")
		artifacts[id] = a
	}
	adapter := &metadataSigningAdapter{}
	adapter.identity, adapter.issuer = plan.Environment.Signing.Identity, plan.Environment.Signing.Issuer
	newSigner := func(context.Context, *Plan, Node, map[string]Artifact) (sign.OCISigner, error) { return adapter, nil }
	if err := executeSign(t.Context(), plan, node, artifacts, newSigner, io.Discard); err != nil {
		t.Fatal(err)
	}
	if adapter.verifiedSBOMs == 0 || adapter.signedSubject != original {
		t.Fatal("retry did not verify evidence or preserve the original subject")
	}
	if err := executeReferenceRelease(t.Context(), plan, planNode(plan, "package/release"), artifacts, referenceSignerFactory(verifier)); err != nil {
		t.Fatal(err)
	}
	result, err := delivery.InspectMetadata(t.Context(), artifacts["package/release"].Path)
	if err != nil {
		t.Fatal(err)
	}
	const invocation = "https://github.com/example/nvkr/actions/runs/12345/attempts/"
	if result.Root.Release.InvocationID != invocation+"3" {
		t.Fatal("assembly lost current invocation")
	}
	for _, object := range result.Root.Objects {
		if object.Output == nil {
			continue
		}
		origin := object.Output.Publication
		if origin == nil {
			t.Fatal("assembly lost original publication")
		}
		if object.Name == plan.LifecycleOutputs[0].Name {
			if origin.Ref != original.Ref || origin.PreparedInvocationID != invocation+"1" || origin.SignedInvocationID != invocation+"3" {
				t.Fatalf("retry changed origin: %+v", origin)
			}
		} else if origin.PreparedInvocationID != invocation+"2" || origin.SignedInvocationID != invocation+"2" {
			t.Fatalf("retained output changed origin: %+v", origin)
		}
	}
}
