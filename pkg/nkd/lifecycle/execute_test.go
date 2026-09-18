package lifecycle

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/delivery"
	nkderr "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/factory"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/pipeline"
)

func TestObserveOutputReconstructsExactBundleAndPublicationMembership(t *testing.T) {
	workspace := t.TempDir()
	componentPath := filepath.Join(workspace, "component")
	if err := os.WriteFile(componentPath, []byte("exact component bytes"), 0o755); err != nil {
		t.Fatal(err)
	}
	bundlePath := filepath.Join(workspace, "outputs", "cli-bundle")
	if err := os.MkdirAll(filepath.Dir(bundlePath), 0o700); err != nil {
		t.Fatal(err)
	}
	bundleResult, err := delivery.BuildBinaryBundle(context.Background(), bundlePath, delivery.BinaryBundleRequest{
		OS: "linux", Arch: "amd64", Purpose: "cli-bundle",
		Files: []delivery.BinaryFileInput{{Name: "kubectl", Path: componentPath, InstallPath: "/usr/local/bin/kubectl", Mode: 0o755}},
	})
	if err != nil {
		t.Fatal(err)
	}
	producerPath := filepath.Join(workspace, "results", "kubectl.json")
	if err := os.MkdirAll(filepath.Dir(producerPath), 0o700); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(componentPath)
	if err != nil {
		t.Fatal(err)
	}
	producer := pipeline.ArtifactResult{Component: "kubectl", SHA256: pipeline.FileDigest([]byte("exact component bytes")), Size: info.Size()}
	raw, err := canonical.Marshal(producer)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(producerPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	rootPath := filepath.Join(workspace, "outputs", "cli")
	descriptor, err := delivery.PayloadDescriptor(context.Background(), bundlePath)
	if err != nil {
		t.Fatal(err)
	}
	request := testReleaseRequest(t, []Artifact{{Name: "cli-bundle", Kind: OutputExecutable, Digest: descriptor.Digest, BuildDigest: "sha256:" + repeat("5", 64), Components: []string{"kubectl"}}}, []delivery.ObjectInput{{Name: "cli-bundle", Kind: factory.PackageBinaryBundle, Path: bundlePath, Layout: true, ArtifactType: delivery.MediaBinaryBundle, SemanticDigest: bundleResult.SemanticDigest, BlobDigest: bundleResult.RootDigest}}, map[string]string{"kubectl": producer.SHA256})
	_, err = delivery.BuildPublicationRoot(context.Background(), rootPath, request)
	if err != nil {
		t.Fatal(err)
	}
	artifacts := map[string]factory.Artifact{
		"package/cli-bundle": {ID: "package/cli-bundle", Name: "cli-bundle", Kind: factory.PackageBinaryBundle, MediaType: delivery.MediaBinaryBundle, Path: ".nkd/factory/outputs/cli-bundle"},
		"package/cli":        {ID: "package/cli", Name: "cli", Kind: factory.PackageOCI, Path: ".nkd/factory/outputs/cli"},
		"result/cli/kubectl": {ID: "result/cli/kubectl", Component: "kubectl", BuildTarget: "cli", Path: ".nkd/factory/results/kubectl.json"},
	}
	slot := requiredOutput{Name: "cli", Kind: OutputExecutable, ArtifactID: "package/cli", MemberArtifactID: "package/cli-bundle",
		BuildDigest: "sha256:" + repeat("5", 64), Components: []string{"kubectl"}, ComponentResults: []string{"result/cli/kubectl"}}
	observed, err := observeOutput(context.Background(), workspace, slot, artifacts, nil)
	if err != nil {
		t.Fatal(err)
	}
	if observed.Digest != descriptor.Digest || !equalStrings(observed.Components, []string{"kubectl"}) {
		t.Fatalf("observed = %+v", observed)
	}

	t.Run("producer substitution", func(t *testing.T) {
		changed := producer
		changed.SHA256 = "sha256:" + repeat("9", 64)
		raw, marshalErr := canonical.Marshal(changed)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		if writeErr := os.WriteFile(producerPath, raw, 0o600); writeErr != nil {
			t.Fatal(writeErr)
		}
		_, observeErr := observeOutput(context.Background(), workspace, slot, artifacts, nil)
		assertCode(t, observeErr, nkderr.CodeVerifyFailed)
	})
}

func TestObserveAPTRejectsSubstitutedProducerBytes(t *testing.T) {
	ctx := context.Background()
	definition, environment := testDefinition(t), testEnvironment()
	definition.Outputs[0].Format = OutputAPT
	definition.Outputs[0].APT = &APTOutput{Distribution: "stable", Component: "main"}
	environment.Publication = nil
	plan, err := Compile(ctx, "definition.yaml", definition, "environment.yaml", environment, "test")
	if err != nil {
		t.Fatal(err)
	}
	run, err := PrepareRun(definition, environment, plan, "attempt")
	if err != nil {
		t.Fatal(err)
	}
	artifacts := map[string]factory.Artifact{}
	for _, artifact := range plan.Graph.Artifacts {
		artifacts[artifact.ID] = artifact
	}
	t.Chdir(t.TempDir())
	payload := []byte("synthetic kubectl original bytes")
	input := artifacts["artifact/cli/kubectl"]
	if err := os.MkdirAll(filepath.Dir(input.Path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(input.Path, payload, 0o755); err != nil {
		t.Fatal(err)
	}
	resultPath := artifacts["result/cli/kubectl"].Path
	result := pipeline.ArtifactResult{Component: "kubectl", SHA256: pipeline.FileDigest(payload), Size: int64(len(payload))}
	writeCanonicalTestFile(t, resultPath, result)
	if err := factory.ExecuteNode(ctx, plan, "package/cli", nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := observeOutput(ctx, ".nkd/factory", run.Expected[0], artifacts, plan); err != nil {
		t.Fatalf("positive APT fixture failed: %v", err)
	}
	result.SHA256 = "sha256:" + repeat("9", 64)
	writeCanonicalTestFile(t, resultPath, result)
	if _, err := observeOutput(ctx, ".nkd/factory", run.Expected[0], artifacts, plan); err == nil {
		t.Fatal("APT payload accepted against substituted producer digest")
	} else {
		assertCode(t, err, nkderr.CodeVerifyFailed)
	}

	changedPayload := []byte("synthetic kubectl consistently substituted after packaging")
	if err := os.WriteFile(input.Path, changedPayload, 0o755); err != nil {
		t.Fatal(err)
	}
	result.SHA256 = pipeline.FileDigest(changedPayload)
	result.Size = int64(len(changedPayload))
	writeCanonicalTestFile(t, resultPath, result)
	if _, err := observeOutput(ctx, ".nkd/factory", run.Expected[0], artifacts, plan); err == nil {
		t.Fatal("stale APT repository accepted after producer bytes and result changed together")
	} else {
		assertCode(t, err, nkderr.CodeVerifyFailed)
	}
}

func TestObserveHelmReproducesPayloadAcrossWorkspaceRelocation(t *testing.T) {
	ctx := context.Background()
	repo, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(repo)
	definitionPath, environmentPath := "testdata/examples/ai-factory/definition.yaml", "testdata/examples/ai-factory/environment-local.yaml"
	definition, err := LoadDefinition(ctx, definitionPath)
	if err != nil {
		t.Fatal(err)
	}
	environment, err := LoadEnvironment(ctx, environmentPath)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := Compile(ctx, definitionPath, definition, environmentPath, environment, "test")
	if err != nil {
		t.Fatal(err)
	}
	run, err := PrepareRun(definition, environment, plan, "attempt")
	if err != nil {
		t.Fatal(err)
	}
	var slot requiredOutput
	for _, expected := range run.Expected {
		if expected.Name == "gpu-runtime" {
			slot = expected
		}
	}
	if slot.Name == "" {
		t.Fatal("missing Helm output")
	}
	artifacts := map[string]factory.Artifact{}
	for _, artifact := range plan.Graph.Artifacts {
		artifacts[artifact.ID] = artifact
	}
	t.Chdir(t.TempDir())
	inputPath := artifacts["artifact/gpu-runtime/aicr-bundle"].Path
	if err := os.MkdirAll(inputPath, 0o700); err != nil {
		t.Fatal(err)
	}
	chartPath := filepath.Join(inputPath, "Chart.yaml")
	if err := os.WriteFile(chartPath, []byte("apiVersion: v2\nname: gpu-runtime\nversion: 1.0.0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := factory.ExecuteNode(ctx, plan, "package/gpu-runtime", nil, nil); err != nil {
		t.Fatal(err)
	}
	before, err := observeOutput(ctx, ".nkd/factory", slot, artifacts, plan)
	if err != nil {
		t.Fatalf("canonical workspace observation: %v", err)
	}
	if err := os.MkdirAll(".nkd/runs", 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(".nkd/factory", ".nkd/runs/attempt"); err != nil {
		t.Fatal(err)
	}
	after, err := observeOutput(ctx, ".nkd/runs/attempt", slot, artifacts, plan)
	if err != nil {
		t.Fatalf("relocated workspace observation: %v", err)
	}
	if before.Digest != after.Digest {
		t.Fatalf("workspace changed Helm payload identity: %s != %s", before.Digest, after.Digest)
	}
	relocatedInput := filepath.Join(".nkd/runs/attempt", filepath.FromSlash(strings.TrimPrefix(filepath.ToSlash(inputPath), ".nkd/factory/")))
	if err := os.WriteFile(filepath.Join(relocatedInput, "Chart.yaml"), []byte("changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := observeOutput(ctx, ".nkd/runs/attempt", slot, artifacts, plan); err == nil {
		t.Fatal("stale Helm payload accepted after AICR input changed")
	} else {
		assertCode(t, err, nkderr.CodeVerifyFailed)
	}
}

func TestCompleteGPUPublicationClosureAcrossFormatsAndArchitectures(t *testing.T) {
	ctx := context.Background()
	repo, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	for _, arch := range []string{"amd64", "arm64"} {
		t.Run(arch, func(t *testing.T) {
			t.Chdir(repo)
			definitionPath := "testdata/examples/ai-factory/definition.yaml"
			if arch == "arm64" {
				definitionPath = "testdata/examples/ai-factory/definition-arm64.yaml"
			}
			definition, err := LoadDefinition(ctx, definitionPath)
			if err != nil {
				t.Fatal(err)
			}
			environment, err := LoadEnvironment(ctx, "testdata/examples/ai-factory/environment-local.yaml")
			if err != nil {
				t.Fatal(err)
			}
			plan, err := Compile(ctx, definitionPath, definition, "testdata/examples/ai-factory/environment-local.yaml", environment, "test")
			if err != nil {
				t.Fatal(err)
			}
			t.Chdir(t.TempDir())
			artifacts := map[string]factory.Artifact{}
			for _, artifact := range plan.Graph.Artifacts {
				artifacts[artifact.ID] = artifact
				payload := []byte("synthetic GPU producer " + artifact.Component)
				switch {
				case artifact.Kind == "producer-result":
					writeCanonicalTestFile(t, artifact.Path, pipeline.ArtifactResult{Component: artifact.Component,
						OutputType: "binary", Path: artifact.Component, SHA256: pipeline.FileDigest(payload), Size: int64(len(payload))})
				case strings.HasSuffix(artifact.ID, "/aicr-bundle"):
					if err := os.MkdirAll(artifact.Path, 0o700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(artifact.Path, "Chart.yaml"), []byte("apiVersion: v2\nname: gpu\nversion: 1.0.0\n"), 0o600); err != nil {
						t.Fatal(err)
					}
				case strings.HasPrefix(artifact.ID, "artifact/"):
					if err := os.MkdirAll(filepath.Dir(artifact.Path), 0o700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(artifact.Path, payload, 0o755); err != nil {
						t.Fatal(err)
					}
				}
			}
			for _, kind := range []string{factory.NodeVerify, factory.NodeSBOM} {
				for _, node := range plan.Graph.Nodes {
					if node.Kind == kind {
						if err := factory.ExecuteNode(ctx, plan, node.ID, nil, nil); err != nil {
							t.Fatalf("%s: %v", node.ID, err)
						}
					}
				}
			}
			for _, nodeID := range []string{"package/gpu-runtime", "package/worker-apt", "package/worker-binaries", "package/release"} {
				if err := factory.ExecuteNode(ctx, plan, nodeID, nil, nil); err != nil {
					t.Fatalf("%s: %v", nodeID, err)
				}
			}
			layout := artifacts["package/release"].Path
			inspection, err := delivery.Inspect(ctx, layout)
			if err != nil {
				t.Fatal(err)
			}
			shared := 0
			for _, object := range inspection.Root.Objects {
				if object.Evidence != nil && len(object.Evidence.Outputs) == 2 {
					shared++
				}
				if object.Kind == "sbom-index" || object.Kind == "lifecycle-binding" {
					t.Fatal("redundant release wrapper", object.Kind)
				}
			}
			if shared == 0 {
				t.Fatal("shared build SBOMs were not deduplicated across APT and binary outputs")
			}
			outputs, subject, err := artifactsFromBinding(ctx, layout, inspection)
			if err != nil {
				t.Fatal(err)
			}
			if len(outputs) != 3 || subject == "" {
				t.Fatalf("incomplete GPU closure: outputs=%+v subject=%s", outputs, subject)
			}
			prepared, err := PrepareRun(definition, environment, plan, "gpu-closure")
			if err != nil {
				t.Fatal(err)
			}
			observed := make([]Artifact, 0, len(prepared.Expected))
			for _, slot := range prepared.Expected {
				artifact, err := observeOutput(ctx, ".nkd/factory", slot, artifacts, plan)
				if err != nil {
					t.Fatal(err)
				}
				observed = append(observed, artifact)
			}
			bound, err := bindObserved(prepared, observed)
			if err != nil {
				t.Fatal(err)
			}
			bound, err = bindReleaseContent(plan, bound)
			if err != nil {
				t.Fatal(err)
			}
			if bound.ContentSubject != subject {
				t.Fatalf("Run and Release content diverged: run=%s release=%s", bound.ContentSubject, subject)
			}
		})
	}
}

func TestFailedAttemptCommitsDespiteCanceledExecutionContextAndCannotBeReplaced(t *testing.T) {
	workspace := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cause := nkderr.New(nkderr.CodeBuildFailed, "producer stopped")
	failed, err := recordFailedAttempt(ctx, workspace, testRun(), cause)
	if !errors.Is(err, cause) || failed == nil || failed.Status != RunFailed {
		t.Fatalf("failed=%+v err=%v", failed, err)
	}
	recordPath := filepath.Join(workspace, "lifecycle.json")
	raw, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatal(err)
	}
	var recorded Run
	if err := canonical.UnmarshalExact(raw, &recorded); err != nil {
		t.Fatal(err)
	}
	if recorded.Status != RunFailed || recorded.Failure == nil || recorded.Failure.Code != string(nkderr.CodeBuildFailed) {
		t.Fatalf("recorded=%+v", recorded)
	}
	if err := writeRunRecord(context.Background(), recordPath, failed); err == nil {
		t.Fatal("immutable attempt record was replaced")
	} else {
		assertCode(t, err, nkderr.CodeImmutableViolation)
	}
	after, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(raw) {
		t.Fatal("failed replacement changed committed bytes")
	}
}
