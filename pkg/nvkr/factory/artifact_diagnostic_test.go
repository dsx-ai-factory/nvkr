// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
)

func diagnosticEvents(packageName, test string) string {
	var raw strings.Builder
	for _, event := range []map[string]string{
		{"Action": "run", "Package": packageName, "Test": test},
		{"Action": "pass", "Package": packageName, "Test": test},
		{"Action": "pass", "Package": packageName},
	} {
		value, _ := json.Marshal(event)
		raw.Write(value)
		raw.WriteByte('\n')
	}
	return raw.String()
}

func TestArtifactDiagnosticResults(t *testing.T) {
	valid := diagnosticEvents("command-line-arguments", "TestContract")
	for _, test := range []struct {
		name, raw, suite string
		pass             bool
	}{
		{"contract", valid, "contract", true},
		{"cli", diagnosticEvents("github.com/dsx-ai-factory/nvke/cli/cmd/nvkectl", "TestContract"), "cli", true},
		{"ordinary-only-limitation", diagnosticEvents("command-line-arguments", "TestOrdinaryUnitOnly"), "contract", true},
		{"empty", "", "contract", false}, {"missing-package", valid[:strings.LastIndex(strings.TrimSpace(valid), "\n")], "contract", false},
		{"pass-without-run", strings.Join(strings.Split(valid, "\n")[1:], "\n"), "contract", false},
		{"missing-pass", strings.Replace(valid, `"Action":"pass"`, `"Action":"output"`, 1), "contract", false},
		{"other-package", diagnosticEvents("unrelated", "TestUnit"), "contract", false},
		{"skip", valid + `{"Action":"skip","Package":"p"}`, "contract", false},
		{"fail", valid + `{"Action":"fail","Package":"p"}`, "contract", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			counts, err := summarizeArtifactTests(strings.NewReader(test.raw), test.suite)
			if (err == nil) != test.pass {
				t.Fatalf("%+v %v", counts, err)
			}
			if test.pass && (counts.TopLevelTests != 1 || counts.Packages != 1) {
				t.Fatal(counts)
			}
		})
	}
	for _, bad := range []string{`{"Action":`, `{}`, `[]`, `{"Action":"pass","Package":3}`, `{"Action":"run","Package":"p","Test":4}`, `{"Action":"pass","Package":"p","Test":[]}`, `{"Action":"pass","Package":"p","Test":null}`} {
		t.Run(bad, func(t *testing.T) {
			_, err := summarizeArtifactTests(strings.NewReader(valid+bad), "contract")
			assertFactoryErrorCode(t, err, e.CodeBuildFailed)
		})
	}
}

func TestArtifactDiagnosticStagingResultsAndCleanup(t *testing.T) {
	plan := writeCLIPlan(t, cliTestPlan(t, "amd64", "scalar", 1))
	for _, phase := range []string{"success", "import", "missing-entrypoint", "run", "empty", "tampered", "caller-swap", "canceled"} {
		for _, keep := range []bool{false, true} {
			t.Run(phase+map[bool]string{true: "/keep", false: "/clean"}[keep], func(t *testing.T) {
				root := t.TempDir()
				artifact := filepath.Join(root, "original")
				if err := os.WriteFile(artifact, []byte("original"), 0o755); err != nil {
					t.Fatal(err)
				}
				workspaceRoot := filepath.Join(root, "workspace space:comma,quote\"")
				options := ArtifactTestOptions{Plan: plan, Artifact: artifact, WorkspaceRoot: workspaceRoot, Keep: keep}
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				workspace := ""
				importSource := func(_ context.Context, _ []string, directory, repo, commit, tree string, _ io.Writer) error {
					workspace = filepath.Dir(directory)
					if repo == "" || commit == "" || tree == "" {
						t.Fatal("missing pin")
					}
					if phase == "import" {
						return errors.New("import failure")
					}
					if err := os.MkdirAll(filepath.Join(directory, "cli"), 0o700); err != nil {
						t.Fatal(err)
					}
					body := "test-artifact:\n"
					if phase == "missing-entrypoint" {
						body = "test:\n"
					}
					return os.WriteFile(filepath.Join(directory, "cli", "Makefile"), []byte(body), 0o600)
				}
				ran := false
				run := func(_ context.Context, input pipeline.ArtifactDiagnostic, _ io.Writer, _ io.Writer) error {
					ran = true
					if phase == "run" {
						return errors.New("runtime failure")
					}
					if phase == "caller-swap" {
						if err := os.WriteFile(artifact, []byte("swapped"), 0o755); err != nil {
							t.Fatal(err)
						}
					}
					raw, err := os.ReadFile(input.Artifact)
					if err != nil || string(raw) != "original" {
						t.Fatalf("staged bytes: %q %v", raw, err)
					}
					if phase == "tampered" {
						if err := os.Chmod(input.Artifact, 0o755); err != nil {
							t.Fatal(err)
						}
						if err := os.WriteFile(input.Artifact, []byte("changed"), 0o755); err != nil {
							t.Fatal(err)
						}
					}
					log := diagnosticEvents("command-line-arguments", "TestContract")
					if phase == "empty" {
						log = ""
					}
					if phase == "canceled" {
						cancel()
					}
					return os.WriteFile(filepath.Join(input.Workspace, "tests.jsonl"), []byte(log), 0o600)
				}
				err := testCLIArtifact(ctx, options, importSource, run)
				if phase == "import" && ran {
					t.Fatal("source import refusal reached container execution")
				}
				passes := phase == "success" || phase == "caller-swap"
				if (err == nil) != passes {
					t.Fatalf("%s: %v", phase, err)
				}
				entries, readErr := os.ReadDir(workspaceRoot)
				if readErr != nil {
					t.Fatal(readErr)
				}
				if !keep {
					if len(entries) != 0 {
						t.Fatal("workspace retained")
					}
					return
				}
				if len(entries) != 1 {
					t.Fatal(entries)
				}
				raw, resultErr := os.ReadFile(filepath.Join(workspace, "result.json"))
				if !passes {
					if !os.IsNotExist(resultErr) {
						t.Fatal("failure published success", resultErr)
					}
					return
				}
				var result artifactTestResult
				if resultErr != nil {
					t.Fatal(resultErr)
				}
				if err := json.Unmarshal(raw, &result); err != nil {
					t.Fatal(err)
				}
				if result.Result != "pass" || result.Authority != "unqualified-diagnostic" || result.BuildDigest == "" || result.TreeHash == "" || result.ArtifactSHA256 == "" || result.TopLevelTests != 1 {
					t.Fatalf("%+v", result)
				}
			})
		}
	}
}

func TestArtifactDiagnosticAdmissionAndExit(t *testing.T) {
	root := t.TempDir()
	err := TestCLIArtifact(t.Context(), ArtifactTestOptions{Plan: filepath.Join(root, "missing-plan"), Artifact: "missing", WorkspaceRoot: filepath.Join(root, "workspace")})
	if err == nil {
		t.Fatal("missing Plan accepted")
	}
	if _, err := os.Stat(filepath.Join(root, "workspace")); !os.IsNotExist(err) {
		t.Fatal("workspace created before admission")
	}
	cmd := exec.CommandContext(t.Context(), "sh", "-c", "exit 7")
	err = cmd.Run()
	if code := GitHubStepExitCode(diagnosticError(t.Context(), err)); code != 7 {
		t.Fatal(code)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	assertFactoryErrorCode(t, diagnosticError(ctx, err), e.CodeTimeout)
}

func TestArtifactDiagnosticSourcePhaseCodes(t *testing.T) {
	cmd := exec.CommandContext(t.Context(), "sh", "-c", "exit 128")
	childErr := cmd.Run()
	for _, test := range []struct {
		name string
		err  error
		code e.Code
	}{
		{"capability", e.New(e.CodeEngineCapability, "unsupported source"), e.CodeEngineCapability},
		{"fetch-exit", e.Wrap(e.CodeFetchFailed, childErr, "fetch failed"), e.CodeFetchFailed},
		{"uncoded-import", errors.New("import failed"), e.CodeFetchFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := diagnosticSourceError(t.Context(), test.err)
			assertFactoryErrorCode(t, err, test.code)
			if got := GitHubStepExitCode(err); got != e.ExitCode(err) {
				t.Fatalf("source error turned into raw process status: %d", got)
			}
		})
	}
	if got := GitHubStepExitCode(diagnosticError(t.Context(), e.Wrap(e.CodeBuildFailed, childErr, "suite failed"))); got != 128 {
		t.Fatalf("suite exit not preserved: %d", got)
	}
}

func TestArtifactDiagnosticCompletedExecutionSurvivesCleanupCancellation(t *testing.T) {
	childErr := exec.CommandContext(t.Context(), "sh", "-c", "exit 7").Run()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for _, test := range []struct {
		name string
		err  error
		want int
	}{
		{"completed-exit", e.Wrap(e.CodeBuildFailed, childErr, "completed before cleanup"), 7},
		{"execution-timed-out", e.Wrap(e.CodeTimeout, childErr, "execution canceled"), e.ExitCode(e.New(e.CodeTimeout, ""))},
		{"unclassified-cancellation", childErr, e.ExitCode(e.New(e.CodeTimeout, ""))},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := GitHubStepExitCode(diagnosticError(ctx, test.err)); got != test.want {
				t.Fatalf("completed execution reclassified during cleanup: got %d want %d", got, test.want)
			}
		})
	}
}
