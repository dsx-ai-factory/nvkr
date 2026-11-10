// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package cicontract

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"os/exec"
	"path"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/dsx-ai-factory/nvkr/internal/ownedcmd"
)

// The merge gate's steps are classified exactly. Every entry must stay in use,
// so an exception cannot outlive the step it was written for.
var (
	// Jobs only GitHub can run; `make ci-check` does not reproduce them.
	providerOnlyJobs = []string{"analyze"} // CodeQL analysis and result upload.
	// Actions that prepare a job without checking anything.
	provisioningActions = []string{"actions/checkout@", "actions/setup-go@", "actions/setup-node@", "actions/upload-artifact@"}
	// Actions that run a make target's check through a hosted wrapper, with
	// the only inputs they may set: another input could narrow the check.
	targetActions = map[string]struct {
		target string
		inputs []string
	}{
		"golangci/golangci-lint-action@": {"lint", []string{"install-mode", "skip-cache", "version"}},
	}
	// Run steps that prepare sources, install a tool or report results, keyed by job and step name.
	supportSteps = [][2]string{
		{"workflows-regen", "Fetch public NVKE validation inputs"},
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

// TestCompleteLocalGateMatchesMergeGate requires `make ci-check` to run exactly
// the make targets the merge gate runs, read from the real workflow and Makefile.
func TestCompleteLocalGateMatchesMergeGate(t *testing.T) {
	hosted, err := hostedMakeTargets(readMergeGateJobs(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := checkLocalGate(hosted, makePrerequisites(t)); err != nil {
		t.Fatal(err)
	}
}

// TestLocalGateRejectsInventoryDrift applies one edit to the real workflow or
// to a Makefile inventory equal to it, and requires each edit to fail.
func TestLocalGateRejectsInventoryDrift(t *testing.T) {
	step := func(t *testing.T, jobs map[string]mergeGateJob, job, name string) *mergeGateStep {
		t.Helper()
		steps := jobs[job].Steps
		index := slices.IndexFunc(steps, func(step mergeGateStep) bool { return step.Name == name })
		if index < 0 {
			t.Fatalf("merge gate %s has no step %q", job, name)
		}
		return &steps[index]
	}
	workflowCases := []struct {
		name, want string
		edit       func(*testing.T, map[string]mergeGateJob)
	}{
		{"raw command replaces make", `actionlint step "actionlint" must run one make target`,
			func(t *testing.T, jobs map[string]mergeGateJob) {
				step(t, jobs, "actionlint", "actionlint").Run = "actionlint -color"
			}},
		{"make with flags", `check step "vet" must run one make target`,
			func(t *testing.T, jobs map[string]mergeGateJob) { step(t, jobs, "check", "vet").Run = "make -k vet" }},
		{"make with variable", `check step "vet" must run one make target`,
			func(t *testing.T, jobs map[string]mergeGateJob) {
				step(t, jobs, "check", "vet").Run = "make vet GOFLAGS=-short"
			}},
		{"multi-line run", `check step "vet" must run one make target`,
			func(t *testing.T, jobs map[string]mergeGateJob) {
				step(t, jobs, "check", "vet").Run = "make vet\nmake lint"
			}},
		{"pipe swallows failure", `must run one make target`,
			func(t *testing.T, jobs map[string]mergeGateJob) {
				shard := step(t, jobs, "tests", "test (ordinary suite shard)")
				shard.Run += " || true"
			}},
		{"pipe without pipefail shell", `must run one make target`,
			func(t *testing.T, jobs map[string]mergeGateJob) {
				step(t, jobs, "tests", "test (ordinary suite shard)").Shell = ""
			}},
		{"conditional step", `check step "vet" must run unconditionally`,
			func(t *testing.T, jobs map[string]mergeGateJob) {
				step(t, jobs, "check", "vet").If = "github.event_name == 'push'"
			}},
		{"soft failure", `check step "vet" must run unconditionally`,
			func(t *testing.T, jobs map[string]mergeGateJob) { step(t, jobs, "check", "vet").ContinueOnError = true }},
		{"conditional action", `lint step "lint" must run unconditionally`,
			func(t *testing.T, jobs map[string]mergeGateJob) {
				step(t, jobs, "lint", "lint").If = "github.event_name == 'push'"
			}},
		{"soft-failing action", `lint step "lint" must run unconditionally`,
			func(t *testing.T, jobs map[string]mergeGateJob) { step(t, jobs, "lint", "lint").ContinueOnError = true }},
		{"unclassified action", `uses unclassified action example/other-linter@v1`,
			func(t *testing.T, jobs map[string]mergeGateJob) {
				step(t, jobs, "lint", "lint").Uses = "example/other-linter@v1"
			}},
		{"narrowing action input", `sets inputs [install-mode only-new-issues skip-cache version]`,
			func(t *testing.T, jobs map[string]mergeGateJob) {
				step(t, jobs, "lint", "lint").With["only-new-issues"] = true
			}},
		{"provider-only job runs make", `provider-only job analyze step "" contains the word make`,
			func(t *testing.T, jobs map[string]mergeGateJob) {
				job := jobs["analyze"]
				job.Steps = append(job.Steps, mergeGateStep{storageWorkflowStep: storageWorkflowStep{Run: "make vet"}})
				jobs["analyze"] = job
			}},
		{"provider-only job runs flagged make", `provider-only job analyze step "" contains the word make`,
			func(t *testing.T, jobs map[string]mergeGateJob) {
				job := jobs["analyze"]
				job.Steps = append(job.Steps, mergeGateStep{storageWorkflowStep: storageWorkflowStep{Run: "echo start\ntrue && make -k vet GOFLAGS=-short"}})
				jobs["analyze"] = job
			}},
		{"provider-only job runs make by path", `provider-only job analyze step "" contains the word make`,
			func(t *testing.T, jobs map[string]mergeGateJob) {
				job := jobs["analyze"]
				job.Steps = append(job.Steps, mergeGateStep{storageWorkflowStep: storageWorkflowStep{Run: "/usr/bin/make test-race"}})
				jobs["analyze"] = job
			}},
		{"provider-only job: quoted make path", `provider-only job analyze step "" contains the word make`,
			func(t *testing.T, jobs map[string]mergeGateJob) {
				job := jobs["analyze"]
				job.Steps = append(job.Steps, mergeGateStep{storageWorkflowStep: storageWorkflowStep{Run: `'/usr/bin/make' vet`}})
				jobs["analyze"] = job
			}},
		{"provider-only job: double-quoted make", `provider-only job analyze step "" contains the word make`,
			func(t *testing.T, jobs map[string]mergeGateJob) {
				job := jobs["analyze"]
				job.Steps = append(job.Steps, mergeGateStep{storageWorkflowStep: storageWorkflowStep{Run: `"make" vet`}})
				jobs["analyze"] = job
			}},
		{"provider-only job: make in substitution", `provider-only job analyze step "" contains the word make`,
			func(t *testing.T, jobs map[string]mergeGateJob) {
				job := jobs["analyze"]
				job.Steps = append(job.Steps, mergeGateStep{storageWorkflowStep: storageWorkflowStep{Run: `echo $(make vet)`}})
				jobs["analyze"] = job
			}},
		{"provider-only job: backslash-escaped make", `provider-only job analyze step "" contains the word make`,
			func(t *testing.T, jobs map[string]mergeGateJob) {
				job := jobs["analyze"]
				job.Steps = append(job.Steps, mergeGateStep{storageWorkflowStep: storageWorkflowStep{Run: "\\make vet"}})
				jobs["analyze"] = job
			}},
		{"provider-only job: make in backticks", `provider-only job analyze step "" contains the word make`,
			func(t *testing.T, jobs map[string]mergeGateJob) {
				job := jobs["analyze"]
				job.Steps = append(job.Steps, mergeGateStep{storageWorkflowStep: storageWorkflowStep{Run: "echo `make vet`"}})
				jobs["analyze"] = job
			}},
		{"provider-only job: prose mentioning make", `provider-only job analyze step "" contains the word make`,
			func(t *testing.T, jobs map[string]mergeGateJob) {
				job := jobs["analyze"]
				job.Steps = append(job.Steps, mergeGateStep{storageWorkflowStep: storageWorkflowStep{Run: `echo 'Please make sure CodeQL is ready'`}})
				jobs["analyze"] = job
			}},
		{"stale support step", `classification entries match no merge gate step: [step tests/Summarize test timings]`,
			func(t *testing.T, jobs map[string]mergeGateJob) {
				job := jobs["tests"]
				job.Steps = slices.DeleteFunc(job.Steps, func(step mergeGateStep) bool { return step.Name == "Summarize test timings" })
				jobs["tests"] = job
			}},
		{"missing job", `merge gate has no docs job`,
			func(t *testing.T, jobs map[string]mergeGateJob) { delete(jobs, "docs") }},
	}
	for _, tc := range workflowCases {
		t.Run(tc.name, func(t *testing.T) {
			jobs := readMergeGateJobs(t)
			tc.edit(t, jobs)
			_, err := hostedMakeTargets(jobs)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("hostedMakeTargets error = %v, want %q", err, tc.want)
			}
		})
	}

	hosted, err := hostedMakeTargets(readMergeGateJobs(t))
	if err != nil {
		t.Fatal(err)
	}
	inventory := func(extra ...string) []string {
		return append(slices.Concat([]string{localOnlyPrerequisite}, hosted), extra...)
	}
	without := func(target string) []string {
		return slices.DeleteFunc(inventory(), func(candidate string) bool { return candidate == target })
	}
	makeCases := []struct {
		name, want     string
		ciCheck, check []string
	}{
		{"ci-check drops a hosted check", "must run exactly", without("docs-check"), []string{"vet"}},
		{"ci-check adds an unhosted check", "must run exactly", inventory("test-race"), []string{"vet"}},
		{"ci-check lists a check twice", "lists a target twice", inventory("vet"), []string{"vet"}},
		{"ci-check skips the toolchain pin", "must admit the pinned toolchain", without(localOnlyPrerequisite), []string{"vet"}},
		{"check runs what ci-check omits", "make check runs test-race", inventory(), []string{localOnlyPrerequisite, "test-race"}},
	}
	for _, tc := range makeCases {
		t.Run(tc.name, func(t *testing.T) {
			err := checkLocalGate(hosted, map[string][]string{"ci-check": tc.ciCheck, "check": tc.check})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("checkLocalGate error = %v, want %q", err, tc.want)
			}
		})
	}
	if err := checkLocalGate(hosted, map[string][]string{"ci-check": inventory(), "check": {localOnlyPrerequisite, "vet"}}); err != nil {
		t.Fatalf("equal inventories rejected: %v", err)
	}
}

// checkLocalGate compares the merge gate's sorted targets with the ci-check and
// check prerequisites make reports.
func checkLocalGate(hosted []string, prerequisites map[string][]string) error {
	local := prerequisites["ci-check"]
	if !slices.Contains(local, localOnlyPrerequisite) {
		return fmt.Errorf("ci-check must admit the pinned toolchain with %s: %v", localOnlyPrerequisite, local)
	}
	local = slices.DeleteFunc(slices.Clone(local), func(target string) bool { return target == localOnlyPrerequisite })
	slices.Sort(local)
	if len(slices.Compact(slices.Clone(local))) != len(local) {
		return fmt.Errorf("ci-check lists a target twice: %v", local)
	}
	if !slices.Equal(hosted, local) {
		return fmt.Errorf("make ci-check must run exactly the merge gate's locally reproducible checks:\nmerge gate %v\nci-check   %v", hosted, local)
	}
	for _, target := range prerequisites["check"] {
		if target != localOnlyPrerequisite && !slices.Contains(local, target) {
			return fmt.Errorf("make check runs %s, which make ci-check omits", target)
		}
	}
	return nil
}

// hostedMakeTargets returns the sorted local targets the merge gate's check
// jobs run. A step that is neither provisioning, a classified support step,
// nor a single make invocation is an error, so a check added to the workflow
// outside make cannot escape the local inventory.
func hostedMakeTargets(jobs map[string]mergeGateJob) ([]string, error) {
	used := map[string]bool{}
	targets := map[string]bool{}
	for _, pair := range mergeGatePairs {
		for _, name := range pair.jobs {
			job, ok := jobs[name]
			if !ok {
				return nil, fmt.Errorf("merge gate has no %s job", name)
			}
			if slices.Contains(providerOnlyJobs, name) {
				used["job "+name] = true
				for _, step := range job.Steps {
					// Any make word or path, quoted or not, could hide a local check here.
					// This is a word match, not a shell parser: it fails closed, so prose
					// or a comment containing the word make is rejected too.
					if slices.ContainsFunc(strings.FieldsFunc(step.Run, isShellSeparator), func(word string) bool {
						return path.Base(word) == "make"
					}) {
						return nil, fmt.Errorf("provider-only job %s step %q contains the word make, which provider-only steps may not use; move any local check to a reproducible job", name, step.Name)
					}
				}
				continue
			}
			for _, step := range job.Steps {
				if step.Uses != "" {
					if slices.ContainsFunc(provisioningActions, func(prefix string) bool { return strings.HasPrefix(step.Uses, prefix) }) {
						continue
					}
					target, ok := "", false
					for prefix, action := range targetActions {
						if strings.HasPrefix(step.Uses, prefix) {
							inputs := slices.Sorted(maps.Keys(step.With))
							if !slices.Equal(inputs, action.inputs) {
								return nil, fmt.Errorf("%s step %q sets inputs %v, want exactly %v", name, step.Name, inputs, action.inputs)
							}
							target, ok = action.target, true
							used["action "+prefix] = true
						}
					}
					if !ok {
						return nil, fmt.Errorf("%s step %q uses unclassified action %s", name, step.Name, step.Uses)
					}
					if err := requireUnconditional(name, step); err != nil {
						return nil, err
					}
					targets[target] = true
					continue
				}
				if slices.Contains(supportSteps, [2]string{name, step.Name}) {
					used["step "+name+"/"+step.Name] = true
					continue
				}
				target, ok := makeStepTarget(step)
				if !ok {
					return nil, fmt.Errorf("%s step %q must run one make target: %q", name, step.Name, step.Run)
				}
				if err := requireUnconditional(name, step); err != nil {
					return nil, err
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
		return nil, fmt.Errorf("classification entries match no merge gate step: %v", stale)
	}
	return slices.Sorted(maps.Keys(targets)), nil
}

func isShellSeparator(r rune) bool {
	return unicode.IsSpace(r) || strings.ContainsRune(";&|()`$'\"\\", r)
}

func appendUnused(stale []string, used map[string]bool, key string) []string {
	if !used[key] {
		return append(stale, key)
	}
	return stale
}

// requireUnconditional rejects a counted step that may not run or whose
// failure would not fail its job.
func requireUnconditional(job string, step mergeGateStep) error {
	if step.If != "" || step.ContinueOnError != nil && step.ContinueOnError != false {
		return fmt.Errorf("%s step %q must run unconditionally and propagate failures", job, step.Name)
	}
	return nil
}

// makeStepTarget accepts `make <target>`, optionally piped into one tee to
// retain its output under bash, whose pipefail propagates make's failure.
func makeStepTarget(step mergeGateStep) (string, bool) {
	run := strings.TrimSpace(step.Run)
	if strings.Contains(run, "\n") {
		return "", false
	}
	command, rest, piped := strings.Cut(run, "|")
	if piped {
		tee := strings.Fields(rest)
		if step.Shell != "bash" || len(tee) != 2 || tee[0] != "tee" || strings.ContainsAny(rest, ";&|<>`") {
			return "", false
		}
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
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	// Parsing evaluates $(shell ...); the owned process group stops any such
	// descendant and bounds the pipe wait when the deadline passes.
	err := ownedcmd.Run(ctx, command)
	// Question mode exits 1 because phony targets are never up to date; 2 is an error.
	var exitError *exec.ExitError
	if err != nil && !(errors.As(err, &exitError) && exitError.ExitCode() == 1) {
		t.Fatalf("make could not read the Makefile database: %v\n%s", err, stderr.Bytes())
	}
	prerequisites := map[string][]string{}
	for line := range strings.Lines(stdout.String()) {
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
