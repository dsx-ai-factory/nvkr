// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package qualificationorigin

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

type nativeSmokeFixture struct {
	invocation     *Invocation
	repository     repositoryResponse
	run            runResponse
	jobs           []nativeSmokeJob
	jobCount       json.Number
	jobPages       map[int][]nativeSmokeJob
	artifacts      map[string]qualificationArtifactList
	archives       map[string][]byte
	requests       []string
	closed         []string
	hook           func(string)
	closeHook      func(string)
	bodyOverride   map[string][]byte
	location       string
	archiveLength  int64
	transportError error
}

func nativeSmokeHeaders() []zip.FileHeader {
	return []zip.FileHeader{{Name: "worker.json", Method: zip.Store}, {Name: "worker.sig", Method: zip.Store},
		{Name: "worker.pem", Method: zip.Store}, {Name: "worker.bundle.json", Method: zip.Store}}
}

func newNativeSmokeFixture(t *testing.T) *nativeSmokeFixture {
	t.Helper()
	request, repository, run := sourceFixture()
	source, err := authenticateSourceResponse(t.Context(), request, repository, run)
	if err != nil {
		t.Fatal(err)
	}
	request.RunID, request.Attempt = "2345", "3"
	request.Commit, request.Workflow = strings.Repeat("d", 40), ".github/workflows/qualification.yml"
	run.ID, run.Attempt, run.HeadSHA, run.Path = "2345", "3", request.Commit, request.Workflow
	run.Event, run.Status, run.Conclusion = "workflow_run", "in_progress", ""
	fixture := &nativeSmokeFixture{invocation: &Invocation{description: describe(request), source: source, closed: new(atomic.Bool)},
		repository: repository, run: run, artifacts: map[string]qualificationArtifactList{}, archives: map[string][]byte{},
		bodyOverride: map[string][]byte{}, location: "https://results-receiver.actions.githubusercontent.com/archive", archiveLength: -1}
	for i, arch := range []string{"amd64", "arm64"} {
		fixture.jobs = append(fixture.jobs, nativeSmokeJob{ID: json.Number(strconv.Itoa(i + 100)), RunID: run.ID, Attempt: run.Attempt,
			HeadSHA: request.Commit, Name: "native-smoke / native-" + arch, Status: "completed", Conclusion: "success"})
		archive := handoffZIP(t, nativeSmokeHeaders(), []byte("worker-"+arch))
		name := "nvkr-smoke-2345-3-" + arch
		expired := false
		fixture.artifacts[name] = qualificationArtifactList{TotalCount: "1", Artifacts: []qualificationArtifact{{
			ID: json.Number(strconv.Itoa(i + 200)), Name: name, Size: json.Number(strconv.Itoa(len(archive))), Digest: handoffArchiveDigest(archive), Expired: &expired,
			WorkflowRun: qualificationArtifactRun{ID: run.ID, RepositoryID: repository.ID, HeadRepositoryID: repository.ID, HeadSHA: request.Commit, HeadBranch: request.DefaultBranch},
		}}}
		fixture.archives[strconv.Itoa(i+200)] = archive
	}
	return fixture
}

func (fixture *nativeSmokeFixture) github(t *testing.T) *GitHub {
	t.Helper()
	g := NewGitHub("read-secret")
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	address, err := url.Parse(fixture.location)
	if err != nil {
		t.Fatal(err)
	}
	jar.SetCookies(address, []*http.Cookie{{Name: "credential", Value: "secret"}})
	g.client.Jar = jar
	g.client.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		deadline, ok := request.Context().Deadline()
		if !ok || time.Until(deadline) > 2*time.Minute || request.Method != http.MethodGet {
			t.Fatal("unbounded or mutating request")
		}
		var phase string
		status, length, header := http.StatusOK, int64(-1), http.Header{}
		var value any
		var body []byte
		if request.URL.Host == "api.github.com" {
			if request.Header.Get("Authorization") != "Bearer read-secret" || request.Header.Get("X-GitHub-Api-Version") != "2022-11-28" {
				t.Fatal("missing API read credential/version")
			}
			switch request.URL.Path {
			case "/repos/example/nvkr":
				phase, value = "repository", fixture.repository
			case "/repos/example/nvkr/actions/runs/2345/attempts/3":
				phase, value = "invocation", fixture.run
			case "/repos/example/nvkr/actions/runs/2345/attempts/3/jobs":
				page, err := strconv.Atoi(request.URL.Query().Get("page"))
				if err != nil || request.URL.Query().Get("per_page") != "100" {
					t.Fatal("incorrect job page")
				}
				phase = "jobs-" + strconv.Itoa(page)
				jobs := fixture.jobs
				if fixture.jobPages != nil {
					jobs = fixture.jobPages[page]
				}
				count := fixture.jobCount
				if count == "" {
					count = json.Number(strconv.Itoa(len(fixture.jobs)))
				}
				value = nativeSmokeJobList{TotalCount: count, Jobs: jobs}
			case "/repos/example/nvkr/actions/runs/2345/artifacts":
				name := request.URL.Query().Get("name")
				phase, value = "list-"+name, fixture.artifacts[name]
				if request.URL.Query().Get("per_page") != "100" {
					t.Fatal("unbounded artifact discovery")
				}
			case "/repos/example/nvkr/actions/artifacts/200/zip", "/repos/example/nvkr/actions/artifacts/201/zip":
				id := strings.Split(request.URL.Path, "/")[6]
				phase, status = "location-"+id, http.StatusFound
				header.Set("Location", fixture.location+"?id="+id+"&sig=download-secret")
			default:
				t.Fatalf("unexpected API path %s", request.URL)
			}
			if value != nil {
				var err error
				body, err = json.Marshal(value)
				if err != nil {
					t.Fatal(err)
				}
			}
		} else {
			id := request.URL.Query().Get("id")
			phase, body, length = "archive-"+id, fixture.archives[id], fixture.archiveLength
			if request.Header.Get("Authorization") != "" || request.Header.Get("Cookie") != "" || request.Header.Get("Referer") != "" {
				t.Fatal("download inherited credentials")
			}
		}
		fixture.requests = append(fixture.requests, phase)
		if fixture.hook != nil {
			fixture.hook(phase)
		}
		if strings.HasPrefix(phase, "archive-") && fixture.transportError != nil {
			return nil, fixture.transportError
		}
		if override, exists := fixture.bodyOverride[phase]; exists {
			body = override
		}
		return &http.Response{StatusCode: status, Header: header, ContentLength: length, Body: &handoffTestBody{reader: bytes.NewReader(body), close: func() {
			fixture.closed = append(fixture.closed, phase)
			if fixture.closeHook != nil {
				fixture.closeHook(phase)
			}
		}}}, nil
	})
	return g
}

func TestDownloadNativeSmokeExactAttemptSortedCompleteTransport(t *testing.T) {
	for _, platforms := range [][]string{{"linux/arm64", "linux/amd64"}, {"linux/amd64"}} {
		t.Run(strings.Join(platforms, "+"), func(t *testing.T) {
			fixture := newNativeSmokeFixture(t)
			got, err := fixture.github(t).DownloadNativeSmoke(t.Context(), fixture.invocation, platforms)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(platforms) || got[0].Platform != "linux/amd64" {
				t.Fatalf("wrong result %+v", got)
			}
			for _, artifact := range got {
				want := []byte("worker-" + strings.TrimPrefix(artifact.Platform, "linux/"))
				for _, actual := range [][]byte{artifact.Document, artifact.Signature, artifact.Certificate, artifact.Bundle} {
					if !bytes.Equal(actual, want) {
						t.Fatalf("wrong member bytes %q", actual)
					}
				}
			}
			if len(fixture.closed) != len(fixture.requests) {
				t.Fatal("unclosed response body")
			}
		})
	}
}

func TestDownloadNativeSmokeRejectsWrongProviderBindings(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*nativeSmokeFixture)
		code e.Code
	}{
		{"wrong invocation attempt", func(f *nativeSmokeFixture) { f.run.Attempt = "2" }, e.CodeVerifyFailed},
		{"wrong invocation commit", func(f *nativeSmokeFixture) { f.run.HeadSHA = strings.Repeat("c", 40) }, e.CodeVerifyFailed},
		{"completed invocation", func(f *nativeSmokeFixture) { f.run.Status = "completed"; f.run.Conclusion = "success" }, e.CodeVerifyFailed},
		{"wrong invocation event", func(f *nativeSmokeFixture) { f.run.Event = "push" }, e.CodeVerifyFailed},
		{"job failed", func(f *nativeSmokeFixture) { f.jobs[0].Conclusion = "failure" }, e.CodeGateUnmet},
		{"job still running", func(f *nativeSmokeFixture) { f.jobs[0].Status = "in_progress" }, e.CodeGateUnmet},
		{"wrong job name", func(f *nativeSmokeFixture) { f.jobs[0].Name = "other / native-amd64" }, e.CodeGateUnmet},
		{"duplicate job name", func(f *nativeSmokeFixture) { f.jobs[1].Name = f.jobs[0].Name }, e.CodeGateUnmet},
		{"duplicate job ID", func(f *nativeSmokeFixture) { f.jobs[1].ID = f.jobs[0].ID }, e.CodeVerifyFailed},
		{"wrong job attempt", func(f *nativeSmokeFixture) { f.jobs[0].Attempt = "2" }, e.CodeVerifyFailed},
		{"wrong job run", func(f *nativeSmokeFixture) { f.jobs[0].RunID = "1234" }, e.CodeVerifyFailed},
		{"wrong job commit", func(f *nativeSmokeFixture) { f.jobs[0].HeadSHA = strings.Repeat("a", 40) }, e.CodeVerifyFailed},
		{"excessive job count", func(f *nativeSmokeFixture) { f.jobCount = "1001" }, e.CodeVerifyFailed},
		{"incomplete job list", func(f *nativeSmokeFixture) { f.jobCount = "3" }, e.CodeVerifyFailed},
		{"duplicate metadata JSON", func(f *nativeSmokeFixture) {
			f.bodyOverride["jobs-1"] = []byte(`{"total_count":2,"total_count":1,"jobs":[]}`)
		}, e.CodeVerifyFailed},
		{"excessive response", func(f *nativeSmokeFixture) { f.bodyOverride["jobs-1"] = bytes.Repeat([]byte(" "), maxResponseBytes+1) }, e.CodeVerifyFailed},
		{"missing artifact", func(f *nativeSmokeFixture) { delete(f.artifacts, "nvkr-smoke-2345-3-arm64") }, e.CodeProvenanceMissing},
		{"wrong artifact attempt name", func(f *nativeSmokeFixture) {
			f.artifacts["nvkr-smoke-2345-3-amd64"].Artifacts[0].Name = "nvkr-smoke-2345-2-amd64"
		}, e.CodeVerifyFailed},
		{"wrong artifact commit", func(f *nativeSmokeFixture) {
			f.artifacts["nvkr-smoke-2345-3-amd64"].Artifacts[0].WorkflowRun.HeadSHA = strings.Repeat("a", 40)
		}, e.CodeVerifyFailed},
		{"wrong artifact repository IDs", func(f *nativeSmokeFixture) {
			r := &f.artifacts["nvkr-smoke-2345-3-amd64"].Artifacts[0].WorkflowRun
			r.RepositoryID = "999"
			r.HeadRepositoryID = "999"
		}, e.CodeVerifyFailed},
		{"fork artifact", func(f *nativeSmokeFixture) {
			f.artifacts["nvkr-smoke-2345-3-amd64"].Artifacts[0].WorkflowRun.HeadRepositoryID = "999"
		}, e.CodeVerifyFailed},
		{"missing digest", func(f *nativeSmokeFixture) { f.artifacts["nvkr-smoke-2345-3-amd64"].Artifacts[0].Digest = "" }, e.CodeUnpinnedInput},
		{"wrong digest", func(f *nativeSmokeFixture) {
			f.artifacts["nvkr-smoke-2345-3-amd64"].Artifacts[0].Digest = "sha256:" + strings.Repeat("a", 64)
		}, e.CodeVerifyFailed},
		{"oversized archive", func(f *nativeSmokeFixture) {
			f.artifacts["nvkr-smoke-2345-3-amd64"].Artifacts[0].Size = json.Number(strconv.Itoa(maxNativeSmokeArchiveBytes + 1))
		}, e.CodeVerifyFailed},
		{"wrong archive size", func(f *nativeSmokeFixture) { f.artifacts["nvkr-smoke-2345-3-amd64"].Artifacts[0].Size = "1" }, e.CodeVerifyFailed},
		{"large content length", func(f *nativeSmokeFixture) { f.archiveLength = maxNativeSmokeArchiveBytes + 1 }, e.CodeVerifyFailed},
		{"unapproved location", func(f *nativeSmokeFixture) { f.location = "https://attacker.invalid/archive" }, e.CodeVerifyFailed},
		{"download transport failure", func(f *nativeSmokeFixture) {
			f.transportError = &url.Error{Op: "Get", URL: f.location + "?secret", Err: errors.New("secret")}
		}, e.CodeFetchFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newNativeSmokeFixture(t)
			tc.edit(fixture)
			got, err := fixture.github(t).DownloadNativeSmoke(t.Context(), fixture.invocation, []string{"linux/amd64", "linux/arm64"})
			assertCode(t, err, tc.code)
			if got != nil {
				t.Fatal("failure returned partial transport")
			}
			if strings.Contains(err.Error(), "secret") {
				t.Fatal("transport exposed credentials")
			}
		})
	}
}

func TestDownloadNativeSmokeBoundsAndCompletesJobPagination(t *testing.T) {
	for _, tc := range []struct {
		name      string
		duplicate bool
		missing   bool
	}{{name: "second page"}, {name: "duplicate across pages", duplicate: true}, {name: "missing second page", missing: true}} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newNativeSmokeFixture(t)
			first := make([]nativeSmokeJob, 100)
			for i := range first {
				first[i] = nativeSmokeJob{ID: json.Number(strconv.Itoa(i + 1000)), RunID: fixture.run.ID, HeadSHA: fixture.run.HeadSHA, Name: "other", Status: "in_progress"}
			}
			fixture.jobPages = map[int][]nativeSmokeJob{1: first, 2: fixture.jobs}
			fixture.jobCount = "102"
			if tc.duplicate {
				fixture.jobs[0].ID = first[0].ID
			}
			if tc.missing {
				delete(fixture.jobPages, 2)
			}
			got, err := fixture.github(t).DownloadNativeSmoke(t.Context(), fixture.invocation, []string{"linux/amd64", "linux/arm64"})
			if tc.duplicate || tc.missing {
				assertCode(t, err, e.CodeVerifyFailed)
				if got != nil {
					t.Fatal("partial output")
				}
				return
			}
			if err != nil || len(got) != 2 {
				t.Fatalf("pagination failed: %v", err)
			}
		})
	}
}

func TestDownloadNativeSmokeAdmissionCancellationAndRevocation(t *testing.T) {
	for _, platforms := range [][]string{nil, {}, {"linux/386"}, {"darwin/arm64"}, {"linux/arm64", "linux/arm64"}, {"linux/amd64", "linux/arm64", "linux/amd64"}} {
		fixture := newNativeSmokeFixture(t)
		_, err := fixture.github(t).DownloadNativeSmoke(t.Context(), fixture.invocation, platforms)
		assertCode(t, err, e.CodeEngineCapability)
		if len(fixture.requests) != 0 {
			t.Fatal("unsupported platform reached IO")
		}
	}
	for _, phase := range []string{"before", "repository", "invocation", "jobs-1", "list-nvkr-smoke-2345-3-amd64", "location-200", "archive-200", "archive-201"} {
		for _, action := range []string{"cancel", "revoke invocation", "revoke source"} {
			t.Run(action+"/"+phase, func(t *testing.T) {
				fixture := newNativeSmokeFixture(t)
				copyInvocation := *fixture.invocation
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				act := func() {
					switch action {
					case "cancel":
						cancel()
					case "revoke invocation":
						_ = fixture.invocation.Close()
					case "revoke source":
						_ = fixture.invocation.source.Close()
					}
				}
				if phase == "before" {
					act()
				} else {
					fixture.closeHook = func(current string) {
						if current == phase {
							act()
						}
					}
				}
				got, err := fixture.github(t).DownloadNativeSmoke(ctx, &copyInvocation, []string{"linux/amd64", "linux/arm64"})
				want := e.CodeProvenanceMissing
				if action == "cancel" {
					want = e.CodeTimeout
				}
				assertCode(t, err, want)
				if got != nil || phase == "before" && len(fixture.requests) != 0 {
					t.Fatal("revoked invocation returned bytes or reached early IO")
				}
				if action == "cancel" && !errors.Is(err, context.Canceled) {
					t.Fatal("lost cancellation cause")
				}
			})
		}
	}
	for _, invocation := range []*Invocation{nil, {}} {
		fixture := newNativeSmokeFixture(t)
		_, err := fixture.github(t).DownloadNativeSmoke(t.Context(), invocation, []string{"linux/amd64"})
		assertCode(t, err, e.CodeProvenanceMissing)
		if len(fixture.requests) != 0 {
			t.Fatal("unissued invocation reached IO")
		}
	}
	fixture := newNativeSmokeFixture(t)
	_, err := fixture.github(t).DownloadNativeSmoke(nil, fixture.invocation, []string{"linux/amd64"}) //nolint:staticcheck // Nil-context refusal is part of the public contract.
	assertCode(t, err, e.CodeUsage)
	if len(fixture.requests) != 0 {
		t.Fatal("nil context reached IO")
	}
}

func TestNativeSmokeArchiveStrictMembershipAndSizes(t *testing.T) {
	for _, name := range []string{"../worker.json", "./worker.json", "/worker.json", "worker\\json", "worker.sig", "unknown"} {
		headers := nativeSmokeHeaders()
		headers[0].Name = name
		archive := handoffZIP(t, headers, []byte("payload"))
		got, err := unpackNativeSmoke(t.Context(), archive)
		assertCode(t, err, e.CodeVerifyFailed)
		if !reflect.DeepEqual(got, NativeSmokeArtifact{}) {
			t.Fatal("invalid archive returned partial bytes")
		}
	}
	for _, tc := range []struct {
		name  string
		edit  func([]zip.FileHeader) []zip.FileHeader
		empty bool
	}{
		{"extra", func(h []zip.FileHeader) []zip.FileHeader { return append(h, zip.FileHeader{Name: "extra"}) }, false},
		{"missing", func(h []zip.FileHeader) []zip.FileHeader { return h[:3] }, false},
		{"symlink", func(h []zip.FileHeader) []zip.FileHeader { h[0].SetMode(os.ModeSymlink | 0o777); return h }, false},
		{"directory", func(h []zip.FileHeader) []zip.FileHeader { h[0].SetMode(os.ModeDir | 0o755); return h }, false},
		{"empty", func(h []zip.FileHeader) []zip.FileHeader { return h }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := []byte("payload")
			if tc.empty {
				payload = nil
			}
			_, err := unpackNativeSmoke(t.Context(), handoffZIP(t, tc.edit(nativeSmokeHeaders()), payload))
			assertCode(t, err, e.CodeVerifyFailed)
		})
	}
	for i, limit := range []uint32{maxNativeSmokeDocumentBytes, maxNativeSmokeSignatureBytes, maxNativeSmokeCertificateBytes, maxNativeSmokeBundleBytes} {
		archive := handoffZIP(t, nativeSmokeHeaders(), []byte("payload"))
		central := 0
		for n := 0; n <= i; n++ {
			central += bytes.Index(archive[central:], []byte{'P', 'K', 1, 2})
			if n < i {
				central += 4
			}
		}
		binary.LittleEndian.PutUint32(archive[central+24:central+28], limit+1)
		_, err := unpackNativeSmoke(t.Context(), archive)
		assertCode(t, err, e.CodeVerifyFailed)
	}
	archive := handoffZIP(t, nativeSmokeHeaders(), []byte("payload"))
	archive[30+len("worker.json")] ^= 1
	_, err := unpackNativeSmoke(t.Context(), archive)
	assertCode(t, err, e.CodeVerifyFailed)
}

func TestNativeSmokeArchiveMetadataBudgetAndCancellation(t *testing.T) {
	headers := make([]zip.FileHeader, 4000)
	for i := range headers {
		headers[i] = zip.FileHeader{Name: "worker.json", Comment: strings.Repeat("x", 512)}
	}
	archive := handoffZIP(t, headers, nil)
	counted := &handoffCountedReaderAt{reader: bytes.NewReader(archive)}
	_, err := readNativeSmokeArchive(t.Context(), counted, int64(len(archive)))
	assertCode(t, err, e.CodeVerifyFailed)
	if counted.bytes > maxQualificationZIPMetadataBytes || !strings.Contains(err.Error(), "metadata exceeds") {
		t.Fatalf("unbounded metadata: %d %v", counted.bytes, err)
	}
	for _, phase := range []string{"before", "metadata", "opening"} {
		t.Run(phase, func(t *testing.T) {
			archive := handoffZIP(t, nativeSmokeHeaders(), bytes.Repeat([]byte("x"), 32<<10))
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			counted := &handoffCountedReaderAt{reader: bytes.NewReader(archive)}
			if phase == "before" {
				cancel()
			}
			counted.afterRead = func(offset int64) {
				if phase == "metadata" || phase == "opening" && offset == 0 {
					cancel()
				}
			}
			got, err := readNativeSmokeArchive(ctx, counted, int64(len(archive)))
			assertCode(t, err, e.CodeTimeout)
			if !reflect.DeepEqual(got, NativeSmokeArtifact{}) || !errors.Is(err, context.Canceled) {
				t.Fatal("canceled parser returned bytes or lost cause")
			}
		})
	}
}
