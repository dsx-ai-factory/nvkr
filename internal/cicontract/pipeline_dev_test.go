// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build linux || darwin

package cicontract

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/ownedcmd"
)

const dispatchRepo = "example/nvkr"
const dispatchBranch = "feature/pipeline;$shell"

var dispatchCommit = strings.Repeat("a", 40)

type adapterCall struct {
	Tool                               string
	Args                               []string
	Directory, Host, Prompt, GitPrompt string
	StdinEOF                           bool
}

func TestPipelineDevCommandHelper(t *testing.T) {
	if os.Getenv("NVKR_PIPELINE_DEV_HELPER") != "1" {
		return
	}
	index := slices.Index(os.Args, "--")
	if index < 0 || len(os.Args) < index+2 {
		os.Exit(90)
	}
	tool, args := os.Args[index+1], os.Args[index+2:]
	dir, err := os.Getwd()
	if err != nil {
		os.Exit(91)
	}
	var probe [1]byte
	_, inputErr := os.Stdin.Read(probe[:])
	call := adapterCall{tool, args, dir, os.Getenv("GH_HOST"), os.Getenv("GH_PROMPT_DISABLED"), os.Getenv("GIT_TERMINAL_PROMPT"), errors.Is(inputErr, io.EOF)}
	log, err := os.OpenFile(os.Getenv("NVKR_ADAPTER_CALLS"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		os.Exit(92)
	}
	if err := json.NewEncoder(log).Encode(call); err != nil {
		os.Exit(93)
	}
	if err := log.Close(); err != nil {
		os.Exit(94)
	}
	if tool == "timeout" {
		real := os.Getenv("NVKR_REAL_TIMEOUT")
		if err := syscall.Exec(real, append([]string{real}, args...), os.Environ()); err != nil {
			os.Exit(95)
		}
	}
	failure := os.Getenv("NVKR_ADAPTER_FAILURE")
	jsonOutput := func(value any) {
		if err := json.NewEncoder(os.Stdout).Encode(value); err != nil {
			os.Exit(96)
		}
	}
	if tool == "git" && len(args) > 0 {
		switch args[0] {
		case "status":
			if failure == "dirty" {
				fmt.Print(" M tracked.go\n")
			}
			if failure == "untracked" {
				fmt.Print("?? untracked.go\n")
			}
		case "symbolic-ref":
			if failure == "detached" {
				os.Exit(1)
			}
			if failure == "invalid branch" {
				fmt.Println("bad branch")
				os.Exit(0)
			}
			marker := filepath.Join(os.Getenv("NVKR_ADAPTER_ROOT"), "branch-read")
			_, err := os.Stat(marker)
			if failure == "local moved" && err == nil {
				fmt.Println("different")
			} else {
				fmt.Println(dispatchBranch)
			}
			if err := os.WriteFile(marker, nil, 0o600); err != nil {
				os.Exit(97)
			}
		case "rev-parse":
			if failure == "invalid commit" {
				fmt.Println("short")
			} else {
				fmt.Println(dispatchCommit)
			}
		default:
			os.Exit(98)
		}
	} else if tool == "gh" && len(args) > 0 {
		switch args[0] {
		case "api":
			if failure == "timeout" {
				os.Exit(124)
			}
			path := args[len(args)-1]
			if strings.HasSuffix(path, "/actions/workflows/nvkr-dev.yml") {
				if failure == "missing workflow" {
					os.Exit(1)
				}
				state := "active"
				if failure == "disabled workflow" {
					state = "disabled_manually"
				}
				jsonOutput(map[string]string{"path": ".github/workflows/nvkr-dev.yml", "state": state})
			} else if path == "repos/"+dispatchRepo+"/git/ref/heads/feature%2Fpipeline%3B%24shell" {
				sha, ref := dispatchCommit, "refs/heads/"+dispatchBranch
				if failure == "remote mismatch" {
					sha = strings.Repeat("b", 40)
				}
				if failure == "wrong ref" {
					ref = "refs/tags/" + dispatchBranch
				}
				value := map[string]any{"ref": ref, "object": map[string]string{"type": "commit", "sha": sha}}
				switch failure {
				case "malformed response":
					fmt.Print("{")
				case "ambiguous response":
					jsonOutput([]any{value})
				case "multiple responses":
					jsonOutput(value)
					jsonOutput(value)
				case "missing object":
					jsonOutput(map[string]string{"ref": ref})
				default:
					jsonOutput(value)
				}
			} else {
				os.Exit(100)
			}
		case "workflow":
			if failure == "dispatch timeout" {
				os.Exit(124)
			}
			if failure == "dispatch rejected" {
				os.Exit(1)
			}
		default:
			os.Exit(101)
		}
	} else {
		os.Exit(102)
	}
	os.Exit(0)
}

func pipelineDevFixture(t *testing.T, failure string, args ...string) (string, int, []adapterCall) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(root, "bin")
	for _, dir := range []string{bin, filepath.Join(root, "scripts")} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	script := filepath.Join(root, "scripts/pipeline-dev.sh")
	if err := os.WriteFile(script, readFile(t, "../../scripts/pipeline-dev.sh"), 0o700); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	timer, err := exec.LookPath("timeout")
	if err != nil {
		timer, err = exec.LookPath("gtimeout")
	}
	if err != nil {
		t.Fatal("timeout or gtimeout is required", err)
	}
	for _, tool := range []string{"git", "gh", "timeout"} {
		body := "#!/bin/sh\nexec " + shellLiteral(executable) + " -test.run=^TestPipelineDevCommandHelper$ -- " + tool + " \"$@\"\n"
		if err := os.WriteFile(filepath.Join(bin, tool), []byte(body), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	callsFile := filepath.Join(root, "calls.jsonl")
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "bash", append([]string{script}, args...)...)
	command.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "NVKR_PIPELINE_DEV_HELPER=1", "NVKR_ADAPTER_CALLS="+callsFile, "NVKR_ADAPTER_ROOT="+root, "NVKR_ADAPTER_FAILURE="+failure, "NVKR_REAL_TIMEOUT="+timer)
	command.Stdin = strings.NewReader("must not reach child commands")
	var output bytes.Buffer
	command.Stdout, command.Stderr = &output, &output
	runErr := ownedcmd.Run(ctx, command)
	status := 0
	if runErr != nil {
		var exit *exec.ExitError
		if !errors.As(runErr, &exit) {
			t.Fatalf("adapter: %v\n%s", runErr, output.String())
		}
		status = exit.ExitCode()
	}
	var calls []adapterCall
	raw, err := os.ReadFile(callsFile)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	for _, row := range bytes.Split(bytes.TrimSpace(raw), []byte("\n")) {
		if len(row) == 0 {
			continue
		}
		var call adapterCall
		if err := json.Unmarshal(row, &call); err != nil {
			t.Fatal(err)
		}
		if call.Directory != root || call.Host != "github.com" || call.Prompt != "1" || call.GitPrompt != "0" || !call.StdinEOF {
			t.Fatalf("command boundary: %+v", call)
		}
		if call.Tool == "timeout" {
			if len(call.Args) < 3 || !slices.Equal(call.Args[:2], []string{"--kill-after=5s", "30"}) {
				t.Fatalf("unbounded command: %v", call.Args)
			}
			continue
		}
		calls = append(calls, call)
	}
	return output.String(), status, calls
}

func dispatchCalls(calls []adapterCall) []adapterCall {
	return slices.DeleteFunc(slices.Clone(calls), func(call adapterCall) bool {
		return call.Tool != "gh" || len(call.Args) < 2 || !slices.Equal(call.Args[:2], []string{"workflow", "run"})
	})
}

func TestPipelineDevDispatch(t *testing.T) {
	for _, check := range []bool{false, true} {
		t.Run(fmt.Sprint(check), func(t *testing.T) {
			args := []string{"--repo", dispatchRepo}
			if check {
				args = append(args, "--check")
			}
			output, status, calls := pipelineDevFixture(t, "", args...)
			if status != 0 || !strings.Contains(output, dispatchCommit) {
				t.Fatalf("exit %d: %s", status, output)
			}
			dispatched := dispatchCalls(calls)
			if check {
				if len(dispatched) != 0 || !strings.Contains(output, "no workflow dispatched") {
					t.Fatal(output, dispatched)
				}
				return
			}
			want := []string{"workflow", "run", "nvkr-dev.yml", "--ref", dispatchBranch, "--repo", dispatchRepo}
			if len(dispatched) != 1 || !slices.Equal(dispatched[0].Args, want) || !strings.Contains(output, "Dispatch request accepted") || strings.Contains(output, "succeeded") {
				t.Fatal(output, dispatched)
			}
			for _, line := range strings.Split(output, "\n") {
				if !strings.HasPrefix(line, "Inspect matching runs: ") {
					continue
				}
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				parsed, err := ownedcmd.Output(ctx, "bash", "-c", "set -- "+strings.TrimPrefix(line, "Inspect matching runs: ")+"; printf '%s\\000' \"$@\"")
				if err != nil {
					t.Fatal(err)
				}
				fields := strings.Split(strings.TrimSuffix(string(parsed), "\x00"), "\x00")
				for flag, value := range map[string]string{"--branch": dispatchBranch, "--commit": dispatchCommit, "--repo": dispatchRepo} {
					i := slices.Index(fields, flag)
					if i < 0 || i+1 >= len(fields) || fields[i+1] != value {
						t.Fatalf("quoted status command %q", fields)
					}
				}
				return
			}
			t.Fatal("missing run inspection command")
		})
	}
}

func TestPipelineDevRejectsPreflight(t *testing.T) {
	for _, tc := range []struct {
		failure, code, message string
		status                 int
	}{
		{"dirty", "NVKR-E-USAGE", "tracked and untracked", 2}, {"untracked", "NVKR-E-USAGE", "tracked and untracked", 2},
		{"detached", "NVKR-E-FETCH-FAILED", "named branch", 20}, {"remote mismatch", "NVKR-E-USAGE", "does not match local HEAD", 2},
		{"missing workflow", "NVKR-E-FETCH-FAILED", "default branch first", 20}, {"disabled workflow", "NVKR-E-USAGE", "registered and active", 2},
		{"timeout", "NVKR-E-TIMEOUT", "No workflow was dispatched", 31}, {"malformed response", "NVKR-E-FETCH-FAILED", "malformed JSON", 20},
		{"ambiguous response", "NVKR-E-FETCH-FAILED", "ambiguous response", 20}, {"multiple responses", "NVKR-E-FETCH-FAILED", "malformed JSON", 20},
		{"missing object", "NVKR-E-USAGE", "does not match local HEAD", 2}, {"wrong ref", "NVKR-E-USAGE", "does not match local HEAD", 2},
		{"local moved", "NVKR-E-USAGE", "changed during preflight", 2}, {"invalid branch", "NVKR-E-USAGE", "invalid branch", 2}, {"invalid commit", "NVKR-E-USAGE", "exact GitHub commit SHA", 2},
	} {
		t.Run(tc.failure, func(t *testing.T) {
			output, status, calls := pipelineDevFixture(t, tc.failure, "--repo", dispatchRepo)
			if status != tc.status || !strings.Contains(output, tc.code) || !strings.Contains(output, tc.message) || len(dispatchCalls(calls)) != 0 {
				t.Fatalf("exit %d: %s; calls %+v", status, output, calls)
			}
		})
	}
}

func TestPipelineDevInvalidRepository(t *testing.T) {
	for _, repo := range []string{"", "nvkr", "https://github.com/example/nvkr", "example/../nvkr", "example/..", "example/nvkr\n", "--flag/repo"} {
		t.Run(repo, func(t *testing.T) {
			output, status, calls := pipelineDevFixture(t, "", "--repo", repo)
			if status != 2 || !strings.Contains(output, "NVKR-E-USAGE") || len(calls) != 0 {
				t.Fatalf("exit %d: %s; calls %+v", status, output, calls)
			}
		})
	}
}

func TestPipelineDevDispatchFailure(t *testing.T) {
	for _, tc := range []struct {
		failure, code, detail string
		status                int
	}{{"dispatch timeout", "NVKR-E-TIMEOUT", "outcome is unknown", 31}, {"dispatch rejected", "NVKR-E-FETCH-FAILED", "inspect matching runs before retrying", 20}} {
		t.Run(tc.failure, func(t *testing.T) {
			output, status, calls := pipelineDevFixture(t, tc.failure, "--repo", dispatchRepo)
			if status != tc.status || !strings.Contains(output, tc.code) || !strings.Contains(output, tc.detail) || len(dispatchCalls(calls)) != 1 || !strings.Contains(output, "Inspect matching runs:") || strings.Contains(output, "Dispatch request accepted") {
				t.Fatalf("exit %d: %s; calls %+v", status, output, calls)
			}
		})
	}
}
