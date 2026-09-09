package factory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/nhensley/nkdctl/pkg/nkd/delivery"
	e "github.com/nhensley/nkdctl/pkg/nkd/errors"
	"github.com/nhensley/nkdctl/pkg/nkd/pipeline"
	"github.com/nhensley/nkdctl/pkg/nkd/provenance"
	"github.com/nhensley/nkdctl/pkg/nkd/sign"
)

// fakeOCISigner records what it was asked to bind and returns exactly what the
// test tells it to. It is deliberately NOT a signer: the executor's contract is
// that it authenticates the subject BEFORE any adapter is consulted, and that it
// writes no record when the adapter refuses.
type fakeOCISigner struct {
	identity  string
	issuer    string
	signErr   error
	verifyErr error

	signedSubject   sign.Subject
	signedPredicate []byte
	verifiedSubject sign.Subject
	verifiedPolicy  sign.VerifyPolicy
}

func (f *fakeOCISigner) SignAndAttest(_ context.Context, subject sign.Subject, predicate []byte) (sign.SigningRecord, error) {
	f.signedSubject = subject
	f.signedPredicate = append([]byte(nil), predicate...)
	if f.signErr != nil {
		return sign.SigningRecord{}, f.signErr
	}
	return sign.SigningRecord{
		SchemaVersion: sign.SigningRecordSchemaVersion, DryRun: true,
		Ref: subject.Ref, Digest: subject.Digest,
		PredicateSHA256: pipeline.FileDigest(predicate),
		PredicateType:   sign.ProvenancePredicateType,
		Identity:        f.identity, Issuer: f.issuer,
		WouldRun: [][]string{{"cosign", "sign", "--yes", subject.Ref}},
	}, nil
}

func (f *fakeOCISigner) Verify(_ context.Context, subject sign.Subject, policy sign.VerifyPolicy) (sign.VerificationRecord, error) {
	f.verifiedSubject, f.verifiedPolicy = subject, policy
	if f.verifyErr != nil {
		return sign.VerificationRecord{}, f.verifyErr
	}
	return sign.VerificationRecord{
		SchemaVersion: sign.VerificationRecordSchemaVersion, DryRun: true,
		Ref: subject.Ref, Digest: subject.Digest,
		Identity: policy.IdentityRegexp, Issuer: policy.Issuer,
		PredicateType: sign.ProvenancePredicateType,
		WouldRun:      [][]string{{"cosign", "verify", subject.Ref}},
	}, nil
}

func fakeSignerFactory(signer *fakeOCISigner) signerFor {
	return func(context.Context, *Plan, Node, map[string]Artifact) (sign.OCISigner, error) {
		return signer, nil
	}
}

// signingHarness is one temp workspace holding a real OCI publication layout, a
// real in-process registry it was pushed to, and the compiled plan that routes it.
type signingHarness struct {
	plan      *Plan
	artifacts map[string]Artifact
	nodes     map[string]Node
	reference string
	digest    string
	root      string
	target    Target
}

func newSigningHarness(t *testing.T) *signingHarness {
	t.Helper()
	t.Chdir(t.TempDir())
	// The signing lane must not silently believe it is running inside GitHub Actions.
	t.Setenv("GITHUB_ACTIONS", "")
	server := httptest.NewServer(registry.New(registry.Logger(log.New(io.Discard, "", 0))))
	t.Cleanup(server.Close)
	host := strings.TrimPrefix(server.URL, "http://")
	target := Target{Kind: TargetOCI, Ref: host + "/nkd/tools:dev", Anonymous: true, PlainHTTP: true}
	plan := signedTestPlan(t, dryRunSigningProfile(), target)
	nodes, artifacts := planIndex(plan)
	root := artifacts["package/publication"].Path
	buildTestPublicationRoot(t, root, "nkectl bytes")
	reference, err := delivery.Push(context.Background(), root, target.Ref,
		delivery.RegistryOptions{PlainHTTP: true, Anonymous: true})
	if err != nil {
		t.Fatalf("publishing test layout: %v", err)
	}
	writeReferenceFile(t, artifacts["publication/publication"].Path, reference)
	return &signingHarness{plan: plan, artifacts: artifacts, nodes: nodes,
		reference: reference, digest: reference[strings.LastIndex(reference, "@")+1:],
		root: root, target: target}
}

func buildTestPublicationRoot(t *testing.T, out, payload string) {
	t.Helper()
	source := filepath.Join(t.TempDir(), "payload.bin")
	if err := os.WriteFile(source, []byte(payload), 0o644); err != nil {
		t.Fatal(err)
	}
	digest, err := digestFactoryPath(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := delivery.BuildPublicationRoot(context.Background(), out, delivery.PublicationRequest{
		Objects: []delivery.ObjectInput{{
			Name: "tools", Kind: PackageBinaryBundle, Path: source,
			ArtifactType:   "application/vnd.nkd.binary-bundle.v1",
			LayerMediaType: "application/vnd.nkd.binary-bundle.v1.tar",
			SemanticDigest: digest, BlobDigest: digest,
		}},
	}); err != nil {
		t.Fatalf("building test publication root: %v", err)
	}
}

func writeReferenceFile(t *testing.T, path, reference string) {
	t.Helper()
	if err := writeFactoryFile(context.Background(), path, []byte(reference+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestExecuteSignBindsTheExactPublishedDigestAndWritesTheProductionPredicate(t *testing.T) {
	harness := newSigningHarness(t)
	signer := &fakeOCISigner{identity: "^local$", issuer: "https://nkd.dev/local"}
	if err := executeSign(context.Background(), harness.plan, harness.nodes["sign/publication"],
		harness.artifacts, fakeSignerFactory(signer), io.Discard); err != nil {
		t.Fatalf("sign: %v", err)
	}
	if signer.signedSubject.Ref != harness.reference || signer.signedSubject.Digest != harness.digest {
		t.Fatalf("signed subject = %+v, want %s", signer.signedSubject, harness.reference)
	}

	var predicate provenance.Predicate
	raw, err := os.ReadFile(harness.artifacts["predicate/publication"].Path)
	if err != nil {
		t.Fatalf("predicate was not written: %v", err)
	}
	if err := json.Unmarshal(raw, &predicate); err != nil {
		t.Fatal(err)
	}
	if err := provenance.ValidateProduction(predicate); err != nil {
		t.Fatalf("emitted predicate is not a valid production predicate: %v", err)
	}
	planDigest, err := harness.plan.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if predicate.BuildDefinition.ExternalParameters["planDigest"] != planDigest ||
		predicate.BuildDefinition.ExternalParameters["compileSpecDigest"] != harness.plan.CompileSpecDigest ||
		predicate.BuildDefinition.ExternalParameters["executionSettingsDigest"] != harness.plan.ExecutionSettingsDigest ||
		predicate.BuildDefinition.ExternalParameters["catalogDigest"] != harness.plan.CatalogDigest {
		t.Fatalf("predicate external parameters do not bind the plan: %+v", predicate.BuildDefinition.ExternalParameters)
	}
	if predicate.BuildDefinition.BuildType != provenance.BuildTypeHermeticBuild {
		t.Fatalf("build type = %q", predicate.BuildDefinition.BuildType)
	}
	if predicate.BuildDefinition.InternalParameters["signingMode"] != SigningModeDryRun {
		t.Fatalf("internal parameters = %+v", predicate.BuildDefinition.InternalParameters)
	}
	if predicate.RunDetails.Builder.ID != provenance.LocalBuilderID {
		t.Fatalf("builder id = %q, want the explicitly local builder", predicate.RunDetails.Builder.ID)
	}
	sources, toolchains := 0, 0
	for _, dependency := range predicate.BuildDefinition.ResolvedDependencies {
		if dependency.Digest["gitCommit"] != "" {
			sources++
		}
		if strings.Contains(dependency.URI, "@sha256:") {
			toolchains++
		}
	}
	if sources == 0 || toolchains == 0 {
		t.Fatalf("resolved dependencies omit sources or toolchains: %+v", predicate.BuildDefinition.ResolvedDependencies)
	}

	// The signing record must be canonical, re-readable, and bound to the predicate.
	record, err := readSigningRecord(context.Background(), harness.artifacts["signature/publication"].Path)
	if err != nil {
		t.Fatalf("signing record: %v", err)
	}
	// The recorded hash must cover the predicate file EXACTLY as it sits on disk. A
	// verifier — or a real `cosign attest --predicate <file>` — reads those bytes and
	// nothing else, so hashing any other slice of them (for instance, silently dropping
	// the trailing newline) would make predicateSha256 uncheckable against the artifact
	// it names.
	if record.Ref != harness.reference || record.PredicateSHA256 != pipeline.FileDigest(raw) {
		t.Fatalf("signing record does not bind the exact subject and predicate: %+v", record)
	}
	if !bytes.Equal(signer.signedPredicate, raw) {
		t.Fatalf("the signer was handed %d predicate bytes but %d were written to disk",
			len(signer.signedPredicate), len(raw))
	}
}

// The reference file is data written by a previous job. If it disagrees with the
// layout this run actually routed, signing must refuse rather than bind whichever
// identity happens to be more convenient.
func TestExecuteSignRefusesWhenTheRoutedLayoutIsNotWhatWasPublished(t *testing.T) {
	harness := newSigningHarness(t)
	// A rerun of the packaging node that produced different bytes: the layout is
	// legitimately replaced, and the previously published reference is now stale.
	if err := os.RemoveAll(harness.root); err != nil {
		t.Fatal(err)
	}
	buildTestPublicationRoot(t, harness.root, "different bytes entirely")
	signer := &fakeOCISigner{identity: "^local$", issuer: "https://nkd.dev/local"}
	err := executeSign(context.Background(), harness.plan, harness.nodes["sign/publication"],
		harness.artifacts, fakeSignerFactory(signer), io.Discard)
	if err == nil {
		t.Fatal("signing bound a digest the routed layout does not produce")
	}
	var coded *e.Error
	if !errors.As(err, &coded) || coded.Code != e.CodeVerifyFailed {
		t.Fatalf("refusal = %v, want %s", err, e.CodeVerifyFailed)
	}
	if signer.signedSubject.Ref != "" {
		t.Fatal("the signing adapter was consulted before the subject was authenticated")
	}
	assertNotWritten(t, harness.artifacts["signature/publication"].Path)
	assertNotWritten(t, harness.artifacts["predicate/publication"].Path)
}

// A signing adapter that refuses must leave the run with no record at all: a
// predicate on disk with no signing record beside it would be read as "we signed
// this, the record is just missing".
func TestExecuteSignWritesNothingWhenTheAdapterRefuses(t *testing.T) {
	harness := newSigningHarness(t)
	refusing := &fakeOCISigner{identity: "^local$", issuer: "https://nkd.dev/local",
		signErr: e.New(e.CodeTrustPolicyMissing, "no signing identity available")}
	if err := executeSign(context.Background(), harness.plan, harness.nodes["sign/publication"],
		harness.artifacts, fakeSignerFactory(refusing), io.Discard); err == nil {
		t.Fatal("a refused signature was reported as success")
	}
	assertNotWritten(t, harness.artifacts["signature/publication"].Path)
	assertNotWritten(t, harness.artifacts["predicate/publication"].Path)
}

func TestExecuteVerifyPublicationChecksTheRegistryAndFailsClosed(t *testing.T) {
	harness := newSigningHarness(t)
	signer := &fakeOCISigner{identity: "^local$", issuer: "https://nkd.dev/local"}
	signNode := harness.nodes["sign/publication"]
	verifyNode := harness.nodes["verify-publication/publication"]
	if err := executeSign(context.Background(), harness.plan, signNode, harness.artifacts,
		fakeSignerFactory(signer), io.Discard); err != nil {
		t.Fatal(err)
	}
	verificationPath := harness.artifacts["verification/publication"].Path

	if err := executeVerifyPublication(context.Background(), harness.plan, verifyNode, harness.artifacts,
		fakeSignerFactory(signer), io.Discard); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if signer.verifiedSubject.Digest != harness.digest ||
		signer.verifiedPolicy.IdentityRegexp != "^local$" || signer.verifiedPolicy.Issuer != "https://nkd.dev/local" ||
		!signer.verifiedPolicy.RequireAttestation {
		t.Fatalf("verification policy is not the delivery's: %+v / %+v", signer.verifiedSubject, signer.verifiedPolicy)
	}
	if _, err := os.Stat(verificationPath); err != nil {
		t.Fatalf("verification record was not written: %v", err)
	}

	t.Run("adapter refusal writes no record", func(t *testing.T) {
		if err := os.Remove(verificationPath); err != nil {
			t.Fatal(err)
		}
		refusing := &fakeOCISigner{identity: "^local$", issuer: "https://nkd.dev/local",
			verifyErr: e.New(e.CodeSignatureInvalid, "no matching signatures")}
		if err := executeVerifyPublication(context.Background(), harness.plan, verifyNode, harness.artifacts,
			fakeSignerFactory(refusing), io.Discard); err == nil {
			t.Fatal("a refused verification was reported as success")
		}
		assertNotWritten(t, verificationPath)
	})

	t.Run("record about another publication", func(t *testing.T) {
		other := filepath.Join(t.TempDir(), "other-root")
		buildTestPublicationRoot(t, other, "another artifact entirely")
		otherRef, err := delivery.Push(context.Background(), other, harness.target.Ref,
			delivery.RegistryOptions{PlainHTTP: true, Anonymous: true})
		if err != nil {
			t.Fatal(err)
		}
		if otherRef == harness.reference {
			t.Fatal("the second publication is not actually different")
		}
		// The signature record now names a REAL, resolvable publication — just not the
		// one this route published. The digest chain must still break.
		signOther := &fakeOCISigner{identity: "^local$", issuer: "https://nkd.dev/local"}
		record, err := signOther.SignAndAttest(context.Background(),
			sign.Subject{Ref: otherRef, Digest: otherRef[strings.LastIndex(otherRef, "@")+1:]}, []byte("{}"))
		if err != nil {
			t.Fatal(err)
		}
		raw, err := record.Canonical()
		if err != nil {
			t.Fatal(err)
		}
		if err := writeFactoryFile(context.Background(), harness.artifacts["signature/publication"].Path,
			append(raw, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		err = executeVerifyPublication(context.Background(), harness.plan, verifyNode, harness.artifacts,
			fakeSignerFactory(signer), io.Discard)
		if err == nil {
			t.Fatal("a signing record about a different publication was accepted")
		}
		var coded *e.Error
		if !errors.As(err, &coded) || coded.Code != e.CodeVerifyFailed {
			t.Fatalf("refusal = %v, want %s", err, e.CodeVerifyFailed)
		}
		assertNotWritten(t, verificationPath)
	})

	t.Run("digest the registry does not serve", func(t *testing.T) {
		unknown := "sha256:" + strings.Repeat("ab", 32)
		repository := harness.reference[:strings.LastIndex(harness.reference, "@")]
		writeReferenceFile(t, harness.artifacts["publication/publication"].Path, repository+"@"+unknown)
		signed := &fakeOCISigner{identity: "^local$", issuer: "https://nkd.dev/local"}
		record, err := signed.SignAndAttest(context.Background(),
			sign.Subject{Ref: repository + "@" + unknown, Digest: unknown}, []byte("{}"))
		if err != nil {
			t.Fatal(err)
		}
		raw, err := record.Canonical()
		if err != nil {
			t.Fatal(err)
		}
		if err := writeFactoryFile(context.Background(), harness.artifacts["signature/publication"].Path,
			append(raw, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := executeVerifyPublication(context.Background(), harness.plan, verifyNode, harness.artifacts,
			fakeSignerFactory(signed), io.Discard); err == nil {
			t.Fatal("an unpublished digest verified successfully")
		}
		assertNotWritten(t, verificationPath)
	})
}

// A GitHub Actions job must attest as its workflow identity, not as an anonymous
// or local builder.
func TestSigningRunContextFollowsTheExecutionEnvironment(t *testing.T) {
	harness := newSigningHarness(t)
	t.Setenv("GITHUB_ACTIONS", "true")
	t.Setenv("GITHUB_SERVER_URL", "https://github.com")
	t.Setenv("GITHUB_REPOSITORY", "example/nkd")
	t.Setenv("GITHUB_WORKFLOW_REF", "example/nkd/.github/workflows/nkd-factory.yml@refs/heads/main")
	t.Setenv("GITHUB_RUN_ID", "42")
	t.Setenv("GITHUB_RUN_ATTEMPT", "1")
	signer := &fakeOCISigner{identity: "^local$", issuer: "https://nkd.dev/local"}
	if err := executeSign(context.Background(), harness.plan, harness.nodes["sign/publication"],
		harness.artifacts, fakeSignerFactory(signer), io.Discard); err != nil {
		t.Fatal(err)
	}
	var predicate provenance.Predicate
	if err := json.Unmarshal(signer.signedPredicate, &predicate); err != nil {
		t.Fatal(err)
	}
	want := "https://github.com/example/nkd/.github/workflows/nkd-factory.yml@refs/heads/main"
	if predicate.RunDetails.Builder.ID != want {
		t.Fatalf("builder id = %q, want %q", predicate.RunDetails.Builder.ID, want)
	}
	if predicate.RunDetails.Metadata["invocationId"] != "https://github.com/example/nkd/actions/runs/42/attempts/1" {
		t.Fatalf("invocation id = %q", predicate.RunDetails.Metadata["invocationId"])
	}
}

func assertNotWritten(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("%s exists after a failed step (err=%v); a refusal must leave no record", path, err)
	}
}
