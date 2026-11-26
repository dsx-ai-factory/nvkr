// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build linux || darwin

package validation

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
)

var fixturePayload = []byte("#!/bin/sh\necho nvkectl\n")

func payloadDigest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func (r *fakeRun) WithDiagnosticOperation(ctx context.Context, observe func(context.Context, lifecycle.LocalDiagnosticDescription) error) error {
	if r.closes > 0 {
		return e.New(e.CodeGateUnmet, "local diagnostic artifacts are closed")
	}
	r.operations++
	if err := observe(ctx, r.description); err != nil {
		return err
	}
	// Lifecycle rechecks the held Run after the callback.
	return r.afterOperation
}

func (r *fakeRun) WriteExecutableMember(_ context.Context, output, member, platform string, maxBytes int64, destination io.Writer) (lifecycle.ExecutableMemberDescription, error) {
	r.members = append(r.members, output+" "+member+" "+platform)
	if r.memberErr != nil {
		return lifecycle.ExecutableMemberDescription{}, r.memberErr
	}
	if maxBytes < int64(len(r.payload)) {
		return lifecycle.ExecutableMemberDescription{}, e.New(e.CodeEngineCapability, "selected executable member exceeds its byte limit")
	}
	size, err := destination.Write(r.payload)
	return lifecycle.ExecutableMemberDescription{Output: output, Member: member, Platform: platform,
		PayloadDigest: payloadDigest(r.payload), Size: int64(size)}, err
}

// fakeExecutor stands in for source import and container execution, which
// sourcegit and pipeline own and test.
type fakeExecutor struct {
	t         *testing.T
	imports   []TestOrigin
	importErr error
	noWorkdir bool
	commands  []pipeline.IsolatedCommand
	results   []error
	during    func(pipeline.IsolatedCommand)
	tick      time.Time
	userErr   error
}

func (f *fakeExecutor) admitUser() error { return f.userErr }

func (f *fakeExecutor) importSource(_ context.Context, _ []string, directory, repository, commit, tree string, _ io.Writer) error {
	f.imports = append(f.imports, TestOrigin{Repository: repository, Commit: commit, Tree: tree})
	if f.importErr != nil {
		return f.importErr
	}
	if f.noWorkdir {
		return os.Mkdir(directory, 0o700)
	}
	return os.MkdirAll(filepath.Join(directory, "cli"), 0o700)
}

func (f *fakeExecutor) run(_ context.Context, command pipeline.IsolatedCommand, stdout, _ io.Writer) error {
	f.commands = append(f.commands, command)
	if command.Offline {
		_, _ = io.WriteString(stdout, "raw test output\n")
	}
	if f.during != nil {
		f.during(command)
	}
	if index := len(f.commands) - 1; index < len(f.results) {
		return f.results[index]
	}
	return nil
}

func (f *fakeExecutor) now() time.Time {
	f.tick = f.tick.Add(time.Second)
	return f.tick
}

func (f *fakeExecutor) executor() suiteExecutor {
	return suiteExecutor{admitUser: f.admitUser, importSource: f.importSource, run: f.run, now: f.now}
}

func exitStatus(t *testing.T, code int) error {
	t.Helper()
	err := exec.Command("sh", "-c", "exit "+strconv.Itoa(code)).Run()
	var status *exec.ExitError
	if !errors.As(err, &status) || status.ExitCode() != code {
		t.Fatalf("exit fixture %d: %v", code, err)
	}
	return e.Wrap(e.CodeBuildFailed, err, "running isolated command")
}

// boundFixture binds the shipped registrations to a Run whose nvkectl build
// subject is the fixture payload.
func boundFixture(t *testing.T, prepared *PreparedValidation) (*BoundValidation, *fakeRun) {
	t.Helper()
	run, opened := fixtureRun(), []string{}
	run.payload = fixturePayload
	run.description.Outputs[0].BuildSubjects[0].Digest = payloadDigest(fixturePayload)
	bound, err := prepared.bind(t.Context(), ".nvkr/runs/build-001", opener(run, &opened))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bound.Close() })
	return bound, run
}

func requireEmptyRoot(t *testing.T, root string) {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("workspace not removed: %v %v", entries, err)
	}
}

func environmentValue(environment []string, name string) (string, bool) {
	for _, binding := range environment {
		if key, value, _ := strings.Cut(binding, "="); key == name {
			return value, true
		}
	}
	return "", false
}

func TestExecuteRunsRegisteredCommand(t *testing.T) {
	bound, run := boundFixture(t, preparedFixture(t))
	suite := bound.Suites()[0]
	root := t.TempDir()
	fake := &fakeExecutor{t: t, tick: time.Date(2026, 11, 24, 0, 0, 0, 0, time.UTC)}
	staged := ""
	fake.during = func(command pipeline.IsolatedCommand) {
		if !command.Offline {
			return
		}
		staged = command.Inputs[0].Source
		raw, err := os.ReadFile(staged)
		info, statErr := os.Stat(staged)
		if err != nil || statErr != nil || !bytes.Equal(raw, fixturePayload) || info.Mode().Perm()&0o222 != 0 {
			t.Errorf("staged input differs or is writable: %v %v", err, statErr)
		}
	}
	var stdout bytes.Buffer
	result, err := bound.execute(t.Context(), suite.ID, ExecuteOptions{WorkspaceRoot: root, Stdout: &stdout}, fake.executor())
	if err != nil {
		t.Fatal(err)
	}
	if result.ID != suite.ID || result.State != ExecutionCompleted || result.Termination != TerminationExited ||
		result.ExitCode == nil || *result.ExitCode != 0 || result.Status != StatusPassed || result.Cleanup != CleanupComplete ||
		!result.StartedAt.Equal(time.Date(2026, 11, 24, 0, 0, 1, 0, time.UTC)) || !result.FinishedAt.After(result.StartedAt) {
		t.Fatalf("unexpected execution: %+v", result)
	}
	if stdout.String() != "raw test output\n" {
		t.Fatalf("raw output changed: %q", stdout.String())
	}
	if len(fake.imports) != 1 || fake.imports[0] != suite.TestOrigin {
		t.Fatalf("test origin not imported exactly: %+v", fake.imports)
	}
	if len(run.members) != 1 || run.members[0] != "nvkectl nvkectl linux/arm64" || run.operations != 1 {
		t.Fatalf("input not streamed once inside one held operation: %v %d", run.members, run.operations)
	}
	if len(fake.commands) != 2 {
		t.Fatalf("commands: %+v", fake.commands)
	}
	acquire, test := fake.commands[0], fake.commands[1]
	for _, command := range fake.commands {
		if command.Image != fixtureImage || command.Platform != "linux/arm64" || command.WorkingDirectory != "cli" ||
			filepath.Dir(command.Source) != filepath.Dir(command.Cache) || filepath.Dir(filepath.Dir(command.Source)) != root {
			t.Fatalf("suite resolution not applied: %+v", command)
		}
		if !slices.IsSorted(command.Environment) {
			t.Fatal("environment is not deterministic", command.Environment)
		}
		// Go state lives in the shared writable cache: the registration, not the
		// executor, declares it.
		for name, want := range map[string]string{"NVKECTL": "/inputs/executable/nvkectl", "NVKECTL_EXPECTED_VERSION": "v1.2.3",
			"GOCACHE": "/cache/go-build", "GOPATH": "/cache/go", "HOME": "/tmp", "GOWORK": "off", "CGO_ENABLED": "0"} {
			if got, ok := environmentValue(command.Environment, name); !ok || got != want {
				t.Fatalf("%s = %q, want %q", name, got, want)
			}
		}
		if len(command.Environment) != len(suite.Environment) {
			t.Fatal("environment bindings added or dropped", command.Environment)
		}
	}
	if acquire.Offline || len(acquire.Inputs) != 0 || !reflect.DeepEqual(acquire.Command, suite.Acquisition.Command) || acquire.Timeout != 30*time.Minute {
		t.Fatalf("acquisition must be networked, without inputs, under its own bound: %+v", acquire)
	}
	if !test.Offline || !reflect.DeepEqual(test.Command, suite.Command) || test.Timeout != 30*time.Minute ||
		!reflect.DeepEqual(test.Inputs, []pipeline.IsolatedMount{{Source: staged, Target: "/inputs/executable/nvkectl"}}) {
		t.Fatalf("test phase lost isolation, command or timeout: %+v", test)
	}
	requireEmptyRoot(t, root)
}

func TestExecuteResolvesRelativeWorkspaceRoot(t *testing.T) {
	bound, _ := boundFixture(t, preparedFixture(t))
	t.Chdir(t.TempDir())
	fake := &fakeExecutor{t: t}
	if _, err := bound.execute(t.Context(), "nvke/nvkectl/contract", ExecuteOptions{WorkspaceRoot: "work"}, fake.executor()); err != nil {
		t.Fatal(err)
	}
	for _, command := range fake.commands {
		mounts := []string{command.Source, command.Cache}
		for _, input := range command.Inputs {
			mounts = append(mounts, input.Source)
		}
		for _, mount := range mounts {
			if !filepath.IsAbs(mount) {
				t.Fatalf("container mount %q is relative", mount)
			}
		}
	}
	requireEmptyRoot(t, "work")
}

func TestExecuteSubstitutesInputReferences(t *testing.T) {
	root := registrationFixture(t)
	raw := strings.Replace(registrationText(t, "nvke/nvkectl/contract"), "timeout: 30m", "timeout: 90s", 1)
	raw = strings.Replace(raw, "  command: [go, mod, download]", "  command: [fetch, \"v=${inputs.executable.version}\"]", 1)
	start := strings.Index(raw, "command:\n  - make")
	end := strings.Index(raw, "environment:")
	raw = raw[:start] + "command: [check, \"--bin=${inputs.executable.path}\", \"${inputs.executable.version}-${inputs.executable.version}\", literal]\n" + raw[end:]
	raw = strings.Replace(raw, "environment:\n", "environment:\n  BOTH: ${inputs.executable.path}:${inputs.executable.version}\n", 1)
	writeCatalogFile(t, filepath.Join(root, "nvke/nvkectl/contract.yaml"), []byte(raw))
	prepared, err := Prepare(t.Context(), root, []string{"nvke/nvkectl/contract"}, "local")
	if err != nil {
		t.Fatal(err)
	}
	bound, _ := boundFixture(t, prepared)
	fake := &fakeExecutor{t: t}
	if _, err := bound.execute(t.Context(), "nvke/nvkectl/contract", ExecuteOptions{WorkspaceRoot: t.TempDir()}, fake.executor()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(fake.commands[0].Command, []string{"fetch", "v=v1.2.3"}) ||
		!reflect.DeepEqual(fake.commands[1].Command, []string{"check", "--bin=/inputs/executable/nvkectl", "v1.2.3-v1.2.3", "literal"}) {
		t.Fatalf("references not substituted as plain text: %v %v", fake.commands[0].Command, fake.commands[1].Command)
	}
	if both, _ := environmentValue(fake.commands[1].Environment, "BOTH"); both != "/inputs/executable/nvkectl:v1.2.3" {
		t.Fatal(both)
	}
	if fake.commands[1].Timeout != 90*time.Second {
		t.Fatal("registration timeout not applied", fake.commands[1].Timeout)
	}
}

func TestExecuteOutcomes(t *testing.T) {
	changeStaged := func(command pipeline.IsolatedCommand) {
		if command.Offline {
			staged := command.Inputs[0].Source
			_ = os.Chmod(staged, 0o700)
			_ = os.WriteFile(staged, []byte("changed"), 0o700)
		}
	}
	timedOut := e.Wrap(e.CodeTimeout, context.DeadlineExceeded, "container execution canceled")
	for _, tc := range []struct {
		name        string
		setup       func(*testing.T, *fakeExecutor, *fakeRun, context.CancelFunc)
		status      SuiteStatus
		termination Termination
		exitCode    int // -1 for none
		code        e.Code
		commands    int
	}{
		{"failed", func(t *testing.T, f *fakeExecutor, _ *fakeRun, _ context.CancelFunc) {
			f.results = []error{nil, exitStatus(t, 2)}
		}, StatusFailed, TerminationExited, 2, e.CodeBuildFailed, 2},
		{"runtime-refused-launch", func(t *testing.T, f *fakeExecutor, _ *fakeRun, _ context.CancelFunc) {
			f.results = []error{nil, exitStatus(t, 125)}
		}, StatusExecutionError, TerminationLaunchError, -1, e.CodeBuildFailed, 2},
		{"command-not-found", func(t *testing.T, f *fakeExecutor, _ *fakeRun, _ context.CancelFunc) {
			f.results = []error{nil, exitStatus(t, 127)}
		}, StatusExecutionError, TerminationLaunchError, -1, e.CodeBuildFailed, 2},
		{"runtime-unavailable", func(_ *testing.T, f *fakeExecutor, _ *fakeRun, _ context.CancelFunc) {
			f.results = []error{nil, e.Wrap(e.CodeBuildFailed, exec.ErrNotFound, "running isolated command")}
		}, StatusExecutionError, TerminationLaunchError, -1, e.CodeBuildFailed, 2},
		{"timeout", func(_ *testing.T, f *fakeExecutor, _ *fakeRun, _ context.CancelFunc) {
			f.results = []error{nil, timedOut}
		}, StatusExecutionError, TerminationTimeout, -1, e.CodeTimeout, 2},
		{"canceled", func(_ *testing.T, f *fakeExecutor, _ *fakeRun, cancel context.CancelFunc) {
			f.results = []error{nil, e.Wrap(e.CodeTimeout, context.Canceled, "container execution canceled")}
			f.during = func(command pipeline.IsolatedCommand) {
				if command.Offline {
					cancel()
				}
			}
		}, StatusExecutionError, TerminationCanceled, -1, e.CodeTimeout, 2},
		{"acquisition-failed", func(t *testing.T, f *fakeExecutor, _ *fakeRun, _ context.CancelFunc) {
			f.results = []error{exitStatus(t, 1)}
		}, StatusExecutionError, "", -1, e.CodeBuildFailed, 1},
		{"acquisition-timeout", func(_ *testing.T, f *fakeExecutor, _ *fakeRun, _ context.CancelFunc) {
			f.results = []error{timedOut}
		}, StatusExecutionError, "", -1, e.CodeTimeout, 1},
		{"source-refused", func(_ *testing.T, f *fakeExecutor, _ *fakeRun, _ context.CancelFunc) {
			f.importErr = e.New(e.CodeFetchFailed, "tree-hash mismatch")
		}, StatusExecutionError, "", -1, e.CodeFetchFailed, 0},
		{"missing-working-directory", func(_ *testing.T, f *fakeExecutor, _ *fakeRun, _ context.CancelFunc) {
			f.noWorkdir = true
		}, StatusExecutionError, "", -1, e.CodeEngineCapability, 0},
		{"member-refused", func(_ *testing.T, _ *fakeExecutor, r *fakeRun, _ context.CancelFunc) {
			r.memberErr = e.New(e.CodeVerifyFailed, "executable member differs from its authenticated build subject")
		}, StatusExecutionError, "", -1, e.CodeVerifyFailed, 0},
		{"staged-bytes-differ", func(_ *testing.T, _ *fakeExecutor, r *fakeRun, _ context.CancelFunc) {
			r.payload = []byte("not the bound subject")
		}, StatusExecutionError, "", -1, e.CodeVerifyFailed, 0},
		{"bytes-changed-during-pass", func(_ *testing.T, f *fakeExecutor, _ *fakeRun, _ context.CancelFunc) {
			f.during = changeStaged
		}, StatusExecutionError, TerminationExited, 0, e.CodeVerifyFailed, 2},
		{"bytes-changed-during-failure", func(t *testing.T, f *fakeExecutor, _ *fakeRun, _ context.CancelFunc) {
			f.results = []error{nil, exitStatus(t, 2)}
			f.during = changeStaged
		}, StatusExecutionError, TerminationExited, 2, e.CodeVerifyFailed, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bound, run := boundFixture(t, preparedFixture(t))
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			fake, root := &fakeExecutor{t: t}, t.TempDir()
			tc.setup(t, fake, run, cancel)
			result, err := bound.execute(ctx, "nvke/nvkectl/contract", ExecuteOptions{WorkspaceRoot: root}, fake.executor())
			catalogCode(t, err, tc.code)
			if result.Status != tc.status || result.Termination != tc.termination || result.State != ExecutionCompleted ||
				result.Cleanup != CleanupComplete || result.Reason == "" || len(fake.commands) != tc.commands {
				t.Fatalf("execution %+v after %d commands", result, len(fake.commands))
			}
			if (tc.exitCode < 0) != (result.ExitCode == nil) || (result.ExitCode != nil && *result.ExitCode != tc.exitCode) {
				t.Fatalf("exit code %v, want %d", result.ExitCode, tc.exitCode)
			}
			requireEmptyRoot(t, root)
		})
	}
}

func TestExecuteRunReleasedDuringSuiteIsNotAPass(t *testing.T) {
	bound, run := boundFixture(t, preparedFixture(t))
	run.afterOperation = e.Wrap(e.CodeTimeout, context.Canceled, "local diagnostic operation canceled")
	result, err := bound.execute(t.Context(), "nvke/nvkectl/contract", ExecuteOptions{WorkspaceRoot: t.TempDir()}, (&fakeExecutor{t: t}).executor())
	catalogCode(t, err, e.CodeTimeout)
	if result.Status != StatusExecutionError || result.Reason == "" || result.Cleanup != CleanupComplete {
		t.Fatalf("a suite whose Run was released reported %+v", result)
	}
}

func TestExecuteReportsCleanupFailureSeparately(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the directory permissions this test uses to make removal fail")
	}
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "passed", true: "failed"}[failed], func(t *testing.T) {
			bound, _ := boundFixture(t, preparedFixture(t))
			fake, root := &fakeExecutor{t: t}, t.TempDir()
			if failed {
				fake.results = []error{nil, exitStatus(t, 2)}
			}
			locked := ""
			fake.during = func(command pipeline.IsolatedCommand) {
				if command.Offline {
					locked = command.Cache
					_ = os.WriteFile(filepath.Join(locked, "entry"), nil, 0o600)
					_ = os.Chmod(locked, 0o500)
				}
			}
			t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })
			result, err := bound.execute(t.Context(), "nvke/nvkectl/contract", ExecuteOptions{WorkspaceRoot: root}, fake.executor())
			if result.Cleanup != CleanupFailed {
				t.Fatalf("cleanup failure not recorded: %+v", result)
			}
			if failed {
				// The suite failure stays primary.
				if result.Status != StatusFailed {
					t.Fatalf("cleanup hid the suite failure: %+v", result)
				}
				catalogCode(t, err, e.CodeBuildFailed)
				var status *exec.ExitError
				if !errors.As(err, &status) {
					t.Fatal("suite exit status lost", err)
				}
				return
			}
			if result.Status != StatusPassed {
				t.Fatalf("cleanup changed the verdict: %+v", result)
			}
			catalogCode(t, err, e.CodeBuildFailed)
		})
	}
}

func TestExecuteRefusesBeforeWork(t *testing.T) {
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	for _, tc := range []struct {
		name   string
		ctx    context.Context
		id     string
		root   func(string) string
		closed bool
		code   e.Code
	}{
		{"nil-context", nil, "nvke/nvkectl/contract", nil, false, e.CodeUsage},
		{"canceled", canceled, "nvke/nvkectl/contract", nil, false, e.CodeTimeout},
		{"unknown-suite", t.Context(), "nvke/nvkectl/other", nil, false, e.CodeUsage},
		{"no-workspace-root", t.Context(), "nvke/nvkectl/contract", func(string) string { return "" }, false, e.CodeUsage},
		{"unsafe-workspace-root", t.Context(), "nvke/nvkectl/contract", func(root string) string { return root + "\n" }, false, e.CodeUsage},
		{"closed-run", t.Context(), "nvke/nvkectl/contract", nil, true, e.CodeGateUnmet},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bound, run := boundFixture(t, preparedFixture(t))
			if tc.closed {
				_ = bound.Close()
			}
			fake, root := &fakeExecutor{t: t}, t.TempDir()
			workspace := root
			if tc.root != nil {
				workspace = tc.root(root)
			}
			result, err := bound.execute(tc.ctx, tc.id, ExecuteOptions{WorkspaceRoot: workspace}, fake.executor())
			catalogCode(t, err, tc.code)
			if result.State != ExecutionNotStarted || result.Status != "" || len(fake.imports) != 0 || len(fake.commands) != 0 || len(run.members) != 0 {
				t.Fatalf("refusal started work: %+v", result)
			}
			requireEmptyRoot(t, root)
		})
	}
	t.Run("root", func(t *testing.T) {
		bound, run := boundFixture(t, preparedFixture(t))
		fake, root := &fakeExecutor{t: t, userErr: e.New(e.CodeEngineCapability, "isolated commands must not run as root")}, t.TempDir()
		result, err := bound.execute(t.Context(), "nvke/nvkectl/contract", ExecuteOptions{WorkspaceRoot: root}, fake.executor())
		catalogCode(t, err, e.CodeEngineCapability)
		if result.State != ExecutionNotStarted || len(fake.imports) != 0 || len(fake.commands) != 0 || len(run.members) != 0 || run.operations != 0 {
			t.Fatalf("root started work: %+v", result)
		}
		requireEmptyRoot(t, root)
	})
	var missing *BoundValidation
	if _, err := missing.Execute(t.Context(), "nvke/nvkectl/contract", ExecuteOptions{WorkspaceRoot: t.TempDir()}); err == nil {
		t.Fatal("nil bound validation executed")
	} else {
		catalogCode(t, err, e.CodeUsage)
	}
}
