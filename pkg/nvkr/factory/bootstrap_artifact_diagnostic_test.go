// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
)

func TestBootstrapTerminalReport(t *testing.T) {
	checksum := strings.Repeat("a", 64)
	valid := `{"apiVersion":"nvke.dev/bootstrap-e2e/v1","suite":"bootstrap","completed":true,"phase":"complete","exitCode":0,"artifactSha256Before":"` + checksum + `","artifactSha256After":"` + checksum + `","cleanup":"unknown","nodes":3,"controlPlaneVip":true}`
	for _, tc := range []struct {
		name, raw string
		code      e.Code
	}{
		{name: "complete with unknown teardown", raw: valid},
		{name: "missing", code: e.CodeBuildFailed},
		{name: "not executed", raw: strings.Replace(valid, `"completed":true`, `"completed":false`, 1), code: e.CodeBuildFailed},
		{name: "wrong protocol", raw: strings.Replace(valid, "/v1", "/v2", 1), code: e.CodeBuildFailed},
		{name: "missing exit", raw: strings.Replace(valid, `"exitCode":0,`, "", 1), code: e.CodeBuildFailed},
		{name: "null exit", raw: strings.Replace(valid, `"exitCode":0`, `"exitCode":null`, 1), code: e.CodeBuildFailed},
		{name: "wrong phase", raw: strings.Replace(valid, `"phase":"complete"`, `"phase":"bootstrap"`, 1), code: e.CodeBuildFailed},
		{name: "failed teardown", raw: strings.Replace(valid, `"cleanup":"unknown"`, `"cleanup":"failed"`, 1), code: e.CodeBuildFailed},
		{name: "invented successful teardown", raw: strings.Replace(valid, `"cleanup":"unknown"`, `"cleanup":"complete"`, 1), code: e.CodeBuildFailed},
		{name: "missing node count", raw: strings.Replace(valid, `"nodes":3,`, "", 1), code: e.CodeBuildFailed},
		{name: "missing VIP configuration", raw: strings.Replace(valid, `,"controlPlaneVip":true`, "", 1), code: e.CodeBuildFailed},
		{name: "null VIP configuration", raw: strings.Replace(valid, `"controlPlaneVip":true`, `"controlPlaneVip":null`, 1), code: e.CodeBuildFailed},
		{name: "multi-node without VIP", raw: strings.Replace(valid, `"controlPlaneVip":true`, `"controlPlaneVip":false`, 1), code: e.CodeBuildFailed},
		{name: "supported single node without VIP", raw: strings.Replace(strings.Replace(valid, `"nodes":3`, `"nodes":1`, 1), `"controlPlaneVip":true`, `"controlPlaneVip":false`, 1)},
		{name: "changed artifact", raw: strings.Replace(valid, checksum, strings.Repeat("b", 64), 1), code: e.CodeVerifyFailed},
		{name: "duplicate", raw: strings.Replace(valid, `"completed":true`, `"completed":false,"completed":true`, 1), code: e.CodeBuildFailed},
		{name: "completion alias only", raw: strings.Replace(valid, `"completed"`, `"Completed"`, 1), code: e.CodeBuildFailed},
		{name: "conflicting completion alias", raw: strings.Replace(valid, `"completed":true`, `"completed":false,"Completed":true`, 1), code: e.CodeBuildFailed},
		{name: "conflicting uppercase completion alias", raw: strings.Replace(valid, `"completed":true`, `"completed":false,"COMPLETED":true`, 1), code: e.CodeBuildFailed},
		{name: "completion alias before canonical", raw: strings.Replace(valid, `"completed":true`, `"Completed":false,"completed":true`, 1), code: e.CodeBuildFailed},
		{name: "matching completion alias", raw: strings.Replace(valid, `"completed":true`, `"completed":true,"Completed":true`, 1), code: e.CodeBuildFailed},
		{name: "exit alias", raw: strings.Replace(valid, `"exitCode":0`, `"exitCode":7,"EXITCODE":0`, 1), code: e.CodeBuildFailed},
		{name: "VIP alias", raw: strings.Replace(valid, `"controlPlaneVip":true`, `"CONTROLPLANEVIP":true`, 1), code: e.CodeBuildFailed},
		{name: "unknown field", raw: strings.Replace(valid, `"suite":`, `"extra":0,"suite":`, 1), code: e.CodeBuildFailed},
		{name: "trailing", raw: valid + "{}", code: e.CodeBuildFailed},
		{name: "oversized whitespace", raw: valid + strings.Repeat(" ", 1024*1024), code: e.CodeBuildFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "report.json")
			if tc.raw != "" {
				if err := os.WriteFile(path, []byte(tc.raw), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			report, err := readBootstrapDiagnosticReport(path, checksum)
			if tc.code != "" {
				assertFactoryErrorCode(t, err, tc.code)
				return
			}
			if err != nil || report.Cleanup != "unknown" {
				t.Fatalf("%+v %v", report, err)
			}
		})
	}
}

func TestBootstrapFactoryUsesExistingSelectionAndStaging(t *testing.T) {
	plan := writeCLIPlan(t, cliTestPlan(t, "amd64", "scalar", 1))
	for _, phase := range []string{"pass", "missing-report", "missing-protocol", "not-executed", "mutated", "suite-failed", "admission"} {
		t.Run(phase, func(t *testing.T) {
			root := t.TempDir()
			artifact := filepath.Join(root, "cli")
			if err := os.WriteFile(artifact, []byte("supplied"), 0o755); err != nil {
				t.Fatal(err)
			}
			opts := ArtifactTestOptions{Plan: plan, Artifact: artifact, Suite: "bootstrap", ControllerImage: "support-controller:fixture", WorkspaceRoot: filepath.Join(root, "runs"), Keep: true}
			imported, ran := false, false
			workspace := ""
			err := testCLIArtifact(t.Context(), opts,
				func(_ context.Context, _ []string, dir, repo, commit, tree string, _ io.Writer) error {
					imported = true
					workspace = filepath.Dir(dir)
					if repo == "" || commit == "" || tree == "" {
						t.Fatal("source identity omitted")
					}
					if err := os.MkdirAll(filepath.Join(dir, "cli", "e2e", "bootstrap"), 0o700); err != nil {
						return err
					}
					if err := os.WriteFile(filepath.Join(dir, "cli", "Makefile"), []byte("test-e2e-bootstrap-artifact:\n"), 0o600); err != nil {
						return err
					}
					if phase == "missing-protocol" {
						return nil
					}
					return os.WriteFile(filepath.Join(dir, "cli", "e2e", "bootstrap", "report.sh"), []byte("nvke.dev/bootstrap-e2e/v1"), 0o600)
				}, func(_ context.Context, input pipeline.ArtifactDiagnostic, _, _ io.Writer) error {
					ran = true
					if input.ControllerImage != opts.ControllerImage || input.Artifact == artifact || input.Platform != "linux/amd64" {
						t.Fatalf("bad binding: %+v", input)
					}
					raw, err := os.ReadFile(input.Artifact)
					if err != nil || string(raw) != "supplied" {
						t.Fatalf("staged subject: %q %v", raw, err)
					}
					if phase == "suite-failed" {
						return e.New(e.CodeBuildFailed, "upstream failure")
					}
					if phase == "missing-report" {
						return nil
					}
					hash := sha256.Sum256(raw)
					zero := 0
					report := bootstrapDiagnosticReport{APIVersion: "nvke.dev/bootstrap-e2e/v1", Suite: "bootstrap", Completed: phase != "not-executed", Phase: "complete", ExitCode: &zero, Before: hex.EncodeToString(hash[:]), After: hex.EncodeToString(hash[:]), Cleanup: "unknown", Nodes: 1, ControlPlaneVIP: new(bool)}
					if phase == "mutated" {
						if err := os.Chmod(input.Artifact, 0o755); err != nil {
							return err
						}
						if err := os.WriteFile(input.Artifact, []byte("changed"), 0o755); err != nil {
							return err
						}
					}
					encoded, err := json.Marshal(report)
					if err != nil {
						return err
					}
					return os.WriteFile(filepath.Join(input.Workspace, "bootstrap.json"), encoded, 0o600)
				}, func(_ context.Context, input pipeline.ArtifactDiagnostic) error {
					if input.Suite != "bootstrap" || input.Platform != "linux/amd64" {
						t.Fatal("selection changed")
					}
					if phase == "admission" {
						return e.New(e.CodeEngineCapability, "unsupported host")
					}
					return nil
				})
			if phase == "admission" {
				assertFactoryErrorCode(t, err, e.CodeEngineCapability)
				if imported || ran {
					t.Fatal("admission performed source or execution work")
				}
				if _, err := os.Stat(opts.WorkspaceRoot); !os.IsNotExist(err) {
					t.Fatal("workspace before admission")
				}
				return
			}
			if phase == "missing-protocol" && ran {
				t.Fatal("missing protocol executed")
			}
			if phase == "pass" {
				if err != nil {
					t.Fatal(err)
				}
				raw, err := os.ReadFile(filepath.Join(workspace, "result.json"))
				if err != nil {
					t.Fatal(err)
				}
				var result artifactTestResult
				if err := json.Unmarshal(raw, &result); err != nil {
					t.Fatal(err)
				}
				if result.Bootstrap == nil || result.Bootstrap.Cleanup != "unknown" || result.TopLevelTests != 0 || result.TestRevision == "" {
					t.Fatalf("invented result: %+v", result)
				}
			} else {
				if err == nil {
					t.Fatal("invalid execution passed")
				}
				if _, err := os.Stat(filepath.Join(workspace, "result.json")); !os.IsNotExist(err) {
					t.Fatal("failure wrote success")
				}
			}
		})
	}
}
