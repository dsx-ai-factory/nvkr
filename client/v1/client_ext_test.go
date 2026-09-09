package client_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	client "github.com/nhensley/nkdctl/client/v1"
	"github.com/nhensley/nkdctl/pkg/nkd/attest"
	"github.com/nhensley/nkdctl/pkg/nkd/canonical"
	"github.com/nhensley/nkdctl/pkg/nkd/catalog"
	"github.com/nhensley/nkdctl/pkg/nkd/criteria"
	nkderr "github.com/nhensley/nkdctl/pkg/nkd/errors"
	"github.com/nhensley/nkdctl/pkg/nkd/ir"
	"github.com/nhensley/nkdctl/pkg/nkd/pipeline"
	"github.com/nhensley/nkdctl/pkg/nkd/profile"
	"github.com/nhensley/nkdctl/pkg/nkd/provenance"
	"github.com/nhensley/nkdctl/pkg/nkd/recipe"
	"github.com/nhensley/nkdctl/pkg/nkd/release"
	"github.com/nhensley/nkdctl/pkg/nkd/sbom"
	"github.com/nhensley/nkdctl/pkg/nkd/status"
	"github.com/nhensley/nkdctl/pkg/nkd/transport"
	"github.com/nhensley/nkdctl/pkg/nkd/trust"
	"github.com/nhensley/nkdctl/pkg/nkd/vuln"
)

func errorCode(err error) nkderr.Code {
	var coded *nkderr.Error
	if errors.As(err, &coded) {
		return coded.Code
	}
	return ""
}

func digestBytes(payload []byte) string {
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func TestResolveRecipeEqualsAuthoritativeModules(t *testing.T) {
	ctx := context.Background()
	query := criteria.Criteria{Role: "control-plane"}
	data := filepath.Join("..", "..", "testdata", "catalogs", "kubernetes-layered")

	wantCatalog, err := catalog.LoadContext(ctx, catalog.Dir(data))
	if err != nil {
		t.Fatal(err)
	}
	wantResolved, err := recipe.Resolve(wantCatalog, query, nil)
	if err != nil {
		t.Fatal(err)
	}
	wantGraph, err := recipe.Compile(wantResolved)
	if err != nil {
		t.Fatal(err)
	}
	wantDigest, err := wantGraph.Digest()
	if err != nil {
		t.Fatal(err)
	}

	sdk := client.New(client.WithData(data))
	got, err := sdk.ResolveRecipe(ctx, client.ResolveRequest{Criteria: query})
	if err != nil {
		t.Fatalf("facade ResolveRecipe failed: %v", err)
	}
	if !reflect.DeepEqual(got.Resolved, wantResolved) {
		t.Fatalf("facade resolved value differs from recipe.Resolve\n got: %#v\nwant: %#v", got.Resolved, wantResolved)
	}
	if !reflect.DeepEqual(got.Graph, wantGraph) || got.RecipeDigest != wantDigest {
		t.Fatalf("facade graph/digest differs from recipe.Compile: got %s, want %s", got.RecipeDigest, wantDigest)
	}

}

func TestResolveRecipeRequiresExplicitCatalog(t *testing.T) {
	_, err := client.New().ResolveRecipe(context.Background(), client.ResolveRequest{})
	if errorCode(err) != nkderr.CodeUsage || !strings.Contains(err.Error(), "requires catalog I/O") {
		t.Fatalf("missing catalog returned %v", err)
	}
}

type countingCatalogIO struct{ calls int }

func (catalogIO *countingCatalogIO) ReadFile(context.Context, string) ([]byte, error) {
	catalogIO.calls++
	return nil, errors.New("must not be called")
}
func (catalogIO *countingCatalogIO) ReadDir(context.Context, string) ([]string, error) {
	catalogIO.calls++
	return nil, errors.New("must not be called")
}
func (catalogIO *countingCatalogIO) Name() string { return "counting" }

type forbiddenReleaseAuthority struct{ calls *int }

func (authority forbiddenReleaseAuthority) Authorize(context.Context,
	release.AuthorizationRequest,
) (*release.Authorization, error) {
	*authority.calls = *authority.calls + 1
	return nil, errors.New("release authority must not run")
}

func TestResolveRecipeCancellationPreventsCatalogIO(t *testing.T) {
	catalogIO := &countingCatalogIO{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := client.New(client.WithCatalogIO(catalogIO)).ResolveRecipe(ctx, client.ResolveRequest{
		Criteria: criteria.Criteria{Role: "worker"},
	})
	if errorCode(err) != nkderr.CodeTimeout {
		t.Fatalf("cancellation must use the stable timeout code, got %v", err)
	}
	if catalogIO.calls != 0 {
		t.Fatalf("pre-canceled operation performed %d catalog reads", catalogIO.calls)
	}
}

func TestRequestedAtomicOutputIsAdmittedBeforeAuthorityIO(t *testing.T) {
	t.Run("resolve", func(t *testing.T) {
		catalogIO := &countingCatalogIO{}
		_, err := client.New(client.WithCatalogIO(catalogIO)).ResolveRecipe(context.Background(), client.ResolveRequest{
			Criteria: criteria.Criteria{Role: "worker"}, OutputName: "graph.json",
		})
		if errorCode(err) != nkderr.CodeUsage {
			t.Fatalf("error = %v, want %s", err, nkderr.CodeUsage)
		}
		if catalogIO.calls != 0 {
			t.Fatalf("missing AtomicOutput performed %d catalog reads", catalogIO.calls)
		}
	})

	t.Run("validation vector", func(t *testing.T) {
		evidenceCalls := 0
		_, err := client.New(client.WithPublisherEvidenceAuthority(fixedPublisherEvidenceAuthority{
			calls: &evidenceCalls,
		})).CreateValidationVector(context.Background(), client.ValidationVectorRequest{
			Report: reportFixture(t), OutputName: "vector.json",
		})
		if errorCode(err) != nkderr.CodeUsage {
			t.Fatalf("error = %v, want %s", err, nkderr.CodeUsage)
		}
		if evidenceCalls != 0 {
			t.Fatalf("missing AtomicOutput reached publisher-evidence authority %d time(s)", evidenceCalls)
		}
	})

	t.Run("release authorization", func(t *testing.T) {
		authorityCalls := 0
		_, err := client.New(client.WithReleaseAuthority(forbiddenReleaseAuthority{
			calls: &authorityCalls,
		})).AuthorizeRelease(context.Background(), client.AuthorizeReleaseRequest{OutputName: "clearance.json"})
		if errorCode(err) != nkderr.CodeUsage {
			t.Fatalf("error = %v, want %s", err, nkderr.CodeUsage)
		}
		if authorityCalls != 0 {
			t.Fatalf("missing AtomicOutput reached release authority %d time(s)", authorityCalls)
		}
	})
}

type cancelRunner struct {
	started chan context.Context
	calls   int
}

func (runner *cancelRunner) Name() string { return "cancel-runner" }

func (runner *cancelRunner) Run(_ *ir.Graph, _ string, options pipeline.RunOptions) (*pipeline.Report, error) {
	runner.calls++
	runner.started <- options.Ctx
	<-options.Ctx.Done()
	return nil, options.Ctx.Err()
}

func TestRunPropagatesCancellationToRunner(t *testing.T) {
	graph := &ir.Graph{
		SchemaVersion: ir.SchemaVersion, RecipeID: "cancel-test",
		Criteria: criteria.Criteria{OS: "ubuntu", Arch: "amd64"},
		Nodes: []ir.Node{
			{ID: "pull-test", Kind: ir.KindPull, Component: "test", Artifact: "https://example.test/test",
				Digest: "sha256:" + strings.Repeat("a", 64), Platform: "linux/amd64", Network: ir.NetworkDefault,
				OutputType: "raw", ArtifactPath: "test", Version: "1.0.0"},
			{ID: "verify", Kind: ir.KindVerify, Needs: []string{"pull-test"}},
		},
	}
	digest, err := graph.Digest()
	if err != nil {
		t.Fatal(err)
	}
	runner := &cancelRunner{started: make(chan context.Context, 1)}
	c := client.New(client.WithRunner(runner))
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, runErr := c.Run(ctx, client.RunRequest{Graph: graph, RecipeDigest: digest})
		result <- runErr
	}()

	passedContext := <-runner.started
	if passedContext != ctx {
		t.Fatal("Run did not pass its exact context to pipeline.RunOptions")
	}
	cancel()
	if err := <-result; errorCode(err) != nkderr.CodeTimeout {
		t.Fatalf("runner cancellation must be normalized to CodeTimeout, got %v", err)
	}
	if runner.calls != 1 {
		t.Fatalf("runner called %d times, want once", runner.calls)
	}
}

type recordingOutput struct {
	committed map[string][]byte
	commit    func(context.Context, string, []byte) error
}

func (output *recordingOutput) Commit(ctx context.Context, name string, canonicalBytes []byte) error {
	if output.commit != nil {
		return output.commit(ctx, name, canonicalBytes)
	}
	output.committed[name] = append([]byte(nil), canonicalBytes...)
	return nil
}

func reportFixture(t *testing.T) *pipeline.Report {
	t.Helper()
	work := filepath.Join(t.TempDir(), "work")
	options := pipeline.RunOptions{Ctx: context.Background(), SourceRoot: filepath.Join(work, "src")}
	graph := &ir.Graph{SchemaVersion: ir.SchemaVersion, RecipeID: "recipe",
		Criteria: criteria.Criteria{OS: "ubuntu", Arch: "amd64"}}
	verify := ir.Node{ID: "verify", Kind: ir.KindVerify}
	for _, fixture := range []struct {
		name string
		body []byte
	}{{"alpha", []byte("alpha")}, {"zeta", []byte("zeta")}} {
		sum := sha256.Sum256(fixture.body)
		digest := "sha256:" + hex.EncodeToString(sum[:])
		node := ir.Node{ID: "pull-" + fixture.name, Kind: ir.KindPull, Component: fixture.name,
			Artifact: "https://example.test/" + fixture.name, Digest: digest,
			Platform: "linux/amd64", Network: ir.NetworkDefault, OutputType: "raw",
			ArtifactPath: fixture.name, Version: "1.0.0"}
		graph.Nodes = append(graph.Nodes, node)
		verify.Needs = append(verify.Needs, node.ID)
		path := filepath.Join(filepath.Dir(options.SourceRoot), "fetch", fixture.name, fixture.name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, fixture.body, 0o644); err != nil {
			t.Fatal(err)
		}
		result := pipeline.ArtifactResult{Component: fixture.name, OutputType: "raw",
			Qualification: pipeline.ArtifactProfileFreeAcquisition, EngineClass: ir.EnginePrebuiltAcquire,
			Path: fixture.name, SHA256: digest, Size: int64(len(fixture.body))}
		payload, err := canonical.Marshal(result)
		if err != nil {
			t.Fatal(err)
		}
		results := pipeline.ResultDirectory(options.SourceRoot)
		if err := os.MkdirAll(results, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(results, node.ID+".json"), payload, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	graph.Nodes = append(graph.Nodes, verify)
	graph.Normalize()
	digest, err := graph.Digest()
	if err != nil {
		t.Fatal(err)
	}
	report, err := (pipeline.Local{}).VerifyResults(graph, digest,
		pipeline.ResultDirectory(options.SourceRoot), options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = report.Close() })
	return report
}

func TestCreateValidationVectorEqualsAttestModule(t *testing.T) {
	report := reportFixture(t)
	want, err := attest.FromQualifiedReport(context.Background(), report)
	if err != nil {
		t.Fatal(err)
	}
	got, err := client.New().CreateValidationVector(context.Background(), client.ValidationVectorRequest{Report: report})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("facade vector differs from attest.FromQualifiedReport\n got: %#v\nwant: %#v", got, want)
	}
}

func TestVerificationResultCloseInvalidatesQualifiedProjection(t *testing.T) {
	report := reportFixture(t)
	manifest, err := attest.FromQualifiedReport(context.Background(), report)
	if err != nil {
		t.Fatal(err)
	}
	result := &client.VerificationResult{Built: manifest, Report: report}
	if err := result.Close(); err != nil {
		t.Fatalf("closing verification result: %v", err)
	}
	if err := report.RequireQualified(context.Background()); err == nil {
		t.Fatal("closed verification result retained report authority")
	}
	if err := result.Close(); err != nil {
		t.Fatalf("verification result close was not idempotent: %v", err)
	}
}

func TestAtomicOutputFailureLeavesDestinationUnchanged(t *testing.T) {
	output := &recordingOutput{committed: map[string][]byte{"vector": []byte("old")}}
	called := false
	output.commit = func(_ context.Context, _ string, canonicalBytes []byte) error {
		called = true
		_ = append([]byte(nil), canonicalBytes...)
		return errors.New("must not publish an evidence-free draft")
	}

	manifest, err := client.New(client.WithAtomicOutput(output)).CreateValidationVector(
		context.Background(),
		client.ValidationVectorRequest{Report: reportFixture(t), OutputName: "vector"},
	)
	if manifest != nil || errorCode(err) != nkderr.CodeProvenanceMissing {
		t.Fatalf("evidence-free output must fail before commit: manifest=%v err=%v", manifest, err)
	}
	if called {
		t.Fatal("evidence-free draft reached the atomic output adapter")
	}
	if string(output.committed["vector"]) != "old" {
		t.Fatalf("failed commit changed destination to %q", output.committed["vector"])
	}
}

func TestAtomicOutputCancellationLeavesDestinationUnchanged(t *testing.T) {
	ctx := context.Background()
	output := &recordingOutput{committed: map[string][]byte{"vector": []byte("old")}}
	called := false
	output.commit = func(ctx context.Context, _ string, _ []byte) error {
		called = true
		return ctx.Err()
	}

	manifest, err := client.New(client.WithAtomicOutput(output)).CreateValidationVector(
		ctx,
		client.ValidationVectorRequest{Report: reportFixture(t), OutputName: "vector"},
	)
	if manifest != nil || errorCode(err) != nkderr.CodeProvenanceMissing {
		t.Fatalf("evidence-free output must fail before commit: manifest=%v err=%v", manifest, err)
	}
	if called {
		t.Fatal("evidence-free draft reached the atomic output adapter")
	}
	if string(output.committed["vector"]) != "old" {
		t.Fatalf("canceled commit changed destination to %q", output.committed["vector"])
	}
}

type fixedRunner struct{ calls int }

func (runner *fixedRunner) Name() string { return "fixed" }
func (runner *fixedRunner) Run(graph *ir.Graph, digest string, _ pipeline.RunOptions) (*pipeline.Report, error) {
	runner.calls++
	return &pipeline.Report{RecipeID: graph.RecipeID, RecipeDigest: digest}, nil
}

func TestRunRejectsUnsafeCallerGraphBeforeInjectedRunner(t *testing.T) {
	resolution, err := client.New(client.WithData(filepath.Join("..", "..", "testdata", "catalogs", "kubernetes-layered"))).ResolveRecipe(context.Background(), client.ResolveRequest{
		Criteria: criteria.Criteria{Category: "kubernetes", K8sVersion: "1.32.0", OS: "ubuntu", Arch: "amd64"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*ir.Node){
		"tagged toolchain": func(node *ir.Node) { node.Toolchain = "ubuntu:latest" },
		"networked build":  func(node *ir.Node) { node.Network = ir.NetworkDefault },
	} {
		t.Run(name, func(t *testing.T) {
			graph := *resolution.Graph
			graph.Nodes = append([]ir.Node(nil), resolution.Graph.Nodes...)
			found := false
			for i := range graph.Nodes {
				if graph.Nodes[i].Kind == ir.KindBuild {
					mutate(&graph.Nodes[i])
					found = true
					break
				}
			}
			if !found {
				t.Fatal("fixture has no source build")
			}
			runner := &fixedRunner{}
			_, err := client.New(client.WithRunner(runner)).Run(context.Background(), client.RunRequest{
				Graph: &graph, RecipeDigest: resolution.RecipeDigest,
			})
			if err == nil || runner.calls != 0 {
				t.Fatalf("unsafe graph reached injected runner: calls=%d err=%v", runner.calls, err)
			}
		})
	}
}

type documentVerifier struct{}

func (documentVerifier) VerifyDocument(context.Context, trust.DocumentVerificationRequest) (trust.VerificationEvidence, error) {
	return trust.VerificationEvidence{
		CertificateIdentities: []string{"publisher@example.com"},
		OIDCIssuer:            "https://issuer.example",
		CertificateSHA256:     testDigest('d'),
	}, nil
}

func TestAuthenticateValidationVectorReturnsExactAuthoritativeDocument(t *testing.T) {
	want := attest.FromReport(reportFixture(t)) // diagnostic fixture before authentication
	want.AttachEvidence(&attest.EvidenceRef{SBOMDigest: testDigest('8'), ProvenanceDigest: testDigest('9')}, testDigest('7'))
	raw, err := want.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	path := t.TempDir() + "/vector.json"
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(raw); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	authenticator, err := trust.NewDocumentAuthenticator(
		documentVerifier{}, trust.JSONSchema[attest.Manifest](attest.SchemaVersion),
	)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := trust.NewRolePolicy(
		trust.RoleValidationVector,
		trust.Store{CLIIdentities: []string{"publisher@example.com"}},
		"https://issuer.example",
	)
	if err != nil {
		t.Fatal(err)
	}
	authority, err := client.NewDocumentValidationVectorAuthority(authenticator, policy)
	if err != nil {
		t.Fatal(err)
	}

	got, err := client.New(client.WithValidationVectorAuthority(authority)).AuthenticateValidationVector(
		context.Background(), client.ValidationVectorInput{SourcePath: path},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Receipt.Authenticated() || !bytes.Equal(got.Receipt.ExactBytes(), raw) {
		t.Fatal("facade did not retain the authenticated exact-byte receipt")
	}
	if !reflect.DeepEqual(got.Manifest, want) {
		t.Fatalf("authenticated vector changed across facade: got %#v, want %#v", got.Manifest, want)
	}
}

type artifactRunner struct {
	artifacts []pipeline.ArtifactResult
	calls     int
}

func (runner *artifactRunner) Name() string { return "artifact-runner" }
func (runner *artifactRunner) Run(graph *ir.Graph, digest string, options pipeline.RunOptions) (*pipeline.Report, error) {
	runner.calls++
	if options.Qualification != nil {
		selected := make(map[string]bool, len(options.Only))
		for _, component := range options.Only {
			selected[component] = true
		}
		var nodes []ir.Node
		for _, node := range graph.Nodes {
			if node.Kind == ir.KindBuild && (len(selected) == 0 || selected[node.Component]) {
				nodes = append(nodes, node)
			}
		}
		observation, err := options.Qualification.Observer.ObserveExecution(options.Ctx, pipeline.ExecutionObservationRequest{BuildNodes: nodes})
		if err != nil {
			return nil, err
		}
		receipt, err := profile.VerifyExecution(options.Ctx, options.Qualification.HostReceipt, options.Qualification.Profile, nodes, observation)
		if err != nil {
			return nil, err
		}
		for _, node := range nodes {
			var result pipeline.ArtifactResult
			for _, candidate := range runner.artifacts {
				if candidate.Component == node.Component {
					result = candidate
					break
				}
			}
			result.Qualification = pipeline.ArtifactQualifiedExecution
			result.ProfileDigest = receipt.ProfileDigest()
			result.ExecutionBinding = receipt.ExecutionBinding()
			artifactPath := pipeline.ArtifactPath(options, node)
			if err := os.MkdirAll(filepath.Dir(artifactPath), 0o755); err != nil {
				return nil, err
			}
			body := []byte("artifact:" + node.Component)
			if err := os.WriteFile(artifactPath, body, 0o755); err != nil {
				return nil, err
			}
			payload, err := canonical.Marshal(result)
			if err != nil {
				return nil, err
			}
			resultsDir := pipeline.ResultDirectory(options.SourceRoot)
			if err := os.MkdirAll(resultsDir, 0o755); err != nil {
				return nil, err
			}
			if err := os.WriteFile(filepath.Join(resultsDir, node.ID+".json"), payload, 0o600); err != nil {
				return nil, err
			}
		}
	}
	return (pipeline.Local{}).VerifyResults(graph, digest,
		pipeline.ResultDirectory(options.SourceRoot), options)
}

type fixedHostObserver struct {
	want  context.Context
	facts map[string]string
	calls int
}

type fixedExecutionObserver struct {
	observation profile.ExecutionObservation
	calls       int
}

func (observer *fixedExecutionObserver) ObserveExecution(context.Context, pipeline.ExecutionObservationRequest) (profile.ExecutionObservation, error) {
	observer.calls++
	return observer.observation, nil
}

type mutatingProfileAuthority struct {
	base client.BuilderProfileAuthority
}

type fixedPublisherEvidenceAuthority struct {
	evidence *attest.PublisherEvidence
	err      error
	calls    *int
}

type forbiddenValidationVectorAuthority struct{ calls *int }

func (authority forbiddenValidationVectorAuthority) Authenticate(context.Context,
	client.ValidationVectorInput,
) (trust.DocumentReceipt, error) {
	*authority.calls = *authority.calls + 1
	return trust.DocumentReceipt{}, errors.New("validation-vector authority must not run")
}

type forbiddenBuilderProfileAuthority struct{ calls *int }

func (authority forbiddenBuilderProfileAuthority) Authenticate(context.Context,
	client.BuilderProfileInput,
) (trust.DocumentReceipt, error) {
	*authority.calls = *authority.calls + 1
	return trust.DocumentReceipt{}, errors.New("builder-profile authority must not run")
}

func (authority fixedPublisherEvidenceAuthority) Qualify(ctx context.Context,
	_ client.PublisherEvidenceRequest,
) (*attest.PublisherEvidence, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if authority.calls != nil {
		*authority.calls = *authority.calls + 1
	}
	return authority.evidence, authority.err
}

func (authority mutatingProfileAuthority) Authenticate(ctx context.Context, input client.BuilderProfileInput) (trust.DocumentReceipt, error) {
	receipt, err := authority.base.Authenticate(ctx, input)
	if err != nil {
		return trust.DocumentReceipt{}, err
	}
	// Simulate an adversarial adapter retaining and mutating the decoded pointer. The
	// facade must derive policy inputs only from receipt.ExactBytes().
	if decoded, ok := receipt.Decoded().(*profile.BuilderProfile); ok {
		decoded.Fields[0].Value = "mutated-after-authentication"
	}
	return receipt, nil
}

func (observer *fixedHostObserver) Observe(ctx context.Context) (map[string]string, error) {
	observer.calls++
	if ctx != observer.want {
		return nil, errors.New("observer received a different context")
	}
	return observer.facts, nil
}

func TestVerifyAuthenticatedEnforcesProfileReceiptAndPreflight(t *testing.T) {
	ctx := context.Background()
	query := criteria.Criteria{Role: "control-plane"}
	resolution, err := client.New(client.WithData(filepath.Join("..", "..", "testdata", "catalogs", "kubernetes-layered"))).ResolveRecipe(ctx, client.ResolveRequest{Criteria: query})
	if err != nil {
		t.Fatal(err)
	}
	builderProfile := profile.BuilderProfile{
		SchemaVersion: profile.SchemaVersion,
		Identity:      "operator-profile", Version: "1",
		HostControlClass: profile.OperatorControlled,
		Provides:         recipe.RequiredBuilders(resolution.Resolved),
		Fields: []profile.Field{
			{Name: "host.test", Value: "matching", HardFail: true, MeasurementStage: profile.MeasurementHost},
			{Name: profile.FieldRunnerDigest, Value: testDigest('1'), HardFail: true, MeasurementStage: profile.MeasurementExecution},
			{Name: profile.FieldRuntimeIdentity, Value: testDigest('2'), HardFail: true, MeasurementStage: profile.MeasurementExecution},
			{Name: profile.FieldOCISandboxMode, Value: "test-sandbox", HardFail: true, MeasurementStage: profile.MeasurementExecution},
		},
	}
	var (
		artifacts     []pipeline.ArtifactResult
		observedNodes []profile.NodeExecutionObservation
	)
	for _, node := range resolution.Graph.Nodes {
		if node.Kind != ir.KindBuild {
			continue
		}
		toolchainDigest := node.Toolchain
		if !strings.HasPrefix(toolchainDigest, "sha256:") {
			var ok bool
			_, toolchainDigest, ok = strings.Cut(node.Toolchain, "@")
			if !ok {
				t.Fatalf("fixture toolchain is not pinned: %s", node.Toolchain)
			}
		}
		artifacts = append(artifacts, pipeline.ArtifactResult{
			Component: node.Component, OutputType: node.OutputType, Toolchain: node.Toolchain,
			Qualification: pipeline.ArtifactQualifiedExecution,
			InputDigest:   testDigest('7'), BuilderType: node.BuilderType,
			EngineClass: ir.EngineOCIHermetic, Path: node.ArtifactPath,
			SHA256: digestBytes([]byte("artifact:" + node.Component)),
			Size:   int64(len([]byte("artifact:" + node.Component))),
		})
		observedNodes = append(observedNodes, profile.NodeExecutionObservation{
			NodeID: node.ID, EngineClass: ir.EngineOCIHermetic,
			ToolchainDigest: toolchainDigest, SandboxMode: "test-sandbox",
		})
	}
	if len(artifacts) == 0 {
		t.Fatal("fixture has no from-source build nodes")
	}
	executionObserver := &fixedExecutionObserver{observation: profile.ExecutionObservation{
		RunnerDigest: testDigest('1'), RunnerOrigin: profile.RunnerPreexisting,
		RuntimeIdentity: testDigest('2'), Nodes: observedNodes,
	}}
	profileDigest, err := builderProfile.Digest()
	if err != nil {
		t.Fatal(err)
	}
	expectedHostReceipt, _, err := builderProfile.VerifyHost(ctx, map[string]string{"host.test": "matching"})
	if err != nil {
		t.Fatal(err)
	}
	expectedObserver := &fixedExecutionObserver{observation: executionObserver.observation}
	expectedRunner := &artifactRunner{artifacts: artifacts}
	expectedReport, err := expectedRunner.Run(resolution.Graph, resolution.RecipeDigest, pipeline.RunOptions{
		Ctx: ctx, SourceRoot: filepath.Join(t.TempDir(), "expected", "src"), Runtime: os.Args[0],
		Qualification: &pipeline.QualifiedExecution{
			Profile: builderProfile, HostReceipt: expectedHostReceipt, Observer: expectedObserver,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	expectedSBOM, err := sbom.BuildVerifiedFromReport(ctx, resolution.Resolved, expectedReport, nil)
	if err != nil {
		t.Fatal(err)
	}
	expectedProvenance, err := provenance.BuildQualifiedFromReport(ctx, expectedReport, resolution.Graph)
	if err != nil {
		t.Fatal(err)
	}
	expectedEvidence, err := attest.QualifyPublisherEvidence(ctx, expectedSBOM, expectedProvenance)
	if err != nil {
		t.Fatal(err)
	}
	creationCalls := 0
	creationClient := client.New(client.WithPublisherEvidenceAuthority(
		fixedPublisherEvidenceAuthority{evidence: expectedEvidence, calls: &creationCalls}))
	if _, err := creationClient.CreateValidationVector(ctx, client.ValidationVectorRequest{
		Report: expectedReport,
	}); errorCode(err) != nkderr.CodeUsage {
		t.Fatalf("publisher-evidence authority ran without the resolved recipe/graph: %v", err)
	}
	created, err := creationClient.CreateValidationVector(ctx, client.ValidationVectorRequest{
		Report: expectedReport, Resolved: resolution.Resolved, Graph: resolution.Graph,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := created.MarshalQualified(ctx); err != nil || creationCalls != 1 {
		t.Fatalf("CreateValidationVector did not consume its narrow evidence authority: calls=%d err=%v",
			creationCalls, err)
	}
	expected, err := attest.FromQualifiedReport(ctx, expectedReport)
	if err != nil {
		t.Fatal(err)
	}
	expected.SetBuilderProfile(builderProfile.Identity, profileDigest, builderProfile.HostControlClass)
	if err := expected.AttachPublisherEvidence(expectedEvidence); err != nil {
		t.Fatal(err)
	}
	vectorBytes, err := expected.MarshalQualified(ctx)
	if err != nil {
		t.Fatal(err)
	}
	profileBytes, err := canonical.Marshal(builderProfile)
	if err != nil {
		t.Fatal(err)
	}
	vectorPath := writeTestDocument(t, "vector.json", vectorBytes)
	profilePath := writeTestDocument(t, "profile.json", profileBytes)

	authenticator, err := trust.NewDocumentAuthenticator(
		documentVerifier{},
		trust.JSONSchema[attest.Manifest](attest.SchemaVersion),
		trust.JSONSchema[*profile.BuilderProfile](profile.SchemaVersion),
	)
	if err != nil {
		t.Fatal(err)
	}
	vectorPolicy, err := trust.NewRolePolicy(
		trust.RoleValidationVector,
		trust.Store{CLIIdentities: []string{"publisher@example.com"}},
		"https://issuer.example",
	)
	if err != nil {
		t.Fatal(err)
	}
	profilePolicy, err := trust.NewRolePolicy(
		trust.RoleBuilderProfile,
		trust.Store{CLIIdentities: []string{"publisher@example.com"}},
		"https://issuer.example",
	)
	if err != nil {
		t.Fatal(err)
	}
	vectorAuthority, err := client.NewDocumentValidationVectorAuthority(authenticator, vectorPolicy)
	if err != nil {
		t.Fatal(err)
	}
	profileAuthority, err := client.NewDocumentBuilderProfileAuthority(authenticator, profilePolicy)
	if err != nil {
		t.Fatal(err)
	}
	request := client.VerifyAuthenticatedRequest{
		Expected: client.ValidationVectorInput{SourcePath: vectorPath},
		Profile:  client.BuilderProfileInput{SourcePath: profilePath},
		Resolve:  client.ResolveRequest{Criteria: query},
	}
	withoutEvidenceCalls := 0
	withoutEvidence := client.New(client.WithValidationVectorAuthority(
		forbiddenValidationVectorAuthority{calls: &withoutEvidenceCalls}))
	request.RunOptions = pipeline.RunOptions{SourceRoot: filepath.Join(t.TempDir(), "absent", "src"), Runtime: os.Args[0]}
	if _, err := withoutEvidence.VerifyAuthenticated(ctx, request); errorCode(err) != nkderr.CodeProvenanceMissing {
		t.Fatalf("SDK verification without publisher-evidence authority did not fail closed: %v", err)
	}
	if withoutEvidenceCalls != 0 {
		t.Fatalf("missing publisher-evidence authority reached vector authentication %d time(s)", withoutEvidenceCalls)
	}
	for _, test := range []struct {
		name    string
		options []client.Option
	}{
		{
			name: "host observer",
			options: []client.Option{
				client.WithExecutionObserver(executionObserver),
			},
		},
		{
			name: "execution observer",
			options: []client.Option{
				client.WithHostObserver(&fixedHostObserver{want: ctx, facts: map[string]string{"host.test": "matching"}}),
			},
		},
	} {
		t.Run("missing "+test.name+" fails before profile authentication", func(t *testing.T) {
			profileCalls := 0
			options := []client.Option{
				client.WithValidationVectorAuthority(vectorAuthority),
				client.WithPublisherEvidenceAuthority(fixedPublisherEvidenceAuthority{evidence: expectedEvidence}),
				client.WithBuilderProfileAuthority(forbiddenBuilderProfileAuthority{calls: &profileCalls}),
			}
			options = append(options, test.options...)
			_, err := client.New(options...).VerifyAuthenticated(ctx, request)
			if errorCode(err) != nkderr.CodePreflightFailed {
				t.Fatalf("error = %v, want %s", err, nkderr.CodePreflightFailed)
			}
			if profileCalls != 0 {
				t.Fatalf("missing %s reached profile authentication %d time(s)", test.name, profileCalls)
			}
		})
	}

	_, _, _, _, _, _, staleEvidence := releasePublisherAuthorityFixture(t,
		"stale-evidence", []byte("stale-evidence-artifact"))
	staleClient := client.New(
		client.WithData(filepath.Join("..", "..", "testdata", "catalogs", "kubernetes-layered")),
		client.WithValidationVectorAuthority(vectorAuthority),
		client.WithPublisherEvidenceAuthority(fixedPublisherEvidenceAuthority{evidence: staleEvidence}),
		client.WithBuilderProfileAuthority(profileAuthority),
		client.WithHostObserver(&fixedHostObserver{want: ctx, facts: map[string]string{"host.test": "matching"}}),
		client.WithExecutionObserver(&fixedExecutionObserver{observation: executionObserver.observation}),
		client.WithRunner(&artifactRunner{artifacts: artifacts}),
	)
	request.RunOptions = pipeline.RunOptions{SourceRoot: filepath.Join(t.TempDir(), "stale", "src"), Runtime: os.Args[0]}
	if _, err := staleClient.VerifyAuthenticated(ctx, request); errorCode(err) != nkderr.CodeVerifyFailed {
		t.Fatalf("SDK verification accepted stale SBOM/provenance authority: %v", err)
	}

	fullSBOMReceipts, fullProvenanceReceipts, err := expectedEvidence.LocalReceipts()
	if err != nil {
		t.Fatal(err)
	}
	if len(fullSBOMReceipts) < 2 || len(fullProvenanceReceipts) < 2 {
		t.Fatal("full-vector fixture needs at least two publisher-evidence rows")
	}
	subsetEvidence, err := attest.QualifyPublisherEvidenceReceipts(ctx,
		fullSBOMReceipts[:1], fullProvenanceReceipts[:1])
	if err != nil {
		t.Fatal(err)
	}
	subsetClient := client.New(
		client.WithData(filepath.Join("..", "..", "testdata", "catalogs", "kubernetes-layered")),
		client.WithValidationVectorAuthority(vectorAuthority),
		client.WithPublisherEvidenceAuthority(fixedPublisherEvidenceAuthority{evidence: subsetEvidence}),
		client.WithBuilderProfileAuthority(profileAuthority),
		client.WithHostObserver(&fixedHostObserver{want: ctx, facts: map[string]string{"host.test": "matching"}}),
		client.WithExecutionObserver(&fixedExecutionObserver{observation: executionObserver.observation}),
		client.WithRunner(&artifactRunner{artifacts: artifacts}),
	)
	request.RunOptions = pipeline.RunOptions{SourceRoot: filepath.Join(t.TempDir(), "subset", "src"), Runtime: os.Args[0]}
	if _, err := subsetClient.VerifyAuthenticated(ctx, request); errorCode(err) != nkderr.CodeVerifyFailed {
		t.Fatalf("SDK subset verification claimed full-vector publisher evidence: %v", err)
	}

	runner := &artifactRunner{artifacts: artifacts}
	observer := &fixedHostObserver{want: ctx, facts: map[string]string{"host.test": "matching"}}
	c := client.New(
		client.WithData(filepath.Join("..", "..", "testdata", "catalogs", "kubernetes-layered")),
		client.WithValidationVectorAuthority(vectorAuthority),
		client.WithPublisherEvidenceAuthority(client.NewLocalPublisherEvidenceAuthority(nil)),
		client.WithBuilderProfileAuthority(mutatingProfileAuthority{base: profileAuthority}),
		client.WithHostObserver(observer),
		client.WithExecutionObserver(executionObserver),
		client.WithRunner(runner),
	)

	request.RunOptions = pipeline.RunOptions{SourceRoot: filepath.Join(t.TempDir(), "work", "src"), Runtime: os.Args[0]}
	result, err := c.VerifyAuthenticated(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Profile == nil || result.Profile.Digest != profileDigest || result.Built.ProfileDigest != profileDigest {
		t.Fatalf("qualified result lost profile binding: %#v", result)
	}
	if observer.calls != 1 || executionObserver.calls != 2 || runner.calls != 1 {
		t.Fatalf("host observer calls=%d execution observer calls=%d runner calls=%d, want 1/2/1",
			observer.calls, executionObserver.calls, runner.calls)
	}
}

func writeTestDocument(t *testing.T, name string, contents []byte) string {
	t.Helper()
	path := t.TempDir() + "/" + name
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(contents); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

type releaseReceiptAuthenticator struct{}

func (releaseReceiptAuthenticator) AuthenticateReceipt(_ context.Context, _, _ []byte, role release.ReceiptRole) (string, error) {
	switch role {
	case release.RoleReproduction:
		return "reproduction-signer", nil
	case release.RoleCompatibility:
		return "compatibility-signer", nil
	case release.RoleConformance:
		return "conformance-signer", nil
	default:
		return "", errors.New("unexpected release receipt role")
	}
}

type releaseQualifiedScanner struct {
	observation vuln.ScanObservation
}

type noIOObjectStore struct{ calls int }

func (store *noIOObjectStore) Put(context.Context, transport.Target, string, string, io.Reader) error {
	store.calls++
	return errors.New("publication adapter must not be called")
}

func (store *noIOObjectStore) Get(context.Context, transport.Target, string, io.Writer) error {
	store.calls++
	return errors.New("publication adapter must not be called")
}

func (scanner releaseQualifiedScanner) Scan(context.Context, vuln.QualifiedScanRequest) (vuln.ScanObservation, error) {
	return scanner.observation, nil
}

func releaseStatusBinding(t *testing.T, target string) release.StatusBinding {
	t.Helper()
	checkpoint := status.StatusCheckpoint{
		SchemaVersion: status.CheckpointSchemaVersion, Target: target, Authority: "status-authority",
		Sequence: 0, DeclarationDigests: []string{}, IssuedEpoch: 1, ValidUntilEpoch: 20,
	}
	raw, err := checkpoint.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	path := writeTestDocument(t, "status-checkpoint.json", raw)
	authenticator, err := trust.NewDocumentAuthenticator(
		documentVerifier{}, trust.JSONSchema[status.StatusCheckpoint](status.CheckpointSchemaVersion),
	)
	if err != nil {
		t.Fatal(err)
	}
	rolePolicy, err := trust.NewRolePolicy(trust.RoleStatus,
		trust.Store{CLIIdentities: []string{"publisher@example.com"}}, "https://issuer.example")
	if err != nil {
		t.Fatal(err)
	}
	documentReceipt, err := authenticator.Authenticate(context.Background(), trust.AuthenticateRequest{
		SourcePath: path, Role: trust.RoleStatus, Policy: rolePolicy,
	})
	if err != nil {
		t.Fatal(err)
	}
	checkpointDigest, err := checkpoint.Digest()
	if err != nil {
		t.Fatal(err)
	}
	const timeAuthorityID = "client-test-clock"
	checkpointPolicy, err := status.NewReleaseCheckpointPolicy(target, "status-authority",
		"https://issuer.example", []string{"publisher@example.com"}, timeAuthorityID)
	if err != nil {
		t.Fatal(err)
	}
	highWater, err := status.NewHighWaterMark(target, "status-authority", 0, checkpointDigest)
	if err != nil {
		t.Fatal(err)
	}
	timeAuthority, err := status.NewTimeAuthority(timeAuthorityID,
		status.EpochSourceFunc(func(context.Context) (int64, error) { return 10, nil }))
	if err != nil {
		t.Fatal(err)
	}
	trustedTime, err := timeAuthority.Now(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	checkpointReceipt, err := status.VerifyCheckpointForRelease(context.Background(), status.ReleaseCheckpointRequest{
		Policy: checkpointPolicy, HighWater: highWater, Time: trustedTime, Checkpoint: documentReceipt,
	})
	if err != nil {
		t.Fatal(err)
	}
	binding, err := release.BindVerifiedStatusCheckpoint(checkpointReceipt)
	if err != nil {
		t.Fatal(err)
	}
	return binding
}

func releasePromotionPolicyReceipt(t *testing.T, compatibilityPolicy, conformancePolicy string) release.PromotionPolicyReceipt {
	t.Helper()
	policy := release.PromotionPolicy{
		SchemaVersion:             release.PromotionPolicySchemaVersion,
		CompatibilityPolicyDigest: compatibilityPolicy,
		ConformancePolicyDigest:   conformancePolicy,
		Rules: []release.PromotionRule{{
			ArtifactKind: release.ArtifactRaw,
			Evidence:     []release.EvidenceRequirement{{Type: release.EvidenceVulnerability}},
		}},
		EffectiveEpoch: 1, ExpiresEpoch: 20,
	}
	raw, err := policy.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	path := writeTestDocument(t, "promotion-policy.json", raw)
	authenticator, err := trust.NewDocumentAuthenticator(
		documentVerifier{}, trust.JSONSchema[release.PromotionPolicy](release.PromotionPolicySchemaVersion),
	)
	if err != nil {
		t.Fatal(err)
	}
	rolePolicy, err := trust.NewRolePolicy(trust.RolePromotionPolicy,
		trust.Store{OperatorIdentities: []string{"publisher@example.com"}}, "https://issuer.example")
	if err != nil {
		t.Fatal(err)
	}
	document, err := authenticator.Authenticate(context.Background(), trust.AuthenticateRequest{
		SourcePath: path, Role: trust.RolePromotionPolicy, Policy: rolePolicy,
	})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := release.VerifyPromotionPolicy(context.Background(), document)
	if err != nil {
		t.Fatal(err)
	}
	return receipt
}

func releaseSetReceipt(t *testing.T, set *release.ReleaseSet) release.ReleaseSetReceipt {
	t.Helper()
	raw, err := set.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	path := writeTestDocument(t, "release-set.json", raw)
	authenticator, err := trust.NewDocumentAuthenticator(
		documentVerifier{}, trust.JSONSchema[release.ReleaseSet](release.ReleaseSetSchemaVersion),
	)
	if err != nil {
		t.Fatal(err)
	}
	rolePolicy, err := trust.NewRolePolicy(trust.RoleReleaseSet,
		trust.Store{OperatorIdentities: []string{"publisher@example.com"}}, "https://issuer.example")
	if err != nil {
		t.Fatal(err)
	}
	document, err := authenticator.Authenticate(context.Background(), trust.AuthenticateRequest{
		SourcePath: path, Role: trust.RoleReleaseSet, Policy: rolePolicy,
	})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := release.VerifyAuthenticatedReleaseSet(context.Background(), document)
	if err != nil {
		t.Fatal(err)
	}
	return receipt
}

func releaseQualifiedReport(t *testing.T, recipeDigest, artifactDigest string,
	localSBOM sbom.LocalEvidenceReceipt,
) vuln.QualifiedReport {
	t.Helper()
	binding, err := localSBOM.Binding()
	if err != nil {
		t.Fatal(err)
	}
	if binding.RecipeDigest != recipeDigest || binding.ArtifactDigest != artifactDigest {
		t.Fatalf("local SBOM fixture does not bind vulnerability subject")
	}
	canonicalSBOM, err := localSBOM.ExactBytes()
	if err != nil {
		t.Fatal(err)
	}
	request := vuln.QualifiedScanRequest{
		Scanner: vuln.ScannerGrype, ScannerDigest: testDigest('9'), DatabaseDigest: testDigest('a'),
		RecipeDigest: recipeDigest, ArtifactDigest: artifactDigest,
		SBOMDigest: binding.EvidenceDigest, CanonicalSBOM: canonicalSBOM,
	}
	report, err := vuln.QualifyScan(context.Background(), request, releaseQualifiedScanner{observation: vuln.ScanObservation{
		ScannerDigest: request.ScannerDigest, DatabaseDigest: request.DatabaseDigest,
		Result: []byte(`{"matches":[]}`),
	}})
	if err != nil {
		t.Fatal(err)
	}
	return report
}

func releasePublisherAuthorityFixture(t *testing.T, component string, artifactBytes []byte) (
	recipeDigest, vectorDigest, artifactDigest string,
	vector attest.PublishedVectorReceipt,
	sbomReceipts []sbom.LocalEvidenceReceipt,
	provenanceReceipts []provenance.LocalEvidenceReceipt,
	publisherEvidence *attest.PublisherEvidence,
) {
	t.Helper()
	artifactDigest = digestBytes(artifactBytes)
	resolved := &recipe.Resolved{
		RecipeID: "client-release-" + component,
		Criteria: criteria.Criteria{OS: "ubuntu", Arch: "amd64"},
		Components: []catalog.NamedComponent{{Name: component, Component: catalog.Component{
			Tag: "v1.0.0", OutputType: "raw",
			Prebuilt: &catalog.Prebuilt{
				URL:      "https://artifacts.example.test/" + component,
				Digests:  map[string]string{"amd64": artifactDigest},
				Evidence: &catalog.ExternalEvidence{None: true},
			},
		}}},
	}
	graph, err := recipe.Compile(resolved)
	if err != nil {
		t.Fatal(err)
	}
	recipeDigest, err = graph.Digest()
	if err != nil {
		t.Fatal(err)
	}
	var pull ir.Node
	for _, node := range graph.Nodes {
		if node.Kind == ir.KindPull {
			pull = node
			break
		}
	}
	if pull.ID == "" {
		t.Fatal("release publisher fixture has no pull node")
	}
	options := pipeline.RunOptions{Ctx: context.Background(), SourceRoot: filepath.Join(t.TempDir(), "work", "src")}
	artifactPath := filepath.Join(filepath.Dir(options.SourceRoot), "fetch", component, pull.ArtifactPath)
	if err := os.MkdirAll(filepath.Dir(artifactPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(artifactPath, artifactBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	result := pipeline.ArtifactResult{
		Component: component, OutputType: pull.OutputType, Path: pull.ArtifactPath,
		Qualification: pipeline.ArtifactProfileFreeAcquisition, EngineClass: ir.EnginePrebuiltAcquire,
		SHA256: artifactDigest, Size: int64(len(artifactBytes)),
	}
	payload, err := canonical.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	resultsDir := pipeline.ResultDirectory(options.SourceRoot)
	if err := os.MkdirAll(resultsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(resultsDir, pull.ID+".json"), payload, 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := (pipeline.Local{}).VerifyResults(graph, recipeDigest, resultsDir, options)
	if err != nil {
		t.Fatal(err)
	}
	verifiedSBOM, err := sbom.BuildVerifiedFromReport(context.Background(), resolved, report, nil)
	if err != nil {
		t.Fatal(err)
	}
	qualifiedProvenance, err := provenance.BuildQualifiedFromReport(context.Background(), report, graph)
	if err != nil {
		t.Fatal(err)
	}
	publisherEvidence, err = attest.QualifyPublisherEvidence(context.Background(), verifiedSBOM, qualifiedProvenance)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := attest.FromQualifiedReport(context.Background(), report)
	if err != nil {
		t.Fatal(err)
	}
	if err := manifest.AttachPublisherEvidence(publisherEvidence); err != nil {
		t.Fatal(err)
	}
	raw, err := manifest.MarshalQualified(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	vectorDigest, err = manifest.Identity()
	if err != nil {
		t.Fatal(err)
	}
	authenticator, err := trust.NewDocumentAuthenticator(
		documentVerifier{}, trust.JSONSchema[attest.Manifest](attest.SchemaVersion),
	)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := trust.NewRolePolicy(trust.RoleValidationVector,
		trust.Store{OperatorIdentities: []string{"publisher@example.com"}}, "https://issuer.example")
	if err != nil {
		t.Fatal(err)
	}
	document, err := authenticator.Authenticate(context.Background(), trust.AuthenticateRequest{
		SourcePath: writeTestDocument(t, "client-release-vector.json", raw),
		Role:       trust.RoleValidationVector, Policy: policy,
	})
	if err != nil {
		t.Fatal(err)
	}
	vector, err = attest.VerifyPublishedVectorReceipt(context.Background(), document)
	if err != nil {
		t.Fatal(err)
	}
	sbomReceipts, provenanceReceipts, err = publisherEvidence.LocalReceipts()
	if err != nil {
		t.Fatal(err)
	}
	return recipeDigest, vectorDigest, artifactDigest, vector, sbomReceipts, provenanceReceipts,
		publisherEvidence
}

func releaseRequestFixture(t *testing.T) release.AuthorizationRequest {
	t.Helper()
	recipeDigest, vectorDigest, artifactDigest, vectorReceipt, sbomReceipts, provenanceReceipts, _ :=
		releasePublisherAuthorityFixture(t, "component", []byte("client-release-component"))
	inventory := &release.Inventory{
		SchemaVersion: release.InventorySchemaVersion,
		Version:       "1",
		Components: []release.InventoryComponent{{
			Name: "component", RequiredArtifacts: []string{artifactDigest},
			ArtifactKinds: []release.ArtifactKindBinding{{ArtifactDigest: artifactDigest, Kind: release.ArtifactRaw}},
		}},
	}
	inventoryDigest, err := inventory.Digest()
	if err != nil {
		t.Fatal(err)
	}
	member := release.Member{
		Component: "component", Arch: "amd64",
		RecipeDigest: recipeDigest, VectorDigest: vectorDigest, ResolvedInputDigest: testDigest('c'),
		ArtifactDigests: []string{artifactDigest},
	}
	set := &release.ReleaseSet{
		SchemaVersion: release.ReleaseSetSchemaVersion, Version: "1",
		InventoryRef: inventoryDigest, Members: []release.Member{member},
	}
	releaseReceipt := releaseSetReceipt(t, set)
	releaseDigest, err := set.Digest()
	if err != nil {
		t.Fatal(err)
	}
	publicationIdentity, err := set.PublicationIdentity()
	if err != nil {
		t.Fatal(err)
	}
	receiptAuthenticator := releaseReceiptAuthenticator{}
	reproductionReceipt, err := release.VerifyReproduction(context.Background(), release.ReproductionResult{
		SchemaVersion: release.ReproductionSchemaVersion, ReleaseSetDigest: releaseDigest,
		Component: member.Component, Role: member.Role, Arch: member.Arch,
		RecipeDigest: member.RecipeDigest, VectorDigest: member.VectorDigest,
		ProfileIndependent: true, ResolvedInputDigest: member.ResolvedInputDigest,
		ArtifactDigests: member.ArtifactDigests, Verdict: release.Reproduced,
		EffectiveEpoch: 1, ExpiresEpoch: 20,
	}, []byte("reproduction signature"), releaseReceipt, receiptAuthenticator)
	if err != nil {
		t.Fatal(err)
	}
	compatibilityReceipt, err := release.VerifyCompatibility(context.Background(), release.CompatibilityResult{
		SchemaVersion: release.CompatibilitySchemaVersion, ReleaseSetDigest: releaseDigest,
		InventoryDigest: inventoryDigest, CompatibilityPolicyDigest: testDigest('6'),
		RecipeDigests: []string{member.RecipeDigest}, VectorDigests: []string{member.VectorDigest},
		ArtifactDigests: member.ArtifactDigests, Verdict: release.CompatibilityAccepted,
		EffectiveEpoch: 1, ExpiresEpoch: 20,
	}, []byte("compatibility signature"), releaseReceipt, inventory, receiptAuthenticator)
	if err != nil {
		t.Fatal(err)
	}
	conformanceReceipt, err := release.AuthenticateConformance(context.Background(), release.ConformanceResult{
		SchemaVersion: release.ConformanceSchemaVersion, ReleaseSetDigest: releaseDigest,
		InventoryDigest: inventoryDigest, VectorDigests: []string{member.VectorDigest},
		Suite: "kubernetes", SuitePolicyDigest: testDigest('7'), Verdict: release.ConformancePassed,
		ResultsDigest: testDigest('5'), EffectiveEpoch: 1, ExpiresEpoch: 20,
	}, []byte("conformance signature"), releaseReceipt, receiptAuthenticator)
	if err != nil {
		t.Fatal(err)
	}
	return release.AuthorizationRequest{
		Release: releaseReceipt, Inventory: inventory,
		ValidationVectors:       []attest.PublishedVectorReceipt{vectorReceipt},
		LocalSBOMEvidence:       sbomReceipts,
		LocalProvenanceEvidence: provenanceReceipts,
		Reproduction:            []release.ReproductionReceipt{reproductionReceipt}, Compatibility: compatibilityReceipt,
		Conformance: []release.ConformanceReceipt{conformanceReceipt},
		VulnerabilityReports: []vuln.QualifiedReport{releaseQualifiedReport(t, member.RecipeDigest,
			artifactDigest, sbomReceipts[0])},
		Status:          releaseStatusBinding(t, publicationIdentity),
		PromotionPolicy: releasePromotionPolicyReceipt(t, testDigest('6'), testDigest('7')),
		AsOfEpoch:       10,
	}
}

func TestAuthorizeReleaseEqualsAuthoritativeModule(t *testing.T) {
	request := releaseRequestFixture(t)
	want, err := release.Authorize(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	got, err := client.New().AuthorizeRelease(context.Background(), client.AuthorizeReleaseRequest{Authorization: request})
	if err != nil {
		t.Fatal(err)
	}
	wantBytes, err := want.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	gotBytes, err := got.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotBytes, wantBytes) {
		t.Fatalf("facade authorization differs from release.Authorize\n got: %s\nwant: %s", gotBytes, wantBytes)
	}
}

func TestPublishFailsBeforeArtifactIOWithoutPublicationRootProtocol(t *testing.T) {
	request := releaseRequestFixture(t)
	authorization, err := release.Authorize(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	objectStore := &noIOObjectStore{}
	err = client.New(client.WithPublicationSeams(transport.Seams{Object: objectStore})).Publish(
		context.Background(), client.PublishRequest{
			Authorization: authorization,
			Artifacts: []transport.Artifact{{
				Name: "component", Kind: "object", Path: "/path/that/must/not/be-read",
				Digest: digestBytes([]byte("client-release-component")),
			}},
			Targets: transport.Targets{Object: transport.Target{Ref: "destination"}},
		})
	if errorCode(err) != nkderr.CodeGateUnmet {
		t.Fatalf("missing authenticated publication commit must fail closed, got %v", err)
	}
	if objectStore.calls != 0 {
		t.Fatalf("publication adapter was called %d time(s) before root-protocol preflight", objectStore.calls)
	}
}

func testDigest(character byte) string {
	return "sha256:" + strings.Repeat(string(character), 64)
}
