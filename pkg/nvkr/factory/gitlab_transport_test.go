// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/transportarchive"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	provider "github.com/dsx-ai-factory/nvkr/pkg/nvkr/gitlab"
)

func TestGitLabProducerOutcomeExactCurrentMembership(t *testing.T) {
	_, plan := gitlabFixture(t, 1)
	node := plan.Graph.Nodes[0]
	for _, candidate := range plan.Graph.Nodes {
		if len(candidate.Inputs) > 0 && len(candidate.Outputs) > 0 {
			node = candidate
			break
		}
	}
	identity := provider.Identity{APIURL: plan.Environment.GitLab.APIURL, ProjectID: 123, PipelineID: 2, JobID: 3, Commit: strings.Repeat("a", 40), Ref: "main", Source: "push"}
	digest, err := plan.Digest()
	if err != nil {
		t.Fatal(err)
	}
	good := gitlabOutcome{Ref: "main", Source: "push", Schema: "nvkr.dev/gitlab-operation/v1", APIURL: identity.APIURL, ProjectID: 123, PipelineID: 2, JobID: 9, Commit: identity.Commit, Plan: digest, Node: node.ID, Status: "success", Carriers: []gitlabCarrier{}}
	for _, id := range node.Outputs {
		good.Carriers = append(good.Carriers, gitlabCarrier{Artifact: id, Digest: "sha256:" + strings.Repeat("b", 64), Bytes: 1024})
	}
	nodes, artifacts := planIndex(plan)
	jobs := map[string]provider.Job{}
	for index, id := range node.Inputs {
		name := gitlabNodeJob(artifacts[id].Producer)
		job, ok := jobs[name]
		if !ok {
			job = provider.Job{ID: uint64(100 + index), Name: name, Status: "success"}
			jobs[name] = job
		}
		good.Inputs = append(good.Inputs, gitlabInput{Artifact: id, ProducerJobID: job.ID})
	}
	cases := []struct {
		name   string
		change func(*gitlabOutcome)
		valid  bool
	}{
		{"success", func(*gitlabOutcome) {}, true},
		{"failed", func(o *gitlabOutcome) { o.Status = "failed"; o.ExitCode = 1 }, false},
		{"foreign API", func(o *gitlabOutcome) { o.APIURL = "https://foreign.example/api/v4" }, false},
		{"foreign project", func(o *gitlabOutcome) { o.ProjectID++ }, false},
		{"foreign pipeline", func(o *gitlabOutcome) { o.PipelineID++ }, false},
		{"previous retry job", func(o *gitlabOutcome) { o.JobID-- }, false},
		{"wrong ref", func(o *gitlabOutcome) { o.Ref = "feature" }, false},
		{"wrong pipeline source", func(o *gitlabOutcome) { o.Source = "api" }, false},
		{"missing invocation", func(o *gitlabOutcome) { o.Ref = ""; o.Source = "" }, false},
		{"wrong source", func(o *gitlabOutcome) { o.Commit = strings.Repeat("c", 40) }, false},
		{"wrong Plan", func(o *gitlabOutcome) { o.Plan = "sha256:" + strings.Repeat("d", 64) }, false},
		{"wrong node", func(o *gitlabOutcome) { o.Node = "other" }, false},
		{"missing carrier", func(o *gitlabOutcome) { o.Carriers = o.Carriers[:len(o.Carriers)-1] }, false},
		{"wrong output", func(o *gitlabOutcome) { o.Carriers[0].Artifact = "other" }, false},
		{"unbounded bytes", func(o *gitlabOutcome) { o.Carriers[0].Bytes = transportarchive.MaxArchiveBytes + 1 }, false},
		{"missing original input", func(o *gitlabOutcome) { o.Inputs = nil }, false},
		{"wrong input artifact", func(o *gitlabOutcome) { o.Inputs[0].Artifact = "other" }, false},
		{"superseded input identical bytes", func(o *gitlabOutcome) { o.Inputs[0].ProducerJobID-- }, false},
		{"untyped digest", func(o *gitlabOutcome) { o.Carriers[0].Digest = strings.Repeat("b", 64) }, false},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			outcome := good
			outcome.Carriers = append([]gitlabCarrier(nil), good.Carriers...)
			outcome.Inputs = append([]gitlabInput(nil), good.Inputs...)
			test.change(&outcome)
			raw, err := canonical.Marshal(outcome)
			if err != nil {
				t.Fatal(err)
			}
			_, err = validateGitLabProducerOutcome(raw, identity, digest, 9, node.ID, nodes, artifacts, jobs)
			if (err == nil) != test.valid {
				t.Fatalf("outcome validation=%v", err)
			}
		})
	}
	if _, err := validateGitLabProducerOutcome([]byte("{"), identity, digest, 9, node.ID, nodes, artifacts, jobs); err == nil {
		t.Fatal("corrupt outcome accepted")
	}
}

func TestGitLabSharedSourceVendorCarrierFanout(t *testing.T) {
	producer := t.TempDir()
	files := map[string]string{".nvkr/work/source/component/source.go": "package selected\n", ".nvkr/work/source/component/vendor/module/data": "locked vendor bytes\n", ".nvkr/work/source/component/receipt": "original producer source receipt\n"}
	for name, body := range files {
		path := filepath.Join(producer, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	carrier := filepath.Join(producer, ".nvkr/gitlab/source.tar")
	if err := transportarchive.Pack(t.Context(), carrier, producer, []string{".nvkr/work/source/component"}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		consumer := t.TempDir()
		if err := transportarchive.Unpack(t.Context(), carrier, consumer, []string{".nvkr/work/source/component"}); err != nil {
			t.Fatal(err)
		}
		for name, want := range files {
			raw, err := os.ReadFile(filepath.Join(consumer, name))
			if err != nil || string(raw) != want {
				t.Fatalf("source/vendor fanout changed %s: %v", name, err)
			}
		}
	}
}

func TestGitLabNativeArtifactTransport(t *testing.T) {
	for _, mode := range []string{"success", "signed-storage", "corrupt", "oversize", "redirect", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			canceled := make(chan struct{})
			var insecureRequests atomic.Int64
			insecure := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { insecureRequests.Add(1) }))
			defer insecure.Close()
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/storage" {
					if r.Header.Get("PRIVATE-TOKEN") != "" || r.Header.Get("JOB-TOKEN") != "" || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
						t.Error("storage received API credentials")
					}
					_, _ = w.Write([]byte("original native carrier bytes"))
					return
				}
				if r.Header.Get("PRIVATE-TOKEN") != "synthetic-read" || r.Header.Get("JOB-TOKEN") != "" {
					t.Error("artifact used wrong scoped credential")
				}
				if !strings.HasPrefix(r.URL.Path, "/api/v4/projects/123/jobs/9/artifacts/.nvkr/") {
					t.Error("artifact scope changed")
				}
				if mode == "signed-storage" {
					http.Redirect(w, r, "https://"+r.Host+"/storage?synthetic-signed-location=yes", http.StatusFound)
					return
				}
				if mode == "redirect" {
					http.Redirect(w, r, insecure.URL+"/should-not-receive-credential", http.StatusFound)
					return
				}
				if mode == "timeout" {
					if err := os.WriteFile(filepath.Join(root, "handler-entered"), nil, 0600); err != nil {
						t.Error(err)
					}
					<-r.Context().Done()
					close(canceled)
					return
				}
				_, _ = w.Write([]byte("original native carrier bytes"))
			}))
			defer server.Close()
			certificate := filepath.Join(root, "ca.pem")
			if err := os.WriteFile(certificate, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
				t.Fatal(err)
			}
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			child := exec.CommandContext(t.Context(), executable, "-test.run=^TestGitLabNativeArtifactTransportChild$", "-test.count=1")
			child.Env = append(os.Environ(), "NVKR_GITLAB_TRANSPORT_CHILD="+mode, "NVKR_GITLAB_TEST_API="+server.URL+"/api/v4", "NVKR_GITLAB_TEST_CA="+certificate, "NVKR_GITLAB_TEST_ROOT="+root, "GODEBUG=x509usefallbackroots=1")
			raw, err := child.CombinedOutput()
			if err != nil {
				t.Fatalf("native HTTPS fixture: %v\n%s", err, raw)
			}
			if insecureRequests.Load() != 0 {
				t.Fatal("insecure redirect reached the destination")
			}
			if mode == "timeout" {
				select {
				case <-canceled:
				case <-time.After(5 * time.Second):
					t.Fatal("active artifact handler did not observe cancellation")
				}
			}
		})
	}
}
func TestGitLabNativeArtifactTransportChild(t *testing.T) {
	mode := os.Getenv("NVKR_GITLAB_TRANSPORT_CHILD")
	if mode == "" {
		return
	}
	raw, err := os.ReadFile(os.Getenv("NVKR_GITLAB_TEST_CA"))
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(raw) {
		t.Fatal("fixture CA malformed")
	}
	x509.SetFallbackRoots(pool)
	t.Chdir(os.Getenv("NVKR_GITLAB_TEST_ROOT"))
	state := &gitlabAdmission{defaultBranch: "main", pipelineSources: []string{"push", "web"}, config: provider.Config{APIURL: os.Getenv("NVKR_GITLAB_TEST_API"), ProjectID: 123, ReadCredentialKind: "private-token", ReadCredential: "synthetic-read", CertificateAuthority: string(raw)}}
	payload := []byte("original native carrier bytes")
	digest := sha256.Sum256(payload)
	item := gitlabCarrier{Artifact: "source", Digest: "sha256:" + hex.EncodeToString(digest[:]), Bytes: int64(len(payload))}
	if mode == "corrupt" {
		item.Digest = "sha256:" + strings.Repeat("f", 64)
	}
	if mode == "oversize" {
		item.Bytes--
	}
	ctx := t.Context()
	originalTransport := http.DefaultTransport
	poison := &gitlabPoisonTransport{}
	http.DefaultTransport = poison
	defer func() { http.DefaultTransport = originalTransport }()
	path := ".nvkr/gitlab/carriers/test/carrier.tar"
	if mode == "timeout" {
		bounded, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		finished := make(chan error, 1)
		go func() { finished <- state.download(bounded, 9, path, item) }()
		ticker := time.NewTicker(5 * time.Millisecond)
		defer ticker.Stop()
		for {
			if _, statErr := os.Stat("handler-entered"); statErr == nil {
				break
			}
			select {
			case err := <-finished:
				t.Fatalf("transport returned before active handler cancellation: %v", err)
			case <-bounded.Done():
				cancel()
				<-finished
				t.Fatal("artifact request never entered handler")
			case <-ticker.C:
			}
		}
		cancel()
		err = <-finished
		assertFactoryErrorCode(t, err, e.CodeTimeout)
	} else {
		err = state.download(ctx, 9, path, item)
	}
	if poison.calls.Load() != 0 {
		t.Fatal("production artifact owner used mutable global HTTP transport")
	}
	if mode == "success" || mode == "signed-storage" {
		if err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(path)
		if err != nil || string(got) != string(payload) {
			t.Fatal("download changed original bytes")
		}
	} else {
		if err == nil {
			t.Fatal("invalid artifact transport accepted")
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatal("failed acquisition left published carrier")
		}
	}
}

type gitlabPoisonTransport struct{ calls atomic.Int64 }

func (transport *gitlabPoisonTransport) RoundTrip(*http.Request) (*http.Response, error) {
	transport.calls.Add(1)
	return nil, fmt.Errorf("synthetic mutable transport intercepted request")
}
