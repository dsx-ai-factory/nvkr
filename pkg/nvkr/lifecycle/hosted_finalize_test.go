// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/provenance"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sign"
	"sigs.k8s.io/yaml"
)

func TestFinalizeGeneratedRejectsEmptyWorkspaceBeforeInputsOrOutput(t *testing.T) {
	for _, definitionPath := range []string{"", "missing-definition.yaml"} {
		t.Run(definitionPath, func(t *testing.T) {
			outputPath := filepath.Join(t.TempDir(), "run.json")
			run, err := FinalizeGenerated(t.Context(), definitionPath, "missing-environment.yaml", "missing-plan.json", "test", "attempt", "", outputPath)
			assertCode(t, err, nvkrerr.CodeUsage)
			if run != nil {
				t.Fatal("empty workspace must not create a Run")
			}
			if _, err := os.Stat(outputPath); !os.IsNotExist(err) {
				t.Fatalf("unexpected output after failed admission: %v", err)
			}
		})
	}
}

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
	caseRoot := filepath.ToSlash(filepath.Join(".nvkr", "lifecycle-finalizer-test"))
	_ = os.RemoveAll(caseRoot)
	defer func() { _ = os.RemoveAll(caseRoot) }()
	ctx := context.Background()
	definitionPath := "docs/examples/nvke-tutorial/definition.yaml"
	definition, err := LoadDefinition(ctx, definitionPath)
	if err != nil {
		t.Fatal(err)
	}
	environment, err := LoadEnvironment(ctx, "docs/examples/nvke-tutorial/environment-github.yaml")
	if err != nil {
		t.Fatal(err)
	}
	// This fixture isolates missing CLI signing evidence; other product outputs
	// would fail earlier on their deliberately absent payloads.
	var cliOutputs []DefinitionOutput
	for _, output := range definition.Outputs {
		if output.Name == "nvkectl" {
			cliOutputs = append(cliOutputs, output)
		}
	}
	definition.Outputs = cliOutputs
	definition.Catalog.Base = filepath.Join(repo, "inputs/catalogs/nvke/module-tags")
	definitionPath = filepath.ToSlash(filepath.Join(caseRoot, "definition.json"))
	writeCanonicalTestFile(t, definitionPath, definition)
	environment.Signing.Mode = factory.SigningModeCosign
	environmentPath := filepath.ToSlash(filepath.Join(caseRoot, "environment.json"))
	writeCanonicalTestFile(t, environmentPath, environment)
	plan, err := Compile(ctx, definitionPath, definition, environmentPath, environment, "test")
	if err != nil {
		t.Fatal(err)
	}
	planPath := filepath.ToSlash(filepath.Join(caseRoot, "plan.json"))
	writeCanonicalTestFile(t, planPath, plan)
	workspace := filepath.ToSlash(filepath.Join(caseRoot, "workspace"))
	local := func(path string) string { return filepath.Join(workspace, strings.TrimPrefix(path, ".nvkr/factory/")) }
	artifacts := map[string]factory.Artifact{}
	for _, artifact := range plan.Graph.Artifacts {
		artifacts[artifact.ID] = artifact
	}
	member, outer, result := artifacts["package/nvkectl"], artifacts["package/release"], artifacts["result/nvkectl/nvkectl"]
	payloadPath := local(artifacts["artifact/nvkectl/nvkectl"].Path)
	payload := []byte("synthetic hosted output")
	if err := os.MkdirAll(filepath.Dir(payloadPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(payloadPath, payload, 0o755); err != nil {
		t.Fatal(err)
	}
	bundle, err := delivery.BuildBinaryBundle(ctx, local(member.Path), delivery.BinaryBundleRequest{OS: "linux", Arch: "amd64", Purpose: member.Name,
		Files: []delivery.BinaryFileInput{{Name: "nvkectl", Path: payloadPath, InstallPath: "/usr/local/bin/nvkectl", Mode: 0o755}}})
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := delivery.PayloadDescriptor(ctx, local(member.Path))
	if err != nil {
		t.Fatal(err)
	}
	request := testReleaseRequest(t, []Artifact{{Name: member.Name, Kind: OutputExecutable, Digest: descriptor.Digest, BuildDigest: "sha256:" + repeat("5", 64), Components: []string{"nvkectl"}}}, []delivery.ObjectInput{{Name: member.Name, Kind: member.Kind, Path: local(member.Path), Layout: true, ArtifactType: member.MediaType, SemanticDigest: bundle.SemanticDigest, BlobDigest: bundle.RootDigest}}, map[string]string{"nvkectl": pipeline.FileDigest(payload)})
	_, err = delivery.BuildPublicationRoot(ctx, local(outer.Path), request)
	if err != nil {
		t.Fatal(err)
	}
	writeCanonicalTestFile(t, local(result.Path), fixtureProducerResult(t, plan, result, payloadPath))
	other := "sha256:" + strings.Repeat("9", 64)
	verification := artifacts["verification/release"]
	writeCanonicalTestFile(t, local(verification.Path), sign.VerificationRecord{SchemaVersion: sign.VerificationRecordSchemaVersion,
		Ref: "registry.example.test/unrelated@" + other, Digest: other, Identity: environment.Signing.Identity, Issuer: environment.Signing.Issuer,
		PredicateType: sign.ProvenancePredicateType, SignatureVerified: true, AttestationVerified: true,
		ObservedIdentity: strings.Trim(environment.Signing.Identity, "^$"), ObservedIssuer: environment.Signing.Issuer, Ran: [][]string{{"cosign", "verify"}}})
	writeHostedRestoreOutcomes(t, workspace, plan)
	outputPath := filepath.ToSlash(filepath.Join(caseRoot, "run.json"))
	run, err := FinalizeGenerated(ctx, definitionPath, environmentPath, planPath, "test", "hosted-attempt", workspace, outputPath)
	assertCode(t, err, nvkrerr.CodeProvenanceMissing)
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
			environment.Publication, environment.Signing = nil, nil
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
						input := artifacts["artifact/"+artifact.BuildTarget+"/"+artifact.Component]
						writeCanonicalTestFile(t, artifact.Path, fixtureProducerResult(t, plan, artifact, input.Path))
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
			writeHostedRestoreOutcomes(t, ".nvkr/factory", plan)
			switch mode {
			case "truncated-observation":
				raw, err := os.ReadFile(observationPath)
				if err != nil {
					t.Fatal(err)
				}
				var observation struct {
					SchemaVersion string               `json:"schemaVersion"`
					Qualification factory.EvidenceTier `json:"qualification"`
					BuildDigest   string               `json:"buildDigest"`
					BuildTarget   string               `json:"buildTarget"`
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
				path := factory.HostedRestoreOutcomePath(".nvkr/factory", "package/cli")
				if err := os.WriteFile(path, []byte("job-failed\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "failed-restore":
				path := factory.HostedRestoreOutcomePath(".nvkr/factory", "package/cli")
				if err := os.WriteFile(path, []byte("restore-failed\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			run, err := FinalizeGenerated(ctx, "definition.json", "environment.json", "plan.json", "test", "attempt", ".nvkr/factory", ".nvkr/run.json")
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
	environment.Signing = &Signing{Mode: factory.SigningModeCosign, Identity: "^builder$", Issuer: "issuer"}
	addTestSigningTool(t, &definition)
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
			result := artifacts["result/"+artifact.BuildTarget+"/"+artifact.Component]
			writeCanonicalTestFile(t, result.Path, fixtureProducerResult(t, plan, artifact, artifact.Path))
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
	for _, id := range []string{"package/cli", "package/release"} {
		if err := factory.ExecuteNode(ctx, plan, id, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	if prepared, ok := artifacts["prepared/release"]; ok {
		if _, err := delivery.AttestPreparedEvidence(ctx, prepared.Path, artifacts["package/release"].Path, fixtureSBOMAttestation(t)); err != nil {
			t.Fatal(err)
		}
	}
	envelope, err := delivery.Inspect(ctx, artifacts["package/release"].Path)
	if err != nil {
		t.Fatal(err)
	}
	reference := "registry.example.test/product/releases@" + envelope.RootDigest
	if err := os.MkdirAll(filepath.Dir(artifacts["publication/release"].Path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(artifacts["publication/release"].Path, []byte(reference+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	instant := time.Date(2026, time.September, 8, 12, 0, 0, 0, time.UTC)
	predicate, err := factory.ProductionPredicate(plan, hostedTestRun{started: instant, finished: instant.Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	predicate = append(predicate, '\n')
	if err := os.MkdirAll(filepath.Dir(artifacts["predicate/release"].Path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(artifacts["predicate/release"].Path, predicate, 0o600); err != nil {
		t.Fatal(err)
	}
	writeCanonicalTestFile(t, artifacts["signature/release"].Path, sign.SigningRecord{SchemaVersion: sign.SigningRecordSchemaVersion,
		Ref: reference, Digest: envelope.RootDigest, PredicateSHA256: pipeline.FileDigest(predicate), PredicateType: sign.ProvenancePredicateType,
		Identity: environment.Signing.Identity, Issuer: environment.Signing.Issuer, Ran: [][]string{{"cosign", "sign"}}})
	writeCanonicalTestFile(t, artifacts["verification/release"].Path, sign.VerificationRecord{SchemaVersion: sign.VerificationRecordSchemaVersion,
		Ref: reference, Digest: envelope.RootDigest, Identity: environment.Signing.Identity, Issuer: environment.Signing.Issuer,
		PredicateType: sign.ProvenancePredicateType, SignatureVerified: true, AttestationVerified: true,
		ObservedIdentity: "builder", ObservedIssuer: environment.Signing.Issuer, Ran: [][]string{{"cosign", "verify"}}})
	writeHostedRestoreOutcomes(t, ".nvkr/factory", plan)
	run, err := FinalizeGenerated(ctx, "definition.json", "environment.json", "plan.json", "test", "pipeline-attempt", ".nvkr/factory", ".nvkr/run.json")
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
	if err := os.WriteFile(artifacts["predicate/release"].Path, forgedRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	writeCanonicalTestFile(t, artifacts["signature/release"].Path, sign.SigningRecord{SchemaVersion: sign.SigningRecordSchemaVersion,
		Ref: reference, Digest: envelope.RootDigest, PredicateSHA256: pipeline.FileDigest(forgedRaw), PredicateType: sign.ProvenancePredicateType,
		Identity: environment.Signing.Identity, Issuer: environment.Signing.Issuer, Ran: [][]string{{"cosign", "sign"}}})
	failed, err := FinalizeGenerated(ctx, "definition.json", "environment.json", "plan.json", "test", "forged-predicate", ".nvkr/factory", ".nvkr/failed-run.json")
	if err == nil || failed == nil || failed.Status != RunFailed {
		t.Fatalf("hosted finalizer accepted a well-formed predicate for another Definition: run=%+v err=%v", failed, err)
	}
	assertCode(t, err, nvkrerr.CodeVerifyFailed)
}

type hostedTestRun struct{ started, finished time.Time }

func (hostedTestRun) BuilderID() string       { return "builder" }
func (hostedTestRun) InvocationID() string    { return "https://example.test/runs/1/attempts/1" }
func (r hostedTestRun) StartedOn() time.Time  { return r.started }
func (r hostedTestRun) FinishedOn() time.Time { return r.finished }

var _ provenance.RunContext = hostedTestRun{}

// fixtureProducerResult describes bytes materialized by a test for one exact
// planned producer. These fixture records grant no execution qualification.
func fixtureProducerResult(t *testing.T, plan *factory.Plan, artifact factory.Artifact, payloadPath string) pipeline.ArtifactResult {
	t.Helper()
	var producer ir.Node
	matches := 0
	for _, build := range plan.Builds {
		if build.Name != artifact.BuildTarget {
			continue
		}
		for _, node := range build.Graph.Nodes {
			if build.Name+"/"+node.ID == artifact.Producer && node.Component == artifact.Component && (node.Kind == ir.KindBuild || node.Kind == ir.KindPull) {
				producer = node
				matches++
			}
		}
	}
	if matches != 1 {
		t.Fatalf("fixture artifact %q has %d exact producers", artifact.ID, matches)
	}
	raw, err := os.ReadFile(payloadPath)
	if err != nil {
		t.Fatal(err)
	}
	result := pipeline.ArtifactResult{Component: producer.Component, OutputType: producer.OutputType,
		Qualification: pipeline.ArtifactUnqualified, Toolchain: producer.Toolchain, BuilderType: producer.BuilderType,
		Path: producer.ArtifactPath, SHA256: pipeline.FileDigest(raw), Size: int64(len(raw))}
	if producer.Kind == ir.KindPull {
		if result.SHA256 != producer.Digest {
			t.Fatalf("fixture payload for %q does not match its pinned acquisition", artifact.ID)
		}
		result.EngineClass = ir.EnginePrebuiltAcquire
	}
	return result
}

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

func addTestSigningTool(t *testing.T, definition *Definition) {
	t.Helper()
	// Use a private fixture catalog; no source tree or real lock is changed.
	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS(definition.Catalog.Base)); err != nil {
		t.Fatal(err)
	}
	definition.Catalog.Base = dir
	registryPath := filepath.Join(dir, "registry.yaml")
	raw, err := os.ReadFile(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	var registry map[string]any
	if err := yaml.UnmarshalStrict(raw, &registry); err != nil {
		t.Fatal(err)
	}
	registry["sources"].(map[string]any)["cosign"] = map[string]any{"repo": "https://example.test/cosign", "tool": map[string]any{"format": "raw", "url": "https://example.test/{{.Tag}}/cosign", "platform": "linux/amd64", "executablePath": "cosign"}}
	encoded, err := yaml.Marshal(registry)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(registryPath, encoded, 0600); err != nil {
		t.Fatal(err)
	}
	lock, err := catalog.LoadLockContext(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	pin := "sha256:" + strings.Repeat("1", 64)
	lock.Pins["cosign"] = catalog.Pin{Tag: "v1.0.0", Commit: strings.Repeat("1", 40), TreeHash: strings.Repeat("2", 40), SourceDateEpoch: 1, Tool: &catalog.ToolPin{Format: "raw", URL: "https://example.test/v1.0.0/cosign", Platform: "linux/amd64", ExecutablePath: "cosign", ArchiveDigest: pin, ExecutableDigest: pin}}
	if err := catalog.WriteLockContext(t.Context(), dir, lock); err != nil {
		t.Fatal(err)
	}
	definition.CosignTool = "cosign"
}
