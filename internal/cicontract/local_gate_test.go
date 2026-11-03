// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package cicontract

import (
	"context"
	"errors"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"
)

// The merge gate's steps are classified exactly. Every entry must stay in use,
// so an exception cannot outlive the step it was written for.
var (
	// Jobs only GitHub can run; `make ci-check` does not reproduce them.
	providerOnlyJobs = []string{"analyze"} // CodeQL analysis and result upload.
	// Actions that prepare a job without checking anything.
	provisioningActions = []string{"actions/checkout@", "actions/setup-go@", "actions/setup-node@", "actions/upload-artifact@"}
	// Actions that run a make target's check through a hosted wrapper.
	targetActions = map[string]string{"golangci/golangci-lint-action@": "lint"}
	// Run steps that install a tool or report results, keyed by job and step name.
	supportSteps = [][2]string{
		{"docs", "Install Fern CLI"},
		{"lint", "Install checksum-pinned golangci-lint"},
		{"tests", "Summarize test timings"},
	}
	// Hosted targets that run a slice of a local target.
	hostedSlices = map[string]string{"test-shard": "test"}
)

// localOnlyPrerequisite admits the toolchain locally; hosted setup-go installs
// the .go-version patch instead.
const localOnlyPrerequisite = "go-version-check"

func TestCompleteLocalGateMatchesMergeGate(t *testing.T) {
	hosted := hostedMakeTargets(t, readMergeGateJobs(t))
	prerequisites := makePrerequisites(t)
	local := prerequisites["ci-check"]
	if !slices.Contains(local, localOnlyPrerequisite) {
		t.Fatalf("ci-check must admit the pinned toolchain with %s: %v", localOnlyPrerequisite, local)
	}
	local = slices.DeleteFunc(slices.Clone(local), func(target string) bool { return target == localOnlyPrerequisite })
	slices.Sort(local)
	if len(slices.Compact(slices.Clone(local))) != len(local) {
		t.Fatalf("ci-check lists a target twice: %v", local)
	}
	if !slices.Equal(hosted, local) {
		t.Fatalf("make ci-check must run exactly the merge gate's locally reproducible checks:\nmerge gate %v\nci-check   %v", hosted, local)
	}
	for _, target := range prerequisites["check"] {
		if target != localOnlyPrerequisite && !slices.Contains(local, target) {
			t.Fatalf("make check runs %s, which make ci-check omits", target)
		}
	}
}

// hostedMakeTargets returns the sorted local targets the merge gate's check
// jobs run. A step that is neither provisioning, a classified support step,
// nor a single make invocation fails the test, so a check added to the
// workflow outside make cannot escape the local inventory.
func hostedMakeTargets(t *testing.T, jobs map[string]mergeGateJob) []string {
	t.Helper()
	used := map[string]bool{}
	targets := map[string]bool{}
	for _, pair := range mergeGatePairs {
		for _, name := range pair.jobs {
			job, ok := jobs[name]
			if !ok {
				t.Fatalf("merge gate has no %s job", name)
			}
			if slices.Contains(providerOnlyJobs, name) {
				used["job "+name] = true
				continue
			}
			for _, step := range job.Steps {
				if step.Uses != "" {
					if slices.ContainsFunc(provisioningActions, func(prefix string) bool { return strings.HasPrefix(step.Uses, prefix) }) {
						continue
					}
					target, ok := "", false
					for prefix, candidate := range targetActions {
						if strings.HasPrefix(step.Uses, prefix) {
							target, ok = candidate, true
							used["action "+prefix] = true
						}
					}
					if !ok {
						t.Fatalf("%s step %q uses unclassified action %s", name, step.Name, step.Uses)
					}
					targets[target] = true
					continue
				}
				if slices.Contains(supportSteps, [2]string{name, step.Name}) {
					used["step "+name+"/"+step.Name] = true
					continue
				}
				target, ok := makeStepTarget(step.Run)
				if !ok {
					t.Fatalf("%s step %q must run one make target: %q", name, step.Name, step.Run)
				}
				if local, sliced := hostedSlices[target]; sliced {
					used["slice "+target] = true
					target = local
				}
				targets[target] = true
			}
		}
	}
	var stale []string
	for _, name := range providerOnlyJobs {
		stale = appendUnused(stale, used, "job "+name)
	}
	for prefix := range targetActions {
		stale = appendUnused(stale, used, "action "+prefix)
	}
	for _, step := range supportSteps {
		stale = appendUnused(stale, used, "step "+step[0]+"/"+step[1])
	}
	for target := range hostedSlices {
		stale = appendUnused(stale, used, "slice "+target)
	}
	if len(stale) > 0 {
		slices.Sort(stale)
		t.Fatalf("classification entries match no merge gate step: %v", stale)
	}
	result := make([]string, 0, len(targets))
	for target := range targets {
		result = append(result, target)
	}
	slices.Sort(result)
	return result
}

func appendUnused(stale []string, used map[string]bool, key string) []string {
	if !used[key] {
		return append(stale, key)
	}
	return stale
}

// makeStepTarget accepts `make <target>`, optionally piped into tee to retain
// its output.
func makeStepTarget(run string) (string, bool) {
	run = strings.TrimSpace(run)
	if strings.Contains(run, "\n") {
		return "", false
	}
	command, rest, piped := strings.Cut(run, "|")
	if piped && !strings.HasPrefix(strings.TrimSpace(rest), "tee ") {
		return "", false
	}
	fields := strings.Fields(command)
	if len(fields) != 2 || fields[0] != "make" || strings.HasPrefix(fields[1], "-") || strings.Contains(fields[1], "=") {
		return "", false
	}
	return fields[1], true
}

// makePrerequisites reads the check and ci-check prerequisites from make's own
// parsed database, after variable expansion, without running any recipe.
func makePrerequisites(t *testing.T) map[string][]string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "make", "-C", "../..", "--no-print-directory", "-pRrq", "ci-check")
	// An enclosing make (for example `make test`) must not pass its flags down.
	command.Env = append(command.Environ(), "MAKEFLAGS=", "MFLAGS=", "MAKELEVEL=")
	output, err := command.Output()
	// Question mode exits 1 because phony targets are never up to date; 2 is an error.
	var exitError *exec.ExitError
	if err != nil && !(errors.As(err, &exitError) && exitError.ExitCode() == 1) {
		t.Fatalf("make could not read the Makefile database: %v", err)
	}
	prerequisites := map[string][]string{}
	for line := range strings.Lines(string(output)) {
		for _, target := range []string{"check", "ci-check"} {
			if rest, ok := strings.CutPrefix(strings.TrimSuffix(line, "\n"), target+":"); ok && !strings.HasPrefix(rest, "=") {
				if _, seen := prerequisites[target]; seen {
					t.Fatalf("make database lists %s twice", target)
				}
				prerequisites[target] = strings.Fields(rest)
			}
		}
	}
	for _, target := range []string{"check", "ci-check"} {
		if len(prerequisites[target]) == 0 {
			t.Fatalf("make database has no prerequisites for %s", target)
		}
	}
	return prerequisites
}
