// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/testresults"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/validation"
	"github.com/urfave/cli/v2"
)

func validationCLI(run validationRunner, writer io.Writer) *cli.App {
	command := cmdValidateWithRunner(run)
	configureCommandUsage(command)
	return &cli.App{Commands: []*cli.Command{command}, Writer: writer, ErrWriter: io.Discard}
}
func validationOutput() testresults.Output {
	return testresults.Output{SchemaVersion: testresults.SchemaVersion, CaptureStatus: testresults.CaptureComplete,
		Producer: testresults.ProducerIdentity{Name: "upstream-adapter", Revision: testresults.Metadata{Status: "unknown"}},
		Suite:    testresults.SuiteSelection{Name: "kube-conformance", Selection: []string{"conformance"}, Revision: testresults.Metadata{Status: "unknown"}},
		Reports:  []testresults.ReportReference{{Format: testresults.FormatJUnit, Profile: "ginkgo", Reference: "/private/original.xml", Digest: "sha256:" + strings.Repeat("a", 64), ByteLength: 42}}}
}
func TestValidateCLIAdmissionBeforeRunner(t *testing.T) {
	for _, args := range [][]string{nil, {"--config", " "}, {"--config", "profile.json", "--workspace", ""}, {"--config", "profile.json", ""}, {"--config", "profile.json", "first", "second"}, {"--config", "profile.json", "--run", qualificationTestRunURL}, {"--config", "profile.json", "--policy", "policy.yaml"}, {"--config", "profile.json", "--skip-authentication"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			called := false
			var output bytes.Buffer
			app := validationCLI(func(context.Context, string, string, lifecycle.QualificationRuntimeOptions) ([]testresults.Output, error) {
				called = true
				return nil, nil
			}, &output)
			err := app.RunContext(t.Context(), append([]string{"nvkr", "validate"}, args...))
			if cliErrorCode(err) != e.CodeUsage || called || output.Len() != 0 {
				t.Fatalf("invalid input reached work: called%v output%q error%v", called, output.String(), err)
			}
		})
	}
}
func TestValidateCLIForwardsConfiguredSelectionAndPartialOutputs(t *testing.T) {
	type callerKey struct{}
	for _, selected := range []string{"", "cluster-conformance"} {
		t.Run(selected, func(t *testing.T) {
			t.Setenv("NVKR_GITHUB_TOKEN", "explicit-token")
			t.Setenv("NVKR_REGISTRY", "registry.example.test")
			t.Setenv("NVKR_REGISTRY_USER", "explicit-user")
			t.Setenv("NVKR_REGISTRY_PASSWORD", "explicit-secret")
			original := validationOutput()
			var runErr error
			workspace := ".nvkr/validation"
			args := []string{"nvkr", "validate", "--config", "profile.json"}
			if selected != "" {
				runErr = e.New(e.CodeVerifyFailed, "upstream failed")
				original.CaptureStatus = testresults.CapturePartial
				workspace = "private-validation"
				args = append(args, "--workspace", workspace, selected)
			}
			ctx, cancel := context.WithCancel(context.WithValue(t.Context(), callerKey{}, true))
			defer cancel()
			calls := 0
			var output bytes.Buffer
			app := validationCLI(func(got context.Context, name, config string, options lifecycle.QualificationRuntimeOptions) ([]testresults.Output, error) {
				calls++
				if got.Value(callerKey{}) != true {
					t.Fatal("runner context lost a caller value")
				}
				if got.Err() != nil || name != selected || config != "profile.json" || options.WorkspaceRoot != workspace || options.GitHubToken != "explicit-token" || options.Credential.Registry != "registry.example.test" || options.Registry.Password != "explicit-secret" || options.Registry.Anonymous {
					t.Fatal("changed selection, context or explicit credentials")
				}
				cancel()
				if !errors.Is(got.Err(), context.Canceled) {
					t.Fatalf("runner context did not follow caller cancellation: %v", got.Err())
				}
				return []testresults.Output{original}, runErr
			}, &output)
			err := app.RunContext(ctx, args)
			document, encodeErr := testresults.Encode(original)
			if encodeErr != nil {
				t.Fatal(encodeErr)
			}
			want, marshalErr := canonical.Marshal([]json.RawMessage{document})
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			if calls != 1 || !errors.Is(err, runErr) || !bytes.Equal(output.Bytes(), append(want, '\n')) {
				t.Fatalf("changed results: calls%d error%v output%q", calls, err, output.String())
			}
			if strings.Contains(output.String(), "explicit-secret") || strings.Contains(output.String(), "explicit-token") {
				t.Fatal("credential leaked")
			}
		})
	}
}
func TestValidateCLIUnsupportedSelectionRemainsOwnerError(t *testing.T) {
	var output bytes.Buffer
	refused := e.New(e.CodeUnsupported, "check not configured")
	app := validationCLI(func(_ context.Context, name, _ string, _ lifecycle.QualificationRuntimeOptions) ([]testresults.Output, error) {
		if name != "custom-check" {
			t.Fatal("selection changed")
		}
		return nil, refused
	}, &output)
	err := app.RunContext(t.Context(), []string{"nvkr", "validate", "--config", "profile.json", "custom-check"})
	if !errors.Is(err, refused) || output.Len() != 0 {
		t.Fatalf("invented output or lost error: %v %q", err, output.String())
	}
}

type validationShortWriter struct{}

func (validationShortWriter) Write(p []byte) (int, error) { return len(p) - 1, nil }
func TestValidateCLIPresentationFailurePreservesProducerError(t *testing.T) {
	for _, tc := range []struct {
		name    string
		writer  io.Writer
		invalid bool
		cause   error
	}{{"writer", qualificationFailedWriter{}, false, io.ErrClosedPipe}, {"short writer", validationShortWriter{}, false, io.ErrShortWrite}, {"encoding", io.Discard, true, nil}} {
		t.Run(tc.name, func(t *testing.T) {
			original := e.New(e.CodeVerifyFailed, "producer failure")
			app := validationCLI(func(context.Context, string, string, lifecycle.QualificationRuntimeOptions) ([]testresults.Output, error) {
				output := validationOutput()
				if tc.invalid {
					output.Producer.Name = string([]byte{0xff})
				}
				return []testresults.Output{output}, original
			}, tc.writer)
			err := app.RunContext(t.Context(), []string{"nvkr", "validate", "--config", "profile.json"})
			if !errors.Is(err, original) || e.ExitCode(err) != e.ExitCode(original) || tc.cause != nil && !errors.Is(err, tc.cause) {
				t.Fatalf("lost primary producer failure: %v", err)
			}
		})
	}
}
func TestValidateCLIRegisteredHelp(t *testing.T) {
	app := newApp()
	var output bytes.Buffer
	app.Writer = &output
	if err := app.RunContext(t.Context(), []string{"nvkr", "validate", "--help"}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"[check-name]", "--config", "--workspace", ".nvkr/validation", "without qualification signing"} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("missing help %q", want)
		}
	}
	if strings.Contains(output.String(), "--run") || strings.Contains(output.String(), "--policy") {
		t.Fatal("generic validate exposes source-provider flags")
	}
}

func TestValidateCLIExplicitRegistryAuthenticationMode(t *testing.T) {
	for _, tc := range []struct {
		name, host, user, password string
		anonymous, refused         bool
	}{
		{name: "anonymous", anonymous: true},
		{name: "scoped credentials", host: "registry.example.test", user: "user", password: "password"},
		{name: "host only", host: "registry.example.test", refused: true},
		{name: "user only", user: "user", refused: true},
		{name: "password only", password: "password", refused: true},
		{name: "credentials without host", user: "user", password: "password", refused: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("NVKR_GITHUB_TOKEN", "explicit-provider-token")
			t.Setenv("NVKR_REGISTRY", tc.host)
			t.Setenv("NVKR_REGISTRY_USER", tc.user)
			t.Setenv("NVKR_REGISTRY_PASSWORD", tc.password)
			var output bytes.Buffer
			var ownerErr error
			if tc.refused {
				ownerErr = e.New(e.CodeUsage, "owning adapter rejected credential shape")
			}
			app := validationCLI(func(_ context.Context, _, _ string, options lifecycle.QualificationRuntimeOptions) ([]testresults.Output, error) {
				if options.GitHubToken != "explicit-provider-token" || options.Registry.Anonymous != tc.anonymous || options.Credential.Registry != tc.host || options.Credential.Username != tc.user || options.Registry.Password != tc.password {
					t.Fatal("credentials silently defaulted or lost")
				}
				return nil, ownerErr
			}, &output)
			err := app.RunContext(t.Context(), []string{"nvkr", "validate", "--config", "profile.json"})
			if !errors.Is(err, ownerErr) || output.Len() != 0 {
				t.Fatalf("owner refusal changed: %v %q", err, output.String())
			}
		})
	}
}

func TestValidateCLIProgressStaysOnStderrWithUnchangedJSON(t *testing.T) {
	var output, diagnostics bytes.Buffer
	app := validationCLI(func(ctx context.Context, _, _ string, _ lifecycle.QualificationRuntimeOptions) ([]testresults.Output, error) {
		reporter := validation.ReporterFrom(ctx)
		if reporter == nil {
			t.Fatal("runner context carried no progress reporter")
		}
		reporter(validation.Progress{Check: "cluster-conformance", Phase: validation.PhaseRunning})
		reporter(validation.Progress{Check: "cluster-conformance", Phase: validation.PhaseCompleted})
		return []testresults.Output{validationOutput()}, nil
	}, &output)
	app.ErrWriter = &diagnostics
	err := app.RunContext(t.Context(), []string{"nvkr", "validate", "--config", "profile.json"})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := diagnostics.String(), "validation check cluster-conformance: running\nvalidation check cluster-conformance: completed\n"; got != want {
		t.Fatalf("progress lines %q want %q", got, want)
	}
	requireCanonicalValidationOutput(t, output.Bytes())
}

func TestValidateCLIProgressPreservesOwnerFailure(t *testing.T) {
	var output, diagnostics bytes.Buffer
	original := e.New(e.CodeVerifyFailed, "producer failure with private /workspace/secret detail")
	app := validationCLI(func(ctx context.Context, _, _ string, _ lifecycle.QualificationRuntimeOptions) ([]testresults.Output, error) {
		reporter := validation.ReporterFrom(ctx)
		reporter(validation.Progress{Check: "cluster-conformance", Phase: validation.PhaseRunning})
		reporter(validation.Progress{Check: "cluster-conformance", Phase: validation.PhaseFailed})
		return []testresults.Output{validationOutput()}, original
	}, &output)
	app.ErrWriter = &diagnostics
	err := app.RunContext(t.Context(), []string{"nvkr", "validate", "--config", "profile.json"})
	if !errors.Is(err, original) || e.ExitCode(err) != e.ExitCode(original) {
		t.Fatalf("owner failure changed: %v", err)
	}
	if got, want := diagnostics.String(), "validation check cluster-conformance: running\nvalidation check cluster-conformance: failed\n"; got != want {
		t.Fatalf("progress lines %q want %q", got, want)
	}
	requireCanonicalValidationOutput(t, output.Bytes())
}

func TestValidateCLIProgressPreservesResultsWithClosedStderr(t *testing.T) {
	const fixture = "NVKR_TEST_VALIDATE_CLOSED_STDERR"
	if scenario := os.Getenv(fixture); scenario != "" {
		var owner error
		if scenario == "failure" {
			owner = e.New(e.CodeVerifyFailed, "fixture owner error")
		}
		assertValidationProgressRetainsResult(t, os.Stderr, owner)
		return
	}
	for _, scenario := range []string{"success", "failure"} {
		t.Run(scenario, func(t *testing.T) {
			reader, writer, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer writer.Close()
			if err := reader.Close(); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestValidateCLIProgressPreservesResultsWithClosedStderr$")
			cmd.Env = append(os.Environ(), fixture+"="+scenario)
			cmd.Stderr = writer
			if diagnostics, err := cmd.Output(); err != nil {
				t.Fatalf("validation did not survive the closed stderr reader: %v\n%s", err, diagnostics)
			}
		})
	}
}

func TestValidateCLIProgressPreservesResultsWithWriterError(t *testing.T) {
	assertValidationProgressRetainsResult(t, failingWriter{}, e.New(e.CodeVerifyFailed, "fixture owner error"))
}

func assertValidationProgressRetainsResult(t *testing.T, writer io.Writer, owner error) {
	t.Helper()
	var output bytes.Buffer
	app := validationCLI(func(ctx context.Context, _, _ string, _ lifecycle.QualificationRuntimeOptions) ([]testresults.Output, error) {
		reporter := validation.ReporterFrom(ctx)
		if reporter == nil {
			t.Fatal("runner context carried no progress reporter")
		}
		reporter(validation.Progress{Check: "cluster-conformance", Phase: validation.PhaseRunning})
		phase := validation.PhaseCompleted
		if owner != nil {
			phase = validation.PhaseFailed
		}
		reporter(validation.Progress{Check: "cluster-conformance", Phase: phase})
		return []testresults.Output{validationOutput()}, owner
	}, &output)
	app.ErrWriter = writer
	err := app.RunContext(t.Context(), []string{"nvkr", "validate", "--config", "profile.json"})
	if !errors.Is(err, owner) || e.ExitCode(err) != e.ExitCode(owner) {
		t.Fatalf("lost owner status: %v", err)
	}
	requireCanonicalValidationOutput(t, output.Bytes())
}

func requireCanonicalValidationOutput(t *testing.T, output []byte) {
	t.Helper()
	document, err := testresults.Encode(validationOutput())
	if err != nil {
		t.Fatal(err)
	}
	want, err := canonical.Marshal([]json.RawMessage{document})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(output, append(want, '\n')) {
		t.Fatalf("canonical stdout changed: %q", output)
	}
}
