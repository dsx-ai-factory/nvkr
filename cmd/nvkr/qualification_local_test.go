// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/internal/nvkedemo"
	"github.com/dsx-ai-factory/nvkr/internal/qualificationprogress"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/urfave/cli/v2"
	"golang.org/x/sys/unix"
)

func TestQualifyLocalCommand(t *testing.T) {
	for _, mode := range []string{"exact", "missing", "positional", "failure", "progress", "invalid progress"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			var output bytes.Buffer
			var progressReader *os.File
			progressFD := -1
			if mode == "progress" {
				reader, writer, err := os.Pipe()
				if err != nil {
					t.Fatal(err)
				}
				progressReader = reader
				defer reader.Close()
				progressFD, err = unix.FcntlInt(writer.Fd(), unix.F_DUPFD_CLOEXEC, 3)
				if err != nil {
					t.Fatal(err)
				}
				_ = writer.Close()
			}
			command := cmdRunQualifyLocalWith(func(_ context.Context, options lifecycle.LocalQualificationOptions) (*lifecycle.LocalQualificationResult, error) {
				calls++
				if (options.Progress != nil) != (mode == "progress") {
					t.Fatal("qualification progress did not require explicit descriptor")
				}
				if options.Progress != nil {
					options.Progress(qualificationprogress.Event{Kind: qualificationprogress.KindStage, Stage: qualificationprogress.StageSmoke, State: qualificationprogress.StateRunning})
					options.Progress(qualificationprogress.Event{Kind: qualificationprogress.KindStage, Stage: "private-sentinel\x1b[2J", State: qualificationprogress.StateCompleted})
				}
				if options.Workspace != "attempt" || options.PolicyPath != "policy" || options.BuilderKeyPath != "builder" || options.SmokeKeyPath != "smoke" || options.QualifierKeyPath != "qualifier" || options.CosignExecutable != "cosign" || options.Repository != "localhost:5000/demo" || options.Scratch != "scratch" || options.EvidenceOut != "evidence" || !options.Registry.PlainHTTP || !options.Registry.Anonymous {
					t.Fatal("command lost explicit qualification inputs")
				}
				if mode == "failure" {
					return &lifecycle.LocalQualificationResult{Status: "failed"}, e.New(e.CodeGateUnmet, "fixture refusal")
				}
				return &lifecycle.LocalQualificationResult{SchemaVersion: "nvkr.dev/local-qualification-result/v1", Status: "passed", Lane: "unqualified-local-execution", Attempt: "attempt", ContentSubject: "sha256:" + strings.Repeat("a", 64), Publication: lifecycle.LocalQualificationReferences{Evidence: "localhost:5000/demo@sha256:" + strings.Repeat("b", 64)}}, nil
			})
			args := []string{"nvkr", "qualify-local", "--workspace", "attempt", "--policy", "policy", "--builder-key", "builder", "--smoke-key", "smoke", "--qualifier-key", "qualifier", "--cosign", "cosign", "--repository", "localhost:5000/demo", "--scratch", "scratch", "--evidence-out", "evidence", "--plain-http", "--anonymous"}
			if mode == "missing" {
				args = []string{"nvkr", "qualify-local"}
			}
			if mode == "positional" {
				args = append(args, "extra")
			}
			if mode == "progress" {
				args = append(args, "--qualification-progress-fd", strconv.Itoa(progressFD))
			}
			if mode == "invalid progress" {
				args = append(args, "--qualification-progress-fd", "2")
			}
			app := &cli.App{Commands: []*cli.Command{command}, Writer: &output, ErrWriter: &output}
			err := app.RunContext(t.Context(), args)
			if mode == "missing" || mode == "positional" || mode == "invalid progress" {
				if e.ExitCode(err) != e.ExitCode(e.New(e.CodeUsage, "")) || calls != 0 {
					t.Fatal("invalid command reached lifecycle", err)
				}
				return
			}
			if calls != 1 {
				t.Fatal("lifecycle invocation count", calls)
			}
			if progressReader != nil {
				raw, err := io.ReadAll(progressReader)
				if err != nil {
					t.Fatal(err)
				}
				event, err := qualificationprogress.Decode(bytes.TrimSuffix(raw, []byte{'\n'}))
				if err != nil || event.Stage != qualificationprogress.StageSmoke || event.State != qualificationprogress.StateRunning {
					t.Fatalf("invalid progress framing: %q %v", raw, err)
				}
			}
			if mode == "failure" {
				if err == nil || !strings.Contains(output.String(), `"status":"failed"`) {
					t.Fatal("failure was not preserved", err)
				}
				return
			}
			if err := nvkedemo.ValidateLocalQualificationResult(output.Bytes(), "attempt"); err != nil {
				t.Fatalf("actual CLI output refused by demo reader: %v", err)
			}
			if err != nil || !strings.Contains(output.String(), `"lane":"unqualified-local-execution"`) {
				t.Fatal("local result missing", err)
			}
		})
	}
}
