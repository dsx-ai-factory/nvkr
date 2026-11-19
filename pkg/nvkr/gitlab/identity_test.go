// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
package gitlab

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

const testCommit = "0123456789abcdef0123456789abcdef01234567"

type rejectDefaultTransport struct{}

func (rejectDefaultTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, fmt.Errorf("mutable default transport was invoked")
}

func TestPublicClientIgnoresMutableDefaultTransport(t *testing.T) {
	if os.Getenv("NVKR_GITLAB_TRANSPORT_CHILD") == "1" {
		raw, err := os.ReadFile(os.Getenv("NVKR_GITLAB_TRANSPORT_CA"))
		if err != nil {
			t.Fatal(err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(raw) {
			t.Fatal("invalid fixture CA")
		}
		x509.SetFallbackRoots(pool)
		http.DefaultTransport = rejectDefaultTransport{}
		client, err := New(t.Context(), Config{APIURL: os.Getenv("NVKR_GITLAB_TRANSPORT_API"), ProjectID: 1, JobToken: "job-secret", ReadCredential: "read-secret", ReadCredentialKind: "private-token"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := client.Current(t.Context(), id(), []string{"nvkr:finalize"}); err != nil {
			t.Fatal(err)
		}
		return
	}
	self := fixtureJob(3, "nvkr:finalize", "running")
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v4/projects/1":
			_, _ = w.Write([]byte(`{"id":1,"ci_config_path":null}`))
		case "/api/v4/projects/1/pipelines/2":
			_, _ = w.Write([]byte(`{"id":2,"project_id":1,"sha":"` + testCommit + `","ref":"main","source":"push"}`))
		case "/api/v4/job":
			_ = json.NewEncoder(w).Encode(self)
		case "/api/v4/projects/1/pipelines/2/jobs":
			w.Header().Set("X-Next-Page", "")
			_ = json.NewEncoder(w).Encode([]apiJob{self})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	child := exec.CommandContext(t.Context(), executable, "-test.run=^TestPublicClientIgnoresMutableDefaultTransport$", "-test.count=1")
	child.Env = []string{"GODEBUG=x509usefallbackroots=1", "NVKR_GITLAB_TRANSPORT_CHILD=1", "NVKR_GITLAB_TRANSPORT_CA=" + ca, "NVKR_GITLAB_TRANSPORT_API=" + server.URL + "/api/v4"}
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("public HTTPS transport fixture: %v\n%s", err, output)
	}
}

func fixtureJob(id uint64, name, status string) apiJob {
	var j apiJob
	branch := false
	j.Tag = &branch
	j.ID = id
	j.Name = name
	j.Status = status
	j.Ref = "main"
	j.Pipeline.Ref = "main"
	j.Pipeline.Source = "push"
	j.Commit.ID = testCommit
	j.Pipeline.ID = 2
	j.Pipeline.ProjectID = 1
	j.Pipeline.SHA = testCommit
	return j
}
func fixtureClient(t *testing.T, fn func(http.ResponseWriter, *http.Request)) *Client {
	t.Helper()
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v4/projects/1":
			_, _ = w.Write([]byte(`{"id":1,"ci_config_path":null}`))
			return
		case "/api/v4/projects/1/pipelines/2":
			_, _ = w.Write([]byte(`{"id":2,"project_id":1,"sha":"` + testCommit + `","ref":"main","source":"push"}`))
			return
		}
		fn(w, r)
	}))
	t.Cleanup(s.Close)
	c, err := newWithHTTPClient(t.Context(), Config{APIURL: s.URL + "/api/v4", ProjectID: 1, JobToken: "job-secret", ReadCredential: "read-secret", ReadCredentialKind: "private-token"}, s.Client())
	if err != nil {
		t.Fatal(err)
	}
	return c
}
func id() Identity { return Identity{ProjectID: 1, PipelineID: 2, JobID: 3, Commit: testCommit} }
func TestCurrentAuthenticatedMap(t *testing.T) {
	self := fixtureJob(3, "nvkr:finalize", "running")
	producer := fixtureJob(4, "nvkr:op:build", "success")
	c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v4/job":
			if r.Header.Get("JOB-TOKEN") != "job-secret" || r.Header.Get("PRIVATE-TOKEN") != "" {
				t.Error("wrong self credential")
			}
			_ = json.NewEncoder(w).Encode(self)
		case "/api/v4/projects/1/pipelines/2/jobs":
			w.Header().Set("X-Next-Page", "")
			if r.Header.Get("PRIVATE-TOKEN") != "read-secret" || r.Header.Get("JOB-TOKEN") != "" || r.URL.Query().Get("include_retried") != "false" {
				t.Error("wrong listing contract")
			}
			_ = json.NewEncoder(w).Encode([]apiJob{self, producer})
		default:
			t.Errorf("unexpected request path")
			w.WriteHeader(404)
		}
	})
	receipt, err := c.Current(context.Background(), id(), []string{self.Name, producer.Name})
	if err != nil {
		t.Fatal(err)
	}
	want := id()
	want.APIURL = c.base.String()
	want.Ref = "main"
	want.Source = "push"
	if receipt.CurrentIdentity() != want {
		t.Fatal("identity mismatch")
	}
	if j, ok := receipt.Job(producer.Name); !ok || j.ID != 4 || j.Status != "success" {
		t.Fatal("job mismatch")
	}
	if _, ok := receipt.Job("foreign"); ok {
		t.Fatal("foreign job")
	}
}
func TestCurrentRejectsBadMembership(t *testing.T) {
	self := fixtureJob(3, "nvkr:finalize", "running")
	good := fixtureJob(4, "nvkr:op:build", "success")
	for _, tc := range []struct {
		name string
		self apiJob
		list []apiJob
	}{{"missing", self, []apiJob{self}}, {"duplicate", self, []apiJob{self, good, good}}, {"reserved foreign", self, []apiJob{self, good, fixtureJob(9, "nvkr:foreign", "success")}}, {"old self", fixtureJob(8, self.Name, "running"), []apiJob{self, good}}, {"wrong pipeline", self, []apiJob{self, func() apiJob { x := good; x.Pipeline.ID = 99; return x }()}}, {"wrong commit", self, []apiJob{self, func() apiJob { x := good; x.Commit.ID = strings.Repeat("a", 40); return x }()}}, {"unknown status", self, []apiJob{self, func() apiJob { x := good; x.Status = "unknown"; return x }()}}} {
		t.Run(tc.name, func(t *testing.T) {
			c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/job") {
					_ = json.NewEncoder(w).Encode(tc.self)
				} else {
					w.Header().Set("X-Next-Page", "")
					_ = json.NewEncoder(w).Encode(tc.list)
				}
			})
			if _, err := c.Current(context.Background(), id(), []string{self.Name, good.Name}); err == nil {
				t.Fatal("accepted bad current map")
			}
		})
	}
}
func TestCurrentPaginationAndUnrelatedJobs(t *testing.T) {
	self := fixtureJob(3, "nvkr:finalize", "running")
	expected := []string{self.Name}
	page := []apiJob{self}
	for n := 0; n < 100; n++ {
		name := fmt.Sprintf("nvkr:op:%03d", n)
		expected = append(expected, name)
		page = append(page, fixtureJob(uint64(n+10), name, "success"))
	}
	var calls atomic.Int64
	c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/job") {
			_ = json.NewEncoder(w).Encode(self)
			return
		}
		calls.Add(1)
		n, _ := strconv.Atoi(r.URL.Query().Get("page"))
		if n == 1 {
			w.Header().Set("X-Next-Page", "2")
			_ = json.NewEncoder(w).Encode(page[:100])
		} else {
			w.Header().Set("X-Next-Page", "")
			_ = json.NewEncoder(w).Encode(append(page[100:], fixtureJob(900, "operator-job", "failed")))
		}
	})
	if _, err := c.Current(context.Background(), id(), expected); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 4 {
		t.Fatalf("pages including confirmation %d", calls.Load())
	}
}

func TestCurrentPaginationRejectsChangedConfirmation(t *testing.T) {
	for _, test := range []struct {
		name string
		code e.Code
	}{
		{"retried producer", e.CodeVerifyFailed},
		{"changed status", e.CodeVerifyFailed},
		{"canceled confirmation", e.CodeTimeout},
	} {
		t.Run(test.name, func(t *testing.T) {
			self := fixtureJob(3, "nvkr:finalize", "running")
			expected := []string{self.Name}
			var initial []apiJob
			for n := 0; n < 101; n++ {
				name := fmt.Sprintf("nvkr:op:%03d", n)
				expected = append(expected, name)
				initial = append(initial, fixtureJob(uint64(210-n), name, "success"))
			}
			initial[100].Status = "pending"
			initial = append(initial, self)
			current := append([]apiJob(nil), initial...)
			current[100].Status = "success"
			if test.name == "changed status" {
				current[0].Status = "failed"
			} else {
				current[0].ID = 211
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var calls atomic.Int64
			client := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v4/job" {
					_ = json.NewEncoder(w).Encode(self)
					return
				}
				request := calls.Add(1)
				if request == 3 && test.name == "canceled confirmation" {
					cancel()
					<-r.Context().Done()
					return
				}
				// After page one, A changes on that page and B completes on page
				// two. The initial scan alone assembles an inconsistent result.
				jobs := current
				if request == 1 {
					jobs = initial
				}
				switch r.URL.Query().Get("page") {
				case "1":
					w.Header().Set("X-Next-Page", "2")
					_ = json.NewEncoder(w).Encode(jobs[:100])
				case "2":
					w.Header().Set("X-Next-Page", "")
					_ = json.NewEncoder(w).Encode(jobs[100:])
				default:
					t.Error("unexpected pagination request")
					w.WriteHeader(http.StatusBadRequest)
				}
			})
			defer func() { _ = client.Close() }()
			receipt, err := client.Current(ctx, id(), expected)
			var coded *e.Error
			if receipt != nil || !errors.As(err, &coded) || coded.Code != test.code {
				t.Fatalf("receipt=%v err=%v; want no receipt and %s", receipt, err, test.code)
			}
			wantCalls := int64(4)
			if test.code == e.CodeTimeout {
				wantCalls = 3
			}
			if calls.Load() != wantCalls {
				t.Fatalf("listing requests=%d, want bounded confirmation count %d", calls.Load(), wantCalls)
			}
		})
	}
}

func TestClientAPIURLCanonicality(t *testing.T) {
	for _, test := range []struct {
		name, api string
		valid     bool
	}{
		{"canonical", "https://gitlab.example.test/api/v4", true},
		{"explicit port", "https://gitlab.example.test:8443/api/v4", true},
		{"IPv6 hostname", "https://[::1]:8443/api/v4", true},
		{"missing hostname", "https://:443/api/v4", false},
		{"empty query", "https://gitlab.example.test/api/v4?", false},
		{"empty fragment", "https://gitlab.example.test/api/v4#", false},
		{"query", "https://gitlab.example.test/api/v4?token=value", false},
		{"fragment", "https://gitlab.example.test/api/v4#fragment", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			client, err := New(t.Context(), Config{APIURL: test.api, ProjectID: 1, JobToken: "j", ReadCredential: "r", ReadCredentialKind: "private-token"})
			if test.valid {
				if err != nil {
					t.Fatal(err)
				}
				if err := client.Close(); err != nil {
					t.Fatal(err)
				}
				return
			}
			var coded *e.Error
			if client != nil || !errors.As(err, &coded) || coded.Code != e.CodeUsage {
				t.Fatalf("client=%v err=%v; want no client and %s", client, err, e.CodeUsage)
			}
		})
	}
}

func TestCurrentStartedAtComesOnlyFromAuthenticatedSelf(t *testing.T) {
	const timestamp = "2026-11-18T12:00:00.123Z"
	want, err := time.Parse(time.RFC3339Nano, timestamp)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, raw string
		want      time.Time
		code      e.Code
	}{
		{"reported", strconv.Quote(timestamp), want, ""},
		{"missing", "", time.Time{}, ""},
		{"null", "null", time.Time{}, ""},
		{"invalid", `"not-a-timestamp"`, time.Time{}, e.CodeVerifyFailed},
		{"wrong type", `123`, time.Time{}, e.CodeVerifyFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			self := fixtureJob(3, "nvkr:finalize", "running")
			listedStart := want.Add(-time.Hour)
			listed := self
			listed.StartedAt = &listedStart
			client := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v4/job" {
					raw, err := json.Marshal(self)
					if err != nil {
						t.Error(err)
						return
					}
					var fields map[string]json.RawMessage
					if err := json.Unmarshal(raw, &fields); err != nil {
						t.Error(err)
						return
					}
					delete(fields, "started_at")
					if test.raw != "" {
						fields["started_at"] = json.RawMessage(test.raw)
					}
					_ = json.NewEncoder(w).Encode(fields)
					return
				}
				w.Header().Set("X-Next-Page", "")
				_ = json.NewEncoder(w).Encode([]apiJob{listed})
			})
			defer func() { _ = client.Close() }()
			receipt, err := client.Current(t.Context(), id(), []string{self.Name})
			if test.code != "" {
				var coded *e.Error
				if receipt != nil || !errors.As(err, &coded) || coded.Code != test.code {
					t.Fatalf("receipt=%v err=%v; want no receipt and %s", receipt, err, test.code)
				}
				return
			}
			if err != nil || !receipt.StartedAt().Equal(test.want) {
				t.Fatalf("started_at=%v err=%v; want %v", receipt.StartedAt(), err, test.want)
			}
		})
	}
	if !(*Receipt)(nil).StartedAt().IsZero() || !(&Receipt{}).StartedAt().IsZero() {
		t.Fatal("unissued receipt supplied a provider start")
	}
}

func TestRejectCredentialsRedirectAndInvalidJSON(t *testing.T) {
	for _, tc := range []struct {
		name     string
		response func(http.ResponseWriter, string)
	}{
		{"redirect", func(w http.ResponseWriter, target string) { w.Header().Set("Location", target); w.WriteHeader(302) }},
		{"duplicate json", func(w http.ResponseWriter, _ string) { _, _ = w.Write([]byte(`{"id":3,"id":4}`)) }},
		{"error secret", func(w http.ResponseWriter, _ string) { w.WriteHeader(403); _, _ = w.Write([]byte("secret-token")) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var foreignRequests atomic.Int64
			foreign := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				foreignRequests.Add(1)
				_, _ = w.Write([]byte(`{"id":3}`))
			}))
			defer foreign.Close()
			c := fixtureClient(t, func(w http.ResponseWriter, _ *http.Request) { tc.response(w, foreign.URL) })
			defer c.Close()
			transport := c.http.Transport.(*http.Transport).Clone()
			transport.TLSClientConfig = transport.TLSClientConfig.Clone()
			transport.TLSClientConfig.RootCAs = transport.TLSClientConfig.RootCAs.Clone()
			transport.TLSClientConfig.RootCAs.AddCert(foreign.Certificate())
			c.http.Transport = transport
			_, err := c.Current(t.Context(), id(), []string{"nvkr:finalize"})
			if err == nil || strings.Contains(err.Error(), "secret-token") || strings.Contains(err.Error(), foreign.URL) {
				t.Fatal("unsafe or missing refusal")
			}
			if foreignRequests.Load() != 0 {
				t.Fatal("GitLab metadata redirect reached a foreign endpoint")
			}
		})
	}
	if _, err := New(t.Context(), Config{APIURL: "http://bad/api/v4", ProjectID: 1, JobToken: "x", ReadCredential: "x", ReadCredentialKind: "private-token"}); err == nil {
		t.Fatal("accepted non-HTTPS")
	}
}

func TestPaginationRefusesMissingOrInvalidNextPage(t *testing.T) {
	self := fixtureJob(3, "nvkr:finalize", "running")
	for _, next := range []string{"missing", "3", "not-a-page"} {
		t.Run(next, func(t *testing.T) {
			c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/job") {
					_ = json.NewEncoder(w).Encode(self)
					return
				}
				if next != "missing" {
					w.Header().Set("X-Next-Page", next)
				}
				_ = json.NewEncoder(w).Encode([]apiJob{self})
			})
			if _, err := c.Current(context.Background(), id(), []string{self.Name}); err == nil {
				t.Fatal("accepted incomplete pagination")
			}
		})
	}
}
func TestCurrentRejectsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) { t.Error("request after cancellation") })
	if _, err := c.Current(ctx, id(), []string{"nvkr:finalize"}); err == nil {
		t.Fatal("accepted canceled context")
	}
}
func TestCurrentRejectsForgedCurrentStatus(t *testing.T) {
	self := fixtureJob(3, "nvkr:finalize", "running")
	c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/job") {
			_ = json.NewEncoder(w).Encode(self)
			return
		}
		w.Header().Set("X-Next-Page", "")
		changed := self
		changed.Status = "success"
		_ = json.NewEncoder(w).Encode([]apiJob{changed})
	})
	if _, err := c.Current(context.Background(), id(), []string{self.Name}); err == nil {
		t.Fatal("accepted changed current status")
	}
}

func TestCurrentProjectAndPipelineAdmission(t *testing.T) {
	goodProject := `{"id":1,"ci_config_path":null}`
	goodPipeline := `{"id":2,"project_id":1,"sha":"` + testCommit + `","ref":"main","source":"push"}`
	cases := []struct {
		name, project, pipeline string
		valid                   bool
	}{
		{"null default", goodProject, goodPipeline, true},
		{"empty default", `{"id":1,"ci_config_path":""}`, goodPipeline, true},
		{"explicit default", `{"id":1,"ci_config_path":".gitlab-ci.yml"}`, goodPipeline, true},
		{"missing path", `{"id":1}`, goodPipeline, false},
		{"foreign path", `{"id":1,"ci_config_path":"other.yml"}`, goodPipeline, false},
		{"foreign project", `{"id":9,"ci_config_path":null}`, goodPipeline, false},
		{"missing pipeline source", goodProject, `{"id":2,"project_id":1,"sha":"` + testCommit + `","ref":"main"}`, false},
		{"wrong pipeline commit", goodProject, `{"id":2,"project_id":1,"sha":"` + strings.Repeat("b", 40) + `","ref":"main","source":"push"}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			self := fixtureJob(3, "nvkr:finalize", "running")
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v4/projects/1":
					_, _ = w.Write([]byte(tc.project))
				case "/api/v4/projects/1/pipelines/2":
					_, _ = w.Write([]byte(tc.pipeline))
				case "/api/v4/job":
					_ = json.NewEncoder(w).Encode(self)
				default:
					w.Header().Set("X-Next-Page", "")
					_ = json.NewEncoder(w).Encode([]apiJob{self})
				}
			}))
			defer srv.Close()
			c, err := newWithHTTPClient(t.Context(), Config{APIURL: srv.URL + "/api/v4", ProjectID: 1, JobToken: "j", ReadCredential: "r", ReadCredentialKind: "private-token"}, srv.Client())
			if err != nil {
				t.Fatal(err)
			}
			_, err = c.Current(context.Background(), id(), []string{self.Name})
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v err=%v", tc.valid, err)
			}
		})
	}
}

func TestZeroClientFailsClosed(t *testing.T) {
	if _, err := (&Client{}).Current(context.Background(), Identity{PipelineID: 2, JobID: 3, Commit: testCommit}, []string{"nvkr:finalize"}); err == nil {
		t.Fatal("zero client accepted")
	}
}

func TestClientCloseCancelsAndJoinsCurrent(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(started); <-r.Context().Done() }))
	defer server.Close()
	client, err := newWithHTTPClient(t.Context(), Config{APIURL: server.URL + "/api/v4", ProjectID: 1, JobToken: "synthetic-job", ReadCredential: "synthetic-read", ReadCredentialKind: "private-token"}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	parent, cancelParent := context.WithCancel(context.Background())
	defer cancelParent()
	finished := make(chan error, 1)
	go func() { _, err := client.Current(parent, id(), []string{"nvkr:finalize"}); finished <- err }()
	select {
	case <-started:
	case err := <-finished:
		t.Fatalf("lookup returned before reaching transport: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("lookup never reached transport")
	}
	closed := make(chan struct{})
	go func() { _ = client.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("Close failed to cancel and join the lookup")
	}
	select {
	case err := <-finished:
		var coded *e.Error
		if !errors.As(err, &coded) || coded.Code != e.CodeTimeout {
			t.Fatalf("canceled lookup error=%v; want %s", err, e.CodeTimeout)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("lookup did not finish after Close joined it")
	}
	if parent.Err() != nil {
		t.Fatal("Close canceled the caller context")
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	if client.jobToken != "" || client.readCredential != "" {
		t.Fatal("closed client retained credential references")
	}
	if _, err := client.Current(context.Background(), id(), []string{"nvkr:finalize"}); err == nil {
		t.Fatal("closed client admitted another lookup")
	}
}

func TestCurrentRegistryMetadataPresence(t *testing.T) {
	cases := []struct {
		name, project string
		want          ProjectRegistry
		valid         bool
	}{
		{"enabled", `{"id":1,"ci_config_path":null,"container_registry_enabled":true,"container_registry_image_prefix":"registry.example.test/group/project"}`, ProjectRegistry{Present: true, Enabled: true, Prefix: "registry.example.test/group/project"}, true},
		{"disabled", `{"id":1,"ci_config_path":null,"container_registry_enabled":false,"container_registry_image_prefix":"registry.example.test/group/project"}`, ProjectRegistry{Present: true, Enabled: false, Prefix: "registry.example.test/group/project"}, true},
		{"missing", `{"id":1,"ci_config_path":null}`, ProjectRegistry{}, true},
		{"null prefix", `{"id":1,"ci_config_path":null,"container_registry_enabled":true,"container_registry_image_prefix":null}`, ProjectRegistry{}, true},
		{"wrong type", `{"id":1,"ci_config_path":null,"container_registry_enabled":"yes","container_registry_image_prefix":"registry.example.test/group/project"}`, ProjectRegistry{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			self := fixtureJob(3, "nvkr:finalize", "running")
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v4/projects/1":
					_, _ = w.Write([]byte(tc.project))
				case "/api/v4/projects/1/pipelines/2":
					_, _ = w.Write([]byte(`{"id":2,"project_id":1,"sha":"` + testCommit + `","ref":"main","source":"push"}`))
				case "/api/v4/job":
					_ = json.NewEncoder(w).Encode(self)
				default:
					w.Header().Set("X-Next-Page", "")
					_ = json.NewEncoder(w).Encode([]apiJob{self})
				}
			}))
			defer srv.Close()
			client, err := newWithHTTPClient(t.Context(), Config{APIURL: srv.URL + "/api/v4", ProjectID: 1, JobToken: "j", ReadCredential: "r", ReadCredentialKind: "private-token"}, srv.Client())
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			receipt, err := client.Current(t.Context(), id(), []string{self.Name})
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v err=%v", tc.valid, err)
			}
			if err == nil && receipt.ProjectRegistry() != tc.want {
				t.Fatalf("registry metadata mismatch: %+v", receipt.ProjectRegistry())
			}
		})
	}
}

type trackedIdleTransport struct {
	http.RoundTripper
	closed atomic.Int64
}

func (t *trackedIdleTransport) CloseIdleConnections() { t.closed.Add(1) }
func TestClientCloseClosesOwnedTransport(t *testing.T) {
	transport := &trackedIdleTransport{RoundTripper: http.DefaultTransport}
	client, err := newWithHTTPClient(t.Context(), Config{APIURL: "https://gitlab.example.test/api/v4", ProjectID: 1, JobToken: "j", ReadCredential: "r", ReadCredentialKind: "private-token"}, &http.Client{Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	if transport.closed.Load() == 0 {
		t.Fatal("provider transport idle connections were not closed")
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
}

// fixtureValidServer serves one complete valid current-map exchange and counts
// every transport request, so tests can prove refusals happen before transport.
func fixtureValidServer(t *testing.T, requests *atomic.Int64) *httptest.Server {
	t.Helper()
	self := fixtureJob(3, "nvkr:finalize", "running")
	return httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		switch r.URL.Path {
		case "/api/v4/projects/1":
			_, _ = w.Write([]byte(`{"id":1,"ci_config_path":null}`))
		case "/api/v4/projects/1/pipelines/2":
			_, _ = w.Write([]byte(`{"id":2,"project_id":1,"sha":"` + testCommit + `","ref":"main","source":"push"}`))
		case "/api/v4/job":
			_ = json.NewEncoder(w).Encode(self)
		default:
			w.Header().Set("X-Next-Page", "")
			_ = json.NewEncoder(w).Encode([]apiJob{self})
		}
	}))
}

func TestClientConstructorRejectsUnusableCallerContext(t *testing.T) {
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	expired, cancelExpired := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer cancelExpired()
	for _, tc := range []struct {
		name string
		ctx  context.Context
		want e.Code
	}{
		{"nil", nil, e.CodeUsage},
		{"canceled", canceled, e.CodeTimeout},
		{"expired", expired, e.CodeTimeout},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, err := New(tc.ctx, Config{APIURL: "https://gitlab.example.test/api/v4", ProjectID: 1, JobToken: "j", ReadCredential: "r", ReadCredentialKind: "private-token"})
			if client != nil {
				_ = client.Close()
				t.Fatal("rejected constructor returned a usable client")
			}
			var coded *e.Error
			if err == nil || !errors.As(err, &coded) || coded.Code != tc.want {
				t.Fatalf("err = %v; want code %s", err, tc.want)
			}
		})
	}
	t.Run("live parent", func(t *testing.T) {
		client, err := New(t.Context(), Config{APIURL: "https://gitlab.example.test/api/v4", ProjectID: 1, JobToken: "j", ReadCredential: "r", ReadCredentialKind: "private-token"})
		if err != nil {
			t.Fatal(err)
		}
		if err := client.Close(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestCurrentRefusesCanceledClientLifetime(t *testing.T) {
	for _, tc := range []struct {
		name string
		// newParent creates the still-live parent context the client lifetime
		// derives from; the returned cancel belongs to the case.
		newParent func() (context.Context, context.CancelFunc)
		// expire drives the already-constructed parent to its real end state and
		// asserts the exact cause, so the refusal below observes a genuinely
		// expired lifetime rather than a locally simulated cancellation.
		expire func(t *testing.T, parent context.Context, cancel context.CancelFunc)
	}{
		{
			name:      "canceled parent",
			newParent: func() (context.Context, context.CancelFunc) { return context.WithCancel(t.Context()) },
			expire: func(t *testing.T, parent context.Context, cancel context.CancelFunc) {
				cancel()
				if !errors.Is(parent.Err(), context.Canceled) {
					t.Fatalf("parent.Err() = %v; want context.Canceled", parent.Err())
				}
			},
		},
		{
			name: "expired parent deadline",
			newParent: func() (context.Context, context.CancelFunc) {
				// The deadline is still in the future when the client is built.
				return context.WithDeadline(t.Context(), time.Now().Add(250*time.Millisecond))
			},
			expire: func(t *testing.T, parent context.Context, _ context.CancelFunc) {
				// Await the actual deadline expiry; cancellation is never called
				// to simulate it.
				select {
				case <-parent.Done():
				case <-time.After(5 * time.Second):
					t.Fatal("parent deadline never expired")
				}
				if !errors.Is(parent.Err(), context.DeadlineExceeded) {
					t.Fatalf("parent.Err() = %v; want context.DeadlineExceeded", parent.Err())
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests atomic.Int64
			server := fixtureValidServer(t, &requests)
			defer server.Close()
			parent, cancel := tc.newParent()
			defer cancel()
			// The client is constructed while the parent is still live.
			client, err := newWithHTTPClient(parent, Config{APIURL: server.URL + "/api/v4", ProjectID: 1, JobToken: "j", ReadCredential: "r", ReadCredentialKind: "private-token"}, server.Client())
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			tc.expire(t, parent, cancel)
			// The lookup runs on an independently still-live context, so only the
			// expired client lifetime can refuse the work.
			receipt, err := client.Current(context.Background(), id(), []string{"nvkr:finalize"})
			if receipt != nil {
				t.Fatal("expired lifetime returned a receipt")
			}
			var coded *e.Error
			if err == nil || !errors.As(err, &coded) || coded.Code != e.CodeTimeout {
				t.Fatalf("err = %v; want code %s", err, e.CodeTimeout)
			}
			if requests.Load() != 0 {
				t.Fatalf("expired lifetime reached transport %d time(s)", requests.Load())
			}
			if err := client.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestParentCancellationAbortsStartedLookup(t *testing.T) {
	var (
		// started is closed once the lookup reaches the transport handler.
		started = make(chan struct{})
		// handlerCanceled is closed only when the handler observed genuine
		// request-context cancellation, never via the cleanup fallback.
		handlerCanceled = make(chan struct{})
		// handlerDone is closed when the first handler exits on either path.
		handlerDone = make(chan struct{})
		// fallback releases a still-blocked handler during cleanup only, after
		// normal assertions had their bounded chance to observe real cancellation.
		fallback = make(chan struct{})
		// lookupErr carries the lookup result exactly once; lookupDone closes
		// after that send, giving cleanup a completion signal that never
		// consumes a result already taken by normal assertions.
		lookupErr  = make(chan error, 1)
		lookupDone = make(chan struct{})
		// clientClosed and clientCloseErr carry the single owned Close
		// goroutine's completion and its returned error, so no path discards
		// that error silently.
		clientClosed   = make(chan struct{})
		clientCloseErr = make(chan error, 1)
		// serverClosed is closed by the single owned server-shutdown goroutine
		// after httptest Close returns; that Close itself waits for every
		// outstanding handler, including late or duplicate entries.
		serverClosed = make(chan struct{})
	)
	var (
		entered       atomic.Int64
		lookupStarted atomic.Bool
	)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if entered.Add(1) != 1 {
			t.Error("server received a duplicate handler invocation")
			return
		}
		close(started)
		select {
		case <-r.Context().Done():
			close(handlerCanceled)
		case <-fallback:
			// Cleanup-only release: genuine cancellation was not observed, so
			// cleanup unblocks the handler instead of manufacturing a pass.
		}
		close(handlerDone)
	}))
	parent, cancelParent := context.WithCancel(t.Context())
	// The lookup keeps an independent, still-live call context; only the client
	// lifetime derived from the parent must abort the already-started request.
	callCtx, cancelCall := context.WithCancel(context.Background())
	releaseFallback := sync.OnceFunc(func() { close(fallback) })
	// client is assigned only after construction succeeds, so the single owned
	// Close start must tolerate the constructor-error path where it is nil.
	var client *Client
	startClose := sync.OnceFunc(func() {
		if client == nil {
			close(clientClosed)
			return
		}
		go func() {
			err := client.Close()
			clientCloseErr <- err
			close(clientClosed)
		}()
	})
	// Server shutdown owns its own completion signal. httptest Close blocks
	// until every outstanding handler exits, so it runs in exactly one owned
	// goroutine and every caller joins serverClosed with a bound instead of
	// following a timeout with an unbounded synchronous Close. Defining it
	// here, before any client work, lets the constructor-error path reuse the
	// same safe cleanup.
	startServerClose := sync.OnceFunc(func() {
		go func() {
			server.Close()
			close(serverClosed)
		}()
	})
	// One coherent cleanup path registered before any client or lookup work
	// starts, so the constructor-error path uses the same safe cleanup. It owns
	// cancellation, the fallback release, the single Close start, bounded joins
	// and the server shutdown after handlers are released, so every failure
	// path (including Goexit from t.Fatal) is joined without an unbounded
	// synchronous server.Close. Each wait is bounded and its timeout is
	// reported as a failure, never converted into a successful join.
	boundedJoin := func(name string, done chan struct{}) {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Errorf("cleanup: %s did not complete within bound", name)
		}
	}
	t.Cleanup(func() {
		cancelParent()
		cancelCall()
		releaseFallback()
		startClose()
		// The lookup goroutine is joined only when it actually started; the
		// constructor-error path never launched one and must not wait.
		if lookupStarted.Load() {
			boundedJoin("lookup goroutine", lookupDone)
		}
		boundedJoin("client Close", clientClosed)
		// entered is read after the fallback release: a handler that entered
		// earlier is joined here, and one entering later is released by the
		// closed fallback and completes before the bounded server shutdown
		// below returns. entered==0 alone never proves a late handler cannot
		// start, so late and duplicate entries stay covered by that shutdown.
		if entered.Load() != 0 {
			boundedJoin("request handler", handlerDone)
		}
		// Drain an unconsumed lookup result without disturbing the normal path.
		select {
		case err := <-lookupErr:
			if err != nil {
				t.Errorf("cleanup: unconsumed lookup error: %v", err)
			}
		default:
		}
		// Surface a Close error the normal path never consumed.
		select {
		case err := <-clientCloseErr:
			if err != nil {
				t.Errorf("cleanup: client Close failed: %v", err)
			}
		default:
		}
		// Handlers are released, so the owned server shutdown cannot block
		// indefinitely; its completion is still bounded and reported.
		startServerClose()
		boundedJoin("server shutdown", serverClosed)
	})
	client, err := newWithHTTPClient(parent, Config{APIURL: server.URL + "/api/v4", ProjectID: 1, JobToken: "j", ReadCredential: "r", ReadCredentialKind: "private-token"}, server.Client())
	if err != nil {
		// Cleanup owns cancellation, the fallback, the nil-safe Close start
		// and the bounded server shutdown on this path.
		t.Fatal(err)
	}
	lookupStarted.Store(true)
	go func() {
		_, err := client.Current(callCtx, id(), []string{"nvkr:finalize"})
		lookupErr <- err
		close(lookupDone)
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("lookup never reached transport")
	}
	cancelParent()
	select {
	case err := <-lookupErr:
		var coded *e.Error
		if err == nil || !errors.As(err, &coded) || coded.Code != e.CodeTimeout {
			t.Fatalf("err = %v; want code %s", err, e.CodeTimeout)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("parent cancellation did not abort the started lookup")
	}
	// Genuine request cancellation must be observed before the cleanup-only
	// fallback is ever released.
	select {
	case <-handlerCanceled:
	case <-time.After(5 * time.Second):
		t.Fatal("transport handler never observed the abort")
	}
	startClose()
	select {
	case <-clientClosed:
	case <-time.After(5 * time.Second):
		t.Fatal("Close failed to join after parent cancellation")
	}
	if err := <-clientCloseErr; err != nil {
		t.Fatalf("client Close failed: %v", err)
	}
	select {
	case <-handlerDone:
	case <-time.After(5 * time.Second):
		t.Fatal("transport handler never completed")
	}
	if callCtx.Err() != nil {
		t.Fatal("client canceled the independent call context")
	}
	if client.jobToken != "" || client.readCredential != "" {
		t.Fatal("closed client retained credential references")
	}
}

func TestCloseNeverCancelsCallerParent(t *testing.T) {
	parent := t.Context()
	client, err := newWithHTTPClient(parent, Config{APIURL: "https://gitlab.example.test/api/v4", ProjectID: 1, JobToken: "j", ReadCredential: "r", ReadCredentialKind: "private-token"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	if parent.Err() != nil {
		t.Fatal("Close canceled its caller parent context")
	}
}

func TestLookupCancellationDoesNotPoisonClient(t *testing.T) {
	c := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/job") {
			_ = json.NewEncoder(w).Encode(fixtureJob(3, "nvkr:finalize", "running"))
			return
		}
		w.Header().Set("X-Next-Page", "")
		_ = json.NewEncoder(w).Encode([]apiJob{fixtureJob(3, "nvkr:finalize", "running")})
	})
	defer func() { _ = c.Close() }()
	callCtx, cancelCall := context.WithCancel(context.Background())
	cancelCall()
	_, err := c.Current(callCtx, id(), []string{"nvkr:finalize"})
	var coded *e.Error
	if err == nil || !errors.As(err, &coded) || coded.Code != e.CodeTimeout {
		t.Fatalf("err = %v; want code %s", err, e.CodeTimeout)
	}
	// The parent lifetime stayed live, so a later valid lookup still succeeds.
	receipt, err := c.Current(context.Background(), id(), []string{"nvkr:finalize"})
	if err != nil {
		t.Fatal(err)
	}
	if receipt == nil || receipt.CurrentIdentity().JobID != 3 {
		t.Fatal("client poisoned by an earlier canceled lookup")
	}
}
