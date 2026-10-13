// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package testobservation

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/testresults"
)

func observationFixture() Observation {
	pin := rawPin("pinned")
	return Observation{SchemaVersion: SchemaVersion, Inputs: Inputs{DefinitionDigest: pin, EnvironmentDigest: pin, PlanDigest: pin}, Origin: Origin{Status: "unknown"}, Subjects: []Subject{{Output: "controller", ArtifactDigest: pin, PayloadDigest: pin, BuildDigest: pin, Platform: "linux/amd64"}}, Suite: Suite{Name: "conformance", HarnessRevision: strings.Repeat("a", 40), ImageDigest: pin, Selection: []string{"Conformance"}, PolicyDigest: pin, Invocation: "test-invocation", RequiredCases: []CaseID{{Suite: "suite", Class: "class", Name: "case"}}}, Environment: Environment{Role: "tenant", Facts: []Fact{{Name: "kubernetesVersion", Value: "v1.34.0", Evidence: "cluster-api", Phase: "before"}}}, Execution: Outcome{Status: "completed"}, Cleanup: Outcome{Status: "completed"}}
}
func emitFixture(t *testing.T, o Observation, result string) string {
	t.Helper()
	source := t.TempDir()
	input := resultFile(t, source, "results/junit.xml", result)
	input.Role = "junit"
	out := filepath.Join(t.TempDir(), "bundle")
	if err := Emit(t.Context(), source, o, []FileInput{input}, out); err != nil {
		t.Fatal(err)
	}
	return out
}

const passingJUnit = `<testsuite name="suite" tests="1" failures="0" errors="0" skipped="0"><testcase classname="class" name="case"/></testsuite>`

func TestBundleIndependentRoundTrip(t *testing.T) {
	o := observationFixture()
	root := emitFixture(t, o, passingJUnit)
	b, err := Open(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	observed := b.Observation()
	if !b.Passing() || observed.Summary.Total != 1 || observed.Summary.Passed != 1 {
		t.Fatalf("unexpected observation %+v", observed)
	}
	expected := Expected{ObservationDigest: b.Digest(), Inputs: o.Inputs, Subjects: o.Subjects, Suite: o.Suite}
	v, err := VerifyLocal(t.Context(), root, expected)
	if err != nil || !v.SelfConsistent || !v.Passing || v.SignerAuthenticated {
		t.Fatalf("verification = %+v, %v", v, err)
	}
	observed.Subjects[0].Output = "changed"
	if b.Observation().Subjects[0].Output != "controller" {
		t.Fatal("mutable description escaped")
	}
	if err := os.WriteFile(filepath.Join(root, "files/results/junit.xml"), []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	raw, err := b.Read(t.Context(), "results/junit.xml")
	if err != nil || string(raw) != passingJUnit {
		t.Fatalf("held bundle changed: %q, %v", raw, err)
	}
	_, err = Open(t.Context(), root)
	requireCode(t, err, e.CodeVerifyFailed)
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = b.Canonical(t.Context())
	requireCode(t, err, e.CodeVerifyFailed)
}
func TestBundleFailuresRemainValidWithoutPassing(t *testing.T) {
	for _, tc := range []struct {
		name, result string
		change       func(*Observation)
	}{
		{"failed", `<testsuite name="suite" tests="1" failures="1" errors="0" skipped="0"><testcase classname="class" name="case"><failure/></testcase></testsuite>`, nil},
		{"skipped required", `<testsuite name="suite" tests="1" failures="0" errors="0" skipped="1"><testcase classname="class" name="case"><skipped/></testcase></testsuite>`, nil},

		{"no required cases", passingJUnit, func(o *Observation) { o.Suite.RequiredCases = nil }},
		{"missing required case", passingJUnit, func(o *Observation) { o.Suite.RequiredCases[0].Name = "absent" }},
		{"cleanup missing", passingJUnit, func(o *Observation) { o.Cleanup.Status = "unknown" }},
		{"cleanup failed", passingJUnit, func(o *Observation) { o.Cleanup = Outcome{Status: "failed", ExitCode: 1} }},
		{"execution failed", passingJUnit, func(o *Observation) { o.Execution = Outcome{Status: "failed", ExitCode: 1} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := observationFixture()
			if tc.change != nil {
				tc.change(&o)
			}
			root := emitFixture(t, o, tc.result)
			b, err := Open(t.Context(), root)
			if err != nil {
				t.Fatal(err)
			}
			defer b.Close()
			if b.Passing() {
				t.Fatal("nonpassing observation became passing")
			}
		})
	}
}
func TestBundleRejectsTamperedControlAndMembers(t *testing.T) {
	for _, kind := range []string{"unknown schema", "summary", "consistent forged summary", "unknown field", "whitespace", "extra file", "extra directory", "missing file", "payload symlink", "control symlink", "wrong size"} {
		t.Run(kind, func(t *testing.T) {
			root := emitFixture(t, observationFixture(), passingJUnit)
			path := filepath.Join(root, ObservationFileName)
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "unknown schema":
				raw = []byte(strings.Replace(string(raw), SchemaVersion, "nvkr.dev/test-observation/v2", 1))
			case "summary":
				raw = []byte(strings.Replace(string(raw), `"passed":1`, `"passed":2`, 1))
			case "consistent forged summary":
				raw = []byte(strings.Replace(strings.Replace(string(raw), `"passed":1`, `"passed":0`, 1), `"failed":0`, `"failed":1`, 1))
			case "unknown field":
				raw = append([]byte(`{"unknown":true,`), raw[1:]...)
			case "whitespace":
				raw = append(raw, '\n')
			case "wrong size":
				var o Observation
				if err := canonical.UnmarshalExact(raw, &o); err != nil {
					t.Fatal(err)
				}
				o.Files[0].Size++
				raw, err = canonical.Marshal(o)
				if err != nil {
					t.Fatal(err)
				}
			case "extra file":
				if err := os.WriteFile(filepath.Join(root, "private.log"), []byte("secret"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "extra directory":
				if err := os.Mkdir(filepath.Join(root, "extra"), 0o700); err != nil {
					t.Fatal(err)
				}
			case "missing file":
				if err := os.Rename(filepath.Join(root, "files/results/junit.xml"), filepath.Join(t.TempDir(), "moved.xml")); err != nil {
					t.Fatal(err)
				}
			case "payload symlink":
				payload := filepath.Join(root, "files/results/junit.xml")
				target := filepath.Join(t.TempDir(), "moved.xml")
				if err := os.Rename(payload, target); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, payload); err != nil {
					t.Fatal(err)
				}
			case "control symlink":
				target := filepath.Join(t.TempDir(), "control.json")
				if err := os.Rename(path, target); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			}
			if kind != "control symlink" {
				if err := os.WriteFile(path, raw, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			b, err := Open(t.Context(), root)
			if b != nil {
				_ = b.Close()
				t.Fatal("tampered bundle accepted")
			}
			requireCode(t, err, e.CodeVerifyFailed)
		})
	}
}
func TestBundleRejectsIndependentBindingSubstitution(t *testing.T) {
	root := emitFixture(t, observationFixture(), passingJUnit)
	b, err := Open(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	o := b.Observation()
	for _, kind := range []string{"digest", "input", "output", "payload", "build", "platform", "suite", "policy", "invocation", "artifact", "image", "revision", "selection", "required cases", "subjects"} {
		t.Run(kind, func(t *testing.T) {
			expected := Expected{ObservationDigest: b.Digest(), Inputs: o.Inputs, Subjects: append([]Subject(nil), o.Subjects...), Suite: clone(o).Suite}
			pin := rawPin("substitute")
			switch kind {
			case "digest":
				expected.ObservationDigest = pin
			case "input":
				expected.Inputs.PlanDigest = pin
			case "output":
				expected.Subjects[0].Output = "sibling"
			case "payload":
				expected.Subjects[0].PayloadDigest = pin
			case "build":
				expected.Subjects[0].BuildDigest = pin
			case "platform":
				expected.Subjects[0].Platform = "linux/arm64"
			case "suite":
				expected.Suite.Name = "other"
			case "policy":
				expected.Suite.PolicyDigest = pin
			case "invocation":
				expected.Suite.Invocation = "other"
			case "artifact":
				expected.Subjects[0].ArtifactDigest = pin
			case "image":
				expected.Suite.ImageDigest = pin
			case "revision":
				expected.Suite.HarnessRevision = strings.Repeat("b", 40)
			case "selection":
				expected.Suite.Selection = []string{"other"}
			case "required cases":
				expected.Suite.RequiredCases[0].Name = "other"
			case "subjects":
				expected.Subjects = append(expected.Subjects, Subject{Output: "extra", Platform: "linux/amd64", ArtifactDigest: pin, PayloadDigest: pin, BuildDigest: pin})
			}
			_, err := VerifyLocal(t.Context(), root, expected)
			requireCode(t, err, e.CodeVerifyFailed)
		})
	}
}
func TestBundleEmissionNoReplaceDeterminismAndAdmission(t *testing.T) {
	source := t.TempDir()
	input := resultFile(t, source, "junit.xml", passingJUnit)
	input.Role = "junit"
	o := observationFixture()
	base := t.TempDir()
	first := filepath.Join(base, "first")
	second := filepath.Join(base, "second")
	if err := Emit(t.Context(), source, o, []FileInput{input}, first); err != nil {
		t.Fatal(err)
	}
	if err := Emit(t.Context(), source, o, []FileInput{input}, second); err != nil {
		t.Fatal(err)
	}
	a, err := os.ReadFile(filepath.Join(first, ObservationFileName))
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(second, ObservationFileName))
	if err != nil || string(a) != string(b) {
		t.Fatal("emission not deterministic")
	}
	requireCode(t, Emit(t.Context(), source, o, []FileInput{input}, first), e.CodeVerifyFailed)
	entries, err := os.ReadDir(base)
	if err != nil || len(entries) != 2 {
		t.Fatalf("failed no-replace left stage: %+v, %v", entries, err)
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	requireCode(t, Emit(canceled, source, o, []FileInput{input}, filepath.Join(base, "canceled")), e.CodeTimeout)
	for _, kind := range []string{"invented origin", "unreviewed role", "supplied summary", "supplied inventory", "duplicate subjects"} {
		t.Run(kind, func(t *testing.T) {
			bad := clone(o)
			in := input
			switch kind {
			case "invented origin":
				bad.Origin.Run = "fake"
			case "unreviewed role":
				in.Role = "private-log"
			case "supplied summary":
				bad.Summary.Total = 1
			case "supplied inventory":
				bad.Files = []File{{Name: "fake"}}
			case "duplicate subjects":
				bad.Subjects = append(bad.Subjects, bad.Subjects[0])
			}
			out := filepath.Join(base, "rejected")
			requireCode(t, Emit(t.Context(), source, bad, []FileInput{in}, out), e.CodeVerifyFailed)
			if _, err := os.Lstat(out); !os.IsNotExist(err) {
				t.Fatal("admission failure touched output")
			}
		})
	}
}

func TestBundleRejectsEmptyJUnitBeforeOutput(t *testing.T) {
	root := t.TempDir()
	input := resultFile(t, root, "empty.xml", `<testsuite name="suite" tests="0" failures="0" errors="0" skipped="0"/>`)
	input.Role = "junit"
	out := filepath.Join(t.TempDir(), "bundle")
	requireCode(t, Emit(t.Context(), root, observationFixture(), []FileInput{input}, out), e.CodeVerifyFailed)
	if _, err := os.Lstat(out); !os.IsNotExist(err) {
		t.Fatal("empty JUnit created output")
	}
}

func TestObservationCanonicalGolden(t *testing.T) {
	root := emitFixture(t, observationFixture(), passingJUnit)
	b, err := Open(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	actual, err := b.Canonical(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	golden, err := os.ReadFile("testdata/observation-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	if string(actual) != string(golden) {
		t.Fatalf("canonical observation contract drift:\ngot %s\nwant %s", actual, golden)
	}
	if b.Digest() != "sha256:7d796697fe496105d0711510c3e6884bac816c9be20491f90f66b146ae2529db" {
		t.Fatalf("semantic digest drift: %s", b.Digest())
	}
	o, err := Decode(golden)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := o.Canonical()
	if err != nil || string(raw) != string(golden) {
		t.Fatal("canonical decoding changed bytes")
	}
}

func TestExpectedValidationDoesNotMutateConsumer(t *testing.T) {
	o := observationFixture()
	o.Subjects = append(o.Subjects, Subject{Output: "a", ArtifactDigest: rawPin("a"), PayloadDigest: rawPin("a"), BuildDigest: rawPin("a"), Platform: "linux/arm64"})
	o.Suite.Selection = []string{"z", "a"}
	o.Suite.RequiredCases = append(o.Suite.RequiredCases, CaseID{Name: "a"})
	expected := Expected{ObservationDigest: rawPin("observation"), Inputs: o.Inputs, Subjects: o.Subjects, Suite: o.Suite}
	if err := expected.Validate(); err != nil {
		t.Fatal(err)
	}
	if expected.Subjects[0].Output != "controller" || expected.Suite.Selection[0] != "z" || expected.Suite.RequiredCases[0].Name != "case" {
		t.Fatal("validation mutated consumer expectations")
	}
}

func TestConcurrentEmissionCommitsOneCompleteBundle(t *testing.T) {
	source := t.TempDir()
	input := resultFile(t, source, "results/junit.xml", passingJUnit)
	input.Role = "junit"
	out := filepath.Join(t.TempDir(), "bundle")
	var workers sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		workers.Go(func() { results <- Emit(t.Context(), source, observationFixture(), []FileInput{input}, out) })
	}
	workers.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		} else {
			requireCode(t, err, e.CodeVerifyFailed)
		}
	}
	if successes != 1 {
		t.Fatalf("committed writers=%d; want exactly one", successes)
	}
	b, err := Open(t.Context(), out)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if !b.Passing() {
		t.Fatal("winning bundle is incomplete")
	}
}

func TestObservationAggregateCaseBudget(t *testing.T) {
	o := observationFixture()
	o.Files = []File{{Name: "a.xml", Role: "junit"}, {Name: "b.xml", Role: "junit"}}
	perFile := testresults.MaxCases/2 + 1
	results := map[string][]byte{}
	for _, name := range []string{"a.xml", "b.xml"} {
		var raw strings.Builder
		fmt.Fprintf(&raw, `<testsuite name="suite" tests="%d" failures="0" errors="0" skipped="0">`, perFile)
		for i := range perFile {
			fmt.Fprintf(&raw, `<testcase classname="class" name="%s-%d"/>`, name, i)
		}
		raw.WriteString(`</testsuite>`)
		results[name] = []byte(raw.String())
	}
	_, passing, err := assess(t.Context(), o, func(name string) ([]byte, error) { return results[name], nil })
	requireCode(t, err, e.CodeVerifyFailed)
	if passing {
		t.Fatal("aggregate overflow returned passing")
	}
}

func TestEmitRejectsSymlinkOutputParent(t *testing.T) {
	source := t.TempDir()
	input := resultFile(t, source, "results/junit.xml", passingJUnit)
	input.Role = "junit"
	base := t.TempDir()
	link := filepath.Join(base, "parent")
	if err := os.Symlink(t.TempDir(), link); err != nil {
		t.Fatal(err)
	}
	requireCode(t, Emit(t.Context(), source, observationFixture(), []FileInput{input}, filepath.Join(link, "bundle")), e.CodeVerifyFailed)
}

func TestEmissionDetectsReplacedRequestedParent(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run(fmt.Sprint(committed), func(t *testing.T) {
			base := t.TempDir()
			requested := filepath.Join(base, "output")
			if err := os.Mkdir(requested, 0o700); err != nil {
				t.Fatal(err)
			}
			held, info, err := securepath.OpenDirectory(requested)
			if err != nil {
				t.Fatal(err)
			}
			defer held.Close()
			if err := os.Rename(requested, filepath.Join(base, "moved")); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(requested, 0o700); err != nil {
				t.Fatal(err)
			}
			err = recheckOutputParent(t.Context(), requested, info, committed)
			requireCode(t, err, e.CodeVerifyFailed)
			if committed && !strings.Contains(err.Error(), "observation committed") {
				t.Fatal("partial commit diagnosis omitted")
			}
		})
	}
}

func TestNoResultObservationsRemainNonpassing(t *testing.T) {
	for _, status := range []string{"not-run", "infrastructure-error", "canceled", "unknown"} {
		t.Run(status, func(t *testing.T) {
			o := observationFixture()
			o.Execution = Outcome{Status: status}
			o.Cleanup = Outcome{Status: "not-run"}
			out := filepath.Join(t.TempDir(), "bundle")
			if err := Emit(t.Context(), "/nonexistent-unused-source", o, nil, out); err != nil {
				t.Fatal(err)
			}
			b, err := Open(t.Context(), out)
			if err != nil {
				t.Fatal(err)
			}
			defer b.Close()
			if b.Passing() || len(b.Observation().Files) != 0 || b.Observation().Summary.Total != 0 {
				t.Fatal("no-result observation became passing")
			}
			v, err := VerifyLocal(t.Context(), out, Expected{ObservationDigest: b.Digest(), Inputs: o.Inputs, Subjects: o.Subjects, Suite: o.Suite})
			if err != nil || v.Passing || !v.SelfConsistent {
				t.Fatalf("no-result check %+v, %v", v, err)
			}
		})
	}
	for _, status := range []string{"completed", "failed"} {
		t.Run(status+" no result rejected", func(t *testing.T) {
			o := observationFixture()
			o.Execution = Outcome{Status: status}
			out := filepath.Join(t.TempDir(), "bundle")
			requireCode(t, Emit(t.Context(), "/nonexistent-unused-source", o, nil, out), e.CodeVerifyFailed)
			if _, err := os.Lstat(out); !os.IsNotExist(err) {
				t.Fatal("executed no-result touched output")
			}
		})
	}
}

func TestValidEmptyJUnitPreservedForInfrastructureFailure(t *testing.T) {
	raw := `<testsuite name="suite" tests="0" failures="0" errors="0" skipped="0"/>`
	o := observationFixture()
	o.Execution = Outcome{Status: "infrastructure-error", ExitCode: 1}
	out := emitFixture(t, o, raw)
	b, err := Open(t.Context(), out)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	result, err := b.Read(t.Context(), "results/junit.xml")
	if err != nil || string(result) != raw || b.Passing() {
		t.Fatalf("empty raw preservation %q, %v", result, err)
	}
}

func TestCommittedFailureRetainsPartialCommitDiagnosisOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err := committedError(ctx, ctx.Err(), "observation committed but exact recheck failed")
	requireCode(t, err, e.CodeTimeout)
	if !strings.Contains(err.Error(), "observation committed") || !strings.Contains(err.Error(), "no rollback performed") {
		t.Fatal("cancellation hid partial commit diagnosis")
	}
}

func TestLocalRoleRemainsUnsignedObservation(t *testing.T) {
	o := observationFixture()
	o.Environment.Role = "local"
	root := emitFixture(t, o, passingJUnit)
	bundle, err := Open(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer bundle.Close()
	result, err := VerifyLocal(t.Context(), root, Expected{ObservationDigest: bundle.Digest(), Inputs: o.Inputs, Subjects: o.Subjects, Suite: o.Suite})
	if err != nil || result.SignerAuthenticated {
		t.Fatal("local description gained authenticated authority", err)
	}
}
