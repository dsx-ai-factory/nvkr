package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/attest"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/catalog"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/criteria"
	nkderr "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/pipeline"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/profile"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/provenance"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/recipe"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/trust"
	"github.com/urfave/cli/v2"
)

const (
	vectorSigner          = "vector@example.test"
	vectorIssuer          = "https://vector-issuer.example.test"
	profileSigner         = "profile@example.test"
	profileIssuer         = "https://profile-issuer.example.test"
	testArtifactDigest    = "sha256:c7c5c1d70c5dec4416ab6158afd0b223ef40c29b1dc1f97ed9428b94d4cadb1c"
	testInputDigest       = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	testCertificateDigest = "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
)

var testRecipeDigest = func() string {
	digest, err := prebuiltFixtureGraph().Digest()
	if err != nil {
		panic(err)
	}
	return digest
}()

func verifyTestDigest(ch byte) string { return "sha256:" + strings.Repeat(string(ch), 64) }

type cliVerifierFunc func(context.Context, trust.DocumentVerificationRequest) (trust.VerificationEvidence, error)

func (f cliVerifierFunc) VerifyDocument(ctx context.Context, req trust.DocumentVerificationRequest) (trust.VerificationEvidence, error) {
	return f(ctx, req)
}

type cliExecutionObserverFunc func(context.Context, pipeline.ExecutionObservationRequest) (profile.ExecutionObservation, error)

func (f cliExecutionObserverFunc) ObserveExecution(ctx context.Context, req pipeline.ExecutionObservationRequest) (profile.ExecutionObservation, error) {
	return f(ctx, req)
}

func cliEvidence(identity, issuer string) trust.VerificationEvidence {
	return trust.VerificationEvidence{
		CertificateIdentities: []string{identity},
		OIDCIssuer:            issuer,
		CertificateSHA256:     testCertificateDigest,
	}
}

func writeCLIFile(t *testing.T, name string, raw []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func manifestBytes(t *testing.T, manifest *attest.Manifest) []byte {
	t.Helper()
	raw, err := manifest.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func prebuiltVector(artifactDigest string) *attest.Manifest {
	return &attest.Manifest{
		SchemaVersion: attest.SchemaVersion,
		RecipeID:      "test-recipe",
		RecipeDigest:  testRecipeDigest,
		Evidence: &attest.EvidenceRef{
			SBOMDigest: verifyTestDigest('8'), ProvenanceDigest: verifyTestDigest('9'),
		},
		EvidenceDigest: verifyTestDigest('7'),
		Components: []attest.ComponentDigest{{
			Name: "artifact", OutputType: "raw", EngineClass: ir.EnginePrebuiltAcquire, SHA256: artifactDigest,
		}},
	}
}

func acceptTestEvidence(context.Context, trust.DocumentReceipt, *recipe.Resolved, *pipeline.Report) error {
	return nil
}

func prebuiltFixtureGraph() *ir.Graph {
	return &ir.Graph{
		SchemaVersion: ir.SchemaVersion, RecipeID: "test-recipe",
		Criteria: criteria.Criteria{OS: "ubuntu", Arch: "amd64"},
		Nodes: []ir.Node{
			{ID: "pull-artifact", Kind: ir.KindPull, Component: "artifact", OutputType: "raw",
				Artifact: "file:///artifact", Digest: testArtifactDigest, Platform: "linux/amd64",
				Network: ir.NetworkDefault, ArtifactPath: "artifact", Version: "1.0.0"},
			{ID: "verify", Kind: ir.KindVerify, Needs: []string{"pull-artifact"}},
		},
	}
}

func prebuiltReport(t *testing.T, artifactDigest string) *pipeline.Report {
	t.Helper()
	graph := prebuiltFixtureGraph()
	work := filepath.Join(t.TempDir(), "work")
	sourceRoot := filepath.Join(work, "src")
	artifactPath := filepath.Join(work, "fetch", "artifact", "artifact")
	if err := os.MkdirAll(filepath.Dir(artifactPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(artifactPath, []byte("artifact"), 0o755); err != nil {
		t.Fatal(err)
	}
	result := pipeline.ArtifactResult{
		Component: "artifact", OutputType: "raw", Path: "artifact", SHA256: artifactDigest, Size: int64(len("artifact")),
		Qualification: pipeline.ArtifactProfileFreeAcquisition, EngineClass: ir.EnginePrebuiltAcquire,
	}
	raw, err := canonical.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	resultsDir := pipeline.ResultDirectory(sourceRoot)
	if err := os.MkdirAll(resultsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(resultsDir, "pull-artifact.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := (pipeline.Local{}).VerifyResults(graph, testRecipeDigest, resultsDir, pipeline.RunOptions{
		Ctx: context.Background(), SourceRoot: sourceRoot,
	})
	if err != nil {
		t.Fatal(err)
	}
	return report
}

func prebuiltResolved() *recipe.Resolved {
	return &recipe.Resolved{
		RecipeID: "test-recipe", Criteria: criteria.Criteria{OS: "ubuntu", Arch: "amd64"},
		Components: []catalog.NamedComponent{{Name: "artifact", Component: catalog.Component{
			Tag: "v1.0.0", OutputType: "raw", Prebuilt: &catalog.Prebuilt{
				URL: "file:///artifact", Digests: map[string]string{"amd64": testArtifactDigest},
				Evidence: &catalog.ExternalEvidence{None: true},
			},
		}}},
	}
}

func authenticateVectorReceipt(t *testing.T, raw []byte) trust.DocumentReceipt {
	t.Helper()
	authenticator, err := trust.NewDocumentAuthenticator(
		cliVerifierFunc(func(context.Context, trust.DocumentVerificationRequest) (trust.VerificationEvidence, error) {
			return cliEvidence(vectorSigner, vectorIssuer), nil
		}),
		trust.JSONSchema[attest.Manifest](attest.SchemaVersion),
	)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := trust.NewRolePolicy(trust.RoleValidationVector,
		trust.Store{OperatorIdentities: []string{vectorSigner}}, vectorIssuer)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := authenticator.Authenticate(context.Background(), trust.AuthenticateRequest{
		SourcePath: writeCLIFile(t, "published-vector.json", raw),
		Role:       trust.RoleValidationVector,
		Policy:     policy,
	})
	if err != nil {
		t.Fatal(err)
	}
	return receipt
}

func TestVerifyEvidenceUsesAuthenticatedVectorAndTerminalPublisherAuthority(t *testing.T) {
	ctx := context.Background()
	resolved := prebuiltResolved()
	graph, err := recipe.Compile(resolved)
	if err != nil {
		t.Fatal(err)
	}
	report := prebuiltReport(t, testArtifactDigest)
	verifiedSBOM, err := buildVerifiedSBOMFromReport(ctx, resolved, report)
	if err != nil {
		t.Fatal(err)
	}
	qualifiedProvenance, err := provenance.BuildQualifiedFromReport(ctx, report, graph)
	if err != nil {
		t.Fatal(err)
	}
	publisherEvidence, err := attest.QualifyPublisherEvidence(ctx, verifiedSBOM, qualifiedProvenance)
	if err != nil {
		t.Fatal(err)
	}
	vector, err := attest.FromQualifiedReport(ctx, report)
	if err != nil {
		t.Fatal(err)
	}
	if err := vector.AttachPublisherEvidence(publisherEvidence); err != nil {
		t.Fatal(err)
	}
	raw, err := vector.MarshalQualified(ctx)
	if err != nil {
		t.Fatal(err)
	}
	receipt := authenticateVectorReceipt(t, raw)
	if err := verifyEvidenceDigests(ctx, receipt, resolved, report); err != nil {
		t.Fatalf("matching authenticated publisher evidence failed: %v", err)
	}

	wrong := *vector
	wrongEvidence := *vector.Evidence
	wrong.Evidence = &wrongEvidence
	wrong.EvidenceDigest = verifyTestDigest('a')
	wrongRaw, err := wrong.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	wrongReceipt := authenticateVectorReceipt(t, wrongRaw)
	if err := verifyEvidenceDigests(ctx, wrongReceipt, resolved, report); cliErrorCode(err) != nkderr.CodeVerifyFailed {
		t.Fatalf("signed but false publisher evidence was not rejected: %v", err)
	}
	if err := verifyEvidenceDigests(ctx, trust.DocumentReceipt{}, resolved, report); cliErrorCode(err) != nkderr.CodeSignatureInvalid {
		t.Fatalf("zero authenticated-vector receipt was not rejected: %v", err)
	}
}

func baseVerifyArgs(t *testing.T, vectorPath string) []string {
	t.Helper()
	return []string{
		"verify",
		"--expected-digests", vectorPath,
		"--signature", "vector.sig",
		"--certificate", "vector.pem",
		"--identity", vectorSigner,
		"--issuer", vectorIssuer,
		"--trust-store", filepath.Join(t.TempDir(), "absent-trust-store.json"),
	}
}

func runVerifyCLI(ctx context.Context, deps qualifiedVerifyDependencies, args []string) error {
	app := &cli.App{Commands: []*cli.Command{cmdVerifyWithDependencies(deps)}}
	return app.RunContext(ctx, append([]string{"nkdctl"}, args...))
}

func TestStockVerifyRequiresProducerResultsBeforeAuthorityIO(t *testing.T) {
	app := &cli.App{Commands: []*cli.Command{cmdVerify()}}
	err := app.Run([]string{
		"nkdctl", "verify", "--expected-digests", "must-not-be-read/vector.json",
		"--signature", "must-not-be-read/vector.sig", "--certificate", "must-not-be-read/vector.pem",
		"--bundle", "must-not-be-read/vector.bundle", "--trusted-root", "must-not-be-read/root.json",
		"--trusted-root-digest", verifyTestDigest('1'), "--identity", vectorSigner, "--issuer", vectorIssuer,
		"--cosign", "must-not-be-run/cosign", "--cosign-digest", verifyTestDigest('2'),
		"--trust-store", "must-not-be-read/trust.json",
	})
	if cliErrorCode(err) != nkderr.CodeEngineCapability {
		t.Fatalf("missing producer result source reached trust/profile/catalog I/O: %v", err)
	}
}

func cliErrorCode(err error) nkderr.Code {
	var coded *nkderr.Error
	if errors.As(err, &coded) {
		return coded.Code
	}
	return ""
}

func TestVerifyCLIUsesAuthenticatedVectorSnapshotAfterPathMutation(t *testing.T) {
	type requestContextKey struct{}
	ctx := context.WithValue(context.Background(), requestContextKey{}, "request-context")
	original := manifestBytes(t, prebuiltVector(testArtifactDigest))
	changed := manifestBytes(t, prebuiltVector("sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"))
	vectorPath := writeCLIFile(t, "vector.json", original)
	buildCalls := 0
	deps := qualifiedVerifyDependencies{
		Verifier: cliVerifierFunc(func(gotContext context.Context, req trust.DocumentVerificationRequest) (trust.VerificationEvidence, error) {
			if gotContext.Value(requestContextKey{}) != "request-context" {
				t.Fatal("CLI did not propagate request context to document verifier")
			}
			if req.Role != trust.RoleValidationVector {
				t.Fatalf("unexpected signer role %q", req.Role)
			}
			if req.SnapshotPath == vectorPath {
				t.Fatal("CLI passed mutable vector pathname to verifier")
			}
			snapshot, err := os.ReadFile(req.SnapshotPath)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(snapshot, original) {
				t.Fatalf("private vector snapshot differs: %s", snapshot)
			}
			if err := os.WriteFile(vectorPath, changed, 0o600); err != nil {
				t.Fatal(err)
			}
			return cliEvidence(vectorSigner, vectorIssuer), nil
		}),
		Resolve: func(*cli.Context) (*recipe.Resolved, string, error) { return prebuiltResolved(), "", nil },
		Build: func(c *cli.Context, _ *recipe.Resolved, _ *profile.BuilderProfile, _ profile.HostPreflightReceipt) (*pipeline.Report, error) {
			if c.Context.Value(requestContextKey{}) != "request-context" {
				t.Fatal("CLI did not propagate request context to build seam")
			}
			buildCalls++
			return prebuiltReport(t, testArtifactDigest), nil
		},
		ObserveHost:    func() map[string]string { return map[string]string{} },
		VerifyEvidence: acceptTestEvidence,
	}
	if err := runVerifyCLI(ctx, deps, baseVerifyArgs(t, vectorPath)); err != nil {
		t.Fatalf("qualified verify must use authenticated original bytes: %v", err)
	}
	if buildCalls != 1 {
		t.Fatalf("build calls=%d want=1", buildCalls)
	}
}

func TestVerifyCLIRejectsEvidenceFreeSignedVectorBeforeBuild(t *testing.T) {
	vector := prebuiltVector(testArtifactDigest)
	vector.Evidence = nil
	vector.EvidenceDigest = ""
	path := writeCLIFile(t, "evidence-free-vector.json", manifestBytes(t, vector))
	buildCalled := false
	deps := qualifiedVerifyDependencies{
		Verifier: cliVerifierFunc(func(context.Context, trust.DocumentVerificationRequest) (trust.VerificationEvidence, error) {
			return cliEvidence(vectorSigner, vectorIssuer), nil
		}),
		Resolve: func(*cli.Context) (*recipe.Resolved, string, error) {
			t.Fatal("evidence-free vector reached resolution")
			return nil, "", nil
		},
		Build: func(*cli.Context, *recipe.Resolved, *profile.BuilderProfile, profile.HostPreflightReceipt) (*pipeline.Report, error) {
			buildCalled = true
			return nil, nil
		},
		ObserveHost:    func() map[string]string { return map[string]string{} },
		VerifyEvidence: acceptTestEvidence,
	}
	err := runVerifyCLI(context.Background(), deps, baseVerifyArgs(t, path))
	if cliErrorCode(err) != nkderr.CodeVerifyFailed || buildCalled {
		t.Fatalf("evidence-free signed vector was not rejected before build: called=%v err=%v", buildCalled, err)
	}
}

func TestStockVerifierRequiresMeasureOnlyResults(t *testing.T) {
	flags := flag.NewFlagSet("verify", flag.ContinueOnError)
	flags.String("results-dir", "", "")
	ctx := cli.NewContext(&cli.App{}, flags, nil)
	if _, err := runMeasureOnlyVerification(ctx, prebuiltResolved(), nil, profile.HostPreflightReceipt{}); cliErrorCode(err) != nkderr.CodeEngineCapability {
		t.Fatalf("stock verifier attempted an in-process rebuild: %v", err)
	}
}

func TestStockQualifiedVerifierRequiresBinaryPinBeforeDocumentIO(t *testing.T) {
	missingVector := filepath.Join(t.TempDir(), "does-not-exist.json")
	err := runVerifyCLI(context.Background(), defaultQualifiedVerifyDependencies(), []string{
		"verify", "--expected-digests", missingVector,
	})
	if cliErrorCode(err) != nkderr.CodeUnpinnedInput {
		t.Fatalf("stock verifier must reject an unpinned cosign executable before opening inputs: %v", err)
	}
}

func TestVerifyCLIMandatoryAuthenticationAndOfflineMaterial(t *testing.T) {
	vectorPath := writeCLIFile(t, "vector.json", manifestBytes(t, prebuiltVector(testArtifactDigest)))
	verifierCalls := 0
	deps := qualifiedVerifyDependencies{
		Verifier: cliVerifierFunc(func(context.Context, trust.DocumentVerificationRequest) (trust.VerificationEvidence, error) {
			verifierCalls++
			return cliEvidence(vectorSigner, vectorIssuer), nil
		}),
		Resolve: func(*cli.Context) (*recipe.Resolved, string, error) { return prebuiltResolved(), "", nil },
		Build: func(*cli.Context, *recipe.Resolved, *profile.BuilderProfile, profile.HostPreflightReceipt) (*pipeline.Report, error) {
			t.Fatal("authentication failure reached build")
			return nil, nil
		},
		ObserveHost:    func() map[string]string { return map[string]string{} },
		VerifyEvidence: acceptTestEvidence,
	}
	tests := []struct {
		name string
		args []string
		want nkderr.Code
	}{
		{
			name: "missing-signature-and-bundle",
			args: []string{"verify", "-e", vectorPath, "--certificate", "vector.pem", "--identity", vectorSigner, "--issuer", vectorIssuer},
			want: nkderr.CodeSignatureInvalid,
		},
		{
			name: "offline-missing-bundle",
			args: append(baseVerifyArgs(t, vectorPath), "--offline"),
			want: nkderr.CodeSignatureInvalid,
		},
		{
			name: "offline-missing-root",
			args: append(baseVerifyArgs(t, vectorPath), "--offline", "--bundle", "vector.bundle"),
			want: nkderr.CodeUnpinnedInput,
		},
		{
			name: "offline-path-only-root",
			args: append(baseVerifyArgs(t, vectorPath), "--offline", "--bundle", "vector.bundle", "--trusted-root", "root.json"),
			want: nkderr.CodeUnpinnedInput,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			before := verifierCalls
			err := runVerifyCLI(context.Background(), deps, tc.args)
			if cliErrorCode(err) != tc.want {
				t.Fatalf("code=%q want=%q: %v", cliErrorCode(err), tc.want, err)
			}
			if verifierCalls != before {
				t.Fatal("invalid material reached verifier adapter")
			}
		})
	}

	err := runVerifyCLI(context.Background(), deps,
		append(baseVerifyArgs(t, vectorPath), "--insecure-ignore-tlog"))
	if err == nil || !strings.Contains(err.Error(), "flag provided but not defined") {
		t.Fatalf("qualified CLI must not expose insecure-ignore-tlog: %v", err)
	}
}

func TestVerifyCLIRejectsFabricatedSignerAndNonCanonicalVector(t *testing.T) {
	canonicalPath := writeCLIFile(t, "vector.json", manifestBytes(t, prebuiltVector(testArtifactDigest)))
	noncanonicalPath := writeCLIFile(t, "vector-noncanonical.json",
		append(manifestBytes(t, prebuiltVector(testArtifactDigest)), '\n'))
	depsFor := func(evidence trust.VerificationEvidence) qualifiedVerifyDependencies {
		return qualifiedVerifyDependencies{
			Verifier: cliVerifierFunc(func(context.Context, trust.DocumentVerificationRequest) (trust.VerificationEvidence, error) {
				return evidence, nil
			}),
			Resolve: func(*cli.Context) (*recipe.Resolved, string, error) { return prebuiltResolved(), "", nil },
			Build: func(*cli.Context, *recipe.Resolved, *profile.BuilderProfile, profile.HostPreflightReceipt) (*pipeline.Report, error) {
				t.Fatal("rejected document reached build")
				return nil, nil
			},
			ObserveHost:    func() map[string]string { return map[string]string{} },
			VerifyEvidence: acceptTestEvidence,
		}
	}
	err := runVerifyCLI(context.Background(), depsFor(cliEvidence("attacker@example.test", vectorIssuer)), baseVerifyArgs(t, canonicalPath))
	if cliErrorCode(err) != nkderr.CodeUntrustedSigner {
		t.Fatalf("caller policy identity must not become verifier result: %v", err)
	}
	err = runVerifyCLI(context.Background(), depsFor(cliEvidence(vectorSigner, vectorIssuer)), baseVerifyArgs(t, noncanonicalPath))
	if cliErrorCode(err) != nkderr.CodeVerifyFailed {
		t.Fatalf("signed noncanonical vector must fail closed: %v", err)
	}
}

func TestVerifyCLIUsesAuthenticatedProfileObjectAfterPathMutation(t *testing.T) {
	originalProfile := profile.BuilderProfile{
		SchemaVersion:    profile.SchemaVersion,
		Identity:         "profile-original",
		Version:          "1",
		HostControlClass: profile.OperatorControlled,
		Provides:         []string{"oci-hermetic"},
		Fields: []profile.Field{
			{Name: profile.FieldTargetArch, Value: "amd64", HardFail: true, MeasurementStage: profile.MeasurementHost},
			{Name: profile.FieldRunnerDigest, Value: "sha256:" + strings.Repeat("4", 64), HardFail: true, MeasurementStage: profile.MeasurementExecution},
			{Name: profile.FieldRuntimeIdentity, Value: "sha256:" + strings.Repeat("5", 64), HardFail: true, MeasurementStage: profile.MeasurementExecution},
			{Name: profile.FieldOCISandboxMode, Value: "test-sandbox", HardFail: true, MeasurementStage: profile.MeasurementExecution},
		},
	}
	changedProfile := originalProfile
	changedProfile.Identity = "profile-swapped"
	changedProfile.Fields = []profile.Field{{Name: profile.FieldTargetArch, Value: "arm64", HardFail: true}}
	profileDigest, err := originalProfile.Digest()
	if err != nil {
		t.Fatal(err)
	}
	profileRaw, err := canonical.Marshal(originalProfile)
	if err != nil {
		t.Fatal(err)
	}
	changedProfileRaw, err := canonical.Marshal(changedProfile)
	if err != nil {
		t.Fatal(err)
	}
	profilePath := writeCLIFile(t, "profile.json", profileRaw)
	resolvedFixture := &recipe.Resolved{
		RecipeID: "test-recipe", Criteria: criteria.Criteria{OS: "ubuntu", Arch: "amd64"},
		Components: []catalog.NamedComponent{{Name: "artifact", Component: catalog.Component{
			FromSource: true, BuilderType: "oci-hermetic", Tag: "v1.0.0", SourceDate: 1,
			ToolchainImage: "toolchain@sha256:" + strings.Repeat("6", 64),
			Repo:           "https://source.example/repo", Commit: strings.Repeat("1", 40),
			TreeHash: strings.Repeat("2", 40), BuildCommand: []string{"true"},
			WriteDir: "out", ArtifactPath: "out/artifact", OutputType: "raw",
		}}},
	}
	graph, err := recipe.Compile(resolvedFixture)
	if err != nil {
		t.Fatal(err)
	}
	recipeDigest, err := graph.Digest()
	if err != nil {
		t.Fatal(err)
	}
	var selectedBuildNode ir.Node
	for _, node := range graph.Nodes {
		if node.Kind == ir.KindBuild {
			selectedBuildNode = node
		}
	}
	if selectedBuildNode.ID == "" {
		t.Fatal("fixture did not compile a build node")
	}
	vector := &attest.Manifest{
		SchemaVersion:  attest.SchemaVersion,
		RecipeID:       "test-recipe",
		RecipeDigest:   recipeDigest,
		ProfileDigest:  profileDigest,
		BuilderProfile: &attest.BuilderProfileRef{Digest: profileDigest},
		Components: []attest.ComponentDigest{{
			Name: "artifact", OutputType: "raw", SHA256: testArtifactDigest, InputDigest: testInputDigest,
			BuilderType: "oci-hermetic", EngineClass: "oci-hermetic",
			Toolchain: "sha256:" + strings.Repeat("6", 64),
		}},
	}
	vector.AttachEvidence(&attest.EvidenceRef{SBOMDigest: verifyTestDigest('8'), ProvenanceDigest: verifyTestDigest('9')}, verifyTestDigest('7'))
	vectorPath := writeCLIFile(t, "vector.json", manifestBytes(t, vector))
	profileCalls := 0
	deps := qualifiedVerifyDependencies{
		Verifier: cliVerifierFunc(func(_ context.Context, req trust.DocumentVerificationRequest) (trust.VerificationEvidence, error) {
			switch req.Role {
			case trust.RoleValidationVector:
				return cliEvidence(vectorSigner, vectorIssuer), nil
			case trust.RoleBuilderProfile:
				profileCalls++
				if req.SnapshotPath == profilePath {
					t.Fatal("CLI passed mutable profile path to verifier")
				}
				if err := os.WriteFile(profilePath, changedProfileRaw, 0o600); err != nil {
					t.Fatal(err)
				}
				return cliEvidence(profileSigner, profileIssuer), nil
			default:
				t.Fatalf("unexpected role %q", req.Role)
				return trust.VerificationEvidence{}, nil
			}
		}),
		Resolve: func(*cli.Context) (*recipe.Resolved, string, error) { return resolvedFixture, recipeDigest, nil },
		Build: func(c *cli.Context, resolved *recipe.Resolved, gotProfile *profile.BuilderProfile, host profile.HostPreflightReceipt) (*pipeline.Report, error) {
			if gotProfile == nil || gotProfile.Identity != originalProfile.Identity {
				t.Fatalf("build did not receive authenticated profile object: %#v", gotProfile)
			}
			buildNode := selectedBuildNode
			observation := profile.ExecutionObservation{
				RunnerDigest: "sha256:" + strings.Repeat("4", 64), RunnerOrigin: profile.RunnerPreexisting,
				RuntimeIdentity: "sha256:" + strings.Repeat("5", 64),
				Nodes: []profile.NodeExecutionObservation{{
					NodeID: buildNode.ID, EngineClass: ir.EngineOCIHermetic,
					ToolchainDigest: "sha256:" + strings.Repeat("6", 64), SandboxMode: "test-sandbox",
				}},
			}
			receipt, err := profile.VerifyExecution(c.Context, host, *gotProfile, []ir.Node{buildNode}, observation)
			if err != nil {
				t.Fatal(err)
			}
			work := filepath.Join(t.TempDir(), "work")
			sourceRoot := filepath.Join(work, "src")
			artifactPath := pipeline.ArtifactPath(pipeline.RunOptions{SourceRoot: sourceRoot}, buildNode)
			if err := os.MkdirAll(filepath.Dir(artifactPath), 0o755); err != nil {
				return nil, err
			}
			if err := os.WriteFile(artifactPath, []byte("artifact"), 0o755); err != nil {
				return nil, err
			}
			result := pipeline.ArtifactResult{
				Component: "artifact", OutputType: "raw", Path: buildNode.ArtifactPath,
				SHA256: testArtifactDigest, Size: int64(len("artifact")), InputDigest: testInputDigest,
				Qualification: pipeline.ArtifactQualifiedExecution,
				BuilderType:   "oci-hermetic", EngineClass: "oci-hermetic", Toolchain: buildNode.Toolchain,
				ProfileDigest: receipt.ProfileDigest(), ExecutionBinding: receipt.ExecutionBinding(),
			}
			raw, err := canonical.Marshal(result)
			if err != nil {
				return nil, err
			}
			resultsDir := pipeline.ResultDirectory(sourceRoot)
			if err := os.MkdirAll(resultsDir, 0o755); err != nil {
				return nil, err
			}
			if err := os.WriteFile(filepath.Join(resultsDir, buildNode.ID+".json"), raw, 0o600); err != nil {
				return nil, err
			}
			return pipeline.Local{}.VerifyResults(graph, recipeDigest, resultsDir, pipeline.RunOptions{
				Ctx: c.Context, SourceRoot: sourceRoot, Runtime: os.Args[0],
				Qualification: &pipeline.QualifiedExecution{
					Profile: *gotProfile, HostReceipt: host,
					Observer: cliExecutionObserverFunc(func(context.Context, pipeline.ExecutionObservationRequest) (profile.ExecutionObservation, error) {
						return observation, nil
					}),
				},
			})
		},
		ObserveHost:    func() map[string]string { return map[string]string{profile.FieldTargetArch: "amd64"} },
		VerifyEvidence: acceptTestEvidence,
	}
	args := append(baseVerifyArgs(t, vectorPath),
		"--profile", profilePath,
		"--profile-signature", "profile.sig",
		"--profile-certificate", "profile.pem",
		"--profile-identity", profileSigner,
		"--profile-issuer", profileIssuer,
	)
	if err := runVerifyCLI(context.Background(), deps, args); err != nil {
		t.Fatalf("qualified verify must use authenticated profile object: %v", err)
	}
	if profileCalls != 1 {
		t.Fatalf("profile verifier calls=%d want=1", profileCalls)
	}
}

func TestVerifyCLITerminalResultsUseReceiptsWithoutSourceBuild(t *testing.T) {
	work := filepath.Join(t.TempDir(), "work")
	res := &recipe.Resolved{
		RecipeID: "terminal-recipe",
		Criteria: criteria.Criteria{OS: "ubuntu", Arch: "amd64"},
		Components: []catalog.NamedComponent{{Name: "artifact", Component: catalog.Component{
			FromSource: true, BuilderType: ir.BuilderOCIHermetic, Tag: "v1.0.0", SourceDate: 1,
			Repo: "file:///definitely-not-a-source-repository", Commit: strings.Repeat("1", 40),
			TreeHash: strings.Repeat("2", 40), ToolchainImage: "toolchain@sha256:" + strings.Repeat("3", 64),
			OutputType: "raw", BuildCommand: []string{"false"}, WriteDir: "out", ArtifactPath: "out/artifact",
		}}},
	}
	graph, err := recipe.Compile(res)
	if err != nil {
		t.Fatal(err)
	}
	recipeDigest, err := graph.Digest()
	if err != nil {
		t.Fatal(err)
	}
	var buildNode ir.Node
	for _, node := range graph.Nodes {
		if node.Kind == ir.KindBuild {
			buildNode = node
		}
	}
	if buildNode.ID == "" {
		t.Fatal("fixture did not compile a build node")
	}

	payload := []byte("artifact produced by isolated CI job")
	payloadSum := sha256.Sum256(payload)
	artifactDigest := "sha256:" + hex.EncodeToString(payloadSum[:])
	sourceRoot := filepath.Join(work, "src")
	artifactPath := pipeline.ArtifactPath(pipeline.RunOptions{SourceRoot: sourceRoot}, buildNode)
	if err := os.MkdirAll(filepath.Dir(artifactPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(artifactPath, payload, 0o755); err != nil {
		t.Fatal(err)
	}
	result := pipeline.ArtifactResult{
		Component: buildNode.Component, OutputType: buildNode.OutputType, Toolchain: buildNode.Toolchain,
		Qualification: pipeline.ArtifactUnqualified,
		InputDigest:   testInputDigest, BuilderType: buildNode.BuilderType,
		EngineClass: ir.EngineOCIHermetic, Path: buildNode.ArtifactPath,
		SHA256: artifactDigest, Size: int64(len(payload)),
	}
	resultsDir := filepath.Join(work, "results")
	if err := os.MkdirAll(resultsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	resultRaw, err := canonical.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(resultsDir, buildNode.ID+".json"), resultRaw, 0o600); err != nil {
		t.Fatal(err)
	}

	builderProfile := profile.BuilderProfile{
		SchemaVersion: profile.SchemaVersion, Identity: "terminal-profile", Version: "1",
		HostControlClass: profile.OperatorControlled, Provides: []string{ir.BuilderOCIHermetic},
		Fields: []profile.Field{
			{Name: profile.FieldTargetArch, Value: "amd64", HardFail: true, MeasurementStage: profile.MeasurementHost},
			{Name: profile.FieldRunnerDigest, Value: "sha256:" + strings.Repeat("4", 64), HardFail: true, MeasurementStage: profile.MeasurementExecution},
			{Name: profile.FieldRuntimeIdentity, Value: "sha256:" + strings.Repeat("5", 64), HardFail: true, MeasurementStage: profile.MeasurementExecution},
			{Name: profile.FieldOCISandboxMode, Value: "test-sandbox", HardFail: true, MeasurementStage: profile.MeasurementExecution},
		},
	}
	profileDigest, err := builderProfile.Digest()
	if err != nil {
		t.Fatal(err)
	}
	profileRaw, err := canonical.Marshal(builderProfile)
	if err != nil {
		t.Fatal(err)
	}
	profilePath := writeCLIFile(t, "terminal-profile.json", profileRaw)
	toolchainIdentity := buildNode.Toolchain
	if _, digest, ok := strings.Cut(toolchainIdentity, "@"); ok {
		toolchainIdentity = digest
	}
	vector := &attest.Manifest{
		SchemaVersion: attest.SchemaVersion, RecipeID: res.RecipeID, RecipeDigest: recipeDigest,
		ProfileDigest: profileDigest, BuilderProfile: &attest.BuilderProfileRef{Digest: profileDigest},
		Components: []attest.ComponentDigest{{
			Name: buildNode.Component, OutputType: buildNode.OutputType, Toolchain: toolchainIdentity,
			InputDigest: testInputDigest, BuilderType: buildNode.BuilderType,
			EngineClass: ir.EngineOCIHermetic, SHA256: artifactDigest,
		}},
	}
	vector.AttachEvidence(&attest.EvidenceRef{SBOMDigest: verifyTestDigest('8'), ProvenanceDigest: verifyTestDigest('9')}, verifyTestDigest('7'))
	vectorPath := writeCLIFile(t, "terminal-vector.json", manifestBytes(t, vector))
	roles := map[trust.SignerRole]int{}
	deps := qualifiedVerifyDependencies{
		Verifier: cliVerifierFunc(func(_ context.Context, req trust.DocumentVerificationRequest) (trust.VerificationEvidence, error) {
			roles[req.Role]++
			switch req.Role {
			case trust.RoleValidationVector:
				if err := os.WriteFile(vectorPath, []byte(`{"schemaVersion":"attacker"}`), 0o600); err != nil {
					t.Fatal(err)
				}
				return cliEvidence(vectorSigner, vectorIssuer), nil
			case trust.RoleBuilderProfile:
				if err := os.WriteFile(profilePath, []byte(`{"schemaVersion":"attacker"}`), 0o600); err != nil {
					t.Fatal(err)
				}
				return cliEvidence(profileSigner, profileIssuer), nil
			default:
				t.Fatalf("unexpected signer role %q", req.Role)
				return trust.VerificationEvidence{}, nil
			}
		}),
		Resolve: func(*cli.Context) (*recipe.Resolved, string, error) { return res, recipeDigest, nil },
		Build: func(c *cli.Context, _ *recipe.Resolved, gotProfile *profile.BuilderProfile, host profile.HostPreflightReceipt) (*pipeline.Report, error) {
			observation := profile.ExecutionObservation{
				RunnerDigest: "sha256:" + strings.Repeat("4", 64), RunnerOrigin: profile.RunnerPreexisting,
				RuntimeIdentity: "sha256:" + strings.Repeat("5", 64),
				Nodes: []profile.NodeExecutionObservation{{
					NodeID: buildNode.ID, EngineClass: ir.EngineOCIHermetic,
					ToolchainDigest: "sha256:" + strings.Repeat("3", 64), SandboxMode: "test-sandbox",
				}},
			}
			receipt, err := profile.VerifyExecution(c.Context, host, *gotProfile, []ir.Node{buildNode}, observation)
			if err != nil {
				return nil, err
			}
			boundResult := result
			boundResult.Qualification = pipeline.ArtifactQualifiedExecution
			boundResult.ProfileDigest = receipt.ProfileDigest()
			boundResult.ExecutionBinding = receipt.ExecutionBinding()
			raw, err := canonical.Marshal(boundResult)
			if err != nil {
				return nil, err
			}
			if err := os.WriteFile(filepath.Join(resultsDir, buildNode.ID+".json"), raw, 0o600); err != nil {
				return nil, err
			}
			return pipeline.Local{}.VerifyResults(graph, recipeDigest, resultsDir, pipeline.RunOptions{
				Ctx: c.Context, SourceRoot: sourceRoot, Runtime: os.Args[0],
				Qualification: &pipeline.QualifiedExecution{
					Profile: *gotProfile, HostReceipt: host,
					Observer: cliExecutionObserverFunc(func(context.Context, pipeline.ExecutionObservationRequest) (profile.ExecutionObservation, error) {
						return observation, nil
					}),
				},
			})
		},
		ObserveHost: func() map[string]string {
			return map[string]string{profile.FieldTargetArch: "amd64"}
		},
		VerifyEvidence: acceptTestEvidence,
	}
	args := append(baseVerifyArgs(t, vectorPath),
		"--profile", profilePath,
		"--profile-signature", "profile.sig",
		"--profile-certificate", "profile.pem",
		"--profile-identity", profileSigner,
		"--profile-issuer", profileIssuer,
		"--source-root", sourceRoot,
		"--results-dir", resultsDir,
		"--docker", "/definitely-not-a-container-runtime",
	)
	if err := runVerifyCLI(context.Background(), deps, args); err != nil {
		t.Fatalf("terminal results must verify through authenticated receipts without rebuilding: %v", err)
	}
	if roles[trust.RoleValidationVector] != 1 || roles[trust.RoleBuilderProfile] != 1 {
		t.Fatalf("terminal verification did not authenticate both role receipts: %v", roles)
	}
}

func TestVectorProfileBindingFailsClosedOnIncompleteStructuredReference(t *testing.T) {
	for _, vector := range []*attest.Manifest{
		{ProfileDigest: testArtifactDigest, BuilderProfile: &attest.BuilderProfileRef{}},
		{BuilderProfile: &attest.BuilderProfileRef{Digest: testArtifactDigest}},
	} {
		if _, err := vectorProfileDigest(vector); cliErrorCode(err) != nkderr.CodeVerifyFailed {
			t.Fatalf("incomplete profile binding must fail closed: %v", err)
		}
	}
}
