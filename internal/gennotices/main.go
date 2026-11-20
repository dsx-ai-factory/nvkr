// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Command gennotices emits notices from the modules linked into distributed binaries.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"unicode/utf8"

	nerrors "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

const header = "# Third-Party Notices\n\n" +
	"NVKR (`nvkr`, `nvke-demo`, and the private `nvkr-site-identity-helper` and `nvkr-results-publish`) is\n" +
	"distributed under the Apache License 2.0. These binaries link the third-party\n" +
	"modules listed below. Each module's license, copyright\n" +
	"notice, and NOTICE file (where one exists) is reproduced in full.\n\n" +
	"This file lists modules linked into those four binaries. Tools invoked as\n" +
	"separate processes (the Go and GCC toolchains, `cosign`, `syft`, and `docker`)\n" +
	"are not linked and are not covered here.\n\n" +
	"The License column in the Summary is a best-effort SPDX guess from each\n" +
	"license's opening text; the reproduced license and NOTICE text below it is the\n" +
	"authoritative record. Only each module's top-level LICENSE/NOTICE is\n" +
	"reproduced — a module that vendors sub-tree licenses under a different license\n" +
	"(for example an Apache-2.0 subtree inside a BSD module) is not fully covered\n" +
	"by that reproduction.\n\n" +
	"Regenerate with `make notices`.\n\n"

func readText(ctx context.Context, path string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if !utf8.Valid(raw) {
		return "", fmt.Errorf("%s: invalid UTF-8", path)
	}
	// Python's text mode normalizes universal newlines before stripping.
	return strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\r", "\n")), nil
}
func generate(ctx context.Context, linked string) (string, error) {
	if !utf8.ValidString(linked) {
		return "", fmt.Errorf("linked module records: invalid UTF-8")
	}
	linked = strings.ReplaceAll(strings.ReplaceAll(linked, "\r\n", "\n"), "\r", "\n")
	type metadata struct{ version, directory string }
	mods := map[string]metadata{}
	for _, line := range strings.Split(strings.TrimSuffix(linked, "\n"), "\n") {
		if linked == "" {
			break
		}
		fields := strings.Split(line, "\t")
		if len(fields) != 3 || fields[0] == "" {
			return "", fmt.Errorf("malformed linked module record")
		}
		m := metadata{fields[1], fields[2]}
		if old, ok := mods[fields[0]]; ok && old != m {
			return "", fmt.Errorf("conflicting linked versions for %s", fields[0])
		}
		mods[fields[0]] = m
	}
	names := make([]string, 0, len(mods))
	for name := range mods {
		names = append(names, name)
	}
	slices.Sort(names)
	summary, bodies := []string{}, []string{}
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		m := mods[name]
		entries, err := os.ReadDir(m.directory)
		if err != nil || m.directory == "" {
			return "", fmt.Errorf("module %s: no local module directory (run `go mod download %s` and retry)", name, name)
		}
		license, notice := "", ""
		for _, entry := range entries {
			upper := strings.ToUpper(entry.Name())
			if license == "" && (strings.HasPrefix(upper, "LICENSE") || strings.HasPrefix(upper, "LICENCE") || strings.HasPrefix(upper, "COPYING")) { //nolint:misspell // Upstream license filenames use both spellings.
				license = entry.Name()
			}
			if notice == "" && strings.HasPrefix(upper, "NOTICE") {
				notice = entry.Name()
			}
		}
		if license == "" {
			return "", fmt.Errorf("module %s: no LICENSE/LICENCE/COPYING file in %s", name, m.directory) //nolint:misspell // Match the accepted upstream filenames.
		}
		text, err := readText(ctx, filepath.Join(m.directory, license))
		if err != nil {
			return "", err
		}
		runes := []rune(text)
		opening := strings.ToLower(string(runes[:min(400, len(runes))]))
		spdx := "see text"
		switch {
		case strings.Contains(opening, "apache license"):
			spdx = "Apache-2.0"
		case strings.Contains(opening, "mit license") || strings.Contains(opening, "permission is hereby granted, free of charge"):
			spdx = "MIT"
		case strings.Contains(opening, "redistribution and use in source and binary forms"):
			spdx = "BSD-3-Clause"
		}
		summary = append(summary, fmt.Sprintf("| `%s` | %s | %s |", name, m.version, spdx))
		body := fmt.Sprintf("\n---\n\n## %s\n\nVersion: %s\n\n```\n%s\n```\n", name, m.version, text)
		if notice != "" {
			text, err := readText(ctx, filepath.Join(m.directory, notice))
			if err != nil {
				return "", err
			}
			if text != "" {
				body += "\nNOTICE:\n\n```\n" + text + "\n```\n"
			}
		}
		bodies = append(bodies, body)
	}
	return header + "## Summary\n\n| Module | Version | License (heuristic) |\n|---|---|---|\n" + strings.Join(summary, "\n") + "\n" + strings.Join(bodies, "") + "\n", nil
}
func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, nerrors.New(nerrors.CodeUsage, "gen-notices requires a linked-module TSV"))
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	raw, err := os.ReadFile(os.Args[1])
	var output string
	if err == nil {
		output, err = generate(ctx, string(raw))
	}
	if err == nil {
		err = ctx.Err()
	}
	stop()
	if err != nil {
		fmt.Fprintln(os.Stderr, nerrors.Wrap(nerrors.CodeGateUnmet, err, "gen-notices"))
		os.Exit(1)
	}
	fmt.Print(output)
}
