// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package testreporting

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sign"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/testobservation"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/testresults"
)

func pin(raw string) string { return fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(raw))) }
func code(t *testing.T, err error, want e.Code) {
	t.Helper()
	var coded *e.Error
	if !errors.As(err, &coded) || coded.Code != want {
		t.Fatalf("error=%v; want %s", err, want)
	}
}

type fakeReceipt struct {
	ctx              context.Context
	observed         testobservation.Observation
	description      testobservation.Verification
	identity, issuer string
	raw              map[string][]byte
	closed           atomic.Bool
}

func (f *fakeReceipt) check(ctx context.Context) error {
	if err := checkContext(ctx); err != nil {
		return err
	}
	if f.closed.Load() {
		return failure("fixture receipt revoked")
	}
	return checkContext(f.ctx)
}
func (f *fakeReceipt) Observation(ctx context.Context) (testobservation.Observation, error) {
	if err := f.check(ctx); err != nil {
		return testobservation.Observation{}, err
	}
	raw, err := f.observed.Canonical()
	if err != nil {
		return testobservation.Observation{}, err
	}
	return testobservation.Decode(raw)
}
func (f *fakeReceipt) Description(ctx context.Context) (testobservation.Verification, error) {
	return f.description, f.check(ctx)
}
func (f *fakeReceipt) Signer(ctx context.Context) (string, string, error) {
	return f.identity, f.issuer, f.check(ctx)
}
func (f *fakeReceipt) Read(ctx context.Context, name string) ([]byte, error) {
	if err := f.check(ctx); err != nil {
		return nil, err
	}
	raw, ok := f.raw[name]
	if !ok {
		return nil, failure("fixture member missing")
	}
	return bytes.Clone(raw), nil
}
func (f *fakeReceipt) Close() error { f.closed.Store(true); return nil }
func fixture(t *testing.T, invocation, status, source, tier string) (Input, *fakeReceipt) {
	t.Helper()
	result := `<testsuite name="suite" tests="1" failures="0" errors="0" skipped="0"><testcase classname="class" name="case"/></testsuite>`
	execution := testobservation.Outcome{Status: "completed"}
	if status == "failed" {
		result = `<testsuite name="suite" tests="1" failures="1" errors="0" skipped="0"><testcase classname="class" name="case"><failure/></testcase></testsuite>`
		execution = testobservation.Outcome{Status: "failed", ExitCode: 1}
	}
	p := pin("binding")
	o := testobservation.Observation{SchemaVersion: testobservation.SchemaVersion, Inputs: testobservation.Inputs{DefinitionDigest: p, EnvironmentDigest: p, PlanDigest: p}, Origin: testobservation.Origin{Status: "declared", Repository: "https://github.com/example/producer", Commit: strings.Repeat("a", 40), Run: invocation, Attempt: 1, Workflow: "test.yml"}, Subjects: []testobservation.Subject{{Output: "controller", ArtifactDigest: p, PayloadDigest: p, BuildDigest: p, Platform: "linux/amd64"}}, Suite: testobservation.Suite{Name: "conformance", HarnessRevision: strings.Repeat("a", 40), ImageDigest: p, Selection: []string{"Conformance"}, PolicyDigest: p, Invocation: invocation, RequiredCases: []testobservation.CaseID{{Suite: "suite", Class: "class", Name: "case"}}}, Environment: testobservation.Environment{Role: "tenant", Facts: []testobservation.Fact{{Name: "testStartedUnix", Value: "1800000000", Evidence: "declared", Phase: "before"}, {Name: "testFinishedUnix", Value: "1800000060", Evidence: "declared", Phase: "after"}, {Name: "kubernetesVersion", Value: "v1.34.0", Evidence: "cluster-api", Phase: "before"}}}, Execution: execution, Cleanup: testobservation.Outcome{Status: "completed"}}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "junit.xml"), []byte(result), 0o600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "bundle")
	if err := testobservation.Emit(t.Context(), root, o, []testobservation.FileInput{{Name: "junit.xml", ExpectedDigest: pin(result), Role: "junit"}}, out); err != nil {
		t.Fatal(err)
	}
	bundle, err := testobservation.Open(t.Context(), out)
	if err != nil {
		t.Fatal(err)
	}
	defer bundle.Close()
	observed := bundle.Observation()
	identity := "https://github.com/example/producer/.github/workflows/test.yml@refs/heads/main"
	issuer := "https://token.actions.githubusercontent.com"
	if tier == Community {
		source, err = ProjectionSourcePath(p, identity, issuer)
		if err != nil {
			t.Fatal(err)
		}
	}
	admission := Admission{Identity: identity, Issuer: issuer, Tier: tier, Namespace: "ghcr.io/example/observations", SourcePath: source, DefinitionDigest: p}
	input := Input{Locator: testobservation.Locator{SchemaVersion: testobservation.LocatorSchemaVersion, Reference: admission.Namespace + "/" + source + "@" + pin("transport"+invocation), ObservationDigest: bundle.Digest()}, Expected: testobservation.Expected{ObservationDigest: bundle.Digest(), Inputs: observed.Inputs, Subjects: observed.Subjects, Suite: observed.Suite}, Admission: admission}
	receiptRaw, err := observed.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	receiptObservation, err := testobservation.Decode(receiptRaw)
	if err != nil {
		t.Fatal(err)
	}
	return input, &fakeReceipt{ctx: t.Context(), observed: receiptObservation, description: testobservation.Verification{ObservationDigest: bundle.Digest(), SignerAuthenticated: true, SelfConsistent: true, Passing: bundle.Passing()}, identity: identity, issuer: issuer, raw: map[string][]byte{"junit.xml": []byte(result)}}
}
func verifier(inputs []Input, receipts []*fakeReceipt, calls *int) verifyFunc {
	return func(ctx context.Context, input Input, options testobservation.RegistryOptions) (receipt, error) {
		for i, item := range inputs {
			if item.Locator.Reference == input.Locator.Reference {
				*calls++
				return receipts[i], nil
			}
		}
		return nil, failure("fixture missing")
	}
}
func TestIngestVerifiesEveryInputThenRetainsAttemptsFailuresSources(t *testing.T) {
	first, a := fixture(t, "run-1", "pass", "workflow/nightly", FirstParty)
	second, b := fixture(t, "run-2", "failed", "workflow/nightly", FirstParty)
	third, c := fixture(t, "run-1", "pass", "other/workflow", FirstParty)
	inputs := []Input{first, first, second, third}
	calls := 0
	report, err := ingestWith(t.Context(), inputs, testobservation.RegistryOptions{Transport: delivery.RegistryOptions{Anonymous: true}}, verifier([]Input{first, second, third}, []*fakeReceipt{a, b, c}, &calls))
	if err != nil {
		t.Fatal(err)
	}
	defer report.Close()
	index, err := report.Index(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if calls != 4 || len(index.Entries) != 3 {
		t.Fatalf("calls=%d entries=%d", calls, len(index.Entries))
	}
	failed := 0
	for _, entry := range index.Entries {
		if !entry.Passing {
			failed++
		}
	}
	if failed != 1 {
		t.Fatal("historical failure was lost")
	}
	raw, err := report.JSON(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := canonical.ValidateJSONDocument(raw); err != nil {
		t.Fatal(err)
	}
	md, err := report.Markdown(t.Context())
	if err != nil || !strings.Contains(md, "Authenticated historical test observations. These records do not establish current cluster health, release qualification or TestGrid publication.") {
		t.Fatalf("Markdown %q, %v", md, err)
	}
	inputs[0].Expected.Subjects[0].Output = "caller mutated"
	if _, err := report.Index(t.Context()); err != nil {
		t.Fatal("caller changed retained expectations", err)
	}
}
func TestCommunityRequiresAuthenticatedSignerProjectionAndDefinition(t *testing.T) {
	input, verified := fixture(t, "community-run", "pass", "ignored", Community)
	calls := 0
	report, err := ingestWith(t.Context(), []Input{input}, testobservation.RegistryOptions{Transport: delivery.RegistryOptions{Anonymous: true}}, verifier([]Input{input}, []*fakeReceipt{verified}, &calls))
	if err != nil {
		t.Fatal(err)
	}
	defer report.Close()
	index, err := report.Index(t.Context())
	if err != nil || len(index.Entries) != 1 || len(index.Entries[0].SignerProjectionToken) != 64 {
		t.Fatalf("community projection %+v, %v", index, err)
	}
	for _, kind := range []string{"identity", "issuer", "path", "definition", "revoked", "outside namespace"} {
		t.Run(kind, func(t *testing.T) {
			bad, r := fixture(t, "bad-"+kind, "pass", "ignored", Community)
			switch kind {
			case "identity":
				r.identity = "wrong"
			case "issuer":
				r.issuer = "wrong"
			case "path":
				bad.Admission.SourcePath = "community/another/signer-invalid"
				bad.Locator.Reference = bad.Admission.Namespace + "/" + bad.Admission.SourcePath + "@" + pin("transport")
			case "definition":
				bad.Admission.DefinitionDigest = pin("different")
			case "revoked":
				bad.Admission.Revoked = true
			case "outside namespace":
				bad.Locator.Reference = "ghcr.io/other/source@" + pin("transport")
			}
			calls := 0
			got, err := ingestWith(t.Context(), []Input{bad}, testobservation.RegistryOptions{Transport: delivery.RegistryOptions{Anonymous: true}}, func(context.Context, Input, testobservation.RegistryOptions) (receipt, error) { calls++; return r, nil })
			if got != nil || err == nil {
				t.Fatal("unadmitted community source accepted")
			}
			if (kind == "definition" || kind == "revoked" || kind == "outside namespace") && calls != 0 {
				t.Fatal("admission occurred after network work")
			}
			switch kind {
			case "identity", "issuer", "revoked":
				code(t, err, e.CodeUntrustedSigner)
			default:
				code(t, err, e.CodeVerifyFailed)
			}
		})
	}
}
func TestReportRevokedLifetimeAndStockAdmission(t *testing.T) {
	input, verified := fixture(t, "lifetime", "pass", "workflow/main", FirstParty)
	ctx, cancel := context.WithCancel(t.Context())
	verified.ctx = ctx
	report, err := ingestWith(ctx, []Input{input}, testobservation.RegistryOptions{Transport: delivery.RegistryOptions{Anonymous: true}}, func(context.Context, Input, testobservation.RegistryOptions) (receipt, error) { return verified, nil })
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	_, err = report.JSON(t.Context())
	code(t, err, e.CodeTimeout)
	if err := report.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = report.Index(t.Context())
	code(t, err, e.CodeVerifyFailed)
	_, err = Ingest(t.Context(), []Input{input}, testobservation.RegistryOptions{Transport: delivery.RegistryOptions{Anonymous: true}})
	code(t, err, e.CodeTrustPolicyMissing)
	var forged Report
	_, err = forged.Index(t.Context())
	code(t, err, e.CodeVerifyFailed)
}
func TestIngestClosesPriorReceiptsWhenAuthenticationFails(t *testing.T) {
	first, a := fixture(t, "a", "pass", "workflow/main", FirstParty)
	second, _ := fixture(t, "b", "pass", "workflow/main", FirstParty)
	calls := 0
	got, err := ingestWith(t.Context(), []Input{first, second}, testobservation.RegistryOptions{Transport: delivery.RegistryOptions{Anonymous: true}}, func(context.Context, Input, testobservation.RegistryOptions) (receipt, error) {
		calls++
		if calls == 1 {
			return a, nil
		}
		return nil, e.New(e.CodeSignatureInvalid, "rejected")
	})
	if got != nil || err == nil || !a.closed.Load() {
		t.Fatal("failed ingestion leaked prior authenticated receipt")
	}
	code(t, err, e.CodeSignatureInvalid)
}
func TestTestGridExactProjectionAndGates(t *testing.T) {
	input, r := fixture(t, "grid-run", "failed", "workflow/main", FirstParty)
	report, err := ingestWith(t.Context(), []Input{input}, testobservation.RegistryOptions{Transport: delivery.RegistryOptions{Anonymous: true}}, func(context.Context, Input, testobservation.RegistryOptions) (receipt, error) { return r, nil })
	if err != nil {
		t.Fatal(err)
	}
	defer report.Close()
	destination := TestGridDestination{Bucket: "explicit-test-bucket", Prefix: "groups/forge-nonprod/cpu-v1.34/conformance/build-1", ObservationDigest: input.Expected.ObservationDigest, Namespace: input.Admission.Namespace, SourcePath: input.Admission.SourcePath}
	projection, err := report.TestGrid(t.Context(), destination)
	if err != nil {
		t.Fatal(err)
	}
	if projection.Published || len(projection.Files) != 4 || projection.Files[3].Name != "finished.json" {
		t.Fatal("projection fabricated publication or reordered commit marker")
	}
	if !bytes.Equal(projection.Files[1].Bytes, r.raw["junit.xml"]) {
		t.Fatal("projection changed original result bytes")
	}
	var result finished
	if err := json.Unmarshal(projection.Files[3].Bytes, &result); err != nil {
		t.Fatal(err)
	}
	if result.Passed || result.Result != "FAILURE" || result.Timestamp != 1800000060 {
		t.Fatalf("bad finish projection %+v", result)
	}
	bad := destination
	bad.Prefix = "../escape"
	_, err = report.TestGrid(t.Context(), bad)
	code(t, err, e.CodeUsage)
	bad = destination
	bad.ObservationDigest = pin("wrong")
	_, err = report.TestGrid(t.Context(), bad)
	code(t, err, e.CodeVerifyFailed)
	r.observed.Environment.Facts = slices.DeleteFunc(r.observed.Environment.Facts, func(f testobservation.Fact) bool { return f.Name == "testStartedUnix" })
	r.description.ObservationDigest, _ = r.observed.Digest()
	report.receipts[0].input.Expected.ObservationDigest = r.description.ObservationDigest
	report.receipts[0].input.Locator.ObservationDigest = r.description.ObservationDigest
	destination.ObservationDigest = r.description.ObservationDigest
	_, err = report.TestGrid(t.Context(), destination)
	code(t, err, e.CodeGateUnmet)
}
func TestGeneratedIndexImmutableAndNoCachedAuthority(t *testing.T) {
	input, r := fixture(t, "index", "pass", "workflow/main", FirstParty)
	report, err := ingestWith(t.Context(), []Input{input}, testobservation.RegistryOptions{Transport: delivery.RegistryOptions{Anonymous: true}}, func(context.Context, Input, testobservation.RegistryOptions) (receipt, error) { return r, nil })
	if err != nil {
		t.Fatal(err)
	}
	defer report.Close()
	out := filepath.Join(t.TempDir(), "index.json")
	if err := report.ExportIndex(t.Context(), out); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	expected, err := report.JSON(t.Context())
	if err != nil || !bytes.Equal(raw, expected) {
		t.Fatal("generated index differs from verified projection")
	}
	code(t, report.ExportIndex(t.Context(), out), e.CodeImmutableViolation)
	r.closed.Store(true)
	code(t, report.ExportIndex(t.Context(), filepath.Join(t.TempDir(), "revoked.json")), e.CodeVerifyFailed)
	// A local canonical index remains a detached description after revocation.
	if err := canonical.ValidateJSONDocument(raw); err != nil {
		t.Fatal(err)
	}
	var forged Report
	_, err = forged.JSON(t.Context())
	code(t, err, e.CodeVerifyFailed)
}
func TestMarkdownEscapesUntrustedFormatting(t *testing.T) {
	got := markdown("![x](https://example) | <script>\n")
	if strings.Contains(got, "![x]") || strings.Contains(got, "<script>") || strings.Contains(got, "\n") {
		t.Fatalf("unsafe Markdown projection %q", got)
	}
}

func replaceResults(t *testing.T, input *Input, verified *fakeReceipt, o testobservation.Observation, raw map[string][]byte) {
	t.Helper()
	o.Files = nil
	o.Summary = testresults.Summary{}
	root := t.TempDir()
	inputs := []testobservation.FileInput{}
	for name, data := range raw {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, name)), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
		inputs = append(inputs, testobservation.FileInput{Name: name, ExpectedDigest: pin(string(data)), Role: "junit"})
	}
	out := filepath.Join(t.TempDir(), "bundle")
	if err := testobservation.Emit(t.Context(), root, o, inputs, out); err != nil {
		t.Fatal(err)
	}
	bundle, err := testobservation.Open(t.Context(), out)
	if err != nil {
		t.Fatal(err)
	}
	defer bundle.Close()
	verified.observed = bundle.Observation()
	verified.description = testobservation.Verification{ObservationDigest: bundle.Digest(), SignerAuthenticated: true, SelfConsistent: true, Passing: bundle.Passing()}
	verified.raw = raw
	input.Expected = testobservation.Expected{ObservationDigest: bundle.Digest(), Inputs: verified.observed.Inputs, Subjects: verified.observed.Subjects, Suite: verified.observed.Suite}
	input.Locator.ObservationDigest = bundle.Digest()
}
func destinationFor(input Input) TestGridDestination {
	return TestGridDestination{Bucket: "explicit-test-bucket", Prefix: "groups/forge-nonprod/cpu-v1.34/conformance/build-1", ObservationDigest: input.Expected.ObservationDigest, Namespace: input.Admission.Namespace, SourcePath: input.Admission.SourcePath}
}
func TestTestGridMultipleOriginalJUnitAndImmutableLocalExport(t *testing.T) {
	input, verified := fixture(t, "multiple", "pass", "workflow/main", FirstParty)
	first := bytes.Clone(verified.raw["junit.xml"])
	second := []byte(strings.Replace(string(first), `name="case"`, `name="second"`, 1))
	original := map[string][]byte{"first/results.xml": first, "second/results.xml": second}
	replaceResults(t, &input, verified, verified.observed, original)
	report, err := ingestWith(t.Context(), []Input{input}, testobservation.RegistryOptions{Transport: delivery.RegistryOptions{Anonymous: true}}, func(context.Context, Input, testobservation.RegistryOptions) (receipt, error) { return verified, nil })
	if err != nil {
		t.Fatal(err)
	}
	defer report.Close()
	destination := destinationFor(input)
	projection, err := report.TestGrid(t.Context(), destination)
	if err != nil {
		t.Fatal(err)
	}
	if len(projection.Files) != 5 || projection.Files[4].Name != "finished.json" {
		t.Fatal("multi-file projection omitted original results or reordered finished marker")
	}
	for _, file := range projection.Files {
		if strings.HasPrefix(file.Name, "artifacts/junit.") {
			if !strings.HasSuffix(file.Name, ".xml") {
				t.Fatal("unsupported JUnit basename")
			}
			matched := false
			for _, raw := range original {
				if bytes.Equal(raw, file.Bytes) {
					matched = true
				}
			}
			if !matched {
				t.Fatal("JUnit bytes changed")
			}
		}
	}
	out := filepath.Join(t.TempDir(), "projection")
	if err := report.ExportTestGrid(t.Context(), destination, out); err != nil {
		t.Fatal(err)
	}
	for _, file := range projection.Files {
		raw, err := os.ReadFile(filepath.Join(out, file.Name))
		if err != nil || !bytes.Equal(raw, file.Bytes) {
			t.Fatalf("exported %s differs, %v", file.Name, err)
		}
	}
	code(t, report.ExportTestGrid(t.Context(), destination, out), e.CodeImmutableViolation)
	entries, err := os.ReadDir(filepath.Dir(out))
	if err != nil || len(entries) != 1 {
		t.Fatalf("no-replace failure leaked stage: %v, %v", entries, err)
	}
	if _, err := report.Index(t.Context()); err != nil {
		t.Fatal("export failure corrupted verified observation", err)
	}
}
func TestTestGridNoResultsPreservesFailureWithoutFabricatedCases(t *testing.T) {
	input, verified := fixture(t, "not-run", "pass", "workflow/main", FirstParty)
	o := verified.observed
	o.Execution = testobservation.Outcome{Status: "not-run"}
	o.Cleanup = testobservation.Outcome{Status: "not-run"}
	replaceResults(t, &input, verified, o, map[string][]byte{})
	report, err := ingestWith(t.Context(), []Input{input}, testobservation.RegistryOptions{Transport: delivery.RegistryOptions{Anonymous: true}}, func(context.Context, Input, testobservation.RegistryOptions) (receipt, error) { return verified, nil })
	if err != nil {
		t.Fatal(err)
	}
	defer report.Close()
	projection, err := report.TestGrid(t.Context(), destinationFor(input))
	if err != nil {
		t.Fatal(err)
	}
	if len(projection.Files) != 3 {
		t.Fatal("no-result projection invented JUnit")
	}
	var finish finished
	if err := json.Unmarshal(projection.Files[2].Bytes, &finish); err != nil {
		t.Fatal(err)
	}
	if finish.Passed || finish.Result != "FAILURE" || finish.Metadata["nvkr_execution_status"] != "not-run" || !strings.Contains(finish.Metadata["nvkr_result_reason"], "observed_cases=0") {
		t.Fatalf("explicit no-result failure lost %+v", finish)
	}
	out := filepath.Join(t.TempDir(), "no-result")
	if err := report.ExportTestGrid(t.Context(), destinationFor(input), out); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(out, "artifacts/junit.xml")); !os.IsNotExist(err) {
		t.Fatal("no-result export fabricated test cases")
	}
}
func TestTestGridLocalWriteFailureDoesNotBecomePublicationOrPass(t *testing.T) {
	input, verified := fixture(t, "write-failed", "failed", "workflow/main", FirstParty)
	report, err := ingestWith(t.Context(), []Input{input}, testobservation.RegistryOptions{Transport: delivery.RegistryOptions{Anonymous: true}}, func(context.Context, Input, testobservation.RegistryOptions) (receipt, error) { return verified, nil })
	if err != nil {
		t.Fatal(err)
	}
	defer report.Close()
	parent := filepath.Join(t.TempDir(), "symlink-parent")
	if err := os.Symlink(t.TempDir(), parent); err != nil {
		t.Fatal(err)
	}
	code(t, report.ExportTestGrid(t.Context(), destinationFor(input), filepath.Join(parent, "output")), e.CodeVerifyFailed)
	projection, err := report.TestGrid(t.Context(), destinationFor(input))
	if err != nil || projection.Published {
		t.Fatal("local failure fabricated service success")
	}
	index, err := report.Index(t.Context())
	if err != nil || index.Entries[0].Passing {
		t.Fatal("write failure hid original failed observation")
	}
}

func TestEquivalentObservationDedupeIsDeterministicAcrossInputOrder(t *testing.T) {
	first, a := fixture(t, "alias", "pass", "workflow/main", FirstParty)
	second := cloneInput(first)
	second.Locator.Reference = first.Admission.Namespace + "/" + first.Admission.SourcePath + "@" + pin("alternate transport")
	_, b := fixture(t, "alias", "pass", "workflow/main", FirstParty)
	calls := 0
	report, err := ingestWith(t.Context(), []Input{first, second}, testobservation.RegistryOptions{Transport: delivery.RegistryOptions{Anonymous: true}}, verifier([]Input{first, second}, []*fakeReceipt{a, b}, &calls))
	if err != nil {
		t.Fatal(err)
	}
	defer report.Close()
	raw, err := report.JSON(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	_, c := fixture(t, "alias", "pass", "workflow/main", FirstParty)
	_, d := fixture(t, "alias", "pass", "workflow/main", FirstParty)
	reversed, err := ingestWith(t.Context(), []Input{second, first}, testobservation.RegistryOptions{Transport: delivery.RegistryOptions{Anonymous: true}}, verifier([]Input{first, second}, []*fakeReceipt{c, d}, &calls))
	if err != nil {
		t.Fatal(err)
	}
	defer reversed.Close()
	again, err := reversed.JSON(t.Context())
	if err != nil || !bytes.Equal(raw, again) {
		t.Fatal("input order changed verified dedupe projection")
	}
	if calls != 4 {
		t.Fatal("duplicate coordinates skipped authentication")
	}
}

func TestMarkdownEscapesDisplayControlsWithoutChangingObservation(t *testing.T) {
	value := "prefix\u202e\x1b[31m"
	rendered := markdown(value)
	if strings.ContainsRune(rendered, '\u202e') || strings.ContainsRune(rendered, '\x1b') {
		t.Fatal("display control escaped Markdown boundary")
	}
	if text(value) {
		t.Fatal("display-unsafe admission accepted")
	}
}

func TestConcurrentReportProjectionAndClosure(t *testing.T) {
	input, verified := fixture(t, "concurrent", "pass", "workflow/main", FirstParty)
	report, err := ingestWith(t.Context(), []Input{input}, testobservation.RegistryOptions{Transport: delivery.RegistryOptions{Anonymous: true}}, func(context.Context, Input, testobservation.RegistryOptions) (receipt, error) { return verified, nil })
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	var workers sync.WaitGroup
	for range 8 {
		workers.Go(func() {
			<-start
			for range 8 {
				raw, err := report.JSON(t.Context())
				if err != nil {
					code(t, err, e.CodeVerifyFailed)
				} else if err := canonical.ValidateJSONDocument(raw); err != nil {
					t.Error(err)
				}
			}
		})
	}
	workers.Go(func() {
		<-start
		if err := report.Close(); err != nil {
			t.Error(err)
		}
	})
	close(start)
	workers.Wait()
	if !verified.closed.Load() {
		t.Fatal("report closure did not join receipt ownership")
	}
}

func TestIngestBoundsCumulativeAuthenticatedInventoryBytes(t *testing.T) {
	inputs := []Input{}
	verified := []*fakeReceipt{}
	for _, invocation := range []string{"budget-1", "budget-2", "budget-3"} {
		input, r := fixture(t, invocation, "pass", "workflow/main", FirstParty)
		// The private seam models an already authenticated bounded inventory without
		// allocating its held payloads; production issuance belongs to VerifyOCI.
		r.observed.Files[0].Size = testobservation.MaxFileBytes
		r.observed.Files = append(r.observed.Files, testobservation.File{Name: "fixture-a.bin", Role: "fixture", Digest: pin("a"), Size: testobservation.MaxFileBytes}, testobservation.File{Name: "fixture-b.bin", Role: "fixture", Digest: pin("b"), Size: testobservation.MaxFileBytes})
		slices.SortFunc(r.observed.Files, func(a, b testobservation.File) int { return strings.Compare(a.Name, b.Name) })
		digest, err := r.observed.Digest()
		if err != nil {
			t.Fatal(err)
		}
		r.description.ObservationDigest = digest
		input.Expected.ObservationDigest = digest
		input.Locator.ObservationDigest = digest
		inputs = append(inputs, input)
		verified = append(verified, r)
	}
	calls := 0
	got, err := ingestWith(t.Context(), inputs, testobservation.RegistryOptions{Transport: delivery.RegistryOptions{Anonymous: true}}, verifier(inputs, verified, &calls))
	if got != nil {
		t.Fatal("cumulative inventory overflow returned report")
	}
	code(t, err, e.CodeVerifyFailed)
	if calls != 2 || !verified[0].closed.Load() || !verified[1].closed.Load() || verified[2].closed.Load() {
		t.Fatalf("overflow lifecycle calls=%d closed=%t/%t/%t", calls, verified[0].closed.Load(), verified[1].closed.Load(), verified[2].closed.Load())
	}
}

func TestReportingAdmitsAllCredentialHostsBeforeVerification(t *testing.T) {
	first, a := fixture(t, "first-host", "pass", "workflow/main", FirstParty)
	second, _ := fixture(t, "second-host", "pass", "workflow/main", FirstParty)
	second.Admission.Namespace = "other.example/observations"
	second.Locator.Reference = second.Admission.Namespace + "/" + second.Admission.SourcePath + "@" + pin("other")
	options := testobservation.RegistryOptions{Transport: delivery.RegistryOptions{Username: "fixture-reader", Password: "fixture-secret"}, Credential: sign.Credential{Registry: "ghcr.io", Username: "fixture-reader", Password: "fixture-secret"}}
	calls := 0
	got, err := ingestWith(t.Context(), []Input{first, second}, options, func(context.Context, Input, testobservation.RegistryOptions) (receipt, error) { calls++; return a, nil })
	if got != nil || calls != 0 || a.closed.Load() {
		t.Fatal("credential-host contradiction reached verifier")
	}
	code(t, err, e.CodeUsage)
}
