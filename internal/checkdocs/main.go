// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Command checkdocs finds Markdown constructs rejected by Fern's MDX parser.
package main

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"unicode/utf8"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

var voidRE = regexp.MustCompile(`<(?:img|br|hr|input|source|meta|link|area|base|col|embed|param|track|wbr)(?:[\s/][^<>]*)?>`)
var autolinkRE = regexp.MustCompile(`<(https?://[^>\s]+)>`)
var digitRE = regexp.MustCompile(`<\p{Nd}`)
var placeholderRE = regexp.MustCompile(`<[A-Z][A-Z0-9_]+>`)
var fenceRE = regexp.MustCompile("^\\s*(`{3,}|~{3,})(.*)$")

type finding struct {
	line          int
	kind, message string
}
type span struct{ start, end int }

// Inline spans require equal-length backtick runs and at least one content byte.
// Runs with different lengths may appear inside the span.
func inlineSpans(line string) []span {
	var spans []span
	for i := 0; i < len(line); {
		if line[i] != '`' {
			i++
			continue
		}
		start := i
		for i < len(line) && line[i] == '`' {
			i++
		}
		width := i - start
		for j := i; j < len(line); {
			if line[j] != '`' {
				j++
				continue
			}
			end := j
			for j < len(line) && line[j] == '`' {
				j++
			}
			if j-end == width && end > i {
				spans = append(spans, span{start, j})
				i = j
				break
			}
		}
	}
	return spans
}
func blankCode(line string) string {
	raw := []byte(line)
	for _, s := range inlineSpans(line) {
		for i := s.start; i < s.end; i++ {
			raw[i] = ' '
		}
	}
	return string(raw)
}
func stripComments(line string, inComment bool) (string, bool) {
	var out strings.Builder
	for line != "" {
		if inComment {
			end := strings.Index(line, "*/}")
			if end < 0 {
				return out.String(), true
			}
			line = line[end+3:]
			inComment = false
			continue
		}
		start := strings.Index(line, "{/*")
		if start < 0 {
			out.WriteString(line)
			break
		}
		out.WriteString(line[:start])
		line = line[start+3:]
		inComment = true
	}
	return out.String(), inComment
}
func voidMatches(line string) [][]int {
	var matches [][]int
	for _, m := range voidRE.FindAllStringIndex(line, -1) {
		if line[m[1]-2] != '/' {
			matches = append(matches, m)
		}
	}
	return matches
}
func fixSegment(line string) string {
	matches := voidMatches(line)
	for i := len(matches) - 1; i >= 0; i-- {
		m := matches[i]
		line = line[:m[0]] + strings.TrimRight(line[m[0]:m[1]-1], " \t\r\n\f") + " />" + line[m[1]:]
	}
	return autolinkRE.ReplaceAllString(line, "[$1]($1)")
}
func fixOutsideCode(line string) string {
	var out strings.Builder
	pos := 0
	for _, s := range inlineSpans(line) {
		out.WriteString(fixSegment(line[pos:s.start]))
		out.WriteString(line[s.start:s.end])
		pos = s.end
	}
	out.WriteString(fixSegment(line[pos:]))
	return out.String()
}
func scan(text string, fix bool) (string, []finding) {
	lines := strings.Split(text, "\n")
	start := 0
	fence := byte(0)
	width := 0
	inComment := false
	var findings []finding
	if len(lines) > 0 && strings.TrimSpace(lines[0]) == "---" {
		for i := 1; i < len(lines); i++ {
			s := strings.TrimSpace(lines[i])
			if s == "---" || s == "..." {
				start = i + 1
				break
			}
		}
	}
	for idx := start; idx < len(lines); idx++ {
		raw := lines[idx]
		fm := fenceRE.FindStringSubmatch(raw)
		if fence == 0 {
			if fm != nil && !(fm[1][0] == '`' && strings.Contains(fm[2], "`")) {
				fence = fm[1][0]
				width = len(fm[1])
				continue
			}
		} else {
			if fm != nil && fm[1][0] == fence && len(fm[1]) >= width && strings.TrimSpace(fm[2]) == "" {
				fence = 0
			}
			continue
		}
		noCode := blankCode(raw)
		var line string
		line, inComment = stripComments(noCode, inComment)
		add := func(kind, msg string) { findings = append(findings, finding{idx + 1, kind, msg}) }
		for _, m := range voidMatches(line) {
			add("void-element", fmt.Sprintf("non-self-closing void element %q (use <tag ... />)", line[m[0]:m[1]]))
		}
		for _, m := range autolinkRE.FindAllString(line, -1) {
			add("autolink", fmt.Sprintf("autolink %q parses as JSX (use [url](url))", m))
		}
		for range digitRE.FindAllString(line, -1) {
			add("lt-digit", "bare '<' before a digit parses as a tag (use &lt;)")
		}
		for _, m := range placeholderRE.FindAllString(line, -1) {
			add("placeholder", fmt.Sprintf("bare placeholder %q parses as a tag (wrap in backticks)", m))
		}
		for i, c := range line {
			if c == '{' && (i == 0 || line[i-1] != '\\') {
				add("brace", "unescaped '{' parses as a JSX expression (use \\{ or backticks)")
			}
		}
		for range strings.Count(line, "<!--") {
			add("html-comment", "HTML comment not valid in MDX (remove or use {/* ... */})")
		}
		if fix && !inComment && !strings.Contains(noCode, "{/*") {
			lines[idx] = fixOutsideCode(raw)
		}
	}
	return strings.Join(lines, "\n"), findings
}

// Python fnmatch allows '*' to cross directory boundaries. Replace separators
// before path.Match and treat backslashes literally, preserving that behavior.
func excluded(file, glob string) (bool, error) {
	glob = strings.ReplaceAll(glob, `\`, `\\`)
	glob = strings.ReplaceAll(glob, "[!", "[^")
	return path.Match(strings.ReplaceAll(glob, "/", "\x00"), strings.ReplaceAll(file, "/", "\x00"))
}
func skipped(file string) bool {
	for _, part := range strings.Split(filepath.ToSlash(file), "/") {
		if part == ".git" || part == "node_modules" {
			return true
		}
	}
	return false
}
func collect(ctx context.Context, paths, excludes []string) ([]string, error) {
	var files []string
	for _, root := range paths {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		info, err := os.Stat(root)
		if err != nil {
			return nil, err
		}
		candidates := []string{}
		if !info.IsDir() {
			candidates = append(candidates, root)
		} else {
			err = filepath.WalkDir(root, func(file string, d fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if err := ctx.Err(); err != nil {
					return err
				}
				if d.IsDir() {
					if skipped(file) {
						return filepath.SkipDir
					}
					return nil
				}
				candidates = append(candidates, file)
				return nil
			})
			if err != nil {
				return nil, err
			}
			slices.Sort(candidates)
		}
		for _, file := range candidates {
			ext := filepath.Ext(file)
			if (ext != ".md" && ext != ".mdx") || skipped(file) {
				continue
			}
			info, err := os.Stat(file)
			if err != nil {
				return nil, err
			}
			if !info.Mode().IsRegular() {
				continue
			}
			omit := false
			for _, glob := range excludes {
				match, err := excluded(filepath.ToSlash(file), glob)
				if err != nil {
					return nil, fmt.Errorf("invalid exclusion %q: %w", glob, err)
				}
				omit = omit || match
			}
			if !omit {
				files = append(files, file)
			}
		}
	}
	return files, nil
}
func run(ctx context.Context, args []string, stdout, stderr io.Writer) (int, error) {
	fix := false
	var paths, excludes []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--help" || arg == "-h":
			fmt.Fprintln(stdout, "Usage: check-docs-mdx [--fix] [--exclude GLOB]... [PATH ...]")
			return 0, nil
		case arg == "--fix":
			fix = true
		case arg == "--exclude":
			i++
			if i == len(args) {
				return 2, e.New(e.CodeUsage, "--exclude requires a glob")
			}
			excludes = append(excludes, args[i])
		case strings.HasPrefix(arg, "--exclude="):
			excludes = append(excludes, strings.TrimPrefix(arg, "--exclude="))
		case arg == "--":
			paths = append(paths, args[i+1:]...)
			i = len(args)
		case strings.HasPrefix(arg, "-"):
			return 2, e.New(e.CodeUsage, "unknown option %q", arg)
		default:
			paths = append(paths, arg)
		}
	}
	if len(paths) == 0 {
		for _, dir := range []string{"docs", "fern"} {
			if info, err := os.Stat(dir); err == nil && info.IsDir() {
				paths = append(paths, dir)
			}
		}
	}
	if len(paths) == 0 {
		return 2, e.New(e.CodeUsage, "no PATH given and neither docs/ nor fern/ exists")
	}
	files, err := collect(ctx, paths, excludes)
	if err != nil {
		return 2, e.Wrap(e.CodeUsage, err, "collect MDX inputs")
	}
	total := 0
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return 1, e.Wrap(e.CodeTimeout, err, "scan MDX inputs")
		}
		raw, err := os.ReadFile(file)
		if err != nil {
			return 1, e.Wrap(e.CodeGateUnmet, err, "read MDX input")
		}
		if !utf8.Valid(raw) {
			return 1, e.New(e.CodeGateUnmet, "%s is not UTF-8", file)
		}
		text := strings.ReplaceAll(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\r", "\n")
		updated, findings := scan(text, fix)
		if fix {
			if updated != text {
				if err := os.WriteFile(file, []byte(updated), 0o600); err != nil {
					return 1, e.Wrap(e.CodeGateUnmet, err, "write MDX fixes")
				}
			}
			_, findings = scan(updated, false)
		}
		for _, f := range findings {
			fmt.Fprintf(stdout, "%s:%d: [%s] %s\n", file, f.line, f.kind, f.message)
			total++
		}
	}
	if total > 0 {
		fmt.Fprintf(stderr, "\nMDX safety: %d issue(s) found\n", total)
		return 1, nil
	}
	fmt.Fprintln(stdout, "OK: all doc files are MDX-safe")
	return 0, nil
}
func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code, err := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
	os.Exit(code)
}
