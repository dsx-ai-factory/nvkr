// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/urfave/cli/v2"
)

const qualificationTestRunURL = "https://github.com/example/product/actions/runs/123/attempts/2"

func qualificationCLI(check qualificationRunChecker, out io.Writer) *cli.App {
	command := cmdRunCheckArtifactsWithChecker(check)
	configureCommandUsage(command)
	return &cli.App{Commands: []*cli.Command{command}, Writer: out, ErrWriter: io.Discard}
}

func TestCheckArtifactsCLIRequiresRunAndPolicyBeforeWork(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
	}{
		{"missing both", nil},
		{"missing policy", []string{"--run", qualificationTestRunURL}},
		{"missing run", []string{"--policy", "policy.yaml"}},
		{"empty run", []string{"--run", "", "--policy", "policy.yaml"}},
		{"empty policy", []string{"--run", qualificationTestRunURL, "--policy", " "}},
		{"empty workspace", []string{"--run", qualificationTestRunURL, "--policy", "policy.yaml", "--workspace", ""}},
		{"positional", []string{"--run", qualificationTestRunURL, "--policy", "policy.yaml", "unexpected"}},
		{"old authored inputs", []string{"--run", qualificationTestRunURL, "--policy", "policy.yaml", "--definition", "/must-not-open"}},
		{"candidate override", []string{"--run", qualificationTestRunURL, "--policy", "policy.yaml", "--candidate", "/must-not-open"}},
		{"source override", []string{"--run", qualificationTestRunURL, "--policy", "policy.yaml", "--source-commit", "substitute"}},
		{"verifier override", []string{"--run", qualificationTestRunURL, "--policy", "policy.yaml", "--cosign", "/must-not-execute"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			called := false
			var out bytes.Buffer
			check := func(context.Context, string, string, lifecycle.QualificationRuntimeOptions) (*lifecycle.ArtifactCheckReceipt, *lifecycle.ArtifactCheckDescription, error) {
				called = true
				return nil, nil, nil
			}
			err := qualificationCLI(check, &out).RunContext(t.Context(), append([]string{"nvkr", "check-artifacts"}, test.args...))
			if cliErrorCode(err) != e.CodeUsage || called || out.Len() != 0 {
				t.Fatalf("invalid CLI input reached qualification or output: called=%t output=%q error=%v", called, out.String(), err)
			}
		})
	}
}

func TestCheckArtifactsCLIForwardsInputsAndPreservesDiagnostics(t *testing.T) {
	for _, test := range []struct {
		name      string
		workspace string
		checkErr  error
	}{
		{"success defaults", "", nil},
		{"gate failure", "private-workspace", e.New(e.CodeVerifyFailed, "artifact rejected")},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("NVKR_GITHUB_TOKEN", "test-token")
			t.Setenv("NVKR_REGISTRY", "registry.example.test")
			t.Setenv("NVKR_REGISTRY_USER", "test-user")
			t.Setenv("NVKR_REGISTRY_PASSWORD", "test-password")
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			report := &lifecycle.ArtifactCheckDescription{GateVersion: lifecycle.ArtifactCheckGateVersion}
			called := 0
			check := func(gotContext context.Context, run, policy string, options lifecycle.QualificationRuntimeOptions) (*lifecycle.ArtifactCheckReceipt, *lifecycle.ArtifactCheckDescription, error) {
				called++
				workspace := test.workspace
				if workspace == "" {
					workspace = ".nvkr/qualification"
				}
				if gotContext != ctx || run != qualificationTestRunURL || policy != "policy.yaml" || options.WorkspaceRoot != workspace ||
					options.GitHubToken != "test-token" || options.Credential.Registry != "registry.example.test" ||
					options.Credential.Username != "test-user" || options.Credential.Password != "test-password" ||
					options.Registry.Username != "test-user" || options.Registry.Password != "test-password" || options.Registry.Anonymous {
					t.Fatal("CLI did not preserve independently selected inputs and runtime configuration")
				}
				return nil, report, test.checkErr
			}
			args := []string{"nvkr", "check-artifacts", "--run", qualificationTestRunURL, "--policy", "policy.yaml"}
			if test.workspace != "" {
				args = append(args, "--workspace", test.workspace)
			}
			var out bytes.Buffer
			err := qualificationCLI(check, &out).RunContext(ctx, args)
			want, marshalErr := canonical.Marshal(report)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			if called != 1 || !errors.Is(err, test.checkErr) || !bytes.Equal(out.Bytes(), append(want, '\n')) {
				t.Fatalf("CLI changed diagnostics or coded result: calls=%d output=%q error=%v", called, out.String(), err)
			}
		})
	}
}

func TestCheckArtifactsCLIReportsDiagnosticWriteFailure(t *testing.T) {
	check := func(context.Context, string, string, lifecycle.QualificationRuntimeOptions) (*lifecycle.ArtifactCheckReceipt, *lifecycle.ArtifactCheckDescription, error) {
		return nil, &lifecycle.ArtifactCheckDescription{}, nil
	}
	err := qualificationCLI(check, qualificationFailedWriter{}).RunContext(t.Context(), []string{"nvkr", "check-artifacts", "--run", qualificationTestRunURL, "--policy", "policy.yaml"})
	assertLifecycleCommandCode(t, err, e.CodeInternal)
}

type qualificationFailedWriter struct{}

func (qualificationFailedWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestCheckArtifactsCLIHelpExposesOnlyRunPolicyAndWorkspace(t *testing.T) {
	var out bytes.Buffer
	app := newApp()
	app.Writer = &out
	if err := app.RunContext(t.Context(), []string{"nvkr", "run", "check-artifacts", "--help"}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"--run", "/attempts/N", "--policy", "independently selected", "--workspace", ".nvkr/qualification", "grants no signing authority"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("help omits %q: %s", want, out.String())
		}
	}
	for _, old := range []string{"--definition", "--environment", "--plan", "--candidate", "--source-", "--cosign", "--default-branch"} {
		if strings.Contains(out.String(), old) {
			t.Errorf("help exposes obsolete per-Run input %q", old)
		}
	}
}
