// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/qualification"
	"github.com/urfave/cli/v2"
)

func TestQualificationCLISelectsOnlyIndependentPolicy(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success diagnostic", true: "failure diagnostic"}[fail], func(t *testing.T) {
			t.Setenv("NVKR_GITHUB_TOKEN", "provider-token")
			t.Setenv("NVKR_REGISTRY", "registry.example")
			t.Setenv("NVKR_REGISTRY_USER", "registry-user")
			t.Setenv("NVKR_REGISTRY_PASSWORD", "registry-secret")
			var output bytes.Buffer
			app := &cli.App{Writer: &output, Commands: []*cli.Command{cmdRunQualifyWithPublisher(func(ctx context.Context, policy string, options lifecycle.QualificationRuntimeOptions) (*qualification.Publication, error) {
				if ctx == nil || policy != "qualification.yaml" || options.WorkspaceRoot != ".nvkr/qualification" || options.GitHubToken != "provider-token" || options.Credential.Registry != "registry.example" || options.Credential.Username != "registry-user" || options.Registry.Password != "registry-secret" {
					t.Fatal("incorrect policy or process configuration mapping")
				}
				result := &qualification.Publication{Successful: !fail, ArtifactChecks: &lifecycle.ArtifactCheckDescription{Outputs: []lifecycle.ArtifactCheckOutput{{Name: "cli", Overall: "fail", Checks: []lifecycle.ArtifactCheckResult{{Name: "build-signature-and-provenance", Status: "fail", Code: string(e.CodeSignatureInvalid)}}}}}}
				if fail {
					return result, e.New(e.CodeGateUnmet, "missing authenticated smoke")
				}
				return result, nil
			})}}
			err := app.Run([]string{"nvkr", "qualify", "--policy", "qualification.yaml"})
			if fail {
				var coded *e.Error
				if !errors.As(err, &coded) || coded.Code != e.CodeGateUnmet {
					t.Fatalf("lost gate failure: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			var result qualification.Publication
			if err := canonical.UnmarshalExact(bytes.TrimSpace(output.Bytes()), &result); err != nil {
				t.Fatal(err)
			}
			if result.Successful == fail || result.ArtifactChecks == nil || len(result.ArtifactChecks.Outputs) != 1 || result.ArtifactChecks.Outputs[0].Checks[0].Code != string(e.CodeSignatureInvalid) {
				t.Fatal("qualification diagnostics changed")
			}
			if strings.Contains(output.String(), "provider-token") || strings.Contains(output.String(), "registry-secret") {
				t.Fatal("diagnostics expose process credentials")
			}
		})
	}
	for _, flag := range cmdRunQualify().Flags {
		for _, name := range flag.Names() {
			if name != "policy" && name != "workspace" {
				t.Fatalf("qualify exposes candidate or trust override %s", name)
			}
		}
	}
}

func TestQualificationCLIRefusesMissingPolicyAndManualSource(t *testing.T) {
	for _, tc := range []struct {
		name  string
		args  []string
		coded bool
	}{
		{"missing policy", nil, true},
		{"empty policy", []string{"--policy", " "}, true},
		{"empty workspace", []string{"--policy", "policy.yaml", "--workspace", ""}, true},
		{"positional candidate", []string{"--policy", "policy.yaml", "candidate.json"}, true},
		{"manual Run", []string{"--policy", "policy.yaml", "--run", "https://github.com/example/repo/actions/runs/12/attempts/1"}, false},
		{"manual workflow", []string{"--policy", "policy.yaml", "--qualification-workflow", ".github/workflows/other.yml"}, false},
		{"skip smoke", []string{"--policy", "policy.yaml", "--skip-smoke"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var output bytes.Buffer
			called := false
			app := &cli.App{Writer: &output, ErrWriter: &output, Commands: []*cli.Command{cmdRunQualifyWithPublisher(func(context.Context, string, lifecycle.QualificationRuntimeOptions) (*qualification.Publication, error) {
				called = true
				return nil, nil
			})}}
			err := app.Run(append([]string{"nvkr", "qualify"}, tc.args...))
			if called || err == nil {
				t.Fatalf("invalid invocation reached qualification: called=%v err=%v", called, err)
			}
			if tc.coded {
				var coded *e.Error
				if !errors.As(err, &coded) || coded.Code != e.CodeUsage {
					t.Fatalf("missing coded usage error: %v", err)
				}
			}
		})
	}
}
