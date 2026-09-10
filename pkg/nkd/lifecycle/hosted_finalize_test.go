package lifecycle

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nhensley/nkdctl/pkg/nkd/canonical"
	"github.com/nhensley/nkdctl/pkg/nkd/delivery"
	nkderr "github.com/nhensley/nkdctl/pkg/nkd/errors"
	"github.com/nhensley/nkdctl/pkg/nkd/factory"
	"github.com/nhensley/nkdctl/pkg/nkd/pipeline"
	"github.com/nhensley/nkdctl/pkg/nkd/provenance"
	"github.com/nhensley/nkdctl/pkg/nkd/sign"
)

func TestFinalizeGeneratedRecordsFailedAttemptWhenPlannedEvidenceIsMissing(t *testing.T) {
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	repo, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(repo); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(old) }()
	caseRoot := filepath.ToSlash(filepath.Join(".nkd", "lifecycle-finalizer-test"))
	_ = os.RemoveAll(caseRoot)
	defer func() { _ = os.RemoveAll(caseRoot) }()
	ctx := context.Background()
	definitionPath := "definitions/nke-v1/definition.yaml"
	definition, err := LoadDefinition(ctx, definitionPath)
	if err != nil {
		t.Fatal(err)
	}
	environment, err := LoadEnvironment(ctx, "definitions/nke-v1/environment-github.yaml")
	if err != nil {
		t.Fatal(err)
	}
	// This fixture isolates missing CLI signing evidence; other product outputs
	// would fail earlier on their deliberately absent payloads.
	var cliRecipes []RecipeSelection
	for _, recipe := range definition.Recipes {
		if recipe.Name == "nke-cli" {
			cliRecipes = append(cliRecipes, recipe)
		}
	}
	var cliOutputs []DefinitionOutput
	for _, output := range definition.Outputs {
		if output.Name == "nkectl" {
			cliOutputs = append(cliOutputs, output)
		}
	}
	definition.Recipes, definition.Outputs = cliRecipes, cliOutputs
	definition.Catalog.Base = filepath.Join(repo, "definitions", "nke-v1", "catalog")
	definitionPath = filepath.ToSlash(filepath.Join(caseRoot, "definition.json"))
	writeCanonicalTestFile(t, definitionPath, definition)
	environment.Publish = map[string]string{"nkectl": environment.Publish["nkectl"]}
	pin := "sha256:" + strings.Repeat("1", 64)
	environment.Signing.Mode, environment.Signing.CosignTool = factory.SigningModeCosign, "cosign"
	environment.Tools = map[string]Tool{"cosign": {Format: factory.ToolFormatTarGzip, URL: "https://example.test/cosign.tar.gz", ArchiveDigest: pin,
		ExecutablePath: "cosign", ExecutableDigest: pin}}
	environmentPath := filepath.ToSlash(filepath.Join(caseRoot, "environment.json"))
	writeCanonicalTestFile(t, environmentPath, environment)
	plan, err := Compile(ctx, definitionPath, definition, environmentPath, environment, "test")
	if err != nil {
		t.Fatal(err)
	}
	planPath := filepath.ToSlash(filepath.Join(caseRoot, "plan.json"))
	writeCanonicalTestFile(t, planPath, plan)
	workspace := filepath.ToSlash(filepath.Join(caseRoot, "workspace"))
	local := func(path string) string { return filepath.Join(workspace, strings.TrimPrefix(path, ".nkd/factory/")) }
	artifacts := map[string]factory.Artifact{}
	for _, artifact := range plan.Graph.Artifacts {
		artifacts[artifact.ID] = artifact
	}
	member, outer, result := artifacts["package/nkectl"], artifacts["package/nkectl-release"], artifacts["result/nke-cli/nkectl"]
	payloadPath := filepath.Join(caseRoot, "nkectl")
	payload := []byte("synthetic hosted output")
	if err := os.MkdirAll(filepath.Dir(payloadPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(payloadPath, payload, 0o755); err != nil {
		t.Fatal(err)
	}
	bundle, err := delivery.BuildBinaryBundle(ctx, local(member.Path), delivery.BinaryBundleRequest{OS: "linux", Arch: "amd64", Purpose: member.Name,
		Files: []delivery.BinaryFileInput{{Name: "nkectl", Path: payloadPath, InstallPath: "/usr/local/bin/nkectl", Mode: 0o755}}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = delivery.BuildPublicationRoot(ctx, local(outer.Path), delivery.PublicationRequest{Objects: []delivery.ObjectInput{{Name: member.Name,
		Kind: member.Kind, Path: local(member.Path), Layout: true, ArtifactType: member.MediaType,
		SemanticDigest: bundle.SemanticDigest, BlobDigest: bundle.RootDigest}}})
	if err != nil {
		t.Fatal(err)
	}
	writeCanonicalTestFile(t, local(result.Path), pipeline.ArtifactResult{Component: "nkectl", SHA256: pipeline.FileDigest(payload), Size: int64(len(payload))})
	other := "sha256:" + strings.Repeat("9", 64)
	verification := artifacts["verification/nkectl"]
	writeCanonicalTestFile(t, local(verification.Path), sign.VerificationRecord{SchemaVersion: sign.VerificationRecordSchemaVersion,
		Ref: "registry.example.test/unrelated@" + other, Digest: other, Identity: environment.Signing.Identity, Issuer: environment.Signing.Issuer,
		PredicateType: sign.ProvenancePredicateType, SignatureVerified: true, AttestationVerified: true,
		ObservedIdentity: strings.Trim(environment.Signing.Identity, "^$"), ObservedIssuer: environment.Signing.Issuer, Ran: [][]string{{"cosign", "verify"}}})
	writeHostedRestoreOutcomes(t, workspace, plan)
	outputPath := filepath.ToSlash(filepath.Join(caseRoot, "run.json"))
	run, err := FinalizeGenerated(ctx, definitionPath, environmentPath, planPath, "test", "hosted-attempt", workspace, outputPath)
	assertCode(t, err, nkderr.CodeProvenanceMissing)
	if run == nil || run.Status != RunFailed || run.Lane != LaneUnqualified || len(run.Observed) != 0 {
		t.Fatalf("run = %+v", run)
	}
	if _, err := os.Stat(outputPath); err != nil {
		t.Fatalf("failed Run was not committed: %v", err)
	}
}

func TestFinalizeGeneratedRequiresExactStaticHostedEvidenceClosure(t *testing.T) {
	for _, mode := range []string{"complete", "truncated-observation", "missing-sbom-document", "failed-provider-job", "failed-restore"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			t.Chdir(t.TempDir())
			definition, environment := testDefinition(t), testEnvironment()
			definition.Clusters = nil
			environment.Publish, environment.Destinations, environment.Signing = nil, nil, nil
			writeCanonicalTestFile(t, "definition.json", definition)
			writeCanonicalTestFile(t, "environment.json", environment)
			plan, err := CompileFiles(ctx, "definition.json", "environment.json", "test")
			if err != nil {
				t.Fatal(err)
			}
			writeCanonicalTestFile(t, "plan.json", plan)
			artifacts := map[string]factory.Artifact{}
			for _, artifact := range plan.Graph.Artifacts {
				artifacts[artifact.ID] = artifact
			}
			var observationPath, sbomDocument string
			for _, node := range plan.Graph.Nodes {
				if node.Kind != factory.NodeVerify {
					continue
				}
				for _, id := range node.Inputs {
					artifact := artifacts[id]
					payload := []byte("synthetic producer payload for " + artifact.Component)
					if artifact.Kind == "producer-result" {
						writeCanonicalTestFile(t, artifact.Path, pipeline.ArtifactResult{Component: artifact.Component, OutputType: "binary",
							Path: artifact.Component, SHA256: pipeline.FileDigest(payload), Size: int64(len(payload))})
					} else {
						if err := os.MkdirAll(filepath.Dir(artifact.Path), 0o700); err != nil {
							t.Fatal(err)
						}
						if err := os.WriteFile(artifact.Path, payload, 0o755); err != nil {
							t.Fatal(err)
						}
					}
				}
				if err := factory.ExecuteNode(ctx, plan, node.ID, nil, nil); err != nil {
					t.Fatal(err)
				}
				observationPath = artifacts[node.Outputs[0]].Path
			}
			for _, node := range plan.Graph.Nodes {
				if node.Kind != factory.NodeSBOM {
					continue
				}
				if err := factory.ExecuteNode(ctx, plan, node.ID, nil, nil); err != nil {
					t.Fatal(err)
				}
				matches, err := filepath.Glob(filepath.Join(artifacts[node.Outputs[0]].Path, "*.cdx.json"))
				if err != nil || len(matches) == 0 {
					t.Fatalf("SBOM fixture: %v %v", matches, err)
				}
				sbomDocument = matches[0]
			}
			if err := factory.ExecuteNode(ctx, plan, "package/cli", nil, nil); err != nil {
				t.Fatal(err)
			}
			writeHostedRestoreOutcomes(t, ".nkd/factory", plan)
			switch mode {
			case "truncated-observation":
				raw, err := os.ReadFile(observationPath)
				if err != nil {
					t.Fatal(err)
				}
				var observation struct {
					SchemaVersion string               `json:"schemaVersion"`
					Qualification factory.EvidenceTier `json:"qualification"`
					RecipeDigest  string               `json:"recipeDigest"`
					Inputs        []struct {
						ArtifactID string `json:"artifactId"`
						Kind       string `json:"kind"`
						Digest     string `json:"digest"`
					} `json:"inputs"`
				}
				if len(raw) > 0 && raw[len(raw)-1] == '\n' {
					raw = raw[:len(raw)-1]
				}
				if err := canonical.UnmarshalExact(raw, &observation); err != nil {
					t.Fatal(err)
				}
				observation.Inputs = observation.Inputs[:1]
				writeCanonicalTestFile(t, observationPath, observation)
			case "missing-sbom-document":
				if err := os.Rename(sbomDocument, sbomDocument+".withheld"); err != nil {
					t.Fatal(err)
				}
			case "failed-provider-job":
				path := factory.HostedRestoreOutcomePath(".nkd/factory", "package/cli")
				if err := os.WriteFile(path, []byte("job-failed\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "failed-restore":
				path := factory.HostedRestoreOutcomePath(".nkd/factory", "package/cli")
				if err := os.WriteFile(path, []byte("restore-failed\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			run, err := FinalizeGenerated(ctx, "definition.json", "environment.json", "plan.json", "test", "attempt", ".nkd/factory", ".nkd/run.json")
			if mode == "complete" {
				if err != nil || run == nil || run.Status != RunComplete || run.Lane != LaneUnqualified {
					t.Fatalf("positive fixture failed: run=%+v err=%v", run, err)
				}
			} else if err == nil || run == nil || run.Status != RunFailed {
				t.Fatalf("incomplete %s evidence accepted: run=%+v err=%v", mode, run, err)
			}
		})
	}
}

func TestFinalizeGeneratedCompletesPipelineRunFromExactHostedClosure(t *testing.T) {
	ctx := context.Background()
	t.Chdir(t.TempDir())
	definition, environment := testDefinition(t), testEnvironment()
	definition.Clusters = nil
	pin := "sha256:" + strings.Repeat("1", 64)
	environment.Signing = &Signing{Mode: factory.SigningModeCosign, Identity: "^builder$", Issuer: "issuer", CosignTool: "cosign"}
	environment.Tools = map[string]Tool{"cosign": {Format: factory.ToolFormatTarGzip, URL: "https://example.test/cosign.tar.gz", ArchiveDigest: pin,
		ExecutablePath: "cosign", ExecutableDigest: pin}}
	writeCanonicalTestFile(t, "definition.json", definition)
	writeCanonicalTestFile(t, "environment.json", environment)
	plan, err := CompileFiles(ctx, "definition.json", "environment.json", "test")
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
		for _, inputID := range node.Inputs {
			artifact := artifacts[inputID]
			if artifact.Kind == "producer-result" {
				continue
			}
			payload := []byte("exact hosted " + artifact.Component)
			if err := os.MkdirAll(filepath.Dir(artifact.Path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(artifact.Path, payload, 0o755); err != nil {
				t.Fatal(err)
			}
			result := artifacts["result/"+artifact.RecipeTarget+"/"+artifact.Component]
			writeCanonicalTestFile(t, result.Path, pipeline.ArtifactResult{Component: artifact.Component,
				OutputType: "binary", Path: artifact.Component, SHA256: pipeline.FileDigest(payload), Size: int64(len(payload))})
		}
	}
	for _, kind := range []string{factory.NodeVerify, factory.NodeSBOM} {
		for _, node := range plan.Graph.Nodes {
			if node.Kind == kind {
				if err := factory.ExecuteNode(ctx, plan, node.ID, nil, nil); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	for _, id := range []string{"package/cli", "package/cli-release"} {
		if err := factory.ExecuteNode(ctx, plan, id, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	envelope, err := delivery.Inspect(ctx, artifacts["package/cli-release"].Path)
	if err != nil {
		t.Fatal(err)
	}
	reference := "registry.example.test/product@" + envelope.RootDigest
	if err := os.MkdirAll(filepath.Dir(artifacts["publication/cli"].Path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(artifacts["publication/cli"].Path, []byte(reference+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	instant := time.Date(2026, time.September, 8, 12, 0, 0, 0, time.UTC)
	predicate, err := factory.ProductionPredicate(plan, hostedTestRun{started: instant, finished: instant.Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	predicate = append(predicate, '\n')
	if err := os.MkdirAll(filepath.Dir(artifacts["predicate/cli"].Path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(artifacts["predicate/cli"].Path, predicate, 0o600); err != nil {
		t.Fatal(err)
	}
	writeCanonicalTestFile(t, artifacts["signature/cli"].Path, sign.SigningRecord{SchemaVersion: sign.SigningRecordSchemaVersion,
		Ref: reference, Digest: envelope.RootDigest, PredicateSHA256: pipeline.FileDigest(predicate), PredicateType: sign.ProvenancePredicateType,
		Identity: environment.Signing.Identity, Issuer: environment.Signing.Issuer, Ran: [][]string{{"cosign", "sign"}}})
	writeCanonicalTestFile(t, artifacts["verification/cli"].Path, sign.VerificationRecord{SchemaVersion: sign.VerificationRecordSchemaVersion,
		Ref: reference, Digest: envelope.RootDigest, Identity: environment.Signing.Identity, Issuer: environment.Signing.Issuer,
		PredicateType: sign.ProvenancePredicateType, SignatureVerified: true, AttestationVerified: true,
		ObservedIdentity: "builder", ObservedIssuer: environment.Signing.Issuer, Ran: [][]string{{"cosign", "verify"}}})
	writeHostedRestoreOutcomes(t, ".nkd/factory", plan)
	run, err := FinalizeGenerated(ctx, "definition.json", "environment.json", "plan.json", "test", "pipeline-attempt", ".nkd/factory", ".nkd/run.json")
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != RunComplete || run.Lane != LanePipeline || len(run.Observed) != 1 || run.Observed[0].Digest == envelope.RootDigest {
		t.Fatalf("pipeline Run = %+v", run)
	}
	var forged provenance.Predicate
	if err := canonical.UnmarshalExact(bytes.TrimSuffix(predicate, []byte{'\n'}), &forged); err != nil {
		t.Fatal(err)
	}
	forged.BuildDefinition.ExternalParameters["runPlanReference"] = "unrelated-definition"
	forgedRaw, err := provenance.MarshalProduction(forged)
	if err != nil {
		t.Fatal(err)
	}
	forgedRaw = append(forgedRaw, '\n')
	if err := os.WriteFile(artifacts["predicate/cli"].Path, forgedRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	writeCanonicalTestFile(t, artifacts["signature/cli"].Path, sign.SigningRecord{SchemaVersion: sign.SigningRecordSchemaVersion,
		Ref: reference, Digest: envelope.RootDigest, PredicateSHA256: pipeline.FileDigest(forgedRaw), PredicateType: sign.ProvenancePredicateType,
		Identity: environment.Signing.Identity, Issuer: environment.Signing.Issuer, Ran: [][]string{{"cosign", "sign"}}})
	failed, err := FinalizeGenerated(ctx, "definition.json", "environment.json", "plan.json", "test", "forged-predicate", ".nkd/factory", ".nkd/failed-run.json")
	if err == nil || failed == nil || failed.Status != RunFailed {
		t.Fatalf("hosted finalizer accepted a well-formed predicate for another Definition: run=%+v err=%v", failed, err)
	}
	assertCode(t, err, nkderr.CodeVerifyFailed)
}

type hostedTestRun struct{ started, finished time.Time }

func (hostedTestRun) BuilderID() string       { return "builder" }
func (hostedTestRun) InvocationID() string    { return "https://example.test/runs/1/attempts/1" }
func (r hostedTestRun) StartedOn() time.Time  { return r.started }
func (r hostedTestRun) FinishedOn() time.Time { return r.finished }

var _ provenance.RunContext = hostedTestRun{}

func writeHostedRestoreOutcomes(t *testing.T, workspace string, plan *factory.Plan) {
	t.Helper()
	for _, artifact := range plan.Graph.Artifacts {
		path := factory.HostedRestoreOutcomePath(workspace, artifact.ID)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("restored:success\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func writeCanonicalTestFile(t *testing.T, path string, value any) {
	t.Helper()
	raw, err := canonical.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}
