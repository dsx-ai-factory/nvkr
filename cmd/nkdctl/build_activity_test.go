// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/NVIDIA/nke-recipes/internal/buildactivity"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/lifecycle"
	"github.com/urfave/cli/v2"
	"golang.org/x/sys/unix"
)

func TestLocalBuildActivityFlagAdmitsBeforeAuthoredInput(t *testing.T) {
	for _, extra := range [][]string{
		{"--build-activity-fd", "1"}, {"--build-activity-fd", "-1"},
		{"--build-activity-fd", "3", "--dry-run"}, {"--build-activity-fd", "3", "--publish"},
	} {
		called := false
		app := &cli.App{Commands: []*cli.Command{cmdRunWithLocalExecutor(func(context.Context, *lifecycle.PreparedRun, lifecycle.LocalOptions) (*lifecycle.Run, error) {
			called = true
			return nil, nil
		})}}
		args := append([]string{"nkdctl", "run", "local", "--definition", "missing.yaml", "--environment", "missing.yaml", "--attempt", "fresh"}, extra...)
		err := app.RunContext(t.Context(), args)
		var coded *e.Error
		if called || !errors.As(err, &coded) || coded.Code != e.CodeUsage {
			t.Fatalf("invalid fd reached inputs/execution: %v", err)
		}
	}
}

func TestLocalBuildActivityIsSeparateAndOptional(t *testing.T) {
	lifecycleCommandRepo(t)
	for _, opted := range []bool{false, true} {
		t.Run(strconv.FormatBool(opted), func(t *testing.T) {
			reader, writer, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			fd, err := unix.FcntlInt(writer.Fd(), unix.F_DUPFD_CLOEXEC, 3)
			if err != nil {
				t.Fatal(err)
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			if !opted {
				if err := unix.Close(fd); err != nil {
					t.Fatal(err)
				}
			}
			execute := func(_ context.Context, p *lifecycle.PreparedRun, o lifecycle.LocalOptions) (*lifecycle.Run, error) {
				if (o.Activity != nil) != opted {
					t.Fatal("opt-in changed")
				}
				if opted {
					if _, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0); !errors.Is(err, unix.EBADF) {
						t.Fatal("inherited activity fd remains open")
					}
					o.Activity.Offer(buildactivity.Record{Version: 1, Operation: "os/build-node", Available: true, Files: 1, Bytes: 1024})
				}
				run := p.Description()
				run.Status = lifecycle.RunComplete
				run.Lane = "unqualified-local-execution"
				return &run, nil
			}
			app := &cli.App{Commands: []*cli.Command{cmdRunWithLocalExecutor(execute)}}
			args := []string{"nkdctl", "run", "local", "--definition", "definitions/nke-v1/definition.yaml", "--environment", "definitions/nke-v1/environment-local.yaml", "--attempt", "fresh"}
			if opted {
				args = append(args, "--build-activity-fd", strconv.Itoa(fd))
			}
			raw, err := runCLIWithCapturedStdout(t, app, args...)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(raw), "status: complete") || bytes.Contains(raw, []byte("growth_observed")) {
				t.Fatalf("public output changed: %q", raw)
			}
			telemetry, err := io.ReadAll(io.LimitReader(reader, buildactivity.MaxRecordBytes+1))
			if err != nil {
				t.Fatal(err)
			}
			if !opted {
				if len(telemetry) != 0 {
					t.Fatal("default emitted telemetry")
				}
				return
			}
			if len(telemetry) == 0 || telemetry[len(telemetry)-1] != '\n' {
				t.Fatal("missing bounded metadata frame")
			}
			record, err := buildactivity.Decode(telemetry[:len(telemetry)-1])
			if err != nil || record.Operation != "os/build-node" || record.Bytes != 1024 {
				t.Fatalf("metadata=%+v %v", record, err)
			}
		})
	}
}

func TestBuildActivityFlagIsNotPublicHelp(t *testing.T) {
	app := &cli.App{Commands: []*cli.Command{cmdRun()}}
	var output bytes.Buffer
	app.Writer = &output
	if err := app.RunContext(t.Context(), []string{"nkdctl", "run", "local", "--help"}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "build-activity-fd") {
		t.Fatal("internal diagnostic FD exposed in public help")
	}
}
