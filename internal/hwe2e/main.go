// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Command hwe2e holds the checks the NVKE hardware E2E workflows run around the hardware site.
//
// check-junit compares a run's Ginkgo JUnit with the specs a dry run selected. check-node-image checks that a
// node image URL serves the bytes the lane names, or describes what a publication run recorded.
// publish-node-image copies an NVKR-built node image to the location the hardware site reads and records what
// was checked. Each subcommand's usage text lists its arguments, and the header of its source file lists its
// exit codes. None of them signs, publishes a Release or gates one: every record they write is a description,
// not authority.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/signal"
	"strings"
	"syscall"

	nerrors "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

type subcommand struct {
	name string
	run  func(ctx context.Context, args []string, stdout, stderr io.Writer) error
}

var subcommands = []subcommand{
	{"check-junit", runCheckJUnit},
	{"check-node-image", runCheckNodeImage},
	{"publish-node-image", runPublishNodeImage},
}

// run dispatches one subcommand and maps its error to the process exit status.
func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		return report(stderr, usageError("a subcommand is required: %s", subcommandNames()))
	}
	for _, sub := range subcommands {
		if sub.name != args[0] {
			continue
		}
		err := sub.run(ctx, args[1:], stdout, stderr)
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return report(stderr, err)
	}
	return report(stderr, usageError("unknown subcommand %q: want one of %s", args[0], subcommandNames()))
}

func subcommandNames() string {
	names := make([]string, 0, len(subcommands))
	for _, sub := range subcommands {
		names = append(names, sub.name)
	}
	return strings.Join(names, ", ")
}

// report prints a failure as "nvkr: [CODE] message" and returns its exit status. An error that carries no code is
// an internal one: nothing here may reach a caller without a stable code.
func report(stderr io.Writer, err error) int {
	if err == nil {
		return 0
	}
	var coded *nerrors.Error
	if !errors.As(err, &coded) {
		err = nerrors.Wrap(nerrors.CodeInternal, err, "unexpected failure")
	}
	fmt.Fprintf(stderr, "nvkr: %s\n", err)
	return nerrors.ExitCode(err)
}

func usageError(format string, args ...any) error {
	return nerrors.New(nerrors.CodeUsage, format, args...)
}

func unsupported(format string, args ...any) error {
	return nerrors.New(nerrors.CodeUnsupported, format, args...)
}

func verifyFailed(format string, args ...any) error {
	return nerrors.New(nerrors.CodeVerifyFailed, format, args...)
}

func immutable(format string, args ...any) error {
	return nerrors.New(nerrors.CodeImmutableViolation, format, args...)
}

// flagSet is a flag set that reports problems as coded usage errors instead of printing them itself.
type flagSet struct{ fs *flag.FlagSet }

func newCommandFlags(name string) *flagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Usage = func() {}
	return &flagSet{fs: fs}
}

// choice accepts one value from a fixed list.
func (s *flagSet) choice(name, usage string, choices []string) *string {
	value := new(string)
	s.fs.Func(name, usage, func(text string) error {
		for _, choice := range choices {
			if text == choice {
				*value = text
				return nil
			}
		}
		return fmt.Errorf("invalid choice %q (choose from %s)", text, strings.Join(choices, ", "))
	})
	return value
}

// parse parses args and refuses positional arguments, which no subcommand takes. It prints the usage text only
// when it was asked for, so a mistake is reported as one coded line.
func (s *flagSet) parse(args []string, stderr io.Writer, usage string) error {
	if err := s.fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprintf(stderr, "usage: hwe2e %s %s\n", s.fs.Name(), usage)
			s.fs.SetOutput(stderr)
			s.fs.PrintDefaults()
			return err
		}
		return usageError("%v", err)
	}
	if s.fs.NArg() > 0 {
		return usageError("unexpected argument %q", s.fs.Arg(0))
	}
	return nil
}

// given returns the names of the flags the command line gave.
func (s *flagSet) given() map[string]bool {
	given := map[string]bool{}
	s.fs.Visit(func(f *flag.Flag) { given[f.Name] = true })
	return given
}

// requireFlags refuses a command line that omits any of the named flags, and names every one it omits.
func requireFlags(given map[string]bool, names ...string) error {
	var omitted []string
	for _, name := range names {
		if !given[name] {
			omitted = append(omitted, "--"+name)
		}
	}
	if len(omitted) > 0 {
		return usageError("the following arguments are required: %s", strings.Join(omitted, ", "))
	}
	return nil
}

// marshalRecord renders a record the way every consumer of these files reads it: two-space indent, keys sorted at
// every level, and no HTML escaping, so a label filter keeps its "&&". Records are maps so key order is the sort.
func marshalRecord(record map[string]any) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(record); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

// writeNewFile creates path exclusively, so a record is never replaced and a symbolic link is never followed.
func writeNewFile(path string, data []byte, mode os.FileMode) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if errors.Is(err, fs.ErrExist) {
		return immutable("refusing to replace %s", path)
	}
	if err != nil {
		return immutable("could not create %s: %v", path, err)
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return immutable("could not write %s: %v", path, err)
	}
	if err := file.Close(); err != nil {
		return immutable("could not write %s: %v", path, err)
	}
	return nil
}

// pathExists reports whether anything, including a dangling symbolic link, is at path.
func pathExists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}
