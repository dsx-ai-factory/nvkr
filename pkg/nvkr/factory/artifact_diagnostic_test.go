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
		{"cli-rejects-contract-package", valid, "cli", false},
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
			if !test.pass {
				assertFactoryErrorCode(t, err, e.CodeBuildFailed)
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
				err := testCLIArtifact(ctx, options, importSource, run, pipeline.AdmitArtifactDiagnostic)
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

type diagnosticWriterFunc func([]byte) (int, error)

func (write diagnosticWriterFunc) Write(raw []byte) (int, error) { return write(raw) }

func TestArtifactDiagnosticOutputFailurePreservesPrimary(t *testing.T) {
	plan := writeCLIPlan(t, cliTestPlan(t, "amd64", "scalar", 1))
	childErr := exec.CommandContext(t.Context(), "sh", "-c", "exit 7").Run()
	for _, phase := range []string{"success", "source-failed", "suite-failed"} {
		for _, failAt := range []int{0, 1, 2, 3} {
			for _, keep := range []bool{false, true} {
				t.Run(phase+string(rune('0'+failAt))+map[bool]string{true: "/keep", false: "/clean"}[keep], func(t *testing.T) {
					root := t.TempDir()
					artifact := filepath.Join(root, "artifact")
					if err := os.WriteFile(artifact, []byte("executable"), 0o755); err != nil {
						t.Fatal(err)
					}
					calls := 0
					output := diagnosticWriterFunc(func(raw []byte) (int, error) {
						calls++
						if failAt == 0 || calls == failAt {
							return 0, io.ErrClosedPipe
						}
						return len(raw), nil
					})
					workspace := ""
					options := ArtifactTestOptions{Plan: plan, Artifact: artifact, WorkspaceRoot: filepath.Join(root, "workspaces"), Stdout: output, Keep: keep}
					err := testCLIArtifact(t.Context(), options,
						func(_ context.Context, _ []string, directory, _, _, _ string, _ io.Writer) error {
							workspace = filepath.Dir(directory)
							if phase == "source-failed" {
								return e.New(e.CodeFetchFailed, "source failed")
							}
							if err := os.MkdirAll(filepath.Join(directory, "cli"), 0o700); err != nil {
								return err
							}
							return os.WriteFile(filepath.Join(directory, "cli", "Makefile"), []byte("test-artifact:\n"), 0o600)
						}, func(_ context.Context, input pipeline.ArtifactDiagnostic, _, _ io.Writer) error {
							if phase == "suite-failed" {
								return childErr
							}
							return os.WriteFile(filepath.Join(input.Workspace, "tests.jsonl"), []byte(diagnosticEvents("command-line-arguments", "TestContract")), 0o600)
						}, pipeline.AdmitArtifactDiagnostic)
					switch phase {
					case "source-failed":
						assertFactoryErrorCode(t, err, e.CodeFetchFailed)
					case "suite-failed":
						if got := GitHubStepExitCode(err); got != 7 {
							t.Fatalf("output replaced suite exit: %d %v", got, err)
						}
					default:
						assertFactoryErrorCode(t, err, e.CodeBuildFailed)
						if !errors.Is(err, io.ErrClosedPipe) || calls != 3 {
							t.Fatalf("delivery failure missing: calls=%d error=%v", calls, err)
						}
					}
					_, statErr := os.Stat(workspace)
					if !keep && !os.IsNotExist(statErr) {
						t.Fatal("default workspace retained", statErr)
					}
					if keep {
						_, resultErr := os.Stat(filepath.Join(workspace, "result.json"))
						if phase == "success" && resultErr != nil {
							t.Fatal("completed tests lost their retained result", resultErr)
						}
						if phase != "success" && !os.IsNotExist(resultErr) {
							t.Fatal("failed execution published success", resultErr)
						}
					}
				})
			}
		}
	}
}

func TestArtifactDiagnosticCopyCancellation(t *testing.T) {
	for _, phase := range []struct {
		name string
		code e.Code
	}{{"copying staged executable", e.CodeBuildFailed}, {"hashing staged executable", e.CodeVerifyFailed}} {
		t.Run(phase.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "artifact")
			if err := os.WriteFile(path, []byte(strings.Repeat("x", 1<<20)), 0o755); err != nil {
				t.Fatal(err)
			}
			for _, canceled := range []bool{false, true} {
				t.Run(map[bool]string{true: "canceled", false: "io-failure"}[canceled], func(t *testing.T) {
					file, err := os.Open(path)
					if err != nil {
						t.Fatal(err)
					}
					defer file.Close()
					ctx, cancel := context.WithCancel(t.Context())
					defer cancel()
					copied := 0
					writer := diagnosticWriterFunc(func(raw []byte) (int, error) {
						if !canceled {
							return 0, io.ErrClosedPipe
						}
						copied += len(raw)
						cancel()
						return len(raw), nil
					})
					err = copyArtifactDiagnostic(ctx, writer, file, phase.code, phase.name)
					if canceled {
						assertFactoryErrorCode(t, err, e.CodeTimeout)
						if !errors.Is(err, context.Canceled) || copied == 0 || copied > 128<<10 || !strings.Contains(err.Error(), phase.name) {
							t.Fatalf("copy did not stop at a chunk boundary: copied=%d error=%v", copied, err)
						}
					} else {
						assertFactoryErrorCode(t, err, phase.code)
						if !errors.Is(err, io.ErrClosedPipe) {
							t.Fatal(err)
						}
					}
				})
			}
		})
	}
}

// This is factory dispatch with mocked import/container execution, not an upstream run.
func TestAdmittedCLIArtifactFreezesPlanAndSelection(t *testing.T) {
	for _, arch := range []string{"amd64", "arm64"} {
		for _, suite := range []string{"contract", "cli"} {
			t.Run(arch+"/"+suite, func(t *testing.T) {
				plan := writeCLIPlan(t, cliTestPlan(t, arch, "scalar", 1))
				root := t.TempDir()
				artifact := filepath.Join(root, "binary")
				if err := os.WriteFile(artifact, []byte("selected"), 0o755); err != nil {
					t.Fatal(err)
				}
				configuration := []byte(`{"check":"frozen"}`)
				options := ArtifactTestOptions{Plan: plan, Artifact: artifact, Suite: suite, WorkspaceRoot: filepath.Join(root, "workspace"), Keep: true, DiagnosticConfiguration: configuration}
				admitted, err := AdmitCLIArtifact(t.Context(), options)
				if err != nil {
					t.Fatal(err)
				}
				expected := admitted.Inputs()
				if _, err := os.Stat(options.WorkspaceRoot); !os.IsNotExist(err) {
					t.Fatal("workspace created during admission")
				}
				if err := os.WriteFile(plan, []byte("changed after admission"), 0o600); err != nil {
					t.Fatal(err)
				}
				configuration[0] = 'x'
				options.Suite = "bootstrap"
				options.Artifact = "other"
				importer := func(_ context.Context, _ []string, directory, repo, commit, tree string, _ io.Writer) error {
					if repo != expected.Repo || commit != expected.Commit || tree != expected.TreeHash {
						t.Fatal("source changed")
					}
					if err := os.MkdirAll(filepath.Join(directory, "cli"), 0o700); err != nil {
						return err
					}
					return os.WriteFile(filepath.Join(directory, "cli/Makefile"), []byte("test-artifact:\n"), 0o600)
				}
				runner := func(_ context.Context, input pipeline.ArtifactDiagnostic, _ io.Writer, _ io.Writer) error {
					if input.Suite != suite || input.Platform != "linux/"+arch || input.Image != expected.ToolchainImage || input.Version != expected.ExpectedVersion {
						t.Fatal(input)
					}
					raw, err := os.ReadFile(input.Artifact)
					if err != nil || string(raw) != "selected" {
						t.Fatalf("%s %v", raw, err)
					}
					metadata, err := os.ReadFile(filepath.Join(input.Workspace, "selection.json"))
					if err != nil || !strings.Contains(string(metadata), `"check":"frozen"`) || !strings.Contains(string(metadata), expected.Commit) {
						t.Fatalf("%s %v", metadata, err)
					}
					pkg := "command-line-arguments"
					if suite == "cli" {
						pkg = "github.com/dsx-ai-factory/nvke/cli/cmd/nvkectl"
					}
					return os.WriteFile(filepath.Join(input.Workspace, "tests.jsonl"), []byte(diagnosticEvents(pkg, "TestContract")), 0o600)
				}
				if err := runCLIArtifact(t.Context(), admitted.options, &admitted.inputs, importer, runner, pipeline.AdmitArtifactDiagnostic); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestAdmittedCLIArtifactRunRequiresAdmission(t *testing.T) {
	for _, tc := range []struct {
		name       string
		invocation *CLIArtifactInvocation
	}{
		{"nil", nil}, {"zero", &CLIArtifactInvocation{}},
	} {
		t.Run(tc.name, func(t *testing.T) { assertFactoryErrorCode(t, tc.invocation.Run(t.Context()), e.CodeUsage) })
	}
}

func TestAdmittedCLIArtifactChecksExecutableBeforeWorkspace(t *testing.T) {
	for _, kind := range []string{"missing", "non-executable", "directory"} {
		t.Run(kind, func(t *testing.T) {
			plan, err := LoadCLIArtifactPlan(t.Context(), writeCLIPlan(t, cliTestPlan(t, "amd64", "scalar", 1)))
			if err != nil {
				t.Fatal(err)
			}
			root := t.TempDir()
			artifact := filepath.Join(root, "binary")
			switch kind {
			case "directory":
				if err := os.Mkdir(artifact, 0o700); err != nil {
					t.Fatal(err)
				}
			case "non-executable":
				if err := os.WriteFile(artifact, []byte("supplied"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			workspace := filepath.Join(root, "no-workspace")
			_, err = plan.Admit(t.Context(), ArtifactTestOptions{Artifact: artifact, WorkspaceRoot: workspace})
			assertFactoryErrorCode(t, err, e.CodeUsage)
			if _, err := os.Stat(workspace); !os.IsNotExist(err) {
				t.Fatal("admission created a workspace", err)
			}
		})
	}
}
