// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/urfave/cli/v2"
)

func TestRunLocalPrintsDurableDiskOutput(t *testing.T) {
	repository, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(repository)
	attemptsRoot := t.TempDir()
	payload := []byte("durable-qcow2")
	var printedPath string
	execute := func(_ context.Context, prepared *lifecycle.PreparedRun,
		options lifecycle.LocalOptions,
	) (*lifecycle.Run, error) {
		run := prepared.Description()
		run.Status = lifecycle.RunComplete
		run.Lane = "unqualified-local-execution"
		run.Observed = []lifecycle.Artifact{{Name: "node-image", Kind: lifecycle.OutputDiskImage}}
		printedPath, err = lifecycle.LocalDiskOutputPath(
			filepath.Join(options.AttemptsRoot, run.Attempt), run.Observed[0].Name)
		if err != nil {
			return nil, err
		}
		if err := os.MkdirAll(filepath.Dir(printedPath), 0o700); err != nil {
			return nil, err
		}
		if err := os.WriteFile(printedPath, payload, 0o444); err != nil {
			return nil, err
		}
		return &run, nil
	}

	oldStdout := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = writer
	t.Cleanup(func() { os.Stdout = oldStdout })
	app := &cli.App{Commands: []*cli.Command{cmdRunWithLocalExecutor(execute)}}
	runErr := app.RunContext(t.Context(), []string{
		"nvkr", "run", "local",
		"--definition", filepath.Join("docs", "examples", "nvke-tutorial", "definition.yaml"),
		"--environment", filepath.Join("docs", "examples", "nvke-tutorial", "environment-local.yaml"),
		"--attempt", "disk-cli-output", "--attempts-root", attemptsRoot,
	})
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	os.Stdout = oldStdout
	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if runErr != nil {
		t.Fatal(runErr)
	}
	if !strings.Contains(string(output), "disk: "+printedPath+"\n") {
		t.Fatalf("local run output omitted durable disk path: %q", output)
	}
	got, err := os.ReadFile(printedPath)
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("printed disk path is not durable: %q, %v", got, err)
	}
}

func TestRunLocalPrintsRetainedDiskOutputWhenCommitCleanupFails(t *testing.T) {
	repository, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(repository)
	attemptsRoot := t.TempDir()
	sentinel := errors.New("marker cleanup failed")
	var diskPath string
	execute := func(_ context.Context, prepared *lifecycle.PreparedRun,
		options lifecycle.LocalOptions,
	) (*lifecycle.Run, error) {
		run := prepared.Description()
		run.Status = lifecycle.RunComplete
		run.Lane = "unqualified-local-execution"
		run.Observed = []lifecycle.Artifact{{Name: "node-image", Kind: lifecycle.OutputDiskImage}}
		diskPath, err = lifecycle.LocalDiskOutputPath(
			filepath.Join(options.AttemptsRoot, run.Attempt), run.Observed[0].Name)
		if err != nil {
			return nil, err
		}
		return &run, e.Wrap(e.CodeImmutableViolation, sentinel, "completing local disk commit transaction")
	}

	oldStdout := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = writer
	t.Cleanup(func() { os.Stdout = oldStdout })
	app := &cli.App{Commands: []*cli.Command{cmdRunWithLocalExecutor(execute)}}
	runErr := app.RunContext(t.Context(), []string{
		"nvkr", "run", "local",
		"--definition", filepath.Join("docs", "examples", "nvke-tutorial", "definition.yaml"),
		"--environment", filepath.Join("docs", "examples", "nvke-tutorial", "environment-local.yaml"),
		"--attempt", "disk-cli-cleanup-warning", "--attempts-root", attemptsRoot,
	})
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	os.Stdout = oldStdout
	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if !errors.Is(runErr, sentinel) || e.ExitCode(runErr) != 47 {
		t.Fatalf("cleanup result error = %v, exit = %d; want retained cause and immutable-warning exit", runErr, e.ExitCode(runErr))
	}
	for _, want := range []string{
		"status: " + lifecycle.RunComplete + "\n",
		"record: " + filepath.Join(attemptsRoot, "disk-cli-cleanup-warning", "lifecycle.json") + "\n",
		"disk: " + diskPath + "\n",
	} {
		if !strings.Contains(string(output), want) {
			t.Fatalf("local cleanup warning output omitted %q: %q", want, output)
		}
	}
}
