// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/qualification"
	"github.com/urfave/cli/v2"
)

func workerCLIEnvironment(t *testing.T) {
	t.Helper()
	for _, name := range []string{"NVKR_REGISTRY", "NVKR_REGISTRY_USER", "NVKR_REGISTRY_PASSWORD"} {
		t.Setenv(name, "")
	}
	t.Setenv("NVKR_GITHUB_TOKEN", "provider-secret")
	t.Setenv("NVKR_CI_STORAGE_TOKEN", "private-read-secret")
	t.Setenv("NVKR_CI_STORAGE_USER", "storage-reader")
	t.Setenv("NVKR_PULL_REGISTRY", "registry.example")
	t.Setenv("NVKR_PULL_REGISTRY_USER", "reader")
	t.Setenv("NVKR_PULL_REGISTRY_PASSWORD", "pull-secret")
}

func TestSmokeWorkerCLIUsesOnlyPolicyAndPullRuntime(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[fail], func(t *testing.T) {
			workerCLIEnvironment(t)
			var output bytes.Buffer
			app := &cli.App{Writer: &output, Commands: []*cli.Command{cmdRunSmokeWorkerWithRunner(func(ctx context.Context, path string, options lifecycle.QualificationRuntimeOptions) (*qualification.SmokeWorkerResult, error) {
				if options.CIStorageUsername != "storage-reader" || options.CIStorageToken != "private-read-secret" {
					t.Fatal("private registry account was not passed independently of publication pull credentials")
				}
				if ctx == nil || path != "qualification.yaml" || options.WorkspaceRoot != ".nvkr/smoke-worker" || options.GitHubToken != "provider-secret" || options.Credential.Registry != "registry.example" || options.Credential.Username != "reader" || options.Registry.Password != "pull-secret" {
					t.Fatal("wrong worker inputs")
				}
				result := &qualification.SmokeWorkerResult{Smoke: &lifecycle.NativeToolSmokeReport{Status: "fail"}}
				if fail {
					return result, e.New(e.CodeGateUnmet, "smoke failed")
				}
				result.EvidenceDirectory = ".nvkr/smoke-worker/evidence"
				return result, nil
			})}}
			err := app.Run([]string{"nvkr", "smoke-worker", "--policy", "qualification.yaml"})
			if fail {
				var coded *e.Error
				if !errors.As(err, &coded) || coded.Code != e.CodeGateUnmet {
					t.Fatalf("lost failure %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(output.String(), "secret") || !strings.Contains(output.String(), `"smoke"`) {
				t.Fatalf("bad diagnostics %s", output.String())
			}
		})
	}
	command := cmdRunSmokeWorker()
	if !command.Hidden {
		t.Fatal("worker is not internal")
	}
	if len(command.Flags) != 1 || command.Flags[0].Names()[0] != "policy" {
		t.Fatal("worker exposes additional inputs")
	}
}

func TestSmokeWorkerCLIRejectsOverridesAndPublicationCredentials(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		env  string
	}{
		{"missing policy", nil, ""}, {"positional", []string{"--policy", "policy.yaml", "candidate.json"}, ""},
		{"manual run", []string{"--policy", "policy.yaml", "--run", "123"}, ""},
		{"platform", []string{"--policy", "policy.yaml", "--platform", "linux/arm64"}, ""},
		{"output", []string{"--policy", "policy.yaml", "--out", "other"}, ""},
		{"publication password", []string{"--policy", "policy.yaml"}, "NVKR_REGISTRY_PASSWORD"},
		{"publication user", []string{"--policy", "policy.yaml"}, "NVKR_REGISTRY_USER"},
		{"publication registry", []string{"--policy", "policy.yaml"}, "NVKR_REGISTRY"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workerCLIEnvironment(t)
			if tc.env != "" {
				t.Setenv(tc.env, "publication-secret")
			}
			called := false
			var output bytes.Buffer
			app := &cli.App{Writer: &output, ErrWriter: &output, Commands: []*cli.Command{cmdRunSmokeWorkerWithRunner(func(context.Context, string, lifecycle.QualificationRuntimeOptions) (*qualification.SmokeWorkerResult, error) {
				called = true
				return nil, nil
			})}}
			err := app.Run(append([]string{"nvkr", "smoke-worker"}, tc.args...))
			if called || err == nil {
				t.Fatal("invalid worker inputs admitted")
			}
			if tc.env != "" {
				var coded *e.Error
				if !errors.As(err, &coded) || coded.Code != e.CodeTrustPolicyMissing {
					t.Fatal(err)
				}
			}
		})
	}
}
