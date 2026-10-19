// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/testresults"
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
			ctx := t.Context()
			calls := 0
			var output bytes.Buffer
			app := validationCLI(func(got context.Context, name, config string, options lifecycle.QualificationRuntimeOptions) ([]testresults.Output, error) {
				calls++
				if got != ctx || name != selected || config != "profile.json" || options.WorkspaceRoot != workspace || options.GitHubToken != "explicit-token" || options.Credential.Registry != "registry.example.test" || options.Registry.Password != "explicit-secret" || options.Registry.Anonymous {
					t.Fatal("changed selection, context or explicit credentials")
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
