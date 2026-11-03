// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package resultspublish_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/qualificationoutcome"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/resultspublish"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/resultspublish/storetest"
)

const (
	group      = "nvke"
	definition = "nvke-m2"
)

var subject = "sha256:" + strings.Repeat("e", 64)

// outcomeOf is a valid outcome of producer Run 101 attempt 1 from qualifier Run
// 202 attempt 2, as run qualify --out or Plan would write it.
func outcomeOf(status qualificationoutcome.Status, stage qualificationoutcome.Stage) qualificationoutcome.Outcome {
	outcome := qualificationoutcome.Outcome{SchemaVersion: qualificationoutcome.SchemaVersion, Status: status, Stage: stage,
		Lane: qualificationoutcome.LanePipelineAttested, SourceIdentity: qualificationoutcome.SourceEvidence,
		Source: qualificationoutcome.Source{Run: qualificationoutcome.Run{Repository: "example/nvkr", Workflow: ".github/workflows/nvkr-run.yml",
			RunID: "101", Attempt: "1", Commit: strings.Repeat("a", 40)}},
		Qualifier: qualificationoutcome.Run{Repository: "example/nvkr", Workflow: ".github/workflows/nvkr-qualify.yml", RunID: "202", Attempt: "2",
			Commit: strings.Repeat("b", 40)},
		StartedAt: "2026-11-02T10:00:00.5Z", FinishedAt: "2026-11-02T10:30:00Z",
		Checks: []qualificationoutcome.Check{{Output: "tool", Subject: subject, Check: "build-signature-and-provenance", Status: "pass"},
			{Output: "tool", Subject: subject, Check: "delivered-tool-version", Status: "pass"},
			{Output: "tool", Subject: subject, Check: "sbom-attestation", Status: "not-applicable", Message: "not required in this lane"}},
		Result: json.RawMessage(`{"successful":true}`)}
	issued := func() {
		outcome.EvidenceDigest, outcome.ManifestDigest = "sha256:"+strings.Repeat("c", 64), "sha256:"+strings.Repeat("d", 64)
		outcome.ManifestSubject = "ghcr.io/example/nvkr/manifest@sha256:" + strings.Repeat("d", 64)
	}
	switch {
	case status == qualificationoutcome.StatusPassed:
		issued()
	case status == qualificationoutcome.StatusFailed && stage == qualificationoutcome.StageManifest:
		issued()
		outcome.Error = &qualificationoutcome.Failure{Code: string(e.CodeImmutableViolation), Message: "release manifest read-back differs"}
		outcome.Result = json.RawMessage(`{"successful":false}`)
	case status == qualificationoutcome.StatusFailed:
		outcome.Checks[0].Status, outcome.Checks[0].Code, outcome.Checks[0].Message = "fail", string(e.CodeSignatureInvalid), "signature does not verify"
		outcome.Error = &qualificationoutcome.Failure{Code: string(e.CodeGateUnmet), Message: "qualification refused"}
		outcome.Result = json.RawMessage(`{"successful":false}`)
	case status == qualificationoutcome.StatusErrored:
		outcome.SourceIdentity, outcome.Checks, outcome.Result = qualificationoutcome.SourceUnauthenticatedEvent, nil, nil
		outcome.Source.Event, outcome.Source.Conclusion = "push", "success"
		if stage == qualificationoutcome.StageProducer {
			outcome.Source.Conclusion = "failure"
		} else {
			outcome.Error = &qualificationoutcome.Failure{Code: string(e.CodeTrustPolicyMissing), Message: "qualification policy refused"}
		}
	}
	return outcome
}

func passed() qualificationoutcome.Outcome { return outcomeOf(qualificationoutcome.StatusPassed, "") }

func withAttempt(outcome qualificationoutcome.Outcome, attempt, finishedAt string) qualificationoutcome.Outcome {
	outcome.Qualifier.Attempt, outcome.FinishedAt = attempt, finishedAt
	return outcome
}

// memStore is an in-memory ObjectStore that logs every call and fails on demand.
type memStore struct {
	objects      map[string][]byte
	calls        []string
	creates      int
	failCreateAt int
	failRead     bool
}

func newMemStore() *memStore { return &memStore{objects: map[string][]byte{}} }

func (s *memStore) Create(_ context.Context, path string, data []byte) error {
	s.calls = append(s.calls, "create "+path)
	s.creates++
	if s.creates == s.failCreateAt {
		return e.New(e.CodeUnavailable, "injected write failure")
	}
	if _, ok := s.objects[path]; ok {
		return resultspublish.ErrExists
	}
	s.objects[path] = bytes.Clone(data)
	return nil
}

func (s *memStore) Read(_ context.Context, path string, maxBytes int64) ([]byte, error) {
	s.calls = append(s.calls, "read "+path)
	if s.failRead {
		return nil, e.New(e.CodeUnavailable, "injected read failure")
	}
	data, ok := s.objects[path]
	switch {
	case !ok:
		return nil, e.New(e.CodeUnavailable, "no object")
	case int64(len(data)) > maxBytes:
		return nil, e.New(e.CodeImmutableViolation, "object exceeds its bound")
	}
	return bytes.Clone(data), nil
}

func (s *memStore) createdPaths() []string {
	var paths []string
	for _, call := range s.calls {
		if path, ok := strings.CutPrefix(call, "create "); ok {
			paths = append(paths, path)
		}
	}
	return paths
}

func requireCode(t *testing.T, err error, code e.Code) {
	t.Helper()
	var coded *e.Error
	if !errors.As(err, &coded) || coded.Code != code {
		t.Fatalf("got %v, want %s", err, code)
	}
}

// buildOf is the build directory a fixture outcome's objects land in.
func buildOf(outcome qualificationoutcome.Outcome) string {
	_, finished, err := outcome.Times()
	if err != nil {
		panic(err)
	}
	return fmt.Sprintf("groups/nvkr/%s/%s/%019d-%s-%s-%s-%s/", group, definition, finished.UnixNano(),
		outcome.Source.RunID, outcome.Source.Attempt, outcome.Qualifier.RunID, outcome.Qualifier.Attempt)
}

func objectsOf(outcome qualificationoutcome.Outcome) []string {
	build := buildOf(outcome)
	return []string{
		fmt.Sprintf("results/%s/%s/%s-%s/%s-%s/outcome.json", group, definition, outcome.Source.RunID, outcome.Source.Attempt, outcome.Qualifier.RunID, outcome.Qualifier.Attempt),
		build + "started.json", build + "artifacts/junit.xml", build + "finished.json",
	}
}

func TestResultsPublishLayout(t *testing.T) {
	t.Run("an outcome's record, then its build, finished last", func(t *testing.T) {
		store := newMemStore()
		if err := resultspublish.Publish(t.Context(), store, []qualificationoutcome.Outcome{passed()}, group, definition); err != nil {
			t.Fatal(err)
		}
		want := []string{
			"results/nvke/nvke-m2/101-1/202-2/outcome.json",
			"groups/nvkr/nvke/nvke-m2/1793615400000000000-101-1-202-2/started.json",
			"groups/nvkr/nvke/nvke-m2/1793615400000000000-101-1-202-2/artifacts/junit.xml",
			"groups/nvkr/nvke/nvke-m2/1793615400000000000-101-1-202-2/finished.json",
		}
		if !slices.Equal(store.calls, prefixed("create ", want)) {
			t.Fatalf("calls %q, want %q", store.calls, want)
		}
	})
	t.Run("builds sort in finish order down to the nanosecond", func(t *testing.T) {
		first := withAttempt(passed(), "1", "2026-11-02T10:30:00.999999999Z")
		second := withAttempt(passed(), "2", "2026-11-02T10:30:01Z")
		store := newMemStore()
		if err := resultspublish.Publish(t.Context(), store, []qualificationoutcome.Outcome{second, first}, group, definition); err != nil {
			t.Fatal(err)
		}
		var builds []string
		for _, created := range store.createdPaths() {
			if build := path.Base(path.Dir(created)); strings.HasPrefix(created, "groups/") && strings.HasSuffix(created, "/started.json") {
				builds = append(builds, build)
			}
		}
		if want := []string{"1793615400999999999-101-1-202-1", "1793615401000000000-101-1-202-2"}; !slices.Equal(builds, want) || !(builds[0] < builds[1]) {
			t.Fatalf("builds %q, want %q in that order", builds, want)
		}
	})
	t.Run("a Definition name with dots and underscores", func(t *testing.T) {
		const dotted = "nvsentinel-ubuntu-24.04-amd64_candidate"
		if err := resultspublish.Publish(t.Context(), newMemStore(), []qualificationoutcome.Outcome{passed()}, group, dotted); err != nil {
			t.Fatal(err)
		}
	})
	for _, tc := range []struct {
		name, group, definition string
		outcome                 func() qualificationoutcome.Outcome
		code                    e.Code
	}{
		{"an uppercase group", "NVKE", definition, passed, e.CodeUsage},
		{"an empty group", "", definition, passed, e.CodeUsage},
		{"a nested group", "nvke/x", definition, passed, e.CodeUsage},
		{"a parent segment", "..", definition, passed, e.CodeUsage},
		{"a Definition with a space", group, "nvke m2", passed, e.CodeUsage},
		{"a Definition ending in a hyphen", group, "nvke-", passed, e.CodeUsage},
		{"a Definition ending in a dot", group, "nvke.", passed, e.CodeUsage},
		{"a Definition of 129 characters", group, strings.Repeat("d", 129), passed, e.CodeUsage},
		{"a finish past what a build name can carry", group, definition, func() qualificationoutcome.Outcome {
			return withAttempt(passed(), "2", "2263-01-01T00:00:00Z")
		}, e.CodeVerifyFailed},
		{"no qualifier Run ID", group, definition, func() qualificationoutcome.Outcome {
			outcome := passed()
			outcome.Qualifier.RunID = ""
			return outcome
		}, e.CodeVerifyFailed},
		{"no finish", group, definition, func() qualificationoutcome.Outcome { return withAttempt(passed(), "2", "") }, e.CodeVerifyFailed},
		{"a finish at the epoch", group, definition, func() qualificationoutcome.Outcome {
			outcome := withAttempt(passed(), "2", "1970-01-01T00:00:00Z")
			outcome.StartedAt = "1969-12-31T23:00:00Z"
			return outcome
		}, e.CodeVerifyFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newMemStore()
			err := resultspublish.Publish(t.Context(), store, []qualificationoutcome.Outcome{tc.outcome()}, tc.group, tc.definition)
			requireCode(t, err, tc.code)
			if len(store.calls) != 0 {
				t.Fatalf("wrote %q before refusing", store.calls)
			}
		})
	}
}

func prefixed(prefix string, values []string) []string {
	out := make([]string, len(values))
	for i, value := range values {
		out[i] = prefix + value
	}
	return out
}

func TestResultsPublishMetadata(t *testing.T) {
	qualifierURL := "https://github.com/example/nvkr/actions/runs/202/attempts/2"
	for _, tc := range []struct {
		name     string
		outcome  qualificationoutcome.Outcome
		runURL   string
		digests  bool
		manifest bool
	}{
		{"passed", passed(), qualifierURL, true, true},
		{"failed in qualification", outcomeOf(qualificationoutcome.StatusFailed, qualificationoutcome.StageQualify), qualifierURL, false, false},
		{"failed at the manifest", outcomeOf(qualificationoutcome.StatusFailed, qualificationoutcome.StageManifest), qualifierURL, true, true},
		{"errored before qualification", outcomeOf(qualificationoutcome.StatusErrored, qualificationoutcome.StageQualify), qualifierURL, false, false},
		{"a producer that did not succeed", outcomeOf(qualificationoutcome.StatusErrored, qualificationoutcome.StageProducer),
			"https://github.com/example/nvkr/actions/runs/101/attempts/1", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newMemStore()
			if err := resultspublish.Publish(t.Context(), store, []qualificationoutcome.Outcome{tc.outcome}, group, definition); err != nil {
				t.Fatal(err)
			}
			build := buildOf(tc.outcome)
			var started struct {
				Timestamp int64          `json:"timestamp"`
				Metadata  map[string]any `json:"metadata"`
			}
			var finished struct {
				Timestamp int64          `json:"timestamp"`
				Passed    bool           `json:"passed"`
				Result    string         `json:"result"`
				Metadata  map[string]any `json:"metadata"`
			}
			if err := json.Unmarshal(store.objects[build+"started.json"], &started); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(store.objects[build+"finished.json"], &finished); err != nil {
				t.Fatal(err)
			}
			startedAt, finishedAt, err := tc.outcome.Times()
			if err != nil {
				t.Fatal(err)
			}
			want := map[string]any{"sha": strings.Repeat("a", 40), "source_run": "101-1", "qualifier_run": "202-2", "lane": "pipeline-attested",
				"run_url": tc.runURL, "status": string(tc.outcome.Status), "repository": "example/nvkr", "group": group, "definition": definition,
				"outcome": "results/nvke/nvke-m2/101-1/202-2/outcome.json"}
			if tc.digests {
				want["evidence_digest"] = tc.outcome.EvidenceDigest
			}
			if tc.manifest {
				want["manifest_digest"] = tc.outcome.ManifestDigest
			}
			for name, metadata := range map[string]map[string]any{"started": started.Metadata, "finished": finished.Metadata} {
				if fmt.Sprint(metadata) != fmt.Sprint(want) {
					t.Fatalf("%s metadata %v, want %v", name, metadata, want)
				}
				for key, value := range metadata {
					if _, ok := value.(string); !ok {
						t.Fatalf("%s metadata %s is not a string", name, key)
					}
				}
			}
			isPassed := tc.outcome.Status == qualificationoutcome.StatusPassed
			result := map[bool]string{true: "SUCCESS", false: "FAILURE"}[isPassed]
			if started.Timestamp != startedAt.Unix() || finished.Timestamp != finishedAt.Unix() || finished.Passed != isPassed || finished.Result != result {
				t.Fatalf("started %d, finished %d %v %s", started.Timestamp, finished.Timestamp, finished.Passed, finished.Result)
			}
		})
	}
}

func TestResultsPublishJUnitGolden(t *testing.T) {
	failedWithoutCheck := outcomeOf(qualificationoutcome.StatusFailed, qualificationoutcome.StageQualify)
	failedWithoutCheck.Checks[0].Status, failedWithoutCheck.Checks[0].Code, failedWithoutCheck.Checks[0].Message = "pass", "", ""
	cut := outcomeOf(qualificationoutcome.StatusErrored, qualificationoutcome.StageQualify)
	cut.Error.Message = "policy refused at " + strings.Repeat("é", 55)
	twoSubjects := passed()
	twoSubjects.Checks = append([]qualificationoutcome.Check{
		{Output: "image", Subject: "sha256:" + strings.Repeat("1", 64), Check: "build-signature-and-provenance", Status: "pass"},
		{Output: "image", Subject: "sha256:" + strings.Repeat("2", 64), Check: "build-signature-and-provenance", Status: "pass"},
	}, twoSubjects.Checks...)
	for _, tc := range []struct {
		name    string
		outcome qualificationoutcome.Outcome
	}{
		{"passed", passed()},
		{"failed-check", outcomeOf(qualificationoutcome.StatusFailed, qualificationoutcome.StageQualify)},
		{"failed-without-check", failedWithoutCheck},
		{"failed-manifest", outcomeOf(qualificationoutcome.StatusFailed, qualificationoutcome.StageManifest)},
		{"errored-qualify-cut", cut},
		{"errored-producer", outcomeOf(qualificationoutcome.StatusErrored, qualificationoutcome.StageProducer)},
		{"two-subjects", twoSubjects},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newMemStore()
			if err := resultspublish.Publish(t.Context(), store, []qualificationoutcome.Outcome{tc.outcome}, group, definition); err != nil {
				t.Fatal(err)
			}
			actual := store.objects[buildOf(tc.outcome)+"artifacts/junit.xml"]
			for _, match := range regexp.MustCompile(`message="([^"]*)"`).FindAllSubmatch(actual, -1) {
				if len(match[1]) > qualificationoutcome.MaxMessageBytes || !utf8.Valid(match[1]) {
					t.Fatalf("message %q exceeds %d bytes or splits a character", match[1], qualificationoutcome.MaxMessageBytes)
				}
			}
			path := filepath.Join("testdata", "junit-"+tc.name+".xml")
			if os.Getenv("NVKR_UPDATE_GOLDEN") == "1" {
				if err := os.MkdirAll("testdata", 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, actual, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			expected, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(actual, expected) {
				t.Fatalf("junit.xml differs from %s:\n%s", path, actual)
			}
		})
	}
}

func TestResultsPublishProtocol(t *testing.T) {
	first := withAttempt(passed(), "1", "2026-11-02T10:20:00Z")
	second := passed()
	t.Run("a second pass writes nothing new", func(t *testing.T) {
		store := newMemStore()
		for range 2 {
			if err := resultspublish.Publish(t.Context(), store, []qualificationoutcome.Outcome{second}, group, definition); err != nil {
				t.Fatal(err)
			}
		}
		var want []string
		for _, path := range objectsOf(second) {
			want = append(want, "create "+path, "read "+path)
		}
		if len(store.objects) != 4 || !slices.Equal(store.calls[4:], want) {
			t.Fatalf("second pass calls %q", store.calls[4:])
		}
	})
	t.Run("a partial attempt is completed, finished last", func(t *testing.T) {
		store := newMemStore()
		store.failCreateAt = 3
		requireCode(t, resultspublish.Publish(t.Context(), store, []qualificationoutcome.Outcome{first}, group, definition), e.CodeUnavailable)
		store.failCreateAt, store.calls = 0, nil
		if err := resultspublish.Publish(t.Context(), store, []qualificationoutcome.Outcome{second, first}, group, definition); err != nil {
			t.Fatal(err)
		}
		if want := append(objectsOf(first), objectsOf(second)...); !slices.Equal(store.createdPaths(), want) {
			t.Fatalf("created %q, want %q", store.createdPaths(), want)
		}
		if len(store.objects) != 8 {
			t.Fatalf("stored %d objects, want 8", len(store.objects))
		}
	})
	for _, tc := range []struct {
		name  string
		setup func(*memStore)
		code  e.Code
		calls int
	}{
		{"different existing bytes", func(s *memStore) { s.objects[objectsOf(second)[1]] = []byte("{}") }, e.CodeImmutableViolation, 3},
		{"an existing object over the read bound", func(s *memStore) {
			s.objects[objectsOf(second)[0]] = make([]byte, resultspublish.MaxReadBytes+1)
		}, e.CodeImmutableViolation, 2},
		{"a write error", func(s *memStore) { s.failCreateAt = 2 }, e.CodeUnavailable, 2},
		{"an existing object that cannot be read", func(s *memStore) {
			s.objects[objectsOf(second)[0]], s.failRead = []byte("{}"), true
		}, e.CodeUnavailable, 2},
	} {
		t.Run(tc.name+" stops the run", func(t *testing.T) {
			store := newMemStore()
			tc.setup(store)
			requireCode(t, resultspublish.Publish(t.Context(), store, []qualificationoutcome.Outcome{second}, group, definition), tc.code)
			if len(store.calls) != tc.calls {
				t.Fatalf("calls %q, want %d", store.calls, tc.calls)
			}
		})
	}
	t.Run("an object over the read-back bound stops the run before any write", func(t *testing.T) {
		// Each message appears twice in JUnit and escapes to five times its size, so
		// a valid outcome can project a JUnit file that a retry could not read back.
		large := outcomeOf(qualificationoutcome.StatusFailed, qualificationoutcome.StageQualify)
		large.Checks = nil
		for i := range 3000 {
			large.Checks = append(large.Checks, qualificationoutcome.Check{Output: fmt.Sprintf("o%04d", i), Check: "check", Status: "fail",
				Code: string(e.CodeSignatureInvalid), Message: strings.Repeat("&", qualificationoutcome.MaxMessageBytes)})
		}
		if _, err := large.Marshal(); err != nil {
			t.Fatalf("fixture is not a valid outcome: %v", err)
		}
		store := newMemStore()
		err := resultspublish.Publish(t.Context(), store, []qualificationoutcome.Outcome{second, large}, group, definition)
		requireCode(t, err, e.CodeVerifyFailed)
		if !strings.Contains(err.Error(), "artifacts/junit.xml would exceed") || len(store.calls) != 0 {
			t.Fatalf("got %v after %d writes, want the JUnit refused before any write", err, len(store.calls))
		}
	})
	t.Run("an invalid outcome stops the run before any write", func(t *testing.T) {
		invalid := passed()
		invalid.Status = "unknown"
		store := newMemStore()
		requireCode(t, resultspublish.Publish(t.Context(), store, []qualificationoutcome.Outcome{second, invalid}, group, definition), e.CodeVerifyFailed)
		if len(store.calls) != 0 {
			t.Fatalf("wrote %q", store.calls)
		}
	})
	t.Run("usage", func(t *testing.T) {
		requireCode(t, resultspublish.Publish(t.Context(), newMemStore(), nil, group, definition), e.CodeUsage)
		requireCode(t, resultspublish.Publish(t.Context(), nil, []qualificationoutcome.Outcome{second}, group, definition), e.CodeUsage)
	})
}

func TestResultsPublishStoreConformance(t *testing.T) {
	storetest.Conformance(t, func(*testing.T) resultspublish.ObjectStore { return newMemStore() })
}

func TestResultsPublishTimesAreTheOutcomes(t *testing.T) {
	// Publish reads no clock: the same outcome published twice, apart in time,
	// writes byte-identical objects.
	a, b := newMemStore(), newMemStore()
	for _, store := range []*memStore{a, b} {
		if err := resultspublish.Publish(t.Context(), store, []qualificationoutcome.Outcome{passed()}, group, definition); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Millisecond)
	}
	for path, data := range a.objects {
		if !bytes.Equal(data, b.objects[path]) {
			t.Fatalf("%s differs between passes", path)
		}
	}
}
