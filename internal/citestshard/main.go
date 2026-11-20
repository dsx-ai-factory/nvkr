// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Command citestshard partitions the ordinary Go suite without a maintained test allowlist.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"time"

	nerrors "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

type group struct {
	name, path string
	count      int
}

var groups = []group{{"factory", "pkg/nvkr/factory", 8}, {"lifecycle", "pkg/nvkr/lifecycle", 2}, {"packages", "", 5}}
var testName = regexp.MustCompile(`^(Test|Example|Fuzz)[\pL\pN_]*$`)

func partition(names []string, index, count int) ([]string, error) {
	if count < 1 || index < 0 || index >= count {
		return nil, fmt.Errorf("shard index must be in [0, count)")
	}
	seen := map[string]bool{}
	result := []string{}
	for _, name := range names {
		if seen[name] {
			return nil, fmt.Errorf("duplicate selection names")
		}
		seen[name] = true
		// Exact byte hashing is the historical shard assignment, not a build identity.
		sum := sha256.Sum256([]byte(name))
		if binary.BigEndian.Uint64(sum[:8])%uint64(count) == uint64(index) {
			result = append(result, name)
		}
	}
	slices.Sort(result)
	return result, nil
}

func packagesForGroup(packages []string, module, name string, index int) ([]string, error) {
	seen := map[string]bool{}
	for _, p := range packages {
		if seen[p] {
			return nil, fmt.Errorf("duplicate package")
		}
		seen[p] = true
	}
	heavy := map[string]bool{}
	var selected *group
	for i, g := range groups {
		if g.name == name {
			selected = &groups[i]
		}
		if g.path != "" {
			p := module + "/" + g.path
			if !seen[p] {
				return nil, fmt.Errorf("a required heavy package is missing from go list")
			}
			heavy[p] = true
		}
	}
	if selected == nil || index < 0 || index >= selected.count {
		return nil, fmt.Errorf("unknown group or shard index outside the group")
	}
	if selected.path != "" {
		return []string{module + "/" + selected.path}, nil
	}
	ordinary := []string{}
	for _, p := range packages {
		if !heavy[p] {
			ordinary = append(ordinary, p)
		}
	}
	return partition(ordinary, index, selected.count)
}

type event struct {
	Action, Package, Output string
	Test                    *string
}

func events(log string) ([]event, error) {
	var result []event
	for _, line := range strings.Split(strings.TrimSuffix(log, "\n"), "\n") {
		var e event
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			return nil, err
		}
		result = append(result, e)
	}
	return result, nil
}
func listedTests(log, pkg string) ([]string, error) {
	rows, err := events(log)
	if err != nil {
		return nil, err
	}
	names, terminal := []string{}, []string{}
	seen := map[string]bool{}
	for _, e := range rows {
		if e.Package != pkg {
			return nil, fmt.Errorf("test discovery returned an unexpected package")
		}
		if e.Action == "output" {
			name := strings.TrimSpace(e.Output)
			if testName.MatchString(name) {
				if seen[name] {
					return nil, fmt.Errorf("duplicate discovered test")
				}
				seen[name] = true
				names = append(names, name)
			}
		}
		if isTerminal(e.Action) && e.Test == nil {
			terminal = append(terminal, e.Action)
		}
	}
	if !slices.Equal(terminal, []string{"pass"}) || len(names) == 0 {
		return nil, fmt.Errorf("test discovery did not complete with a unique, nonempty inventory")
	}
	return names, nil
}
func isTerminal(action string) bool { return action == "pass" || action == "fail" || action == "skip" }
func validateResults(log string, packages, selected []string) error {
	rows, err := events(log)
	if err != nil {
		return err
	}
	completed := map[string]bool{}
	tests := map[[2]string]bool{}
	for _, e := range rows {
		if !slices.Contains(packages, e.Package) {
			return fmt.Errorf("test run returned an unexpected package")
		}
		if !isTerminal(e.Action) {
			continue
		}
		if e.Test == nil {
			if completed[e.Package] || e.Action == "fail" {
				return fmt.Errorf("duplicate or failed package result")
			}
			completed[e.Package] = true
		} else if !strings.Contains(*e.Test, "/") {
			key := [2]string{e.Package, *e.Test}
			if tests[key] || e.Action == "fail" {
				return fmt.Errorf("duplicate or failed top-level test result")
			}
			tests[key] = true
		}
	}
	if len(completed) != len(packages) {
		return fmt.Errorf("test run did not complete every selected package")
	}
	if selected != nil {
		if len(packages) != 1 || len(tests) != len(selected) {
			return fmt.Errorf("test results do not match the discovered shard selection")
		}
		for _, name := range selected {
			if !tests[[2]string{packages[0], name}] {
				return fmt.Errorf("test results do not match the discovered shard selection")
			}
		}
	}
	return nil
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("ci-test-shard", flag.ContinueOnError)
	flags.SetOutput(stderr)
	matrix := flags.Bool("matrix", false, "emit the complete GitHub job matrix")
	name := flags.String("group", "", "factory, lifecycle or packages")
	index := flags.Int("index", -1, "shard index")
	timeout := flags.String("timeout", "20m", "Go's per-package test timeout")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return nerrors.Wrap(nerrors.CodeUsage, err, "parse shard arguments")
	}
	if flags.NArg() != 0 {
		return nerrors.New(nerrors.CodeUsage, "unexpected positional arguments")
	}
	if *matrix {
		type entry struct {
			Group string `json:"group"`
			Index int    `json:"index"`
		}
		entries := []entry{}
		for _, g := range groups {
			for i := 0; i < g.count; i++ {
				entries = append(entries, entry{g.name, i})
			}
		}
		return json.NewEncoder(stdout).Encode(struct {
			Include []entry `json:"include"`
		}{entries})
	}
	count := 0
	for _, g := range groups {
		if g.name == *name {
			count = g.count
		}
	}
	if count == 0 || *index < 0 || *index >= count {
		return nerrors.New(nerrors.CodeUsage, "--group and an in-range --index are required")
	}
	capture := func(argv []string, limit time.Duration, emit bool) (string, error) {
		return captured(ctx, argv, "", nil, limit, emit, 32*1024*1024, stdout, stderr)
	}
	module, err := capture([]string{"go", "list", "-m"}, 5*time.Minute, false)
	if err != nil {
		return err
	}
	inventory, err := capture([]string{"go", "list", "./..."}, 5*time.Minute, false)
	if err != nil {
		return err
	}
	packages, err := packagesForGroup(strings.Fields(inventory), strings.TrimSpace(module), *name, *index)
	if err != nil {
		return err
	}
	if len(packages) == 0 {
		return fmt.Errorf("empty package shard")
	}
	command := []string{"go", "test", "-json", "-count=1", "-timeout", *timeout}
	var selected []string
	if *name != "packages" {
		log, err := capture(append([]string{"go", "test", "-json", "-list", "."}, packages...), 5*time.Minute, false)
		if err != nil {
			return err
		}
		names, err := listedTests(log, packages[0])
		if err != nil {
			return err
		}
		selected, err = partition(names, *index, count)
		if err != nil {
			return err
		}
		if len(selected) == 0 {
			return fmt.Errorf("empty test shard")
		}
		command = append(command, "-run", "^("+strings.Join(selected, "|")+")$")
		fmt.Fprintf(stderr, "Selected %d of %d top-level tests/examples/fuzz targets\n", len(selected), len(names))
	}
	log, err := capture(append(command, packages...), 30*time.Minute, true)
	if err != nil {
		return err
	}
	return validateResults(log, packages, selected)
}
func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	if err != nil {
		if nerrors.ExitCode(err) == 2 {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		fmt.Fprintln(os.Stderr, nerrors.Wrap(nerrors.CodeGateUnmet, err, "CI test shard failed"))
		os.Exit(1)
	}
}
