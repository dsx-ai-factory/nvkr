// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/provenance"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/qualificationorigin"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sign"
)

func qualificationProducerEnvironment() map[string]string {
	source := qualificationSourceFixture().value
	return map[string]string{
		"GITHUB_ACTIONS": "true", "GITHUB_SERVER_URL": "https://github.com", "GITHUB_EVENT_NAME": "push",
		"GITHUB_REPOSITORY": source.Repository, "GITHUB_REF": source.Ref,
		"GITHUB_WORKFLOW_REF": source.Repository + "/" + source.Workflow + "@" + source.Ref,
		"GITHUB_RUN_ID":       source.RunID, "GITHUB_RUN_ATTEMPT": source.Attempt, "GITHUB_SHA": source.Commit,
	}
}

func TestProducingSourceDescriptionRequiresExactRunnerContext(t *testing.T) {
	for _, tc := range []struct {
		name    string
		edit    func(map[string]string)
		attempt string
		code    e.Code
	}{
		{"push", func(map[string]string) {}, "42-2", ""},
		{"manual", func(v map[string]string) { v["GITHUB_EVENT_NAME"] = "workflow_dispatch" }, "42-2", ""},
		{"not GitHub", func(v map[string]string) { v["GITHUB_ACTIONS"] = "false" }, "42-2", e.CodeEngineCapability},
		{"server", func(v map[string]string) { v["GITHUB_SERVER_URL"] = "https://github.example.test" }, "42-2", e.CodeEngineCapability},
		{"pull request", func(v map[string]string) { v["GITHUB_EVENT_NAME"] = "pull_request" }, "42-2", e.CodeEngineCapability},
		{"reusable workflow", func(v map[string]string) { v["GITHUB_EVENT_NAME"] = "workflow_call" }, "42-2", e.CodeEngineCapability},
		{"tag", func(v map[string]string) { v["GITHUB_REF"] = "refs/tags/v1" }, "42-2", e.CodeProvenanceMissing},
		{"foreign workflow", func(v map[string]string) {
			v["GITHUB_WORKFLOW_REF"] = "other/nvkr/.github/workflows/build.yml@refs/heads/main"
		}, "42-2", e.CodeProvenanceMissing},
		{"different workflow ref", func(v map[string]string) {
			v["GITHUB_WORKFLOW_REF"] = "example/nvkr/.github/workflows/build.yml@refs/heads/other"
		}, "42-2", e.CodeProvenanceMissing},
		{"invalid workflow", func(v map[string]string) { v["GITHUB_WORKFLOW_REF"] = "example/nvkr/other.yml@refs/heads/main" }, "42-2", e.CodeTrustPolicyMissing},
		{"missing commit", func(v map[string]string) { v["GITHUB_SHA"] = "" }, "42-2", e.CodeTrustPolicyMissing},
		{"abbreviated commit", func(v map[string]string) { v["GITHUB_SHA"] = "abcdef0" }, "42-2", e.CodeTrustPolicyMissing},
		{"missing run", func(v map[string]string) { v["GITHUB_RUN_ID"] = "" }, "42-2", e.CodeTrustPolicyMissing},
		{"noncanonical attempt", func(v map[string]string) { v["GITHUB_RUN_ATTEMPT"] = "02" }, "42-2", e.CodeTrustPolicyMissing},
		{"different finalizer attempt", func(map[string]string) {}, "42-3", e.CodeVerifyFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			values := qualificationProducerEnvironment()
			tc.edit(values)
			source, err := producingSourceDescription(tc.attempt, func(key string) string { return values[key] })
			if tc.code == "" {
				if err != nil || source != qualificationSourceFixture().value {
					t.Fatalf("source=%+v error=%v", source, err)
				}
				return
			}
			assertCode(t, err, tc.code)
			if source != (qualificationorigin.Description{}) {
				t.Fatal("refused producer returned a partial source description")
			}
			for key, value := range values {
				t.Setenv(key, value)
			}
			root := t.TempDir()
			output, handoff := filepath.Join(root, "run.json"), filepath.Join(root, "handoff", "handoff.json")
			run, err := FinalizeGeneratedWithHandoff(t.Context(), "missing-definition", "missing-environment", "missing-plan", "test", tc.attempt, "missing-workspace", output, handoff)
			assertCode(t, err, tc.code)
			if run != nil {
				t.Fatal("producer admission failure returned a Run")
			}
			entries, err := os.ReadDir(root)
			if err != nil || len(entries) != 0 {
				t.Fatalf("admission created output: %v, %v", entries, err)
			}
		})
	}
}

func TestFinalizeGeneratedWithHandoffAdmission(t *testing.T) {
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	for _, tc := range []struct {
		name    string
		ctx     context.Context
		handoff string
		code    e.Code
	}{
		{"missing handoff", t.Context(), "", e.CodeUsage},
		{"missing context", nil, "handoff.json", e.CodeUsage},
		{"canceled", canceled, "handoff.json", e.CodeTimeout},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			run, err := FinalizeGeneratedWithHandoff(tc.ctx, "missing-definition", "missing-environment", "missing-plan", "test", "42-2", "missing-workspace", "run.json", tc.handoff)
			assertCode(t, err, tc.code)
			if run != nil {
				t.Fatal("admission failure returned a Run")
			}
			entries, err := os.ReadDir(".")
			if err != nil || len(entries) != 0 {
				t.Fatalf("admission created files: %v, %v", entries, err)
			}
		})
	}
}

// The fixture materializes a hosted contract closure without executing a build,
// contacting a registry, or asserting real signing/qualification evidence.
func qualificationExportFixture(t *testing.T) (*factory.Plan, factory.InputSnapshot, qualificationorigin.Description, string, []byte) {
	t.Helper()
	t.Chdir(t.TempDir())
	for key, value := range qualificationProducerEnvironment() {
		t.Setenv(key, value)
	}
	source, err := producingSourceDescription("42-2", os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	definition, environment := testDefinition(t), testEnvironment()
	addTestSigningTool(t, &definition)
	if err := os.CopyFS("catalog", os.DirFS(definition.Catalog.Base)); err != nil {
		t.Fatal(err)
	}
	definition.Catalog.Base = "catalog"
	environment.Signing = &Signing{Mode: factory.SigningModeCosign, Identity: "^" + regexp.QuoteMeta(source.BuilderID) + "$", Issuer: "https://token.actions.githubusercontent.com"}
	writeCanonicalTestFile(t, "definition.json", definition)
	writeCanonicalTestFile(t, "environment.json", environment)
	plan, snapshot, err := factory.CaptureCompileFiles(t.Context(), ".", "definition.json", "environment.json", "qualification-export-test")
	if err != nil {
		t.Fatal(err)
	}
	writeCanonicalTestFile(t, "plan.json", plan)
	artifacts := map[string]factory.Artifact{}
	for _, artifact := range plan.Graph.Artifacts {
		artifacts[artifact.ID] = artifact
	}
	for _, node := range plan.Graph.Nodes {
		if node.Kind != factory.NodeVerify {
			continue
		}
		for _, id := range node.Inputs {
			artifact := artifacts[id]
			if artifact.Kind == "producer-result" {
				continue
			}
			if err := os.MkdirAll(filepath.Dir(artifact.Path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(artifact.Path, []byte("exact hosted "+artifact.Component), 0o755); err != nil {
				t.Fatal(err)
			}
			result := artifacts["result/"+artifact.BuildTarget+"/"+artifact.Component]
			writeCanonicalTestFile(t, result.Path, fixtureProducerResult(t, plan, artifact, artifact.Path))
		}
	}
	for _, kind := range []string{factory.NodeVerify, factory.NodeSBOM} {
		for _, node := range plan.Graph.Nodes {
			if node.Kind == kind {
				if err := factory.ExecuteNode(t.Context(), plan, node.ID, nil, nil); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	for _, id := range []string{"package/cli", "package/release"} {
		if err := factory.ExecuteNode(t.Context(), plan, id, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	if prepared, ok := artifacts["prepared/release"]; ok {
		if _, err := delivery.AttestEvidence(t.Context(), prepared.Path, artifacts["package/release"].Path, fixtureSBOMAttestation(t)); err != nil {
			t.Fatal(err)
		}
	}
	envelope, err := delivery.Inspect(t.Context(), artifacts["package/release"].Path)
	if err != nil {
		t.Fatal(err)
	}
	reference := "registry.example.test/product/releases@" + envelope.RootDigest
	writeQualificationHandoffFile(t, artifacts["publication/release"].Path, []byte(reference+"\n"))
	instant := time.Date(2026, time.September, 8, 12, 0, 0, 0, time.UTC)
	runContext, err := provenance.NewGitHubRunContext(os.Getenv, instant, instant.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	predicate, err := factory.ProductionPredicate(plan, runContext)
	if err != nil {
		t.Fatal(err)
	}
	predicate = append(predicate, '\n')
	writeQualificationHandoffFile(t, artifacts["predicate/release"].Path, predicate)
	writeCanonicalTestFile(t, artifacts["signature/release"].Path, sign.SigningRecord{SchemaVersion: sign.SigningRecordSchemaVersion,
		Ref: reference, Digest: envelope.RootDigest, PredicateSHA256: pipeline.FileDigest(predicate), PredicateType: sign.ProvenancePredicateType,
		Identity: environment.Signing.Identity, Issuer: environment.Signing.Issuer, Ran: [][]string{{"cosign", "sign"}}})
	writeCanonicalTestFile(t, artifacts["verification/release"].Path, sign.VerificationRecord{SchemaVersion: sign.VerificationRecordSchemaVersion,
		Ref: reference, Digest: envelope.RootDigest, Identity: environment.Signing.Identity, Issuer: environment.Signing.Issuer,
		PredicateType: sign.ProvenancePredicateType, SignatureVerified: true, AttestationVerified: true,
		ObservedIdentity: source.BuilderID, ObservedIssuer: environment.Signing.Issuer, Ran: [][]string{{"cosign", "verify"}}})
	writeHostedRestoreOutcomes(t, ".nvkr/factory", plan)
	return plan, snapshot, source, reference, predicate
}

func TestWriteQualificationHandoffPreservesOriginalBytesAndNeverOverwrites(t *testing.T) {
	plan, inputs, source, reference, predicate := qualificationExportFixture(t)
	path := ".nvkr/qualification/handoff.json"
	if err := writeQualificationHandoff(t.Context(), path, ".nvkr/factory", plan, inputs, source); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var handoff QualificationHandoff
	if err := canonical.UnmarshalExact(original, &handoff); err != nil {
		t.Fatal(err)
	}
	if handoff.SchemaVersion != QualificationHandoffSchema || handoff.Source != source || !bytes.Equal(handoff.Plan, qualificationHandoffJSON(t, plan)) || !reflect.DeepEqual(handoff.Inputs, inputs) ||
		handoff.Candidate.SchemaVersion != QualificationCandidateSchema || handoff.Candidate.Reference != reference || len(handoff.Candidate.Predicates) != 1 ||
		handoff.Candidate.Predicates[0].Reference != reference || !bytes.Equal(handoff.Candidate.Predicates[0].Predicate, predicate) {
		t.Fatal("export changed the exact source, Plan, captured inputs, or original predicate")
	}
	if _, _, err := prepareQualificationHandoff(t.Context(), original, source); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o444 {
		t.Fatalf("handoff protection=%v error=%v", info, err)
	}
	for _, changed := range []bool{false, true} {
		next := source
		if changed {
			next.Commit = strings.Repeat("b", 40)
		}
		assertCode(t, writeQualificationHandoff(t.Context(), path, ".nvkr/factory", plan, inputs, next), e.CodeImmutableViolation)
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, original) {
			t.Fatalf("existing handoff changed: %v", err)
		}
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(entries) != 1 || entries[0].Name() != "handoff.json" {
		t.Fatalf("export left temporary records: %v, %v", entries, err)
	}
}

func TestWriteQualificationHandoffRequiresProducingSigningPolicyBeforeIO(t *testing.T) {
	for _, tc := range []struct {
		name    string
		signing *Signing
		code    e.Code
	}{
		{"unsigned", nil, e.CodeEngineCapability},
		{"dry run", &Signing{Mode: factory.SigningModeDryRun}, e.CodeEngineCapability},
		{"other issuer", &Signing{Mode: factory.SigningModeCosign, Issuer: "https://issuer.example.test"}, e.CodeEngineCapability},
		{"invalid identity", &Signing{Mode: factory.SigningModeCosign, Issuer: "https://token.actions.githubusercontent.com", Identity: "["}, e.CodeUntrustedSigner},
		{"other builder", &Signing{Mode: factory.SigningModeCosign, Issuer: "https://token.actions.githubusercontent.com", Identity: "^other$"}, e.CodeUntrustedSigner},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			plan := &factory.Plan{Environment: Environment{Signing: tc.signing}}
			err := writeQualificationHandoff(t.Context(), filepath.Join(root, "new", "handoff.json"), "missing-workspace", plan, factory.InputSnapshot{}, qualificationSourceFixture().value)
			assertCode(t, err, tc.code)
			entries, err := os.ReadDir(root)
			if err != nil || len(entries) != 0 {
				t.Fatalf("signing admission created files: %v, %v", entries, err)
			}
		})
	}
}

func TestFinalizeGeneratedWithHandoffRecordsExportOutcome(t *testing.T) {
	for _, failure := range []bool{false, true} {
		name := "complete"
		if failure {
			name = "handoff collision"
		}
		t.Run(name, func(t *testing.T) {
			_, _, _, _, _ = qualificationExportFixture(t)
			path := ".nvkr/qualification/handoff.json"
			original := []byte("existing immutable handoff")
			if failure {
				writeQualificationHandoffFile(t, path, original)
			}
			run, err := FinalizeGeneratedWithHandoff(t.Context(), "definition.json", "environment.json", "plan.json", "qualification-export-test", "42-2", ".nvkr/factory", ".nvkr/run.json", path)
			if failure {
				assertCode(t, err, e.CodeImmutableViolation)
				if run == nil || run.Status != RunFailed || run.Failure == nil || run.Failure.Code != string(e.CodeImmutableViolation) || len(run.Observed) != 0 {
					t.Fatalf("export failure escaped final Run: %+v", run)
				}
				raw, readErr := os.ReadFile(path)
				if readErr != nil || !bytes.Equal(raw, original) {
					t.Fatalf("collision replaced handoff: %v", readErr)
				}
			} else if err != nil || run == nil || run.Status != RunComplete || run.Lane != LanePipeline {
				t.Fatalf("complete handoff finalization: run=%+v error=%v", run, err)
			}
			raw, err := os.ReadFile(".nvkr/run.json")
			if err != nil {
				t.Fatal(err)
			}
			var recorded Run
			if err := canonical.UnmarshalExact(raw, &recorded); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(&recorded, run) {
				t.Fatal("finalizer did not persist its exact returned Run")
			}
		})
	}
}
