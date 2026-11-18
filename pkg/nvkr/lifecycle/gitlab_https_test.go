// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
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
	authored "github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
)

// These responses describe synthetic jobs and synthetic unqualified producer
// bytes. They exercise the public production HTTPS adapter, native archive
// owner and complete finalizer; they are not evidence of a live GitLab run.
func TestFinalizeGitLabNativeHTTPSClosure(t *testing.T) {
	for _, mode := range []string{"complete", "failed", "skipped", "missing-outcome", "corrupt-outcome", "wrong-producer-job", "corrupt-carrier", "wrong-role", "foreign-output-parent", "existing-run", "foreign-definition", "foreign-environment", "producer-retried-after-dependent"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			t.Chdir(root)
			var jobs []map[string]any
			var self map[string]any
			var commit string
			members := map[string][]byte{}
			var artifactReads atomic.Int64
			var apiReads atomic.Int64
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				apiReads.Add(1)
				if r.Method != http.MethodGet {
					t.Error("finalizer attempted a provider mutation")
					w.WriteHeader(http.StatusMethodNotAllowed)
					return
				}
				if r.URL.Path == "/api/v4/job" {
					if r.Header.Get("JOB-TOKEN") != "synthetic-job-token" || r.Header.Get("PRIVATE-TOKEN") != "" {
						t.Error("current-job authentication used the wrong credential")
					}
				} else if r.Header.Get("PRIVATE-TOKEN") != "synthetic-read-token" || r.Header.Get("JOB-TOKEN") != "" {
					t.Error("provider read used the wrong scoped credential")
				}
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/api/v4/projects/7":
					_ = json.NewEncoder(w).Encode(map[string]any{"id": 7, "ci_config_path": nil})
				case "/api/v4/projects/7/pipelines/23":
					_ = json.NewEncoder(w).Encode(map[string]any{"id": 23, "project_id": 7, "sha": commit, "ref": "main", "source": "api"})
				case "/api/v4/job":
					_ = json.NewEncoder(w).Encode(self)
				case "/api/v4/projects/7/pipelines/23/jobs":
					if r.URL.Query().Get("include_retried") != "false" || r.URL.Query().Get("page") != "1" {
						t.Error("current membership did not exclude historical retries")
					}
					w.Header().Set("X-Next-Page", "")
					_ = json.NewEncoder(w).Encode(jobs)
				default:
					artifactReads.Add(1)
					body, ok := members[r.URL.Path]
					if !ok {
						w.WriteHeader(http.StatusNotFound)
						return
					}
					_, _ = w.Write(body)
				}
			}))
			defer server.Close()
			plan := prepareGitLabFinalizerPayloads(t, root, server.URL+"/api/v4")
			names, err := factory.GitLabJobNames(t.Context(), plan)
			if err != nil {
				t.Fatal(err)
			}
			jobIDs := map[string]uint64{}
			for index, name := range names {
				jobIDs[name] = uint64(100 + index)
			}
			jobIDs["nvkr:finalize"] = 31
			if mode == "wrong-role" {
				jobIDs["nvkr:finalize"] = 99
				jobIDs["nvkr:bootstrap:linux-amd64"] = 31
			}
			// The renderer owns each operation name and the descriptor-only path
			// APIs own filenames. No alternate transport naming grammar is used.
			operationNames := gitLabOperationNames(t, plan)
			var retiredProducer uint64
			var retriedInput string
			if mode == "producer-retried-after-dependent" {
				for _, node := range plan.Graph.Nodes {
					if node.ID == "package/cli" && len(node.Inputs) > 0 {
						retriedInput = node.Inputs[0]
						producer := operationNames[gitLabArtifact(t, plan, retriedInput).Producer]
						retiredProducer = jobIDs[producer]
						jobIDs[producer] += 10000
					}
				}
				if retiredProducer == 0 {
					t.Fatal("retry fixture has no exact dependency")
				}
			}
			digest, err := plan.Digest()
			if err != nil {
				t.Fatal(err)
			}
			for _, node := range plan.Graph.Nodes {
				jobID := jobIDs[operationNames[node.ID]]
				inputs := []map[string]any{}
				for _, id := range node.Inputs {
					producerJob := jobIDs[operationNames[gitLabArtifact(t, plan, id).Producer]]
					// Current A has succeeded with the same bytes, but B's
					// retained receipt must still name the original A it used.
					// A retry cannot silently rebind B to the new attempt.
					if node.ID == "package/cli" && id == retriedInput {
						producerJob = retiredProducer
					}
					inputs = append(inputs, map[string]any{"artifact": id, "producerJobID": producerJob})
				}
				carriers := []map[string]any{}
				for _, id := range node.Outputs {
					artifact := gitLabArtifact(t, plan, id)
					archive := filepath.Join(root, filepath.FromSlash(factory.GitLabCarrierPath(node.ID, id)))
					if err := transportarchive.Pack(t.Context(), archive, root, []string{artifact.Path}); err != nil {
						t.Fatal(err)
					}
					raw, err := os.ReadFile(archive)
					if err != nil {
						t.Fatal(err)
					}
					carriers = append(carriers, map[string]any{"artifact": id, "digest": pipeline.FileDigest(raw), "bytes": len(raw)})
					if mode == "corrupt-carrier" {
						raw = append(raw, 'x')
					}
					members[gitLabMemberURL(jobID, factory.GitLabCarrierPath(node.ID, id))] = raw
				}
				outcome := map[string]any{"schema": "nvkr.dev/gitlab-operation/v1", "apiURL": server.URL + "/api/v4", "projectID": 7,
					"pipelineID": 23, "jobID": jobID, "commit": "filled-after-native-git-commit", "plan": digest, "node": node.ID,
					"status": "success", "exitCode": 0, "inputs": inputs, "carriers": carriers}
				// Commit is filled below; the original member descriptions are
				// generated only after the actual fixture checkout exists.
				members[gitLabMemberURL(jobID, factory.GitLabOutcomePath(node.ID))] = gitLabJSON(t, outcome)
			}
			if err := os.RemoveAll(".nvkr"); err != nil {
				t.Fatal(err)
			}
			commit = gitLabCommitFixture(t, root)
			if mode == "foreign-definition" || mode == "foreign-environment" {
				name := strings.TrimPrefix(mode, "foreign-") + ".json"
				raw, err := os.ReadFile(name)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile("foreign-"+name, raw, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename("catalog", "withheld-catalog"); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "wrong-role" {
				// This catalog cannot be compiled. Exact current-role admission
				// must refuse first, rather than reporting a later catalog error.
				if err := os.Rename("catalog", "withheld-catalog"); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "foreign-output-parent" {
				if err := os.MkdirAll(".nvkr", 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(t.TempDir(), ".nvkr/gitlab"); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "existing-run" {
				if err := os.MkdirAll(".nvkr/gitlab", 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(".nvkr/gitlab/run.json", []byte("previous immutable attempt\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			for path, raw := range members {
				if !strings.Contains(path, "/outcomes/") {
					continue
				}
				var outcome map[string]any
				if err := json.Unmarshal(raw, &outcome); err != nil {
					t.Fatal(err)
				}
				outcome["commit"] = commit
				switch mode {
				case "missing-outcome":
					delete(members, path)
					continue
				case "corrupt-outcome":
					members[path] = []byte("{")
					continue
				case "wrong-producer-job":
					outcome["jobID"] = 9999
				}
				members[path] = gitLabJSON(t, outcome)
			}
			for _, name := range names {
				status := "success"
				if name == "nvkr:finalize" || (mode == "wrong-role" && jobIDs[name] == 31) {
					status = "running"
				} else if name == operationNames["package/cli"] && (mode == "failed" || mode == "skipped") {
					status = mode
				}
				job := map[string]any{"id": jobIDs[name], "name": name, "status": status, "ref": "main", "commit": map[string]any{"id": commit},
					"pipeline": map[string]any{"id": 23, "project_id": 7, "sha": commit, "ref": "main", "source": "api"}}
				jobs = append(jobs, job)
				if jobIDs[name] == 31 {
					self = job
				}
			}
			ca := filepath.Join(t.TempDir(), "ca.pem")
			if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
				t.Fatal(err)
			}
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			child := exec.CommandContext(t.Context(), executable, "-test.run=^TestFinalizeGitLabNativeHTTPSChild$", "-test.count=1")
			child.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null",
				"GODEBUG=x509usefallbackroots=1", "NVKR_GITLAB_FINALIZE_MODE=" + mode, "NVKR_GITLAB_FINALIZE_ROOT=" + root,
				"NVKR_GITLAB_FINALIZE_CA=" + ca, "NVKR_GITLAB_FINALIZE_COMMIT=" + commit,
				"CI_JOB_TOKEN=synthetic-job-token", "NVKR_API_READ=synthetic-read-token"}
			// Inherit the parent's temporary directory so bounded scratch storage
			// stays with the attempt on hosts where the default location is full.
			if tmpdir := os.Getenv("TMPDIR"); tmpdir != "" {
				child.Env = append(child.Env, "TMPDIR="+tmpdir)
			}
			if raw, err := child.CombinedOutput(); err != nil {
				t.Fatalf("production HTTPS finalizer fixture: %v\n%s", err, raw)
			}
			if mode == "wrong-role" && artifactReads.Load() != 0 {
				t.Fatal("foreign role performed artifact IO")
			}
			if (mode == "foreign-definition" || mode == "foreign-environment") && apiReads.Load() != 0 {
				t.Fatal("foreign authored paths performed provider IO")
			}
		})
	}
}

func TestFinalizeGitLabNativeHTTPSChild(t *testing.T) {
	mode := os.Getenv("NVKR_GITLAB_FINALIZE_MODE")
	if mode == "" {
		return
	}
	raw, err := os.ReadFile(os.Getenv("NVKR_GITLAB_FINALIZE_CA"))
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(raw) {
		t.Fatal("malformed synthetic CA")
	}
	x509.SetFallbackRoots(pool)
	t.Chdir(os.Getenv("NVKR_GITLAB_FINALIZE_ROOT"))
	job := factory.GitLabJobContext{ProjectID: 7, PipelineID: 23, JobID: 31, Commit: os.Getenv("NVKR_GITLAB_FINALIZE_COMMIT")}
	definitionPath, environmentPath := "definition.json", "environment.json"
	if mode == "foreign-definition" {
		definitionPath = "foreign-definition.json"
	}
	if mode == "foreign-environment" {
		environmentPath = "foreign-environment.json"
	}
	run, err := FinalizeGitLab(t.Context(), definitionPath, environmentPath, factory.GitLabPlanPath, "test (deadbeef)", ".nvkr/factory", ".nvkr/gitlab/run.json", job)
	if mode == "foreign-definition" || mode == "foreign-environment" {
		assertCode(t, err, e.CodePipelineDrift)
		if run != nil {
			t.Fatal("foreign authored paths returned a Run")
		}
		return
	}
	if mode == "foreign-output-parent" || mode == "existing-run" {
		assertCode(t, err, e.CodeImmutableViolation)
		if run != nil {
			t.Fatal("unsafe or occupied output returned a Run")
		}
		if mode == "existing-run" {
			raw, readErr := os.ReadFile(".nvkr/gitlab/run.json")
			if readErr != nil || string(raw) != "previous immutable attempt\n" {
				t.Fatal("previous attempt was replaced")
			}
		}
		return
	}
	if mode == "wrong-role" {
		assertCode(t, err, e.CodeProvenanceMissing)
		if run != nil {
			t.Fatal("wrong role returned a Run")
		}
		if _, statErr := os.Stat(".nvkr/gitlab/run.json"); !os.IsNotExist(statErr) {
			t.Fatal("wrong role wrote a Run")
		}
		return
	}
	if run == nil || len(run.Expected) != 1 || run.Lane != LaneUnqualified || run.Attempt != "gitlab-7-23-31" {
		t.Fatalf("frozen requirement/attempt/lane lost: %+v, %v", run, err)
	}
	if mode == "complete" {
		if err != nil || run.Status != RunComplete || len(run.Observed) != 1 || run.Observed[0].Digest == "" {
			t.Fatalf("complete typed closure refused: %+v, %v", run, err)
		}
	} else {
		if err == nil || run.Status != RunFailed || len(run.Observed) != 0 {
			t.Fatalf("incomplete closure promoted: %+v, %v", run, err)
		}
		if mode == "failed" || mode == "skipped" {
			assertCode(t, err, e.CodeReleaseIncomplete)
		}
		if mode == "producer-retried-after-dependent" {
			assertCode(t, err, e.CodeProvenanceMissing)
		}
	}
	if _, err := os.Stat(".nvkr/gitlab/run.json"); err != nil {
		t.Fatal("terminal Run was not durably recorded")
	}
}

func prepareGitLabFinalizerPayloads(t *testing.T, root, api string) *factory.Plan {
	t.Helper()
	definition, environment := testDefinition(t), testEnvironment()
	if err := os.CopyFS(filepath.Join(root, "catalog"), os.DirFS(definition.Catalog.Base)); err != nil {
		t.Fatal(err)
	}
	definition.Catalog.Base = "catalog"
	environment.Publication = nil
	environment.GitLab = &authored.GitLab{APIURL: api, ProjectID: 7, APIReadCredentialRef: "gitlab:NVKR_API_READ", APIReadCredentialKind: "private-token",
		Runners: map[string]authored.GitLabRunner{"linux/amd64": {Tags: []string{"synthetic-native-runner"}, BootstrapImage: "registry.example.test/go@sha256:" + strings.Repeat("a", 64)}}}
	writeCanonicalTestFile(t, "definition.json", definition)
	writeCanonicalTestFile(t, "environment.json", environment)
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
	for _, node := range plan.Graph.Nodes {
		if node.Kind != factory.NodeFetch && node.Kind != factory.NodeBuild && node.Kind != factory.NodeAcquire {
			continue
		}
		for _, id := range node.Outputs {
			artifact := gitLabArtifact(t, plan, id)
			if artifact.Kind == "producer-result" {
				continue
			}
			if err := os.MkdirAll(filepath.Dir(artifact.Path), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(artifact.Path, []byte("synthetic producer bytes for "+id), 0755); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, artifact := range plan.Graph.Artifacts {
		if artifact.Kind == "producer-result" {
			input := gitLabArtifact(t, plan, "artifact/"+artifact.BuildTarget+"/"+artifact.Component)
			writeCanonicalTestFile(t, artifact.Path, fixtureProducerResult(t, plan, artifact, input.Path))
		}
	}
	for _, kind := range []string{factory.NodeVerify, factory.NodeSBOM, factory.NodePackage} {
		for _, node := range plan.Graph.Nodes {
			if node.Kind == kind {
				if err := factory.ExecuteNode(t.Context(), plan, node.ID, nil, nil); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	return plan
}

func gitLabArtifact(t *testing.T, plan *factory.Plan, id string) factory.Artifact {
	t.Helper()
	for _, artifact := range plan.Graph.Artifacts {
		if artifact.ID == id {
			return artifact
		}
	}
	t.Fatalf("fixture has no artifact %s", id)
	return factory.Artifact{}
}

func gitLabJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := canonical.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func gitLabMemberURL(job uint64, member string) string {
	return "/api/v4/projects/7/jobs/" + strconv.FormatUint(job, 10) + "/artifacts/" + member
}

func gitLabOperationNames(t *testing.T, plan *factory.Plan) map[string]string {
	t.Helper()
	names, err := factory.GitLabJobNames(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	// Names are descriptive projection data. Match their exact owning outcome
	// basename rather than implementing the provider's stable-name grammar.
	result := map[string]string{}
	for _, node := range plan.Graph.Nodes {
		want := "nvkr:op:" + strings.TrimSuffix(filepath.Base(factory.GitLabOutcomePath(node.ID)), ".json")
		for _, name := range names {
			if name == want {
				result[node.ID] = name
			}
		}
		if result[node.ID] == "" {
			t.Fatalf("operation has no native projected job: %s", node.ID)
		}
	}
	return result
}

func gitLabCommitFixture(t *testing.T, root string) string {
	t.Helper()
	for _, args := range [][]string{{"init", "--quiet", "--template="}, {"add", "-f", "--", "definition.json", "environment.json", "catalog", ".gitlab", ".gitlab-ci.yml"},
		{"-c", "user.name=Synthetic fixture", "-c", "user.email=fixture@example.test", "-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null", "commit", "--quiet", "-m", "synthetic native GitLab finalizer inputs"}} {
		command := exec.CommandContext(t.Context(), "git", args...)
		command.Dir, command.Env = root, []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"}
		if raw, err := command.CombinedOutput(); err != nil {
			t.Fatalf("native fixture Git: %v: %s", err, raw)
		}
	}
	command := exec.CommandContext(t.Context(), "git", "rev-parse", "HEAD")
	command.Dir = root
	raw, err := command.Output()
	if err != nil {
		t.Fatal(fmt.Errorf("fixture HEAD: %w", err))
	}
	return strings.TrimSpace(string(raw))
}
