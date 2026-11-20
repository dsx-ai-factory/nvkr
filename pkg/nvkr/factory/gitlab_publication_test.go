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
	"encoding/pem"
	"errors"
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

	"github.com/dsx-ai-factory/nvkr/internal/readbudget"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"
)

type gitlabPublicationTrapTransport struct{ calls atomic.Int64 }

func (trap *gitlabPublicationTrapTransport) RoundTrip(*http.Request) (*http.Response, error) {
	trap.calls.Add(1)
	return nil, errors.New("mutable global transport was used")
}

// A loopback TLS registry exercises the production transport, native Push and
// complete Pull, including a failed readback after a successful push.
func TestGitLabNativePublicationHTTPS(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "localhost"}, DNSNames: []string{"localhost"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.2")}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IsCA: true, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert := tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
	for _, mode := range []string{"success", "readback-failure", "insecure-redirect", "response-budget"} {
		t.Run(mode, func(t *testing.T) {
			var deny atomic.Bool
			var deniedReadback atomic.Bool
			var requests atomic.Int64
			reg := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.Header.Get("Authorization") != "" {
					t.Error("anonymous publication sent authorization")
				}
				if mode == "insecure-redirect" {
					http.Redirect(w, r, "http://"+r.Host+"/insecure", http.StatusFound)
					return
				}
				if mode == "readback-failure" && deny.Load() && r.Method == http.MethodGet {
					deniedReadback.Store(true)
					http.Error(w, "SYNTHETIC_PRIVATE_CANARY", http.StatusServiceUnavailable)
					return
				}
				if mode == "readback-failure" && r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/fixture/releases/manifests/") {
					recorded := httptest.NewRecorder()
					reg.ServeHTTP(recorded, r)
					for key, values := range recorded.Header() {
						w.Header()[key] = values
					}
					if recorded.Code >= 200 && recorded.Code < 300 {
						deny.Store(true)
					}
					w.WriteHeader(recorded.Code)
					_, _ = w.Write(recorded.Body.Bytes())
					return
				}
				reg.ServeHTTP(w, r)
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
			server.TLS = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
			server.StartTLS()
			defer server.Close()
			_, port, err := net.SplitHostPort(server.Listener.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			ca := filepath.Join(dir, "ca.pem")
			if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
				t.Fatal(err)
			}
			binary, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			child := exec.CommandContext(t.Context(), binary, "-test.run=^TestGitLabNativePublicationHTTPSChild$", "-test.count=1")
			child.Env = append(os.Environ(), "NVKR_GITLAB_PUBLICATION_TEST="+mode, "NVKR_GITLAB_PUBLICATION_CA="+ca, "NVKR_GITLAB_PUBLICATION_DIR="+dir, "NVKR_GITLAB_PUBLICATION_REPO="+host+":"+port+"/fixture/releases", "GODEBUG=x509usefallbackroots=1", "NO_PROXY=localhost.,localhost,127.0.0.1,127.0.0.2", "no_proxy=localhost.,localhost,127.0.0.1,127.0.0.2")
			if output, err := child.CombinedOutput(); err != nil {
				t.Fatalf("native TLS publication fixture: %v\n%s", err, output)
			}
			if mode == "readback-failure" && (!deny.Load() || !deniedReadback.Load()) {
				t.Fatal("fixture did not publish the release root then reject its readback")
			}
			if requests.Load() == 0 {
				t.Fatal("fixture did not reach its TLS registry")
			}
		})
	}
}

func TestGitLabNativePublicationHTTPSChild(t *testing.T) {
	mode := os.Getenv("NVKR_GITLAB_PUBLICATION_TEST")
	if mode == "" {
		return
	}
	raw, err := os.ReadFile(os.Getenv("NVKR_GITLAB_PUBLICATION_CA"))
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(raw) {
		t.Fatal("invalid fixture CA")
	}
	x509.SetFallbackRoots(pool)
	trap := &gitlabPublicationTrapTransport{}
	remote.DefaultTransport = trap
	http.DefaultTransport = trap
	dir := os.Getenv("NVKR_GITLAB_PUBLICATION_DIR")
	t.Chdir(dir)
	repo := os.Getenv("NVKR_GITLAB_PUBLICATION_REPO")
	plan, artifacts := gitlabCompiledPublicationFixture(t, strings.TrimSuffix(repo, "/releases"))
	node := planNode(plan, "publish/release")
	inspection, err := delivery.Inspect(t.Context(), artifacts["package/release"].Path)
	if err != nil {
		t.Fatal(err)
	}
	output := artifacts[node.Outputs[0]].Path
	ctx := t.Context()
	var budget *readbudget.Budget
	if mode == "response-budget" {
		ctx, budget, err = readbudget.WithLimit(ctx, 1)
		if err != nil {
			t.Fatal(err)
		}
	}
	err = executeGitLabPublication(ctx, plan, node, artifacts, nil)
	if trap.calls.Load() != 0 {
		t.Fatal("publication used a process-mutable default transport")
	}
	if budget != nil {
		if budget.Check() == nil || budget.Used() != 1 {
			t.Fatalf("isolated registry responses escaped their operation budget: used=%d error=%v", budget.Used(), budget.Check())
		}
		assertFactoryErrorCode(t, budget.Check(), e.CodeEngineCapability)
		assertFactoryErrorCode(t, err, e.CodeUnavailable)
	}
	if mode == "success" {
		if err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(output)
		if err != nil || string(got) != repo+"@"+inspection.RootDigest+"\n" {
			t.Fatalf("reference %q: %v", got, err)
		}
	} else if err == nil {
		t.Fatal("failed native readback accepted")
	} else {
		if strings.Contains(err.Error(), "SYNTHETIC_PRIVATE_CANARY") {
			t.Fatal("registry response escaped into public error")
		}
		if mode == "readback-failure" {
			var response *transport.Error
			if !errors.As(err, &response) {
				t.Fatal("typed registry cause lost")
			}
		}
		if _, err := os.Stat(output); !os.IsNotExist(err) {
			t.Fatalf("failed readback wrote output: %v", err)
		}
	}
}

func TestGitLabPublicationRejectsUnsupportedBeforeIO(t *testing.T) {
	safe := Target{Kind: TargetOCI, Ref: "registry.example.com/nvkr/releases", Anonymous: true}
	cases := []struct {
		name   string
		target Target
		node   Node
	}{
		{"valid shape invalid layout", safe, Node{ID: "publish/release", Target: "release", Inputs: []string{"in"}, Outputs: []string{"out"}}},
		{"private", Target{Kind: TargetOCI, Ref: safe.Ref, CredentialRef: "secret"}, Node{ID: "publish/release", Target: "release", Inputs: []string{"in"}, Outputs: []string{"out"}}},
		{"plain http", Target{Kind: TargetOCI, Ref: safe.Ref, Anonymous: true, PlainHTTP: true}, Node{ID: "publish/release", Target: "release", Inputs: []string{"in"}, Outputs: []string{"out"}}},
		{"implicit insecure localhost", Target{Kind: TargetOCI, Ref: "localhost:5000/nvkr/releases", Anonymous: true}, Node{ID: "publish/release", Target: "release", Inputs: []string{"in"}, Outputs: []string{"out"}}},
		{"aliases", Target{Kind: TargetOCI, Ref: safe.Ref, Anonymous: true, ImageTags: map[string]string{"image": "latest"}}, Node{ID: "publish/release", Target: "release", Inputs: []string{"in"}, Outputs: []string{"out"}}},
		{"other node", safe, Node{ID: "publish/other", Target: "release", Inputs: []string{"in"}, Outputs: []string{"out"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			output := filepath.Join(dir, "reference")
			err := executeGitLabPublication(t.Context(), &Plan{Targets: map[string]Target{"release": tc.target}}, tc.node, map[string]Artifact{"in": {Path: filepath.Join(dir, "absent")}, "out": {Path: output}}, nil)
			if tc.name != "valid shape invalid layout" {
				code := e.CodeEngineCapability
				if tc.name == "other node" {
					code = e.CodePipelineDrift
				}
				assertFactoryErrorCode(t, err, code)
			} else if err == nil {
				t.Fatal("missing publication input accepted")
			}
			if _, err := os.Stat(output); !os.IsNotExist(err) {
				t.Fatalf("rejected operation produced output: %v", err)
			}
		})
	}
}
