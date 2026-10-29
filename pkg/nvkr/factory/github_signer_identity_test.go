// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	yamlv3 "go.yaml.in/yaml/v3"
)

const githubSignerTestRepository = "https://github.com/dsx-ai-factory/nvkr/"

// Fulcio derives the keyless certificate identity from the workflow file that
// mints the OIDC token. Every committed producer bundle must keep its OIDC jobs
// in the entry workflow its authored signer policy names, and that policy must
// admit no other workflow file, including the entry's own partition workers.
func TestGitHubCommittedSignerIdentitiesStayOnEntryWorkflow(t *testing.T) {
	root := "../../.."
	plans, err := filepath.Glob(filepath.Join(root, ".github/nvkr-*/plan.json"))
	if err != nil || len(plans) == 0 {
		t.Fatalf("no committed producer Plans: %v", err)
	}
	workflows, err := filepath.Glob(filepath.Join(root, ".github/workflows/*.y*ml"))
	if err != nil || len(workflows) == 0 {
		t.Fatalf("no committed workflows: %v", err)
	}
	for _, planPath := range plans {
		relative, err := filepath.Rel(root, planPath)
		if err != nil {
			t.Fatal(err)
		}
		t.Run(relative, func(t *testing.T) {
			plan, err := LoadPlanContext(t.Context(), planPath)
			if err != nil {
				t.Fatal(err)
			}
			entry := githubLayoutFor(plan).workflowPath
			policy := plan.Environment.Signing.Identity
			if !strings.HasPrefix(policy, "^") || !strings.HasSuffix(policy, "$") {
				t.Fatalf("signer policy %q is not anchored", policy)
			}
			matcher := regexp.MustCompile(policy)
			ref := "@refs/heads/main"
			if plan.Environment.GitHubDevelopment {
				ref = "@refs/heads/feature/signer"
			}
			if !matcher.MatchString(githubSignerTestRepository + entry + ref) {
				t.Fatalf("signer policy %q does not admit entry workflow %s", policy, entry)
			}
			signers := 0
			for _, workflow := range workflows {
				name := ".github/workflows/" + path.Base(workflow)
				if name != entry && matcher.MatchString(githubSignerTestRepository+name+ref) {
					t.Fatalf("signer policy %q also admits %s", policy, name)
				}
				if name != entry && !strings.HasPrefix(name, strings.TrimSuffix(entry, ".yml")+"__") {
					continue
				}
				raw, err := os.ReadFile(workflow)
				if err != nil {
					t.Fatal(err)
				}
				for _, id := range githubOIDCJobs(t, raw) {
					if name != entry {
						t.Fatalf("%s mints OIDC in %s instead of the entry workflow", id, name)
					}
					signers++
				}
			}
			if plan.Environment.Signing.Mode == SigningModeCosign && signers == 0 {
				t.Fatal("cosign bundle has no entry signer jobs")
			}
		})
	}
}

func githubOIDCJobs(t *testing.T, raw []byte) []string {
	t.Helper()
	var document yamlv3.Node
	if err := yamlv3.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	root, err := githubResolveAliases(document.Content[0], map[*yamlv3.Node]bool{})
	if err != nil {
		t.Fatal(err)
	}
	if githubYAMLField(githubYAMLField(root, "permissions"), "id-token") != nil {
		t.Fatal("workflow-level OIDC permission")
	}
	var ids []string
	jobs := githubYAMLField(root, "jobs")
	for n := 0; n < len(jobs.Content); n += 2 {
		if githubYAMLHasKey(githubYAMLField(jobs.Content[n+1], "permissions"), "id-token") {
			ids = append(ids, jobs.Content[n].Value)
		}
	}
	return ids
}

// Workers pre-project consumer fragments. Resolve each forwarded fragment back
// to job IDs and require the original collector and dependency membership.
func TestGitHubPartitionForwardingResolvesExactMembers(t *testing.T) {
	root := "../../.."
	plan, err := LoadPlanContext(t.Context(), filepath.Join(root, ".github/nvkr-dev/plan.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Environment.GitHubExecutionPartitions {
		t.Skip("committed development bundle is not partitioned")
	}
	layout := githubLayoutFor(plan)
	entry := githubForwardingJobs(t, filepath.Join(root, layout.workflowPath))
	reporters := map[string]*yamlv3.Node{}
	referenced := map[string][]string{}
	dependencyKey := regexp.MustCompile(`fromJSON\(inputs\.dependencies\)\['([^']+)'\]`)
	for n := 0; n < len(entry.Content); n += 2 {
		id, job := entry.Content[n].Value, entry.Content[n+1]
		uses := githubYAMLValue(job, "uses")
		if uses == "" {
			continue
		}
		worker := githubForwardingJobs(t, filepath.Join(root, strings.TrimPrefix(uses, "./")))
		reporters[id] = githubYAMLField(githubYAMLField(worker, githubPartitionReporter), "outputs")
		for _, match := range dependencyKey.FindAllStringSubmatch(githubForwardingText(t, worker), -1) {
			referenced[id] = append(referenced[id], match[1])
		}
	}
	projection, err := githubNeedsForPlan(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if len(projection.Collectors) == 0 || len(reporters) == 0 {
		t.Fatal("fixture does not exercise partitioned collectors")
	}
	for id, members := range projection.Collectors {
		outputs := githubYAMLField(githubYAMLField(entry, id), "outputs")
		want := slices.Sorted(slices.Values(members))
		if got := githubForwardedMembers(t, githubYAMLValue(outputs, "results"), reporters, `"([^"]+)":\{\d+\}`, `toJSON\(needs\['([^']+)'\]\)`); !slices.Equal(got, want) {
			t.Fatalf("collector %s forwards results for %v, want %v", id, got, want)
		}
		if got := githubForwardedMembers(t, githubYAMLValue(outputs, "statuses"), reporters, "", `needs\['([^']+)'\]\.result`); !slices.Equal(got, want) {
			t.Fatalf("collector %s forwards statuses for %v, want %v", id, got, want)
		}
	}
	for id, keys := range referenced {
		got := githubForwardedMembers(t, githubYAMLValue(githubYAMLField(githubYAMLField(entry, id), "with"), "dependencies"), reporters, `"([^"]+)":\{\d+\}`, `toJSON\(needs\['([^']+)'\]\)`)
		for _, key := range keys {
			if _, found := slices.BinarySearch(got, key); !found {
				t.Fatalf("worker %s reads dependency %s that its caller does not forward", id, key)
			}
		}
	}
}

// githubForwardedMembers resolves one entry format expression: inline arguments
// name a job directly, worker arguments name one exported reporter fragment.
func githubForwardedMembers(t *testing.T, expression string, reporters map[string]*yamlv3.Node, field, member string) []string {
	t.Helper()
	fragment := regexp.MustCompile(`^\(needs\['([^']+)'\]\.outputs\['([^']+)'\] \|\| '([^']*)'\)$`)
	direct := regexp.MustCompile("^" + member + "$")
	var members []string
	for _, argument := range githubFormatArguments(t, expression) {
		if match := fragment.FindStringSubmatch(argument); match != nil {
			value := githubYAMLValue(reporters[match[1]], match[2])
			if value == "" {
				t.Fatalf("missing worker export %s.%s", match[1], match[2])
			}
			arguments := githubFormatArguments(t, value)
			if field != "" {
				format := regexp.MustCompile(`format\('([^']*)'`).FindStringSubmatch(value)[1]
				keys := regexp.MustCompile(field).FindAllStringSubmatch(format, -1)
				if len(keys) != len(arguments) {
					t.Fatalf("worker export %s.%s has mismatched fields", match[1], match[2])
				}
				for n, key := range keys {
					if got := direct.FindStringSubmatch(arguments[n]); got == nil || got[1] != key[1] {
						t.Fatalf("worker export %s.%s labels %s with another job", match[1], match[2], key[1])
					}
				}
			}
			var exported, missing []string
			for _, nested := range arguments {
				got := direct.FindStringSubmatch(nested)
				if got == nil {
					t.Fatalf("unexpected worker argument %q", nested)
				}
				exported = append(exported, got[1])
				missing = append(missing, `"`+got[1]+`":null`)
			}
			// An unreported worker must degrade to the per-member null results
			// and empty statuses that the replaced projection produced.
			want := strings.Repeat(" ", len(exported)-1)
			if field != "" {
				want = strings.Join(missing, ",")
			}
			if match[3] != want {
				t.Fatalf("worker export %s.%s fallback %q, want %q", match[1], match[2], match[3], want)
			}
			members = append(members, exported...)
			continue
		}
		got := direct.FindStringSubmatch(argument)
		if got == nil {
			t.Fatalf("unexpected entry argument %q", argument)
		}
		members = append(members, got[1])
	}
	slices.Sort(members)
	if len(slices.Compact(slices.Clone(members))) != len(members) {
		t.Fatalf("expression forwards a job more than once: %v", members)
	}
	return members
}

func githubFormatArguments(t *testing.T, expression string) []string {
	t.Helper()
	match := regexp.MustCompile(`^\$\{\{ format\('[^']*', (.*)\) \}\}$`).FindStringSubmatch(expression)
	if match == nil {
		t.Fatalf("unexpected forwarding expression %q", expression)
	}
	return strings.Split(match[1], ", ")
}

func githubForwardingJobs(t *testing.T, path string) *yamlv3.Node {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var document yamlv3.Node
	if err := yamlv3.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	root, err := githubResolveAliases(document.Content[0], map[*yamlv3.Node]bool{})
	if err != nil {
		t.Fatal(err)
	}
	return githubYAMLField(root, "jobs")
}

func githubForwardingText(t *testing.T, node *yamlv3.Node) string {
	t.Helper()
	raw, err := yamlv3.Marshal(node)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestGitHubYAMLHasKeyFindsAliasedOIDCPermission(t *testing.T) {
	for _, test := range []struct {
		name string
		raw  string
		want bool
	}{
		{"absent", "jobs:\n  build:\n    permissions:\n      contents: read\n", false},
		{"value only", "jobs:\n  build:\n    name: id-token\n", false},
		{"nested", "jobs:\n  build:\n    permissions:\n      id-token: write\n", true},
		{"aliased", "x: &p\n  id-token: write\njobs:\n  build:\n    permissions: *p\n", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var document yamlv3.Node
			if err := yamlv3.Unmarshal([]byte(test.raw), &document); err != nil {
				t.Fatal(err)
			}
			if got := githubYAMLHasKey(githubYAMLField(document.Content[0], "jobs"), "id-token"); got != test.want {
				t.Fatalf("githubYAMLHasKey = %t, want %t", got, test.want)
			}
		})
	}
}
