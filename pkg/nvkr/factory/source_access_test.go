// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	authored "github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"

	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

func TestRunPlanSourceAccessBindingAndEngineAdmission(t *testing.T) {
	plan := buildOnlyPlan(t, "")
	source := ""
	for _, node := range plan.Builds[0].Graph.Nodes {
		if node.Kind == ir.KindFetch && node.Repo != "" {
			source = node.Repo
			break
		}
	}
	if source == "" {
		t.Fatal("test plan has no fetch source")
	}
	plan.Environment.SourceAccess = map[string]authored.SourceAccess{source: {
		Mirror: "https://github.com/example/nvke-mirror", CredentialRef: "github:NKE_SOURCE_TOKEN",
	}}
	digest, err := plan.Environment.Digest()
	if err != nil {
		t.Fatal(err)
	}
	plan.EnvironmentDigest = digest
	if err := plan.Validate(); err != nil {
		t.Fatal(err)
	}

	if err := AdmitLocal(context.Background(), plan, LocalRunOptions{}); err == nil {
		t.Fatal("local execution accepted source authentication it cannot enforce")
	} else {
		assertSourceAccessCode(t, err, nvkrerr.CodeEngineCapability)
	}
	plan.Environment.SourceAccess = map[string]authored.SourceAccess{"https://github.com/unknown/source": {
		Mirror: "https://github.com/example/nvke-mirror", CredentialRef: "github:NKE_SOURCE_TOKEN",
	}}
	digest, err = plan.Environment.Digest()
	if err != nil {
		t.Fatal(err)
	}
	plan.EnvironmentDigest = digest
	if err := plan.Validate(); err == nil {
		t.Fatal("unmatched source access accepted")
	} else {
		assertSourceAccessCode(t, err, nvkrerr.CodeInvalidCatalog)
	}
}

func TestRunPlanPinSetOnlySourceAccess(t *testing.T) {
	const repository = "https://github.com/example/private-pins"
	for _, tc := range []struct {
		name, sourceCatalog string
		pinSet, allowed     bool
	}{
		{"root pin set", "root", true, true},
		{"output pin set", "output", true, true},
		{"ordinary root source", "root", false, false},
		{"ordinary output source", "output", false, false},
		{"unknown repository", "", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			definition, environment := fixtureInputs("pin-set-source-access", "acquired", "ubuntu-24.04")
			definition.Catalog.Base = "root"
			definition.Outputs[0].Catalog = &authored.Catalog{Base: "output"}
			definition.Outputs[0].Components = []string{"tool"}
			definition.Outputs[0].Format = authored.OutputOCI
			environment.SourceAccess = map[string]authored.SourceAccess{repository: {
				Mirror: repository, CredentialRef: "github:PIN_SOURCE_TOKEN",
			}}
			// Resolved fixture snapshots model the loader boundary without contacting
			// a private Git server. Catalog pin derivation has its own loader tests.
			plan, err := compileDocumentsWithLoader(t.Context(), "examples/pin-set.yaml", definition,
				"examples/environment.yaml", environment, "test (deadbeef)",
				func(ctx context.Context, spec authored.Catalog) (*catalog.Stack, error) {
					base, err := catalog.LoadContext(ctx, testCatalogProvider(t))
					if err != nil {
						return nil, err
					}
					base.Components = map[string]catalog.Component{"tool": {
						Platforms: []string{"linux/amd64"}, Tag: "1.2.3", SourceDate: 1700000000, OutputType: "oci-image",
						Prebuilt: &catalog.Prebuilt{Image: "supplier.example/tool:1.2.3",
							Digests:  map[string]string{"amd64": "sha256:" + strings.Repeat("a", 64)},
							Evidence: &catalog.ExternalEvidence{None: true}},
					}}
					base.Sources = map[string]catalog.Source{}
					if spec.Base == tc.sourceCatalog {
						source := catalog.Source{Repo: repository}
						if tc.pinSet {
							source.PinSet = &catalog.PinSet{File: "versions.yaml", Members: map[string]catalog.PinSetMember{
								"tool": {Artifact: "tool", Path: "tool.version"},
							}}
						}
						base.Sources["pins"] = source
					}
					return catalog.ApplyStack(base)
				})
			if !tc.allowed {
				assertSourceAccessCode(t, err, nvkrerr.CodeInvalidCatalog)
				if !strings.Contains(err.Error(), "sourceAccess") {
					t.Fatalf("compilation failed before source-access admission: %v", err)
				}
				if plan != nil {
					t.Fatal("source-access rejection returned a partial Plan")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, build := range plan.Builds {
				for _, node := range build.Graph.Nodes {
					if node.Kind == ir.KindFetch {
						t.Fatal("pin-set-only fixture unexpectedly has a source fetch")
					}
				}
			}
			assertSourceAccessCode(t, AdmitLocal(t.Context(), plan, LocalRunOptions{}), nvkrerr.CodeEngineCapability)
			assertPinSetOnlyCredentialScope(t, plan)
			// Merely adding or changing a pin set in a supplied snapshot cannot
			// authorize credentials without the matching catalog identity.
			snapshot := plan.CatalogSnapshot
			if tc.sourceCatalog == "output" {
				snapshot = plan.Builds[0].CatalogSnapshot
			}
			snapshot.Sources["pins"].PinSet.File = "substituted.yaml"
			assertSourceAccessCode(t, plan.Validate(), nvkrerr.CodeUnverifiedRef)
			bundle, err := RenderGitHub(t.Context(), plan)
			assertSourceAccessCode(t, err, nvkrerr.CodeUnverifiedRef)
			if bundle != nil {
				t.Fatal("invalid snapshot returned a credential-bearing bundle")
			}
		})
	}
}

func assertPinSetOnlyCredentialScope(t *testing.T, plan *Plan) {
	t.Helper()
	bundle, err := RenderGitHub(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	document := decodeGitHubWorkflow(t, expandedGitHubWorkflow(t, bundle))
	assertNoCredential := func(env map[string]string) {
		t.Helper()
		for key, value := range env {
			if strings.Contains(key, "SOURCE_TOKEN") || strings.Contains(value, "secrets.PIN_SOURCE_TOKEN") {
				t.Fatal("pin credential escaped catalog revalidation steps")
			}
		}
	}
	assertNoCredential(document.Env)
	found, bootstraps := map[string]int{}, 0
	for id, job := range document.Jobs {
		if strings.HasPrefix(id, "build-runner-") {
			bootstraps++
		}
		assertNoCredential(job.Env)
		for _, step := range job.Steps {
			if step.ID != "validate-catalog" && step.ID != "finalize-catalog" {
				assertNoCredential(step.Env)
				continue
			}
			if step.Env["NVKR_PIN_SOURCE_TOKEN_0"] != "${{ secrets.PIN_SOURCE_TOKEN }}" ||
				!strings.Contains(step.Run, "NVKR_PIN_SOURCE_TOKEN_0") ||
				!strings.Contains(job.If, githubDispatchCondition(plan)) {
				t.Fatal("catalog revalidation lacks the admitted credential or dispatch guard")
			}
			found[step.ID]++
		}
	}
	if bootstraps == 0 || found["validate-catalog"] != bootstraps || found["finalize-catalog"] != 1 {
		t.Fatalf("catalog credential steps = %v for %d bootstraps", found, bootstraps)
	}
}

func assertSourceAccessCode(t *testing.T, err error, want nvkrerr.Code) {
	t.Helper()
	coded, ok := err.(*nvkrerr.Error)
	if !ok || coded.Code != want {
		t.Fatalf("error = %T %v, want code %s", err, err, want)
	}
}
