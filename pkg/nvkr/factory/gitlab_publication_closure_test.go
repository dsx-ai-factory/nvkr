// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	provider "github.com/dsx-ai-factory/nvkr/pkg/nvkr/gitlab"
	"github.com/google/go-containerregistry/pkg/registry"
)

// The native Push/Pull still runs in the positive case. A superseded original
// producer must instead stop after local inspection, before any registry request.
func TestGitLabPublicationRevalidatesBeforeRegistryWrite(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "localhost"}, DNSNames: []string{"localhost"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.2")}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IsCA: true, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"unchanged", "superseded"} {
		t.Run(mode, func(t *testing.T) {
			var requests, writes, refreshes atomic.Int64
			reg := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				job := func(id uint64, name, status string) map[string]any {
					return map[string]any{"id": id, "name": name, "status": status, "ref": "main", "commit": map[string]any{"id": strings.Repeat("a", 40)}, "pipeline": map[string]any{"id": 2, "project_id": 123, "sha": strings.Repeat("a", 40), "ref": "main", "source": "push"}}
				}
				switch r.URL.Path {
				case "/api/v4/projects/123":
					refreshes.Add(1)
					_, _ = w.Write([]byte(`{"id":123,"ci_config_path":null}`))
				case "/api/v4/projects/123/pipelines/2":
					_, _ = w.Write([]byte(`{"id":2,"project_id":123,"sha":"` + strings.Repeat("a", 40) + `","ref":"main","source":"push"}`))
				case "/api/v4/job":
					_ = json.NewEncoder(w).Encode(job(3, gitlabFinalizerJob, "running"))
				case "/api/v4/projects/123/pipelines/2/jobs":
					w.Header().Set("X-Next-Page", "")
					original := uint64(9)
					if mode == "superseded" {
						original++
					}
					_ = json.NewEncoder(w).Encode([]map[string]any{job(3, gitlabFinalizerJob, "running"), job(original, "nvkr:original", "success")})
				default:
					requests.Add(1)
					if r.Method == http.MethodPut || r.Method == http.MethodPost || r.Method == http.MethodPatch {
						writes.Add(1)
					}
					reg.ServeHTTP(w, r)
				}
			}))
			host := "localhost."
			if runtime.GOOS == "linux" {
				listener, err := net.Listen("tcp", "127.0.0.2:0")
				if err != nil {
					t.Fatal(err)
				}
				if err := server.Listener.Close(); err != nil {
					t.Fatal(err)
				}
				server.Listener = listener
				host = "127.0.0.2"
			}
			server.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}, MinVersion: tls.VersionTLS12}
			server.StartTLS()
			defer server.Close()
			_, port, err := net.SplitHostPort(server.Listener.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "ca.pem"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
				t.Fatal(err)
			}
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			child := exec.CommandContext(t.Context(), executable, "-test.run=^TestGitLabPublicationClosureChild$", "-test.count=1")
			child.Env = append(os.Environ(), "NVKR_GITLAB_PUBLICATION_CLOSURE="+mode, "NVKR_GITLAB_TEST_ROOT="+root, "NVKR_GITLAB_TEST_ORIGIN=https://"+host+":"+port, "GODEBUG=x509usefallbackroots=1", "NO_PROXY=localhost.,localhost,127.0.0.2", "no_proxy=localhost.,localhost,127.0.0.2")
			if output, err := child.CombinedOutput(); err != nil {
				t.Fatalf("publication closure fixture: %v\n%s", err, output)
			}
			if refreshes.Load() != 1 {
				t.Fatalf("pre-publication refreshes=%d, want one", refreshes.Load())
			}
			if mode == "unchanged" && writes.Load() == 0 {
				t.Fatal("unchanged original closure did not publish")
			}
			if mode == "superseded" && requests.Load() != 0 {
				t.Fatal("superseded original closure reached the registry")
			}
		})
	}
}

func TestGitLabPublicationClosureChild(t *testing.T) {
	mode := os.Getenv("NVKR_GITLAB_PUBLICATION_CLOSURE")
	if mode == "" {
		return
	}
	t.Chdir(os.Getenv("NVKR_GITLAB_TEST_ROOT"))
	raw, err := os.ReadFile("ca.pem")
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(raw) {
		t.Fatal("invalid fixture CA")
	}
	x509.SetFallbackRoots(pool)
	origin := os.Getenv("NVKR_GITLAB_TEST_ORIGIN")
	config := provider.Config{APIURL: origin + "/api/v4", ProjectID: 123, JobToken: "synthetic-job", ReadCredential: "synthetic-read", ReadCredentialKind: "private-token"}
	client, err := provider.New(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	state := &gitlabAdmission{client: client, names: []string{gitlabFinalizerJob, "nvkr:original"}, identity: provider.Identity{APIURL: config.APIURL, ProjectID: 123, PipelineID: 2, JobID: 3, Commit: strings.Repeat("a", 40)}, acquired: map[string]provider.Job{"nvkr:original": {ID: 9, Name: "nvkr:original", Status: "success"}}}
	plan, artifacts := gitlabCompiledPublicationFixture(t, strings.TrimPrefix(origin, "https://")+"/fixture")
	node := planNode(plan, "publish/release")
	err = executeGitLabPublication(t.Context(), plan, node, artifacts, state)
	if mode == "superseded" {
		assertFactoryErrorCode(t, err, e.CodeProvenanceMissing)
		if _, err := os.Stat(artifacts[node.Outputs[0]].Path); !os.IsNotExist(err) {
			t.Fatal("superseded publication committed its reference")
		}
	} else if err != nil {
		t.Fatal(err)
	}
}
