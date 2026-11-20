// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/dsx-ai-factory/nvkr/internal/transportarchive"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	provider "github.com/dsx-ai-factory/nvkr/pkg/nvkr/gitlab"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
)

// The fixture uses real production HTTPS clients and real native transport
// archives. Synthetic provider data is contract evidence, never a live CI proof.
func TestGitLabEarlierProducerRetryDuringLaterAcquisition(t *testing.T) {
	for _, mode := range []string{"unchanged", "retry-earlier", "unchanged-transitive", "retry-transitive", "retry-after-restore", "unchanged-deep", "stale-deep", "retry-deep"} {
		t.Run(mode, func(t *testing.T) {
			var names []string
			jobs := map[string]provider.Job{}
			members := map[string][]byte{}
			selectedCarriers := map[string]bool{}
			outcomeReads := map[string]*atomic.Int64{}
			var first, second Node
			var plan *Plan
			var changed atomic.Bool
			var projectReads atomic.Int64
			ready := make(chan struct{})
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				<-ready
				switch r.URL.Path {
				case "/api/v4/projects/123":
					if projectReads.Add(1) > 2 && mode == "retry-after-restore" {
						changed.Store(true)
					}
					_, _ = w.Write([]byte(`{"id":123,"ci_config_path":null,"default_branch":"main"}`))
					return
				case "/api/v4/projects/123/pipelines/2":
					_, _ = w.Write([]byte(`{"id":2,"project_id":123,"sha":"` + strings.Repeat("a", 40) + `","ref":"main","source":"push"}`))
					return
				}
				jobJSON := func(job provider.Job) map[string]any {
					return map[string]any{"id": job.ID, "name": job.Name, "status": job.Status, "tag": false, "ref": "main", "commit": map[string]any{"id": strings.Repeat("a", 40)}, "pipeline": map[string]any{"id": 2, "project_id": 123, "sha": strings.Repeat("a", 40), "ref": "main", "source": "push"}}
				}
				if r.URL.Path == "/api/v4/job" {
					_ = json.NewEncoder(w).Encode(jobJSON(jobs[gitlabFinalizerJob]))
					return
				}
				if r.URL.Path == "/api/v4/projects/123/pipelines/2/jobs" {
					w.Header().Set("X-Next-Page", "")
					rows := []map[string]any{}
					for _, name := range names {
						job := jobs[name]
						if changed.Load() && name == gitlabNodeJob(first.ID) {
							job.ID += 10000
						}
						rows = append(rows, jobJSON(job))
					}
					_ = json.NewEncoder(w).Encode(rows)
					return
				}
				payload, ok := members[r.URL.Path]
				if !ok {
					http.NotFound(w, r)
					return
				}
				if strings.HasSuffix(r.URL.Path, ".tar") && !selectedCarriers[r.URL.Path] {
					t.Error("consumer fetched an unselected ancestor carrier")
				}
				if count := outcomeReads[r.URL.Path]; count != nil && count.Add(1) != 1 {
					t.Error("shared ancestor outcome was fetched more than once")
				}
				if (mode == "retry-earlier" || mode == "retry-transitive" || mode == "retry-deep") && strings.Contains(r.URL.Path, "/jobs/"+strconv.FormatUint(jobs[gitlabNodeJob(second.ID)].ID, 10)+"/artifacts/") && strings.HasSuffix(r.URL.Path, ".tar") {
					changed.Store(true)
				}
				_, _ = w.Write(payload)
			}))
			defer server.Close()
			root, compiled := gitlabFixtureAt(t, 2, server.URL+"/api/v4")
			plan = compiled
			var sources []Node
			for _, node := range plan.Graph.Nodes {
				if node.Kind == NodeFetch && len(node.Outputs) > 0 {
					sources = append(sources, node)
				}
			}
			if len(sources) < 2 {
				t.Fatal("native authored selections did not produce two source owners")
			}
			first, second = sources[0], sources[1]
			if strings.Contains(mode, "transitive") {
				nodes, artifacts := planIndex(plan)
				found := false
				for _, node := range plan.Graph.Nodes {
					if node.Kind == NodeBuild && len(node.Inputs) > 0 && len(node.Outputs) > 0 {
						first, second = nodes[artifacts[node.Inputs[0]].Producer], node
						found = true
						break
					}
				}
				if !found {
					t.Fatal("fixture lacks a producer with an original input attempt")
				}
			}
			if strings.Contains(mode, "deep") {
				nodes, artifacts := planIndex(plan)
				found := false
				for _, node := range plan.Graph.Nodes {
					direct := map[string]bool{}
					for _, input := range node.Inputs {
						direct[artifacts[input].Producer] = true
					}
					for _, input := range node.Inputs {
						parent := nodes[artifacts[input].Producer]
						if len(parent.Inputs) > 0 && len(node.Outputs) > 0 && !direct[artifacts[parent.Inputs[0]].Producer] {
							first, second = nodes[artifacts[parent.Inputs[0]].Producer], node
							found = true
							break
						}
					}
					if found {
						break
					}
				}
				if !found {
					t.Fatal("fixture lacks a two-edge producer ancestry")
				}
			}
			namesErr := error(nil)
			names, namesErr = GitLabJobNames(t.Context(), plan)
			if namesErr != nil {
				t.Fatal(namesErr)
			}
			for index, name := range names {
				jobs[name] = provider.Job{ID: uint64(100 + index), Name: name, Status: "success"}
			}
			jobs[gitlabFinalizerJob] = provider.Job{ID: 3, Name: gitlabFinalizerJob, Status: "running"}
			digest, err := plan.Digest()
			if err != nil {
				t.Fatal(err)
			}
			_, artifacts := planIndex(plan)
			var selected []string
			producerRoot := t.TempDir()
			nodes, _ := planIndex(plan)
			closure := map[string]Node{}
			var include func(Node)
			include = func(node Node) {
				if _, exists := closure[node.ID]; exists {
					return
				}
				closure[node.ID] = node
				for _, input := range node.Inputs {
					include(nodes[artifacts[input].Producer])
				}
			}
			include(first)
			include(second)
			for _, nodeID := range sortedKeys(closure) {
				node := closure[nodeID]
				job := jobs[gitlabNodeJob(node.ID)]
				outcome := gitlabOutcome{Ref: "main", Source: "push", Schema: "nvkr.dev/gitlab-operation/v1", APIURL: server.URL + "/api/v4", ProjectID: 123, PipelineID: 2, JobID: job.ID, Commit: strings.Repeat("a", 40), Plan: digest, Node: node.ID, Status: "success", Inputs: []gitlabInput{}, Carriers: []gitlabCarrier{}}
				for _, id := range node.Inputs {
					outcome.Inputs = append(outcome.Inputs, gitlabInput{Artifact: id, ProducerJobID: jobs[gitlabNodeJob(artifacts[id].Producer)].ID})
				}
				for _, id := range node.Outputs {
					artifact := artifacts[id]
					path := filepath.Join(producerRoot, artifact.Path)
					if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
						t.Fatal(err)
					}
					if artifact.Kind == "source-closure" {
						if err := os.MkdirAll(path, 0700); err != nil {
							t.Fatal(err)
						}
						path = filepath.Join(path, "source-and-vendor.txt")
					}
					if err := os.WriteFile(path, []byte("original source/vendor payload"), 0600); err != nil {
						t.Fatal(err)
					}
					carrier := gitlabCarrierPath(node.ID, id)
					if err := transportarchive.Pack(t.Context(), filepath.Join(producerRoot, carrier), producerRoot, []string{artifact.Path}); err != nil {
						t.Fatal(err)
					}
					payload, err := os.ReadFile(filepath.Join(producerRoot, carrier))
					if err != nil {
						t.Fatal(err)
					}
					outcome.Carriers = append(outcome.Carriers, gitlabCarrier{Artifact: id, Digest: pipeline.FileDigest(payload), Bytes: int64(len(payload))})
					members["/api/v4/projects/123/jobs/"+strconv.FormatUint(job.ID, 10)+"/artifacts/"+carrier] = payload
					if node.ID == second.ID || (node.ID == first.ID && !strings.Contains(mode, "transitive") && !strings.Contains(mode, "deep")) {
						selected = append(selected, id)
						selectedCarriers["/api/v4/projects/123/jobs/"+strconv.FormatUint(job.ID, 10)+"/artifacts/"+carrier] = true
					}
				}
				raw, err := canonical.Marshal(outcome)
				if err != nil {
					t.Fatal(err)
				}
				members["/api/v4/projects/123/jobs/"+strconv.FormatUint(job.ID, 10)+"/artifacts/"+gitlabOutcomePath(node.ID)] = raw
				outcomeReads["/api/v4/projects/123/jobs/"+strconv.FormatUint(job.ID, 10)+"/artifacts/"+gitlabOutcomePath(node.ID)] = &atomic.Int64{}
			}
			if mode == "stale-deep" {
				name := gitlabNodeJob(first.ID)
				job := jobs[name]
				job.ID += 10000
				jobs[name] = job
			}
			raw, err := canonical.Marshal(plan)
			if err != nil {
				t.Fatal(err)
			}
			planPath := filepath.Join(root, "fixture-plan.json")
			if err := os.WriteFile(planPath, raw, 0600); err != nil {
				t.Fatal(err)
			}
			selection, err := json.Marshal(selected)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "selected.json"), selection, 0600); err != nil {
				t.Fatal(err)
			}
			ca := filepath.Join(root, "ca.pem")
			if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
				t.Fatal(err)
			}
			close(ready)
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			child := exec.CommandContext(t.Context(), executable, "-test.run=^TestGitLabEarlierProducerRetryChild$", "-test.count=1")
			child.Env = append(os.Environ(), "NVKR_GITLAB_RETRY_CHILD="+mode, "NVKR_GITLAB_TEST_ROOT="+root, "NVKR_GITLAB_TEST_API="+server.URL+"/api/v4", "GODEBUG=x509usefallbackroots=1")
			output, err := child.CombinedOutput()
			if err != nil {
				t.Fatalf("native retry fixture failed: %v\n%s", err, output)
			}
			wantReads := int64(2)
			if mode == "stale-deep" {
				wantReads = 1
			}
			if mode == "retry-after-restore" {
				wantReads = 3
			}
			if projectReads.Load() != wantReads {
				t.Fatalf("restoration metadata refreshes=%d, want bounded %d", projectReads.Load(), wantReads)
			}
		})
	}
}

func TestGitLabEarlierProducerRetryChild(t *testing.T) {
	mode := os.Getenv("NVKR_GITLAB_RETRY_CHILD")
	if mode == "" {
		return
	}
	root := os.Getenv("NVKR_GITLAB_TEST_ROOT")
	raw, err := os.ReadFile(filepath.Join(root, "ca.pem"))
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(raw) {
		t.Fatal("invalid synthetic CA")
	}
	x509.SetFallbackRoots(pool)
	t.Chdir(root)
	plan, err := LoadPlanContext(t.Context(), "fixture-plan.json")
	if err != nil {
		t.Fatal(err)
	}
	names, err := GitLabJobNames(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	config := provider.Config{APIURL: os.Getenv("NVKR_GITLAB_TEST_API"), ProjectID: 123, JobToken: "synthetic-job", ReadCredential: "synthetic-read", ReadCredentialKind: "private-token", CertificateAuthority: string(raw)}
	client, err := provider.New(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := plan.Digest()
	if err != nil {
		t.Fatal(err)
	}
	state := &gitlabAdmission{defaultBranch: "main", pipelineSources: []string{"push", "web"}, lifetime: t.Context(), client: client, config: config, identity: provider.Identity{APIURL: config.APIURL, ProjectID: 123, PipelineID: 2, JobID: 3, Commit: strings.Repeat("a", 40), Ref: "main", Source: "push"}, names: names, digest: digest, acquired: map[string]provider.Job{gitlabFinalizerJob: {ID: 3, Name: gitlabFinalizerJob, Status: "running"}}}
	selection, err := os.ReadFile("selected.json")
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	if err := json.Unmarshal(selection, &ids); err != nil {
		t.Fatal(err)
	}
	_, artifacts := planIndex(plan)
	err = state.restore(t.Context(), plan, ids, artifacts)
	if (mode == "unchanged" || mode == "unchanged-transitive" || mode == "unchanged-deep" || mode == "retry-after-restore") && err != nil {
		t.Fatal(err)
	}
	if mode == "retry-earlier" || mode == "retry-transitive" || mode == "stale-deep" || mode == "retry-deep" {
		assertFactoryErrorCode(t, err, e.CodeProvenanceMissing)
	}
	if mode == "retry-after-restore" {
		err = (&GitLabFinalizer{state: state}).Revalidate(t.Context())
		assertFactoryErrorCode(t, err, e.CodeProvenanceMissing)
	}
}

func TestGitLabSubstitutedEndpointRefusesBeforeCredentialTransport(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	root, original := gitlabFixture(t, 1)
	if _, err := Generate(t.Context(), original, EngineGitLab, root, false); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".gitlab-ci.yml"), []byte("include:\n - local: .gitlab/nvkr-run/pipeline.yml\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) string {
		t.Helper()
		command := exec.CommandContext(t.Context(), "git", append([]string{"-C", root}, args...)...)
		raw, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("native fixture Git: %v", err)
		}
		return strings.TrimSpace(string(raw))
	}
	git("init", "--quiet")
	git("add", "--all")
	git("-c", "user.name=Native Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--no-gpg-sign", "--quiet", "-m", "authenticated original source")
	commit := git("rev-parse", "HEAD")
	// Tamper both the local policy and its matching regenerated Plan/pipeline.
	// Byte agreement among these files must not authorize the changed API origin.
	environment := original.Environment
	environment.GitLab = original.Environment.Clone().GitLab
	environment.GitLab.APIURL = server.URL + "/api/v4"
	writeSnapshotFixtureDocument(t, root, snapshotEnvironment, environment)
	changed, _, err := CaptureCompileFiles(t.Context(), root, snapshotDefinition, snapshotEnvironment, original.GeneratorVersion)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Generate(t.Context(), changed, EngineGitLab, root, false); err != nil {
		t.Fatal(err)
	}
	ca := filepath.Join(root, "origin-ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	child := exec.CommandContext(t.Context(), executable, "-test.run=^TestGitLabSubstitutedEndpointChild$", "-test.count=1")
	child.Env = append(os.Environ(), "NVKR_GITLAB_ORIGIN_CHILD=1", "NVKR_GITLAB_TEST_ROOT="+root, "NVKR_GITLAB_ORIGIN_COMMIT="+commit, "GODEBUG=x509usefallbackroots=1", "FF_GIT_URLS_WITHOUT_TOKENS=true", "CI_JOB_TOKEN=synthetic-job", "NVKR_API_READ=synthetic-read")
	output, err := child.CombinedOutput()
	if err != nil {
		t.Fatalf("native source-origin fixture: %v\n%s", err, output)
	}
	if requests.Load() != 0 {
		t.Fatal("dirty source redirected a credential-bearing API request")
	}
}
func TestGitLabSubstitutedEndpointChild(t *testing.T) {
	if os.Getenv("NVKR_GITLAB_ORIGIN_CHILD") != "1" {
		return
	}
	root := os.Getenv("NVKR_GITLAB_TEST_ROOT")
	raw, err := os.ReadFile(filepath.Join(root, "origin-ca.pem"))
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(raw) {
		t.Fatal("invalid fixture CA")
	}
	x509.SetFallbackRoots(pool)
	t.Chdir(root)
	plan, err := LoadPlanContext(t.Context(), GitLabPlanPath)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := AdmitGitLabFinalizer(t.Context(), plan, GitLabJobContext{ProjectID: 123, PipelineID: 2, JobID: 3, Commit: os.Getenv("NVKR_GITLAB_ORIGIN_COMMIT"), Ref: "main", Source: "push"})
	if owner != nil {
		_ = owner.Close()
		t.Fatal("dirty native source created an admission")
	}
	if err == nil {
		t.Fatal("substituted endpoint accepted")
	}
}
