// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/sourcegit"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
)

// ArtifactTestOptions configures an unqualified, trusted-input diagnostic.
// It authenticates neither executable membership in a Run nor suite completeness.
type ArtifactTestOptions struct {
	Plan, Output, Artifact, Suite, WorkspaceRoot string
	Keep                                         bool
	Timeout                                      time.Duration
	Stdout, Stderr                               io.Writer
}

type artifactTestCounts struct {
	TopLevelTests int `json:"topLevelTests"`
	Packages      int `json:"packages"`
}

type artifactTestResult struct {
	CLIArtifactTestInputs
	ArtifactSHA256 string `json:"artifactSha256"`
	TestRevision   string `json:"testRevision"`
	Suite          string `json:"suite"`
	Authority      string `json:"authority"`
	Result         string `json:"result"`
	artifactTestCounts
}

// TestCLIArtifact validates the complete Plan and selects its CLI producer before
// source, workspace or subprocess work. The source and pipeline owners import and
// execute the matching upstream tests against a private copy of supplied bytes.
func TestCLIArtifact(ctx context.Context, options ArtifactTestOptions) error {
	return testCLIArtifact(ctx, options, sourcegit.ImportLocal, pipeline.RunArtifactDiagnostic)
}

func testCLIArtifact(ctx context.Context, options ArtifactTestOptions,
	importSource func(context.Context, []string, string, string, string, string, io.Writer) error,
	run func(context.Context, pipeline.ArtifactDiagnostic, io.Writer, io.Writer) error) (returnErr error) {
	if ctx == nil {
		return e.New(e.CodeUsage, "artifact testing requires a context")
	}
	if options.Suite == "" {
		options.Suite = "contract"
	}
	if options.Suite != "contract" && options.Suite != "cli" {
		return e.New(e.CodeUsage, "suite must be contract or cli")
	}
	inputs, err := LoadCLIArtifactTestInputs(ctx, options.Plan, options.Output)
	if err != nil {
		return err
	}
	if options.Stdout == nil {
		options.Stdout = io.Discard
	}
	if options.Stderr == nil {
		options.Stderr = io.Discard
	}
	info, err := os.Stat(options.Artifact)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return e.New(e.CodeUsage, "artifact must be an executable regular file")
	}
	artifact, err := os.Open(options.Artifact)
	if err != nil {
		return e.Wrap(e.CodeUsage, err, "opening supplied executable")
	}
	defer func() { _ = artifact.Close() }()
	info, err = artifact.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return e.New(e.CodeUsage, "artifact must be an executable regular file")
	}
	root := options.WorkspaceRoot
	if root == "" {
		root = ".nvkr/artifact-tests"
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return e.Wrap(e.CodeUsage, err, "resolving diagnostic workspace")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return e.Wrap(e.CodeBuildFailed, err, "creating diagnostic workspace root")
	}
	workspace, err := os.MkdirTemp(root, "cli-")
	if err != nil {
		return e.Wrap(e.CodeBuildFailed, err, "creating private diagnostic workspace")
	}
	defer func() {
		if !options.Keep {
			if err := os.RemoveAll(workspace); err != nil {
				returnErr = errors.Join(returnErr, e.Wrap(e.CodeBuildFailed, err, "removing diagnostic workspace"))
			}
		}
	}()
	_, _ = fmt.Fprintf(options.Stdout, "Workspace: %s (retained=%t)\n", workspace, options.Keep)
	staged := filepath.Join(workspace, "nvkectl")
	copy, err := os.OpenFile(staged, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o555)
	if err != nil {
		return e.Wrap(e.CodeBuildFailed, err, "staging executable")
	}
	hash := sha256.New() // exact blob integrity, not structured identity or authority
	_, copyErr := io.Copy(io.MultiWriter(copy, hash), artifact)
	closeErr := copy.Close()
	if err := errors.Join(copyErr, closeErr); err != nil {
		return e.Wrap(e.CodeBuildFailed, err, "copying staged executable")
	}
	checksum := hex.EncodeToString(hash.Sum(nil))
	if err := importSource(ctx, os.Environ(), filepath.Join(workspace, "nvke"), inputs.Repo, inputs.Commit, inputs.TreeHash, options.Stderr); err != nil {
		return diagnosticError(ctx, err)
	}
	makefile, err := os.ReadFile(filepath.Join(workspace, "nvke", "cli", "Makefile"))
	if err != nil {
		return e.Wrap(e.CodeFetchFailed, err, "reading upstream CLI entrypoint")
	}
	if !regexp.MustCompile(`(?m)^test-artifact:`).Match(makefile) {
		return e.New(e.CodeEngineCapability, "pinned NVKE revision has no test-artifact target; refresh explicit inputs and regenerate the Plan")
	}
	invocation := pipeline.ArtifactDiagnostic{Workspace: workspace, Artifact: staged, Image: inputs.ToolchainImage,
		Platform: inputs.Platform, Version: inputs.ExpectedVersion, Suite: options.Suite, Timeout: options.Timeout}
	if err := run(ctx, invocation, options.Stdout, options.Stderr); err != nil {
		return diagnosticError(ctx, err)
	}
	log, err := os.Open(filepath.Join(workspace, "tests.jsonl"))
	if err != nil {
		return e.Wrap(e.CodeBuildFailed, err, "opening diagnostic results")
	}
	counts, summaryErr := summarizeArtifactTests(log, options.Suite)
	_ = log.Close()
	if summaryErr != nil {
		return summaryErr
	}
	file, err := os.Open(staged)
	if err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "reopening staged executable")
	}
	hash.Reset()
	_, hashErr := io.Copy(hash, file)
	_ = file.Close()
	if hashErr != nil {
		return e.Wrap(e.CodeVerifyFailed, hashErr, "hashing staged executable")
	}
	if hex.EncodeToString(hash.Sum(nil)) != checksum {
		return e.New(e.CodeVerifyFailed, "staged artifact bytes changed during testing")
	}
	if ctx.Err() != nil {
		return e.Wrap(e.CodeTimeout, ctx.Err(), "artifact diagnostic canceled")
	}
	result := artifactTestResult{CLIArtifactTestInputs: *inputs, ArtifactSHA256: checksum, TestRevision: inputs.Commit, Suite: options.Suite,
		Authority: "unqualified-diagnostic", Result: "pass", artifactTestCounts: counts}
	raw, err := json.Marshal(result)
	if err != nil {
		return e.Wrap(e.CodeInternal, err, "encoding diagnostic result")
	}
	if err := os.WriteFile(filepath.Join(workspace, "result.json"), append(raw, '\n'), 0o600); err != nil {
		return e.Wrap(e.CodeBuildFailed, err, "writing diagnostic result")
	}
	_, _ = fmt.Fprintln(options.Stdout, string(raw))
	_, _ = fmt.Fprintf(options.Stdout, "PASS: upstream %s suite; staged artifact checksum unchanged\n", options.Suite)
	return nil
}

func diagnosticError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return e.Wrap(e.CodeTimeout, ctx.Err(), "artifact diagnostic canceled")
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() > 0 {
		return &githubStepExitError{status: exit.ExitCode(), cause: e.Wrap(e.CodeBuildFailed, err, "artifact diagnostic process failed")}
	}
	var coded *e.Error
	if errors.As(err, &coded) {
		return err
	}
	return e.Wrap(e.CodeBuildFailed, err, "artifact diagnostic failed")
}

// Package membership is a diagnostic guard, not an upstream-owned inventory of
// required binary contracts. An ordinary test can satisfy it; keep that limitation
// tested until upstream offers an exhaustive acceptance interface.
func summarizeArtifactTests(input io.Reader, suite string) (artifactTestCounts, error) {
	type key struct{ Package, Test string }
	started, passed, packages := map[key]bool{}, map[key]bool{}, map[string]bool{}
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "{") && !strings.HasPrefix(line, "[") {
			continue
		}
		var event struct {
			Action, Package string
			Test            *string
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal([]byte(line), &fields); err != nil {
			return artifactTestCounts{}, e.New(e.CodeBuildFailed, "malformed Go test event")
		}
		if value, present := fields["Test"]; present && string(value) == "null" {
			return artifactTestCounts{}, e.New(e.CodeBuildFailed, "malformed Go test name")
		}
		if err := json.Unmarshal([]byte(line), &event); err != nil || event.Action == "" || event.Package == "" {
			return artifactTestCounts{}, e.New(e.CodeBuildFailed, "malformed Go test event")
		}
		if event.Action == "fail" || event.Action == "skip" {
			return artifactTestCounts{}, e.New(e.CodeBuildFailed, "required suite %s: %s", event.Action, event.Package)
		}
		if event.Test != nil && *event.Test != "" && !strings.Contains(*event.Test, "/") {
			k := key{event.Package, *event.Test}
			if event.Action == "run" {
				started[k] = true
			}
			if event.Action == "pass" {
				passed[k] = true
			}
		} else if event.Test == nil && event.Action == "pass" {
			packages[event.Package] = true
		}
	}
	if err := scanner.Err(); err != nil {
		return artifactTestCounts{}, e.Wrap(e.CodeBuildFailed, err, "reading Go test events")
	}
	expected := "command-line-arguments"
	if suite == "cli" {
		expected = "github.com/dsx-ai-factory/nvke/cli/cmd/nvkectl"
	}
	found := false
	for test := range started {
		if !passed[test] || !packages[test.Package] {
			return artifactTestCounts{}, e.New(e.CodeBuildFailed, "incomplete Go test or package results")
		}
		if test.Package == expected {
			found = true
		}
	}
	if !found || len(started) != len(passed) {
		return artifactTestCounts{}, e.New(e.CodeBuildFailed, "zero or incomplete tests, or no CLI contract package")
	}
	return artifactTestCounts{TopLevelTests: len(passed), Packages: len(packages)}, nil
}
