// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/internal/testutil"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"

	yamlv3 "go.yaml.in/yaml/v3"
)

func TestGitHubPartitionCallerRetainsSingleFinalizer(t *testing.T) {
	plan := githubTestPlan(t)
	bundle, err := RenderGitHub(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	entry, workers, err := lowerGitHubPartitions(t.Context(), plan, bundle.Files[bundle.WorkflowPath])
	if err != nil {
		t.Fatal(err)
	}
	assertGitHubPartitionMembership(t, plan, entry, workers)
}

func TestGitHubPartitionResultRewriteRequiresMarkers(t *testing.T) {
	owners := map[string]string{"build": "partition-00"}
	for _, test := range []struct {
		name, value string
		refuse      bool
	}{
		{"both markers", "${{ toJSON(needs) }} ${{ join(needs.*.result, ' ') }}", false},
		{"missing statuses", "${{ toJSON(needs) }}", true},
		{"missing results", "${{ join(needs.*.result, ' ') }}", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			node := githubYAMLMap("run", githubYAMLString(test.value))
			err := rewriteGitHubPartitionResults(node, githubFinalizerJobID, []string{"build"}, owners)
			if test.refuse {
				assertFactoryErrorCode(t, err, e.CodeEngineCapability)
				return
			}
			if err != nil || strings.Contains(githubYAMLValue(node, "run"), "needs)") || strings.Contains(githubYAMLValue(node, "run"), "needs.*") {
				t.Fatalf("rewrite=%q error=%v", githubYAMLValue(node, "run"), err)
			}
		})
	}
}

func TestGitHubPartitionCallerRefusesMissingResultMarkers(t *testing.T) {
	plan := githubTestPlan(t)
	bundle, err := RenderGitHub(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	raw := string(bundle.Files[bundle.WorkflowPath])
	// Markers are replaced in their YAML-encoded form; the statuses marker is
	// inside a single-quoted scalar, so its quotes are doubled.
	for _, marker := range []string{"toJSON(needs)", "join(needs.*.result, '' '')"} {
		t.Run(marker, func(t *testing.T) {
			if !strings.Contains(raw, marker) {
				t.Fatalf("rendered finalizer lacks %q", marker)
			}
			entry, workers, err := lowerGitHubPartitions(t.Context(), plan, []byte(strings.ReplaceAll(raw, marker, "github.job")))
			assertFactoryErrorCode(t, err, e.CodeEngineCapability)
			if entry != nil || workers != nil {
				t.Fatal("refused partition lowering returned partial output")
			}
		})
	}
}

func TestGitHubPartitionActionlint(t *testing.T) {
	testutil.RequireExtended(t)
	tool, err := exec.LookPath("actionlint")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := LoadPlanContext(t.Context(), "../../../"+FactoryPlanPath)
	if err != nil {
		t.Fatal(err)
	}
	plan.Environment.GitHubExecutionPartitions = true
	plan.EnvironmentDigest, err = plan.Environment.Digest()
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := RenderGitHub(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	files := map[string][]byte{}
	for path, raw := range bundle.Files {
		files[path] = raw
	}
	config, err := os.ReadFile("../../../.github/actionlint.yaml")
	if err != nil {
		t.Fatal(err)
	}
	files[".github/actionlint.yaml"] = config
	for path, raw := range files {
		full := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, raw, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	args := []string{"-shellcheck=", "-pyflakes=", "-config-file", filepath.Join(root, ".github/actionlint.yaml")}
	for _, path := range sortedKeys(bundle.Files) {
		if githubPartitionWorkflowSuffix.MatchString(path) {
			args = append(args, filepath.Join(root, path))
		}
	}
	args = append(args, filepath.Join(root, bundle.WorkflowPath))
	command := exec.CommandContext(t.Context(), tool, args...)
	command.Dir = root
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("actionlint: %v\n%s", err, output)
	}
	yamlLint, err := exec.LookPath("yamllint")
	if err != nil {
		t.Fatal(err)
	}
	command = exec.CommandContext(t.Context(), yamlLint, "-d", "relaxed", filepath.Join(root, ".github/workflows"))
	command.Dir = root
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("yamllint: %v\n%s", err, output)
	}
}

func assertGitHubPartitionMembership(t *testing.T, plan *Plan, entry []byte, workers map[string][]byte) {
	t.Helper()
	projection, err := githubNeedsForPlan(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for path, raw := range workers {
		var doc yamlv3.Node
		if err := yamlv3.Unmarshal(raw, &doc); err != nil {
			t.Fatal(err)
		}
		jobs := githubYAMLField(doc.Content[0], "jobs")
		for i := 0; i < len(jobs.Content); i += 2 {
			id := jobs.Content[i].Value
			if id == githubPartitionReporter {
				continue
			}
			if seen[id] || id == githubFinalizerJobID || projection.Collectors[id] != nil {
				t.Fatalf("unexpected worker membership %s in %s", id, path)
			}
			if githubYAMLValue(githubYAMLField(githubYAMLField(jobs, id), "permissions"), "id-token") == "write" {
				t.Fatalf("worker moved signer %s out of entry workflow", id)
			}
			seen[id] = true
		}
	}
	var doc yamlv3.Node
	if err := yamlv3.Unmarshal(entry, &doc); err != nil {
		t.Fatal(err)
	}
	jobs := githubYAMLField(doc.Content[0], "jobs")
	inline := 0
	for id := range projection.Full {
		if id == githubFinalizerJobID {
			continue
		}
		job := githubYAMLField(jobs, id)
		if job != nil {
			if seen[id] || githubYAMLValue(githubYAMLField(job, "permissions"), "id-token") != "write" {
				t.Fatalf("unexpected inline job %s", id)
			}
			seen[id] = true
			inline++
		}
		if !seen[id] {
			t.Fatalf("missing original job %s", id)
		}
	}
	if len(seen) != len(projection.Full)-1 {
		t.Fatal("partitioning added original jobs")
	}
	if githubYAMLField(jobs, githubFinalizerJobID) == nil {
		t.Fatal("missing single entry finalizer")
	}
	if len(jobs.Content)/2 != len(workers)+len(projection.Collectors)+inline+1 {
		t.Fatal("unexpected entry job set")
	}
}

func TestGitHubPartitionMaintainedCapacity(t *testing.T) {
	plan, err := LoadPlanContext(t.Context(), "../../../"+FactoryPlanPath)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join("../../..", GitHubWorkflowPath))
	if err != nil {
		t.Fatal(err)
	}
	entry, workers, err := lowerGitHubPartitions(t.Context(), plan, raw)
	if err != nil {
		t.Fatal(err)
	}
	assertGitHubPartitionMembership(t, plan, entry, workers)
	maxBytes := len(entry)
	for _, child := range workers {
		maxBytes = max(maxBytes, len(child))
	}
	t.Logf("single Run: entry %d bytes, %d workers, largest file %d bytes", len(entry), len(workers), maxBytes)
	if maxBytes >= len(raw) {
		t.Fatal("partitioning did not reduce maximum workflow size")
	}
}

func TestGitHubPartitionDependenciesCrossSigningBoundary(t *testing.T) {
	plan, err := LoadPlanContext(t.Context(), "../../../"+FactoryPlanPath)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join("../../..", GitHubWorkflowPath))
	if err != nil {
		t.Fatal(err)
	}
	entry, workers, err := lowerGitHubPartitions(t.Context(), plan, raw)
	if err != nil {
		t.Fatal(err)
	}
	var doc yamlv3.Node
	if err := yamlv3.Unmarshal(entry, &doc); err != nil {
		t.Fatal(err)
	}
	rootJobs := githubYAMLField(doc.Content[0], "jobs")
	owners := map[string]string{}
	for i := 0; i < len(rootJobs.Content); i += 2 {
		id, job := rootJobs.Content[i].Value, rootJobs.Content[i+1]
		if child, ok := workers[strings.TrimPrefix(githubYAMLValue(job, "uses"), "./")]; ok {
			var worker yamlv3.Node
			if err := yamlv3.Unmarshal(child, &worker); err != nil {
				t.Fatal(err)
			}
			jobs := githubYAMLField(worker.Content[0], "jobs")
			for n := 0; n < len(jobs.Content); n += 2 {
				if jobs.Content[n].Value != githubPartitionReporter {
					owners[jobs.Content[n].Value] = id
				}
			}
		} else {
			owners[id] = id
		}
	}
	projection, err := githubNeedsForPlan(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	toSigner, fromSigner := 0, 0
	for id, parents := range projection.Reduced {
		if id == githubFinalizerJobID || projection.Collectors[id] != nil {
			continue
		}
		caller := githubYAMLField(rootJobs, owners[id])
		needs, err := githubNeedsValues(githubYAMLField(caller, "needs"))
		if err != nil {
			t.Fatal(err)
		}
		for _, parent := range parents {
			if !slices.Contains(needs, owners[parent]) {
				t.Fatalf("%s lost prerequisite %s", id, parent)
			}
			if owners[id] == id && owners[parent] != parent {
				toSigner++
				condition := githubYAMLValue(caller, "if")
				if !strings.Contains(condition, ".outputs.results") || !strings.Contains(condition, "['"+parent+"'].result == 'success'") {
					t.Fatalf("signer %s does not require original producer %s success", id, parent)
				}
			}
			if owners[id] != id && owners[parent] == parent {
				fromSigner++
				input := githubYAMLValue(githubYAMLField(caller, "with"), "dependencies")
				if !strings.Contains(input, "toJSON(needs['"+parent+"'])") {
					t.Fatalf("worker lost inline signer result %s", parent)
				}
			}
		}
	}
	if toSigner == 0 || fromSigner == 0 {
		t.Fatal("fixture did not exercise both directions of the signing boundary")
	}
}
