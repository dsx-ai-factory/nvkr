// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"encoding/csv"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestArtifactDiagnosticIsolationAndCommands(t *testing.T) {
	for _, suite := range []string{"contract", "cli"} {
		t.Run(suite, func(t *testing.T) {
			workspace := filepath.Join(t.TempDir(), "space:comma,quote\"")
			if err := os.Mkdir(workspace, 0o700); err != nil {
				t.Fatal(err)
			}
			input := ArtifactDiagnostic{Workspace: workspace, Artifact: filepath.Join(workspace, "binary:debug,\""), Image: "go@sha256:" + strings.Repeat("a", 64), Platform: "linux/arm64", Version: "v1", Suite: suite}
			var commands [][]string
			run := func(opts RunOptions, args ...string) error {
				commands = append(commands, append([]string{}, args...))
				if opts.Runtime != "docker" {
					t.Fatal(opts.Runtime)
				}
				if args[0] == "run" && slices.Contains(args, "none") {
					_, _ = io.WriteString(opts.Stdout, "raw test results\n")
				}
				return nil
			}
			if err := runArtifactDiagnostic(t.Context(), input, io.Discard, io.Discard, run); err != nil {
				t.Fatal(err)
			}
			if len(commands) != 2 {
				t.Fatalf("commands: %v", commands)
			}
			acquire, offline := commands[0], commands[1]
			if slices.Contains(acquire, "NVKECTL=/artifact/nvkectl") || strings.Contains(strings.Join(acquire, " "), "dst=/artifact") {
				t.Fatal("artifact exposed to acquisition")
			}
			if !reflect.DeepEqual(acquire[len(acquire)-4:], []string{input.Image, "go", "mod", "download"}) {
				t.Fatal(acquire)
			}
			for _, expected := range []string{"--read-only", "ALL", "no-new-privileges", "none", "GOPROXY=off", "GO=go", "NVKECTL=/artifact/nvkectl", "NVKECTL_EXPECTED_VERSION=v1", "linux/arm64"} {
				if !slices.Contains(offline, expected) {
					t.Fatalf("missing %s", expected)
				}
			}
			target := "test-artifact"
			if suite == "cli" {
				target = "test"
			}
			if !slices.Contains(offline, target) {
				t.Fatal(offline)
			}
			mounts := map[string][]string{}
			for i, arg := range offline {
				if arg == "--mount" {
					fields, err := csv.NewReader(strings.NewReader(offline[i+1])).Read()
					if err != nil {
						t.Fatal(err)
					}
					mounts[fields[2]] = fields
				}
			}
			for target, source := range map[string]string{"/src": filepath.Join(workspace, "nvke"), "/artifact/nvkectl": input.Artifact} {
				if !reflect.DeepEqual(mounts["dst="+target], []string{"type=bind", "src=" + source, "dst=" + target, "readonly"}) {
					t.Fatal(mounts)
				}
			}
			entries, err := os.ReadDir(filepath.Join(workspace, "cache"))
			if err != nil || len(entries) != 0 {
				t.Fatal("cache not initially empty", err)
			}
			raw, err := os.ReadFile(filepath.Join(workspace, "tests.jsonl"))
			if err != nil || string(raw) != "raw test results\n" {
				t.Fatal(string(raw), err)
			}
		})
	}
}

func TestArtifactDiagnosticCleanupAndPrimaryFailure(t *testing.T) {
	for _, phase := range []int{1, 2} {
		t.Run(string(rune('0'+phase)), func(t *testing.T) {
			primary := errors.New("primary process failure")
			calls, cleaned := 0, 0
			err := runArtifactDiagnostic(t.Context(), ArtifactDiagnostic{Workspace: t.TempDir(), Image: "image", Version: "v1", Platform: "linux/amd64", Suite: "contract"}, io.Discard, io.Discard,
				func(opts RunOptions, args ...string) error {
					if args[0] == "rm" {
						cleaned++
						if opts.ctx().Err() != nil {
							t.Fatal("cleanup canceled")
						}
						return errors.New("cleanup failure")
					}
					calls++
					if calls == phase {
						return primary
					}
					return nil
				})
			if !errors.Is(err, primary) || cleaned != 1 {
				t.Fatalf("error=%v cleaned=%d", err, cleaned)
			}
		})
	}
}

func TestArtifactDiagnosticTimeoutCleanup(t *testing.T) {
	cleaned := false
	err := runArtifactDiagnostic(t.Context(), ArtifactDiagnostic{Workspace: t.TempDir(), Image: "image", Version: "v1", Platform: "linux/amd64", Suite: "contract", Timeout: time.Millisecond}, io.Discard, io.Discard,
		func(opts RunOptions, args ...string) error {
			if args[0] == "rm" {
				cleaned = true
				return opts.ctx().Err()
			}
			<-opts.ctx().Done()
			return opts.ctx().Err()
		})
	var coded *e.Error
	if !errors.As(err, &coded) || coded.Code != e.CodeTimeout || !cleaned {
		t.Fatalf("%v cleaned=%t", err, cleaned)
	}
}

func TestArtifactDiagnosticIndependentWorkspaceNames(t *testing.T) {
	names := map[string]bool{}
	for range 2 {
		workspace := filepath.Join(t.TempDir(), "same-basename")
		if err := os.Mkdir(workspace, 0o700); err != nil {
			t.Fatal(err)
		}
		var name string
		err := runArtifactDiagnostic(t.Context(), ArtifactDiagnostic{Workspace: workspace, Image: "image", Version: "v1", Platform: "linux/amd64", Suite: "contract"}, io.Discard, io.Discard,
			func(_ RunOptions, args ...string) error {
				if args[0] == "run" {
					for i, argument := range args {
						if argument == "--name" {
							if name != "" && name != args[i+1] {
								t.Fatal("phases changed invocation ownership")
							}
							name = args[i+1]
						}
					}
				}
				return nil
			})
		if err != nil || name == "" || names[name] {
			t.Fatalf("invocations share container ownership: %q %v", name, err)
		}
		names[name] = true
	}
}
