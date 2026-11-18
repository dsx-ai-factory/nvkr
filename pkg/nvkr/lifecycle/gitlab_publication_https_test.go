// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"archive/tar"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"io"
	"io/fs"
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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/transportarchive"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/assemble"
	authored "github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
	"github.com/google/go-containerregistry/pkg/registry"
	"sigs.k8s.io/yaml"
)

func prepareGitLabPublishedClosure(t *testing.T, root, api, prefix string, authenticated bool) *factory.Plan {
	t.Helper()
	def, env := testDefinition(t), testEnvironment()
	if err := os.CopyFS(filepath.Join(root, "catalog"), os.DirFS(def.Catalog.Base)); err != nil {
		t.Fatal(err)
	}
	registryPath := filepath.Join(root, "catalog", "registry.yaml")
	raw, err := os.ReadFile(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	var catalog map[string]any
	if err := yaml.Unmarshal(raw, &catalog); err != nil {
		t.Fatal(err)
	}
	components := catalog["components"].(map[string]any)
	original := components["containerd"].(map[string]any)
	chart := map[string]any{}
	for k, v := range original {
		chart[k] = v
	}
	chart["outputType"] = "tarball"
	chart["artifactPath"] = "bin/chart.tgz"
	chart["writeDir"] = "bin"
	chart["buildCommand"] = []string{"tar", "-czf", "/src/bin/chart.tgz", "chart"}
	chart["helm"] = map[string]any{"chart": ".", "images": map[string]any{"controller": map[string]any{"values": map[string]any{"digest": "image.digest"}}}}
	components["chart-source"] = chart
	raw, err = yaml.Marshal(catalog)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(registryPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	def.Catalog.Base = "catalog"
	def.Outputs = []DefinitionOutput{
		{Name: "chart", Platform: "linux/amd64", Format: OutputHelm, Components: []string{"chart-source"}, Helm: &authored.HelmOutput{Images: map[string]string{"controller": "image"}}},
		{Name: "cli", Platform: "linux/amd64", Format: OutputExecutable, Components: []string{"containerd"}},
		{Name: "image", Platform: "linux/amd64", Format: OutputOCI, Components: []string{"coredns"}},
	}
	env.UtilityPlatform = "linux/" + runtime.GOARCH
	env.Publication = &Publication{RepositoryPrefix: prefix, Anonymous: true}
	if authenticated {
		env.Publication = &Publication{RepositoryPrefix: prefix, CredentialRef: "gitlab:CI_JOB_TOKEN"}
	}
	runners := map[string]authored.GitLabRunner{}
	for _, arch := range []string{"amd64", "arm64"} {
		runners["linux/"+arch] = authored.GitLabRunner{Tags: []string{"fixture"}, BootstrapImage: "registry.example.test/go@sha256:" + strings.Repeat("a", 64)}
	}
	env.GitLab = &authored.GitLab{APIURL: api, ProjectID: 7, APIReadCredentialRef: "gitlab:NVKR_API_READ", APIReadCredentialKind: "private-token", Runners: runners}
	writeCanonicalTestFile(t, "definition.json", def)
	writeCanonicalTestFile(t, "environment.json", env)
	plan, err := CompileFiles(t.Context(), "definition.json", "environment.json", "test (deadbeef)")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := factory.Generate(t.Context(), plan, factory.EngineGitLab, root, false); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(".gitlab-ci.yml", []byte("include:\n  - local: .gitlab/nvkr-run/pipeline.yml\n"), 0600); err != nil {
		t.Fatal(err)
	}
	// Synthetic producer outputs feed real observation and package operations.
	for _, node := range plan.Graph.Nodes {
		if node.Kind != factory.NodeFetch && node.Kind != factory.NodeBuild && node.Kind != factory.NodeAcquire {
			continue
		}
		for _, id := range node.Outputs {
			a := gitLabArtifact(t, plan, id)
			if a.Kind == "producer-result" {
				continue
			}
			if err := os.MkdirAll(filepath.Dir(a.Path), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(a.Path, []byte("fixture "+id), 0755); err != nil {
				t.Fatal(err)
			}
		}
	}
	work := t.TempDir()
	binary := filepath.Join(work, "worker")
	if err := os.WriteFile(binary, []byte("synthetic worker"), 0755); err != nil {
		t.Fatal(err)
	}
	layout := filepath.Join(work, "image")
	if _, err := assemble.OCIImage(binary, "worker", 1, "amd64", layout); err != nil {
		t.Fatal(err)
	}
	target := gitLabArtifact(t, plan, "artifact/image/coredns").Path
	file, err := os.Create(target)
	if err != nil {
		t.Fatal(err)
	}
	writer := tar.NewWriter(file)
	err = filepath.WalkDir(layout, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		name, err := filepath.Rel(layout, path)
		if err != nil {
			return err
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := writer.WriteHeader(&tar.Header{Name: filepath.ToSlash(name), Mode: 0644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			return err
		}
		_, err = writer.Write(body)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	chartRoot := filepath.Join(work, "chart")
	if err := os.MkdirAll(filepath.Join(chartRoot, "templates"), 0755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"Chart.yaml": "apiVersion: v2\nname: controller\nversion: 0.1.0\n", "values.yaml": "image:\n  repository: old\n  tag: dev\n  digest: ''\n", "templates/controller.yaml": "apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: controller\n"} {
		if err := os.WriteFile(filepath.Join(chartRoot, name), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	archive := filepath.Join(work, "chart.tar")
	if err := transportarchive.Pack(t.Context(), archive, work, []string{"chart"}); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(archive, gitLabArtifact(t, plan, "artifact/chart/chart-source").Path); err != nil {
		t.Fatal(err)
	}
	for _, artifact := range plan.Graph.Artifacts {
		if artifact.Kind == "producer-result" {
			input := gitLabArtifact(t, plan, "artifact/"+artifact.BuildTarget+"/"+artifact.Component)
			writeCanonicalTestFile(t, artifact.Path, fixtureProducerResult(t, plan, artifact, input.Path))
		}
	}
	for _, id := range []string{"image/terminal-observe", "chart/terminal-observe", "cli/terminal-observe", "image/sbom", "chart/sbom", "cli/sbom", "package/image", "package/chart", "package/cli", "package/release"} {
		if err := factory.ExecuteNode(t.Context(), plan, id, io.Discard, io.Discard); err != nil {
			t.Fatalf("compiled operation %s: %v", id, err)
		}
	}
	return plan
}

// This one fixture executes the actual compiled release publisher as a current
// GitLab job, then admits its exact outcome and carriers through the real
// finalizer. The provider and registry are local synthetic HTTPS servers.
func TestGitLabCompiledPublishedClosureCurrentJobs(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("requires a native Linux GitLab utility runner")
	}
	for _, mode := range []string{"anonymous", "job-token", "missing-registry", "disabled-registry", "foreign-namespace", "changed-registry", "change-after-push", "bad-job-token", "foreign-realm", "foreign-scope", "token-redirect", "token-denied"} {
		t.Run(mode, func(t *testing.T) { testGitLabCompiledPublishedClosureCurrentJobs(t, mode) })
	}
}

func testGitLabCompiledPublishedClosureCurrentJobs(t *testing.T, mode string) {
	authenticated := mode != "anonymous"
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	certTemplate := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "localhost"}, DNSNames: []string{"localhost"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.2")}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IsCA: true, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, certTemplate, certTemplate, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	t.Chdir(root)
	var mu sync.Mutex
	members := map[string][]byte{}
	ids := map[string]uint64{}
	var names []string
	var commit string
	var publishName string
	var registryRequests, tokenRequests, foreignRequests, projectReads, closedConnections atomic.Int64
	var rootPublished atomic.Bool
	phase := "prepare"
	reg := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v2/") {
			registryRequests.Add(1)
			if mode == "foreign-scope" && r.Header.Get("Authorization") == "Bearer synthetic-registry-token" {
				w.Header().Set("WWW-Authenticate", `Bearer realm="https://`+r.Host+`/jwt/auth",service="container_registry",scope="repository:foreign/project:push,pull"`)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			if authenticated && r.Header.Get("Authorization") != "Bearer synthetic-registry-token" {
				path := "/jwt/auth"
				if mode == "foreign-realm" {
					path = "/unapproved-realm"
				}
				w.Header().Set("WWW-Authenticate", `Bearer realm="https://`+r.Host+path+`",service="container_registry"`)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			if !authenticated && r.Header.Get("Authorization") != "" {
				t.Error("anonymous registry received credentials")
			}
			reg.ServeHTTP(w, r)
			if r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/fixture/releases/manifests/") {
				rootPublished.Store(true)
			}
			return
		}
		if r.URL.Path == "/jwt/auth" {
			tokenRequests.Add(1)
			user, password, ok := r.BasicAuth()
			if !authenticated || !ok || user != "gitlab-ci-token" || password != "synthetic-job-token" {
				http.Error(w, "invalid synthetic credential", http.StatusUnauthorized)
				return
			}
			if mode == "token-redirect" {
				http.Redirect(w, r, "https://"+r.Host+"/unapproved-realm", http.StatusFound)
				return
			}
			if mode == "token-denied" {
				http.Error(w, "SYNTHETIC_PRIVATE_CANARY", http.StatusForbidden)
				return
			}
			_, _ = w.Write([]byte(`{"token":"synthetic-registry-token"}`))
			return
		}
		if r.URL.Path == "/unapproved-realm" {
			foreignRequests.Add(1)
			http.Error(w, "unexpected", http.StatusForbidden)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if r.Method != http.MethodGet {
			http.Error(w, "method", http.StatusMethodNotAllowed)
			return
		}
		if r.URL.Path == "/api/v4/job" {
			if r.Header.Get("JOB-TOKEN") != "synthetic-job-token" {
				t.Error("missing job token")
			}
			if mode == "bad-job-token" {
				http.Error(w, "rejected", http.StatusUnauthorized)
				return
			}
		} else if r.Header.Get("PRIVATE-TOKEN") != "synthetic-read-token" {
			t.Error("missing read token")
		}
		jobJSON := func(name string) map[string]any {
			status := "success"
			id := ids[name]
			if name == "nvkr:finalize" {
				status = "created"
				if strings.HasPrefix(phase, "finalizer") {
					status = "running"
				}
			}
			if name == publishName {
				status = "running"
				if strings.HasPrefix(phase, "finalizer") {
					status = "success"
				}
				if phase == "finalizer-retry" {
					id = 42
				}
			}
			return map[string]any{"id": id, "name": name, "status": status, "ref": "main", "commit": map[string]any{"id": commit}, "pipeline": map[string]any{"id": 23, "project_id": 7, "sha": commit, "ref": "main", "source": "api"}}
		}
		switch r.URL.Path {
		case "/api/v4/projects/7":
			count := projectReads.Add(1)
			project := map[string]any{"id": 7, "ci_config_path": nil, "container_registry_enabled": true, "container_registry_image_prefix": r.Host + "/fixture"}
			if mode == "missing-registry" {
				delete(project, "container_registry_image_prefix")
			}
			if mode == "disabled-registry" {
				project["container_registry_enabled"] = false
			}
			if (mode == "changed-registry" && count > 1) || (mode == "change-after-push" && rootPublished.Load()) {
				project["container_registry_image_prefix"] = r.Host + "/fixture-other"
			}
			_ = json.NewEncoder(w).Encode(project)
		case "/api/v4/projects/7/pipelines/23":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 23, "project_id": 7, "sha": commit, "ref": "main", "source": "api"})
		case "/api/v4/job":
			name := publishName
			if strings.HasPrefix(phase, "finalizer") {
				name = "nvkr:finalize"
			}
			_ = json.NewEncoder(w).Encode(jobJSON(name))
		case "/api/v4/projects/7/pipelines/23/jobs":
			w.Header().Set("X-Next-Page", "")
			out := []map[string]any{}
			for _, name := range names {
				out = append(out, jobJSON(name))
			}
			_ = json.NewEncoder(w).Encode(out)
		default:
			body, ok := members[r.URL.Path]
			if !ok {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write(body)
		}
	}))
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateClosed {
			closedConnections.Add(1)
		}
	}
	listener, err := net.Listen("tcp", "127.0.0.2:0")
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Listener.Close(); err != nil {
		t.Fatal(err)
	}
	server.Listener = listener
	server.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}, MinVersion: tls.VersionTLS12}
	server.StartTLS()
	defer server.Close()
	_, port, err := net.SplitHostPort(server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	prefix := "127.0.0.2:" + port + "/fixture"
	if mode == "foreign-namespace" {
		prefix = "127.0.0.2:" + port + "/fixture-other"
	}
	plan := prepareGitLabPublishedClosure(t, root, server.URL+"/api/v4", prefix, authenticated)
	names, err = factory.GitLabJobNames(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	operations := gitLabOperationNames(t, plan)
	publishName = operations["publish/release"]
	for i, name := range names {
		ids[name] = uint64(100 + i)
	}
	ids["nvkr:finalize"] = 31
	ids[publishName] = 41
	planDigest, err := plan.Digest()
	if err != nil {
		t.Fatal(err)
	}
	// Capture each real package and observation carrier under its exact native
	// synthetic producer identity. Publisher output is generated by the child.
	for _, node := range plan.Graph.Nodes {
		if node.ID == "publish/release" {
			continue
		}
		jobID := ids[operations[node.ID]]
		inputs := []map[string]any{}
		for _, id := range node.Inputs {
			inputs = append(inputs, map[string]any{"artifact": id, "producerJobID": ids[operations[gitLabArtifact(t, plan, id).Producer]]})
		}
		carriers := []map[string]any{}
		for _, id := range node.Outputs {
			a := gitLabArtifact(t, plan, id)
			archive := filepath.Join(root, factory.GitLabCarrierPath(node.ID, id))
			if err := transportarchive.Pack(t.Context(), archive, root, []string{a.Path}); err != nil {
				t.Fatal(err)
			}
			body, err := os.ReadFile(archive)
			if err != nil {
				t.Fatal(err)
			}
			carriers = append(carriers, map[string]any{"artifact": id, "digest": pipeline.FileDigest(body), "bytes": len(body)})
			members[gitLabMemberURL(jobID, factory.GitLabCarrierPath(node.ID, id))] = body
		}
		outcome := map[string]any{"schema": "nvkr.dev/gitlab-operation/v1", "apiURL": server.URL + "/api/v4", "projectID": 7, "pipelineID": 23, "jobID": jobID, "commit": "pending", "plan": planDigest, "node": node.ID, "status": "success", "exitCode": 0, "inputs": inputs, "carriers": carriers}
		members[gitLabMemberURL(jobID, factory.GitLabOutcomePath(node.ID))] = gitLabJSON(t, outcome)
	}
	if err := os.RemoveAll(".nvkr"); err != nil {
		t.Fatal(err)
	}
	commit = gitLabCommitFixture(t, root)
	for path, body := range members {
		if strings.Contains(path, "/outcomes/") {
			var outcome map[string]any
			if err := json.Unmarshal(body, &outcome); err != nil {
				t.Fatal(err)
			}
			outcome["commit"] = commit
			members[path] = gitLabJSON(t, outcome)
		}
	}
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "podman"), []byte("#!/bin/sh\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	runChild := func(which string) {
		t.Helper()
		child := exec.CommandContext(t.Context(), exe, "-test.run=^TestGitLabCompiledPublishedClosureChild$", "-test.count=1")
		child.Env = []string{"PATH=" + bin + ":" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GODEBUG=x509usefallbackroots=1", "NO_PROXY=127.0.0.2", "no_proxy=127.0.0.2", "CI_JOB_TOKEN=synthetic-job-token", "NVKR_API_READ=synthetic-read-token", "NVKR_GITLAB_CLOSURE_MODE=" + which, "NVKR_GITLAB_CLOSURE_ROOT=" + root, "NVKR_GITLAB_CLOSURE_CA=" + ca, "NVKR_GITLAB_CLOSURE_COMMIT=" + commit}
		// Inherit the parent's temporary directory so bounded scratch storage
		// stays with the attempt on hosts where the default location is full.
		if tmpdir := os.Getenv("TMPDIR"); tmpdir != "" {
			child.Env = append(child.Env, "TMPDIR="+tmpdir)
		}
		if which == "publisher-close-probe" {
			if err := child.Start(); err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(5 * time.Second)
			for time.Now().Before(deadline) {
				if _, err := os.Stat(filepath.Join(root, "admission-returned")); err == nil && closedConnections.Load() > 0 {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if closedConnections.Load() == 0 {
				_ = child.Process.Kill()
				_ = child.Wait()
				t.Fatal("failed admission retained the provider connection while process remained alive")
			}
			if err := os.WriteFile(filepath.Join(root, "continue"), []byte("ok"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := child.Wait(); err != nil {
				t.Fatalf("failed-admission child: %v", err)
			}
			return
		}
		if output, err := child.CombinedOutput(); err != nil {
			t.Fatalf("%s child: %v\n%s", which, err, output)
		}
	}
	mu.Lock()
	phase = "publisher"
	mu.Unlock()
	if mode != "anonymous" && mode != "job-token" {
		which := "publisher-refused"
		if mode == "missing-registry" {
			which = "publisher-close-probe"
		}
		if mode == "change-after-push" {
			which = "publisher-revalidated-refusal"
		}
		runChild(which)
		if (mode == "missing-registry" || mode == "disabled-registry" || mode == "foreign-namespace" || mode == "changed-registry" || mode == "bad-job-token") && registryRequests.Load() != 0 {
			t.Fatal("invalid project registry reached registry I/O")
		}
		if mode == "change-after-push" && !rootPublished.Load() {
			t.Fatal("post-push revalidation case did not publish the root")
		}
		if mode == "foreign-realm" && tokenRequests.Load() != 0 {
			t.Fatal("unapproved realm received job token")
		}
		if mode == "foreign-scope" && tokenRequests.Load() != 1 {
			t.Fatal("injected scope reached the token issuer")
		}
		if foreignRequests.Load() != 0 {
			t.Fatal("unapproved token authority received a request")
		}
		return
	}
	runChild("publisher")
	for _, path := range []string{factory.GitLabOutcomePath("publish/release"), factory.GitLabCarrierPath("publish/release", "publication/release")} {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		mu.Lock()
		members[gitLabMemberURL(41, path)] = body
		members[gitLabMemberURL(42, path)] = body
		mu.Unlock()
	}
	if err := os.RemoveAll(".nvkr"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	phase = "finalizer"
	mu.Unlock()
	runChild("finalizer")
	if err := os.RemoveAll(".nvkr"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	phase = "finalizer-retry"
	mu.Unlock()
	runChild("finalizer-retry")
}

func TestGitLabCompiledPublishedClosureChild(t *testing.T) {
	mode := os.Getenv("NVKR_GITLAB_CLOSURE_MODE")
	if mode == "" {
		return
	}
	raw, err := os.ReadFile(os.Getenv("NVKR_GITLAB_CLOSURE_CA"))
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(raw) {
		t.Fatal("invalid fixture CA")
	}
	x509.SetFallbackRoots(pool)
	t.Chdir(os.Getenv("NVKR_GITLAB_CLOSURE_ROOT"))
	commit := os.Getenv("NVKR_GITLAB_CLOSURE_COMMIT")
	if strings.HasPrefix(mode, "publisher") {
		plan, err := factory.LoadPlanContext(t.Context(), factory.GitLabPlanPath)
		if err != nil {
			t.Fatal(err)
		}
		job := factory.GitLabJobContext{ProjectID: 7, PipelineID: 23, JobID: 41, Commit: commit}
		err = factory.RunGitLabNode(t.Context(), plan, "publish/release", factory.GitLabNodeOptions{Root: ".", Job: job, Stdout: io.Discard, Stderr: io.Discard})
		if mode == "publisher-refused" || mode == "publisher-revalidated-refusal" || mode == "publisher-close-probe" {
			if err == nil {
				t.Fatal("invalid registry authority accepted")
			}
			if strings.Contains(err.Error(), "synthetic-job-token") || strings.Contains(err.Error(), "SYNTHETIC_PRIVATE_CANARY") {
				t.Fatal("registry failure exposed a credential or server response")
			}
			if _, statErr := os.Stat(gitLabArtifact(t, plan, "publication/release").Path); mode == "publisher-refused" && !os.IsNotExist(statErr) {
				t.Fatal("failed publication exposed a success reference")
			}
			if mode == "publisher-revalidated-refusal" {
				body, readErr := os.ReadFile(factory.GitLabOutcomePath("publish/release"))
				if readErr != nil {
					t.Fatal(readErr)
				}
				var outcome struct {
					Status string `json:"status"`
				}
				if json.Unmarshal(body, &outcome) != nil || outcome.Status != "failed" {
					t.Fatal("changed project metadata produced a successful publisher outcome")
				}
			}
			if mode == "publisher-close-probe" {
				root := os.Getenv("NVKR_GITLAB_CLOSURE_ROOT")
				if err := os.WriteFile(filepath.Join(root, "admission-returned"), []byte("ok"), 0600); err != nil {
					t.Fatal(err)
				}
				deadline := time.Now().Add(5 * time.Second)
				for time.Now().Before(deadline) {
					if _, err := os.Stat(filepath.Join(root, "continue")); err == nil {
						return
					}
					time.Sleep(10 * time.Millisecond)
				}
				t.Fatal("parent did not acknowledge failed-admission cleanup")
			}
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		return
	}
	if strings.HasPrefix(mode, "finalizer") {
		job := factory.GitLabJobContext{ProjectID: 7, PipelineID: 23, JobID: 31, Commit: commit}
		run, err := FinalizeGitLab(t.Context(), "definition.json", "environment.json", factory.GitLabPlanPath, "test (deadbeef)", ".nvkr/factory", ".nvkr/gitlab/run.json", job)
		if mode == "finalizer-retry" {
			assertCode(t, err, e.CodeProvenanceMissing)
			if run == nil || run.Status != RunFailed {
				t.Fatalf("superseded publisher accepted: %+v %v", run, err)
			}
			return
		}
		if err != nil || run == nil || run.Status != RunComplete || len(run.Observed) != 3 {
			t.Fatalf("published closure did not finalize: %+v %v", run, err)
		}
		return
	}
	t.Fatal("unknown fixture mode")
}
