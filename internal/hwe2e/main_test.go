// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"strings"
	"testing"

	nerrors "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestRunDispatchesSubcommands(t *testing.T) {
	names := []string{"check-junit", "check-node-image", "publish-node-image"}
	if got := subcommandNames(); got != strings.Join(names, ", ") {
		t.Errorf("subcommands = %s, want %s", got, strings.Join(names, ", "))
	}

	cases := []struct {
		name     string
		args     []string
		code     int
		fragment string
	}{
		{"no subcommand", nil, 2, "a subcommand is required: " + strings.Join(names, ", ")},
		{"an unknown subcommand", []string{"check-everything"}, 2, `unknown subcommand "check-everything"`},
		{"a flag in place of a subcommand", []string{"--help"}, 2, `unknown subcommand "--help"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := run(context.Background(), tc.args, &stdout, &stderr)
			if code != tc.code || !strings.Contains(stderr.String(), "[NVKR-E-USAGE]") || !strings.Contains(stderr.String(), tc.fragment) {
				t.Errorf("run = %d, stderr %q; want %d with %q", code, stderr.String(), tc.code, tc.fragment)
			}
			if stdout.Len() != 0 {
				t.Errorf("stdout = %q, want nothing", stdout.String())
			}
		})
	}

	for _, name := range names {
		t.Run("help for "+name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := run(context.Background(), []string{name, "-h"}, &stdout, &stderr); code != 0 {
				t.Errorf("exit = %d, want 0 for a request for help", code)
			}
			if !strings.Contains(stderr.String(), "usage: hwe2e "+name+" ") {
				t.Errorf("stderr = %q, want the usage text", stderr.String())
			}
		})
	}
}

func TestReportCodesEveryFailure(t *testing.T) {
	cases := []struct {
		name string
		err  error
		code int
		text string
	}{
		{"no error", nil, 0, ""},
		{"a coded error", immutable("refusing to replace %s", "x"), 47, "nvkr: [NVKR-E-IMMUTABLE-VIOLATION] refusing to replace x\n"},
		{"a wrapped coded error", fmt.Errorf("while writing: %w", verifyFailed("digest differs")), 22, "[NVKR-E-VERIFY-FAILED]"},
		{"an error that carries no code", errors.New("boom"), nerrors.ExitCode(nerrors.New(nerrors.CodeInternal, "x")), "[NVKR-E-INTERNAL] unexpected failure"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stderr bytes.Buffer
			if code := report(&stderr, tc.err); code != tc.code {
				t.Errorf("exit = %d, want %d", code, tc.code)
			}
			if !strings.Contains(stderr.String(), tc.text) {
				t.Errorf("stderr = %q, want %q", stderr.String(), tc.text)
			}
		})
	}
	if code := nerrors.ExitCode(nerrors.New(nerrors.CodeInternal, "x")); code == 0 {
		t.Error("an internal failure must not exit 0")
	}
}

func TestRequireFlagsNamesEveryOmittedFlag(t *testing.T) {
	cases := []struct {
		name  string
		given map[string]bool
		want  string
	}{
		{"every flag given", map[string]bool{"slot": true, "out": true}, ""},
		{"extra flags are not required ones", map[string]bool{"slot": true, "out": true, "timeout": true}, ""},
		{"one omitted", map[string]bool{"slot": true}, "the following arguments are required: --out"},
		{"two omitted, in the order they are required", map[string]bool{}, "the following arguments are required: --slot, --out"},
		{"a flag given as false is omitted", map[string]bool{"slot": false, "out": true}, "the following arguments are required: --slot"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := requireFlags(tc.given, "slot", "out")
			if tc.want == "" {
				if err != nil {
					t.Errorf("error = %v, want none", err)
				}
				return
			}
			assertCoded(t, err, nerrors.CodeUsage, tc.want)
			if got := err.Error(); !strings.HasSuffix(got, tc.want) {
				t.Errorf("error = %q, want it to end with %q so no omitted flag is left out", got, tc.want)
			}
		})
	}

	// Every subcommand states all of what it needs, not the first missing piece.
	commands := []struct {
		name string
		want string
	}{
		{"check-junit", "the following arguments are required: --dry, --stage, --label-filter"},
		{"check-node-image", "the following arguments are required: --slot, --out"},
		{"publish-node-image", "the following arguments are required: --slot, --reference, --output, --version, --s3-uri, --prefix, --cdn-domain, --region, --work-dir, --out"},
	}
	for _, tc := range commands {
		t.Run(tc.name+" with no arguments", func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := run(context.Background(), []string{tc.name}, &stdout, &stderr); code != 2 || !strings.Contains(stderr.String(), tc.want+"\n") {
				t.Errorf("run = %d, stderr %q; want 2 with %q", code, stderr.String(), tc.want)
			}
		})
	}
}

func TestFlagSetReportsMistakesAsOneCodedLine(t *testing.T) {
	cases := []struct {
		name     string
		args     []string
		fragment string
	}{
		{"an unknown flag", []string{"--bogus"}, "bogus"},
		{"a missing value", []string{"--count"}, "count"},
		{"a bad choice", []string{"--mode", "sideways"}, `invalid choice "sideways"`},
		{"a positional argument", []string{"stray"}, `unexpected argument "stray"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			set := newCommandFlags("demo")
			set.choice("mode", "a mode", []string{"fast", "slow"})
			set.fs.Int("count", 0, "a count")
			var stderr bytes.Buffer
			err := set.parse(tc.args, &stderr, "--mode MODE")
			assertCoded(t, err, nerrors.CodeUsage, tc.fragment)
			if stderr.Len() != 0 {
				t.Errorf("stderr = %q: the flag set must leave printing to the caller", stderr.String())
			}
		})
	}

	t.Run("a request for help prints the usage text", func(t *testing.T) {
		set := newCommandFlags("demo")
		var stderr bytes.Buffer
		if err := set.parse([]string{"-h"}, &stderr, "--mode MODE"); !errors.Is(err, flag.ErrHelp) {
			t.Errorf("error = %v, want flag.ErrHelp", err)
		}
		if !strings.Contains(stderr.String(), "usage: hwe2e demo --mode MODE") {
			t.Errorf("stderr = %q", stderr.String())
		}
	})

	t.Run("given names only the flags that were set", func(t *testing.T) {
		set := newCommandFlags("demo")
		mode := set.choice("mode", "a mode", []string{"fast", "slow"})
		set.fs.Int("count", 0, "a count")
		if err := set.parse([]string{"--mode", "slow"}, &bytes.Buffer{}, ""); err != nil {
			t.Fatal(err)
		}
		given := set.given()
		if !given["mode"] || given["count"] || *mode != "slow" {
			t.Errorf("given = %v, mode = %q", given, *mode)
		}
	})
}
