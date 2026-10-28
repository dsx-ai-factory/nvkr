// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package development

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

type cleanupFixture struct {
	now            time.Time
	repo           cleanupRepository
	packages       []cleanupPackage
	versions       []cleanupVersion
	run            cleanupRun
	runStatus      int
	deleteStatus   int
	deleteStatusAt int
	deleteUnknown  bool
	recheckPackage func(*cleanupPackage)
	recheckJSON    json.RawMessage
	recheckRun     func(*cleanupRun)
	latestRun      func(*cleanupRun)
	deletes        []string
	requests       int
	runReads       int
	packagePage    func(int) any
	versionPage    func(int) any
}

func newCleanupFixture() *cleanupFixture {
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	old := now.Add(-48 * time.Hour)
	owner := cleanupAccount{ID: 42, Login: "example", Type: "Organization"}
	repo := cleanupRepository{ID: 43, FullName: "example/project", Owner: owner}
	return &cleanupFixture{
		now: now, repo: repo,
		packages: []cleanupPackage{{ID: 11, Name: "project/development/runs/100/attempts/1/releases", Type: "container", Visibility: "internal", Owner: owner, CreatedAt: old, UpdatedAt: old}},
		// Every version is considered, without interpreting tags: this includes
		// unsigned partial uploads, child manifests and Sigstore referrers.
		versions: []cleanupVersion{{ID: 21, CreatedAt: old, UpdatedAt: old}, {ID: 22, CreatedAt: old, UpdatedAt: old}},
		run:      cleanupRun{ID: 100, Attempt: 1, Path: ".github/workflows/nvkr-dev.yml", Event: "workflow_dispatch", Status: "completed", Conclusion: "success", CreatedAt: old, UpdatedAt: old, Repository: repo},
	}
}

func (fixture *cleanupFixture) serve(t *testing.T, writer http.ResponseWriter, request *http.Request) {
	t.Helper()
	fixture.requests++
	if request.Header.Get("Authorization") != "Bearer fixture-secret" || request.Header.Get("X-GitHub-Api-Version") != "2022-11-28" {
		t.Error("cleanup did not use its explicit credential and API version")
	}
	writer.Header().Set("Content-Type", "application/json")
	var payload any
	switch {
	case request.Method == http.MethodDelete:
		fixture.deletes = append(fixture.deletes, request.URL.Path)
		if !strings.Contains(request.RequestURI, "%2F") {
			t.Error("cleanup package API name was not escaped as one path segment")
		}
		if fixture.deleteUnknown {
			connection, _, err := writer.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			_ = connection.Close()
			return
		}
		status := fixture.deleteStatus
		if fixture.deleteStatusAt != 0 && fixture.deleteStatusAt != len(fixture.deletes) {
			status = 0
		}
		if status == 0 {
			status = http.StatusNoContent
		}
		writer.WriteHeader(status)
		return
	case request.URL.Path == "/repos/example/project":
		payload = fixture.repo
	case request.URL.Path == "/orgs/example/packages" || request.URL.Path == "/users/example/packages":
		payload = fixture.packages
		if fixture.packagePage != nil {
			var page int
			_, _ = fmt.Sscan(request.URL.Query().Get("page"), &page)
			payload = fixture.packagePage(page)
		}
	case strings.HasSuffix(request.URL.Path, "/versions"):
		payload = fixture.versions
		if fixture.versionPage != nil {
			var page int
			_, _ = fmt.Sscan(request.URL.Query().Get("page"), &page)
			payload = fixture.versionPage(page)
		}
	case request.URL.Path == "/repos/example/project/actions/runs/100":
		run := fixture.run
		if fixture.latestRun != nil {
			fixture.latestRun(&run)
		}
		payload = run
	case strings.HasPrefix(request.URL.Path, "/repos/example/project/actions/runs/"):
		fixture.runReads++
		if request.URL.Path != "/repos/example/project/actions/runs/100/attempts/1" {
			t.Errorf("cleanup queried a different invocation: %s", request.URL.Path)
		}
		if fixture.runStatus != 0 {
			writer.WriteHeader(fixture.runStatus)
			return
		}
		run := fixture.run
		if fixture.runReads > 1 && fixture.recheckRun != nil {
			fixture.recheckRun(&run)
		}
		payload = run
	default:
		var pkg cleanupPackage
		for _, candidate := range fixture.packages {
			if strings.HasSuffix(request.URL.Path, "/container/"+candidate.Name) {
				pkg = candidate
				break
			}
		}
		if pkg.ID == 0 {
			writer.WriteHeader(http.StatusNotFound)
			return
		}
		if fixture.recheckPackage != nil {
			fixture.recheckPackage(&pkg)
		}
		payload = pkg
		if fixture.recheckJSON != nil {
			payload = fixture.recheckJSON
		}
	}
	if err := json.NewEncoder(writer).Encode(payload); err != nil {
		t.Error(err)
	}
}

func (fixture *cleanupFixture) execute(t *testing.T, ctx context.Context, apply bool) (CleanupReport, error) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) { fixture.serve(t, writer, request) }))
	defer server.Close()
	api := cleanupAPI{client: server.Client(), baseURL: server.URL, now: func() time.Time { return fixture.now }}
	return api.cleanup(ctx, "example/project", "fixture-secret", apply)
}

func TestCleanupAdmitsOnlyExpiredCompleteDevelopmentAttempts(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*cleanupFixture)
		want   int
		code   e.Code
	}{
		{"success", nil, 1, ""},
		{"active retry retains original packages", func(f *cleanupFixture) {
			f.latestRun = func(r *cleanupRun) { r.Attempt = 2; r.Status = "in_progress"; r.Conclusion = "" }
		}, 0, ""},
		{"recent retry retains original packages", func(f *cleanupFixture) {
			f.latestRun = func(r *cleanupRun) { r.Attempt = 2; r.UpdatedAt = f.now.Add(-time.Hour) }
		}, 0, ""},
		{"expired retry", func(f *cleanupFixture) {
			f.latestRun = func(r *cleanupRun) { r.Attempt = 2 }
		}, 1, ""},
		{"failed partial publication", func(f *cleanupFixture) { f.run.Conclusion = "failure" }, 1, ""},
		{"canceled partial publication", func(f *cleanupFixture) { f.run.Conclusion = cleanupCanceledConclusion }, 1, ""},
		{"user account", func(f *cleanupFixture) { f.repo.Owner.Type = "User"; f.packages[0].Owner.Type = "User" }, 1, ""},
		{"queued attempt", func(f *cleanupFixture) { f.run.Status = "queued"; f.run.Conclusion = "" }, 0, ""},
		{"running attempt", func(f *cleanupFixture) { f.run.Status = "in_progress"; f.run.Conclusion = "" }, 0, ""},
		{"young attempt", func(f *cleanupFixture) { f.run.UpdatedAt = f.now.Add(-time.Hour) }, 0, ""},
		{"young package", func(f *cleanupFixture) { f.packages[0].UpdatedAt = f.now.Add(-time.Hour) }, 0, ""},
		{"young signature version", func(f *cleanupFixture) { f.versions[1].UpdatedAt = f.now.Add(-time.Hour) }, 0, ""},
		{"production", func(f *cleanupFixture) { f.packages[0].Name = "project/releases" }, 0, ""},
		{"adjacent prefix", func(f *cleanupFixture) { f.packages[0].Name = "project/development-other/runs/100/attempts/1/releases" }, 0, ""},
		{"another repo", func(f *cleanupFixture) { f.packages[0].Name = "another/development/runs/100/attempts/1/releases" }, 0, ""},
		{"ambiguous run", func(f *cleanupFixture) { f.packages[0].Name = "project/development/runs/0100/attempts/1/releases" }, 0, e.CodeEngineCapability},
		{"path traversal", func(f *cleanupFixture) { f.packages[0].Name = "project/development/runs/100/attempts/1/../releases" }, 0, e.CodeEngineCapability},
		{"wrong owner", func(f *cleanupFixture) { f.packages[0].Owner.ID++ }, 0, e.CodeVerifyFailed},
		{"wrong package type", func(f *cleanupFixture) { f.packages[0].Type = "npm" }, 0, e.CodeVerifyFailed},
		{"private package visibility", func(f *cleanupFixture) { f.packages[0].Visibility = "private" }, 0, e.CodeVerifyFailed},
		{"missing package visibility", func(f *cleanupFixture) { f.packages[0].Visibility = "" }, 0, e.CodeVerifyFailed},
		{"unknown package visibility", func(f *cleanupFixture) { f.packages[0].Visibility = "unknown" }, 0, e.CodeVerifyFailed},
		{"public package", func(f *cleanupFixture) { f.packages[0].Visibility = "public" }, 0, e.CodeVerifyFailed},
		{"wrong repository", func(f *cleanupFixture) { f.run.Repository.ID++ }, 0, e.CodeVerifyFailed},
		{"production workflow", func(f *cleanupFixture) { f.run.Path = ".github/workflows/nvkr-run.yml" }, 0, e.CodeVerifyFailed},
		{"untrusted trigger", func(f *cleanupFixture) { f.run.Event = "pull_request" }, 0, e.CodeVerifyFailed},
		{"wrong run", func(f *cleanupFixture) { f.run.ID++ }, 0, e.CodeVerifyFailed},
		{"wrong attempt", func(f *cleanupFixture) { f.run.Attempt++ }, 0, e.CodeVerifyFailed},
		{"missing run", func(f *cleanupFixture) { f.runStatus = http.StatusNotFound }, 0, e.CodeFetchFailed},
		{"unauthorized run", func(f *cleanupFixture) { f.runStatus = http.StatusForbidden }, 0, e.CodeFetchFailed},
		{"unknown status", func(f *cleanupFixture) { f.run.Status = "unknown" }, 0, e.CodeVerifyFailed},
		{"unknown conclusion", func(f *cleanupFixture) { f.run.Conclusion = "unknown" }, 0, e.CodeVerifyFailed},
		{"missing timestamp", func(f *cleanupFixture) { f.run.UpdatedAt = time.Time{} }, 0, e.CodeVerifyFailed},
		{"future timestamp", func(f *cleanupFixture) { f.versions[0].UpdatedAt = f.now.Add(time.Hour) }, 0, e.CodeVerifyFailed},
		{"out of order timestamps", func(f *cleanupFixture) { f.packages[0].CreatedAt = f.now }, 0, e.CodeVerifyFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newCleanupFixture()
			if test.change != nil {
				test.change(fixture)
			}
			report, err := fixture.execute(t, t.Context(), true)
			if got := cleanupErrorCode(err); got != test.code {
				t.Fatalf("error code=%s, want %s: %v", got, test.code, err)
			}
			if len(fixture.deletes) != test.want || len(report.Deleted) != test.want {
				t.Fatalf("deletes=%v report=%+v, want %d", fixture.deletes, report, test.want)
			}
		})
	}
}

func TestCleanupDryRunAndRecheck(t *testing.T) {
	for _, test := range []struct {
		name   string
		apply  bool
		change func(*cleanupFixture)
		code   e.Code
	}{
		{"dry run", false, nil, ""},
		{"missing recheck fields", true, func(f *cleanupFixture) { f.recheckJSON = json.RawMessage(`{}`) }, e.CodeVerifyFailed},
		{"visibility changed to private", true, func(f *cleanupFixture) { f.recheckPackage = func(p *cleanupPackage) { p.Visibility = "private" } }, e.CodeVerifyFailed},
		{"visibility changed to public", true, func(f *cleanupFixture) { f.recheckPackage = func(p *cleanupPackage) { p.Visibility = "public" } }, e.CodeVerifyFailed},
		{"visibility changed to missing", true, func(f *cleanupFixture) { f.recheckPackage = func(p *cleanupPackage) { p.Visibility = "" } }, e.CodeVerifyFailed},
		{"visibility changed to unknown", true, func(f *cleanupFixture) { f.recheckPackage = func(p *cleanupPackage) { p.Visibility = "unknown" } }, e.CodeVerifyFailed},
		{"recreated package", true, func(f *cleanupFixture) { f.recheckPackage = func(p *cleanupPackage) { p.ID++ } }, e.CodeVerifyFailed},
		{"package updated", true, func(f *cleanupFixture) { f.recheckPackage = func(p *cleanupPackage) { p.UpdatedAt = f.now } }, e.CodeVerifyFailed},
		{"attempt reactivated", true, func(f *cleanupFixture) {
			f.recheckRun = func(r *cleanupRun) { r.Status = "in_progress"; r.Conclusion = "" }
		}, e.CodeVerifyFailed},
		{"latest retry starts on recheck", true, func(f *cleanupFixture) {
			reads := 0
			f.latestRun = func(r *cleanupRun) {
				reads++
				if reads > 1 {
					r.Attempt = 2
					r.Status = "in_progress"
					r.Conclusion = ""
					r.UpdatedAt = f.now
				}
			}
		}, e.CodeVerifyFailed},
		{"attempt updated", true, func(f *cleanupFixture) { f.recheckRun = func(r *cleanupRun) { r.UpdatedAt = f.now } }, e.CodeVerifyFailed},
		{"new signature on recheck", true, func(f *cleanupFixture) {
			reads := 0
			f.versionPage = func(_ int) any {
				reads++
				versions := append([]cleanupVersion(nil), f.versions...)
				if reads > 1 {
					versions[1].UpdatedAt = f.now
				}
				return versions
			}
		}, e.CodeVerifyFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newCleanupFixture()
			if test.change != nil {
				test.change(fixture)
			}
			report, err := fixture.execute(t, t.Context(), test.apply)
			if got := cleanupErrorCode(err); got != test.code {
				t.Fatalf("error=%v code=%s want=%s", err, got, test.code)
			}
			if len(report.Candidates) != 1 || len(report.Deleted) != 0 || len(fixture.deletes) != 0 {
				t.Fatalf("unexpected mutation: %+v, %v", report, fixture.deletes)
			}
		})
	}
}

func TestCleanupInventoryMustCompleteBeforeMutation(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*cleanupFixture)
	}{
		{"malformed later candidate", func(f *cleanupFixture) {
			bad := f.packages[0]
			bad.ID++
			bad.Name = "project/development/invalid"
			f.packages = append(f.packages, bad)
		}},
		{"duplicate packages", func(f *cleanupFixture) { f.packages = append(f.packages, f.packages[0]) }},
		{"null packages", func(f *cleanupFixture) { f.packages = nil }},
		{"null versions", func(f *cleanupFixture) { f.versions = nil }},
		{"duplicate versions", func(f *cleanupFixture) { f.versions = append(f.versions, f.versions[0]) }},
		{"packages pagination bound", func(f *cleanupFixture) {
			f.packagePage = func(page int) any {
				items := make([]cleanupPackage, cleanupPageSize)
				for i := range items {
					items[i] = cleanupPackage{ID: uint64(page*cleanupPageSize + i + 1), Name: fmt.Sprintf("production-%d-%d", page, i)}
				}
				return items
			}
		}},
		{"versions pagination bound", func(f *cleanupFixture) {
			f.versionPage = func(page int) any {
				items := make([]cleanupVersion, cleanupPageSize)
				for i := range items {
					items[i] = f.versions[0]
					items[i].ID = uint64(page*cleanupPageSize + i + 1)
				}
				return items
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newCleanupFixture()
			test.change(fixture)
			_, err := fixture.execute(t, t.Context(), true)
			if err == nil || cleanupErrorCode(err) == "" || len(fixture.deletes) != 0 {
				t.Fatalf("inventory failure allowed mutation: err=%v deletes=%v", err, fixture.deletes)
			}
		})
	}
}

func TestCleanupMutationFailureIsNeverRetried(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*cleanupFixture)
	}{
		{"forbidden", func(f *cleanupFixture) { f.deleteStatus = http.StatusForbidden }},
		{"not found", func(f *cleanupFixture) { f.deleteStatus = http.StatusNotFound }},
		{"server failure", func(f *cleanupFixture) { f.deleteStatus = http.StatusInternalServerError }},
		{"unknown outcome", func(f *cleanupFixture) { f.deleteUnknown = true }},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newCleanupFixture()
			test.change(fixture)
			report, err := fixture.execute(t, t.Context(), true)
			if cleanupErrorCode(err) != e.CodeFetchFailed || len(fixture.deletes) != 1 || len(report.Deleted) != 0 {
				t.Fatalf("mutation failure retried or overclaimed: %v %+v deletes=%v", err, report, fixture.deletes)
			}
			if fixture.deleteUnknown && !strings.Contains(err.Error(), "outcome is unknown") {
				t.Fatalf("ambiguous deletion was not identified: %v", err)
			}
		})
	}
}

func TestCleanupCompletesPaginationAndPreservesPartialReport(t *testing.T) {
	for _, test := range []struct {
		name    string
		partial bool
	}{{"all deleted", false}, {"second deletion fails", true}} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newCleanupFixture()
			second := fixture.packages[0]
			second.ID++
			second.Name = "project/development/runs/100/attempts/1/releases-output"
			fixture.packages = append(fixture.packages, second)
			fixture.packagePage = func(page int) any {
				if page == 1 {
					items := make([]cleanupPackage, cleanupPageSize)
					for i := range items {
						items[i] = cleanupPackage{ID: uint64(1000 + i), Name: fmt.Sprintf("production-%d", i)}
					}
					return items
				}
				return fixture.packages
			}
			fixture.versionPage = func(page int) any {
				if page == 1 {
					items := make([]cleanupVersion, cleanupPageSize)
					for i := range items {
						items[i] = fixture.versions[0]
						items[i].ID = uint64(1000 + i)
					}
					return items
				}
				return fixture.versions
			}
			if test.partial {
				fixture.deleteStatus = http.StatusForbidden
				fixture.deleteStatusAt = 2
			}
			report, err := fixture.execute(t, t.Context(), true)
			want := 2
			if test.partial {
				want = 1
				if cleanupErrorCode(err) != e.CodeFetchFailed {
					t.Fatalf("error=%v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if len(report.Candidates) != 2 || len(report.Deleted) != want || len(fixture.deletes) != 2 {
				t.Fatalf("incomplete pagination or inaccurate report: %+v deletes=%v", report, fixture.deletes)
			}
		})
	}
}

func TestCleanupRejectsBeforeNetworkAndBoundsResponses(t *testing.T) {
	for _, test := range []struct {
		name, repository, token string
		code                    e.Code
	}{
		{"missing token", "example/project", "", e.CodeTrustPolicyMissing},
		{"header injection", "example/project", "secret\r\nother: value", e.CodeTrustPolicyMissing},
		{"invalid repository", "example/project/production", "fixture-secret", e.CodeEngineCapability},
	} {
		t.Run(test.name, func(t *testing.T) {
			api := cleanupAPI{}
			_, err := api.cleanup(t.Context(), test.repository, test.token, true)
			if cleanupErrorCode(err) != test.code {
				t.Fatalf("error=%v want=%s", err, test.code)
			}
		})
	}
	t.Run("canceled context", func(t *testing.T) {
		fixture := newCleanupFixture()
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		_, err := fixture.execute(t, ctx, true)
		if cleanupErrorCode(err) != e.CodeTimeout || fixture.requests != 0 {
			t.Fatalf("canceled cleanup performed I/O: %v requests=%d", err, fixture.requests)
		}
	})
	for _, test := range []struct{ name, body string }{
		{"invalid JSON", "{"},
		{"oversized JSON", strings.Repeat(" ", cleanupBodyLimit+1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(test.body)) }))
			defer server.Close()
			api := cleanupAPI{client: server.Client(), baseURL: server.URL, now: time.Now}
			_, err := api.cleanup(t.Context(), "example/project", "fixture-secret", true)
			if cleanupErrorCode(err) != e.CodeVerifyFailed {
				t.Fatalf("response was accepted: %v", err)
			}
		})
	}
}

func cleanupErrorCode(err error) e.Code {
	var coded *e.Error
	if errors.As(err, &coded) {
		return coded.Code
	}
	return ""
}
