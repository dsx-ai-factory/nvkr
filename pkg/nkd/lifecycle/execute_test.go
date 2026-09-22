package lifecycle

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NVIDIA/nke-recipes/internal/transportarchive"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/catalog"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/delivery"
	nkderr "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/factory"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/pipeline"
)

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
	result := fixtureProducerResult(t, plan, input, input.Path)
	writeCanonicalTestFile(t, resultPath, result)
	if err := factory.ExecuteNode(ctx, plan, "package/cli", nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := observeFixtureOutput(ctx, ".nkd/factory", run.Expected[0], artifacts, plan); err != nil {
		t.Fatalf("positive APT fixture failed: %v", err)
	}
	result.SHA256 = "sha256:" + repeat("9", 64)
	writeCanonicalTestFile(t, resultPath, result)
	if _, err := observeFixtureOutput(ctx, ".nkd/factory", run.Expected[0], artifacts, plan); err == nil {
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
	if _, err := observeFixtureOutput(ctx, ".nkd/factory", run.Expected[0], artifacts, plan); err == nil {
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
	inputPath := artifacts["artifact/gpu-runtime/gpu-runtime-chart"].Path
	chartStage := filepath.Dir(inputPath)
	if err := os.MkdirAll(filepath.Join(chartStage, "helm"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(chartStage, "helm", "Chart.yaml"), []byte("apiVersion: v2\nname: gpu-runtime\nversion: 1.0.0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := transportarchive.Pack(ctx, inputPath, chartStage, []string{"helm"}); err != nil {
		t.Fatal(err)
	}
	resultPath := artifacts["result/gpu-runtime/gpu-runtime-chart"].Path
	writeCanonicalTestFile(t, resultPath, fixtureProducerResult(t, plan, artifacts["artifact/gpu-runtime/gpu-runtime-chart"], inputPath))
	if err := factory.ExecuteNode(ctx, plan, "package/gpu-runtime", nil, nil); err != nil {
		t.Fatal(err)
	}
	before, err := observeFixtureOutput(ctx, ".nkd/factory", slot, artifacts, plan)
	if err != nil {
		t.Fatalf("canonical workspace observation: %v", err)
	}
	if err := os.MkdirAll(".nkd/runs", 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(".nkd/factory", ".nkd/runs/attempt"); err != nil {
		t.Fatal(err)
	}
	after, err := observeFixtureOutput(ctx, ".nkd/runs/attempt", slot, artifacts, plan)
	if err != nil {
		t.Fatalf("relocated workspace observation: %v", err)
	}
	if before.Digest != after.Digest {
		t.Fatalf("workspace changed Helm payload identity: %s != %s", before.Digest, after.Digest)
	}
	relocatedInput := filepath.Join(".nkd/runs/attempt", filepath.FromSlash(strings.TrimPrefix(filepath.ToSlash(inputPath), ".nkd/factory/")))
	if err := os.WriteFile(relocatedInput, []byte("changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := observeFixtureOutput(ctx, ".nkd/runs/attempt", slot, artifacts, plan); err == nil {
		t.Fatal("stale Helm payload accepted after source archive input changed")
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
			// Keep the supplier acquisition in this synthetic closure, but pin its
			// private test overlay to the bytes the fixture will materialize.
			overlay, err := catalog.LoadOverlayContext(ctx, filepath.Join(filepath.Dir(definitionPath), "supplier-kubectl.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			pin := pipeline.FileDigest([]byte("synthetic GPU producer kubectl"))
			overlay.Components["kubectl"].Prebuilt.Digests = map[string]string{"amd64": pin, "arm64": pin}
			overlayPath := filepath.Join(t.TempDir(), "supplier-kubectl.json")
			writeCanonicalTestFile(t, overlayPath, overlay)
			definition.Catalog.Overlays = []string{overlayPath}
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
					input := artifacts["artifact/"+artifact.BuildTarget+"/"+artifact.Component]
					writeCanonicalTestFile(t, artifact.Path, fixtureProducerResult(t, plan, artifact, input.Path))
				case artifact.Kind == "tarball" && artifact.Component == "gpu-runtime-chart":
					chartStage := filepath.Dir(artifact.Path)
					if err := os.MkdirAll(filepath.Join(chartStage, "helm"), 0o700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(chartStage, "helm", "Chart.yaml"), []byte("apiVersion: v2\nname: gpu\nversion: 1.0.0\n"), 0o600); err != nil {
						t.Fatal(err)
					}
					if err := transportarchive.Pack(ctx, artifact.Path, chartStage, []string{"helm"}); err != nil {
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
			outputs, subject, err := inspectTestPublication(t, ctx, layout, inspection.RootDigest)
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
				artifact, err := observeFixtureOutput(ctx, ".nkd/factory", slot, artifacts, plan)
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
	failed, err := recordRunFailure(ctx, filepath.Join(workspace, "lifecycle.json"), testRun(), cause, "recording failed run attempt")
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

func observeFixtureOutput(ctx context.Context, workspace string, slot requiredOutput, artifacts map[string]factory.Artifact, plan *factory.Plan) (Artifact, error) {
	observed, err := observeOutputs(ctx, workspace, []requiredOutput{slot}, artifacts, plan, nil)
	if err != nil {
		return Artifact{}, err
	}
	return observed[0], nil
}
