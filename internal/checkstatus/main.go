// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Command checkstatus rejects added Markdown that restates ledger-owned status.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/dsx-ai-factory/nvkr/internal/ownedcmd"
	nerrors "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

const ledger = "docs/adr/reconciliation/"
const revisionHint = "fetch BASE and HEAD with full history, or set STATUS_BASE/STATUS_HEAD"

// Python's Unicode whitespace class, retained from the original checker.
const whitespace = `\s\v\x{1c}-\x{1f}\x{85}\p{Z}`
const un = `un(?:recorded|proven|exercised|validated|tested)`
const done = `(?:recorded|exercised|proven|validated|qualified|verified)`
const record = `(?:hosted |external |acceptance |development )*(?:run|execution|evidence|result|observation)s?`

type statusPhrase struct {
	pattern     *regexp.Regexp
	endBoundary bool
}

var phrases = func() []statusPhrase {
	patterns := []string{
		`\b(?:remains?|remained|is|are) (?:still )?` + un + `\b`,
		`\b(?:execution|acceptance|qualification|cycle|deployment|build|run|publication)s? (?:remains?|is|are) (?:still )?unverified\b`,
		`\brequires? (?:a |an )?recorded ` + record + `\b`,
		`\b(?:awaits?|needs?|pending|until) (?:a |an )?recorded (?:hosted |external )?(?:run|execution)`,
		`\bno (?:hosted |external )?(?:run|evidence|execution) (?:has been |is )?recorded\b`,
		`\b(?:has|have) not (?:yet )?been ` + done + `\b`,
		`\bnot yet ` + done + `\b`,
		`\b(?:hosted|external|production) (?:publication|signing|acceptance|execution|qualification|verification) (?:was|were|has been|have been|is|are) (?:not )?(?:yet )?` + done + `\b`,
		`\b(?:is|are|was|were|been|not) (?:yet )?(?:externally|operationally) (?:exercised|accepted|validated|proven)\b`,
	}
	result := make([]statusPhrase, 0, len(patterns))
	for _, p := range patterns {
		end := strings.HasSuffix(p, `\b`)
		p = strings.TrimSuffix(strings.TrimPrefix(p, `\b`), `\b`)
		result = append(result, statusPhrase{regexp.MustCompile("(?i)" + strings.ReplaceAll(p, " ", `[`+whitespace+`]+`)), end})
	}
	return result
}()
var escape = regexp.MustCompile(`<!--[` + whitespace + `]*status-ok((?:[^\pL\pN_].*?)?)-->`)
var reason = regexp.MustCompile(`^[` + whitespace + `]*:[` + whitespace + `]*[^` + whitespace + `]`)
var ownerFile = regexp.MustCompile(`^adr-(\d\d)(?:-(\d\d))?\.md$`)
var adrPath = regexp.MustCompile(`^docs/adr/(\d{4})-`)
var adrRef = regexp.MustCompile(`\bADR-(\d{1,2})\b`)
var listItem = regexp.MustCompile(`^[` + whitespace + `]*(?:[-*+]|\p{Nd}+[.)])[` + whitespace + `]`)
var hunk = regexp.MustCompile(`^@@ -\S+ \+(\d+)`)

func git(ctx context.Context, repo, hint string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", repo}, args...)...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	var stdout strings.Builder
	cmd.Stdout = &stdout
	err := ownedcmd.Run(ctx, cmd)
	out := stdout.String()
	if err != nil {
		if hint != "" {
			hint = " (" + hint + ")"
		}
		return "", fmt.Errorf("git %s: %s%s: %w", strings.Join(args, " "), strings.TrimSpace(stderr.String()), hint, err)
	}
	return strings.ToValidUTF8(out, "�"), nil
}
func headerPath(line string) (string, error) {
	name := strings.TrimSuffix(strings.TrimPrefix(line, "+++ "), "\t")
	if strings.HasPrefix(name, `"`) {
		decoded, err := strconv.Unquote(name)
		if err != nil {
			return "", err
		}
		name = decoded
	}
	if name == "/dev/null" {
		return "", nil
	}
	return strings.TrimPrefix(name, "b/"), nil
}
func addedLines(diff string) (map[string]map[int]bool, error) {
	added := map[string]map[int]bool{}
	path := ""
	next := 0
	inHeader := false
	for _, line := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			path = ""
			inHeader = true
		case inHeader && strings.HasPrefix(line, "+++ "):
			var err error
			path, err = headerPath(line)
			if err != nil {
				return nil, err
			}
		case strings.HasPrefix(line, "@@"):
			inHeader = false
			m := hunk.FindStringSubmatch(line)
			if m == nil {
				return nil, fmt.Errorf("malformed diff hunk")
			}
			var err error
			next, err = strconv.Atoi(m[1])
			if err != nil {
				return nil, err
			}
		case !inHeader && strings.HasPrefix(line, "+") && path != "":
			if added[path] == nil {
				added[path] = map[int]bool{}
			}
			added[path][next] = true
			next++
		}
	}
	return added, nil
}

type line struct {
	number int
	text   string
	added  bool
}

func paragraphs(text string, added map[int]bool) [][]line {
	var blocks [][]line
	var block []line
	flush := func() {
		for _, l := range block {
			if l.added {
				blocks = append(blocks, block)
				break
			}
		}
		block = nil
	}
	for i, s := range strings.Split(text, "\n") {
		s = strings.TrimSuffix(s, "\r")
		if len(block) > 0 && (strings.TrimSpace(s) == "" || listItem.MatchString(s)) {
			flush()
		}
		if strings.TrimSpace(s) != "" {
			block = append(block, line{i + 1, s, added == nil || added[i+1]})
		}
	}
	flush()
	return blocks
}
func hasReason(text string) bool {
	for _, m := range escape.FindAllStringSubmatch(text, -1) {
		if reason.MatchString(m[1]) {
			return true
		}
	}
	return false
}

type finding struct {
	number        int
	message, span string
}

func wordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsNumber(r) || r == '_' }

func phraseBoundary(text string, start, end int, endBoundary bool) bool {
	before, _ := utf8.DecodeLastRuneInString(text[:start])
	after, _ := utf8.DecodeRuneInString(text[end:])
	return !wordRune(before) && (!endBoundary || !wordRune(after))
}

func scan(block []line) []finding {
	var text strings.Builder
	starts := []int{}
	for _, l := range block {
		starts = append(starts, text.Len())
		text.WriteString(l.text + " ")
	}
	joined := text.String()
	lineOf := func(offset int) int {
		for i := len(starts) - 1; i >= 0; i-- {
			if starts[i] <= offset {
				return i
			}
		}
		return 0
	}
	findings := []finding{}
	seen := map[int]bool{}
	for _, l := range block {
		if l.added {
			for _, m := range escape.FindAllStringSubmatch(l.text, -1) {
				if !reason.MatchString(m[1]) {
					findings = append(findings, finding{l.number, "status-ok escape needs a reason: <!-- status-ok: why -->", ""})
					break
				}
			}
		}
	}
	for _, phrase := range phrases {
		for _, m := range phrase.pattern.FindAllStringIndex(joined, -1) {
			if !phraseBoundary(joined, m[0], m[1], phrase.endBoundary) {
				continue
			}
			first, last := lineOf(m[0]), lineOf(m[1]-1)
			number := 0
			escaped := false
			var span strings.Builder
			for _, l := range block[first : last+1] {
				if l.added && number == 0 {
					number = l.number
				}
				escaped = escaped || hasReason(l.text)
				span.WriteString(l.text + " ")
			}
			if number == 0 || seen[number] || escaped {
				continue
			}
			seen[number] = true
			findings = append(findings, finding{number, fmt.Sprintf("status restated (\"%s\")", strings.Join(strings.Fields(joined[m[0]:m[1]]), " ")), span.String() + joined})
		}
	}
	return findings
}
func ownerFiles(ctx context.Context, repo, head string) ([]string, error) {
	var names []string
	if head == "" {
		entries, err := os.ReadDir(filepath.Join(repo, ledger))
		if err != nil {
			return nil, fmt.Errorf("cannot list %s to name the owner file: %w (update the checkout so it contains the per-section ledger)", ledger, err)
		}
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
	} else {
		out, err := git(ctx, repo, head+" predates the per-section ledger; merge main to name the owner file", "ls-tree", "--name-only", "-z", head+":"+ledger)
		if err != nil {
			return nil, err
		}
		names = strings.Split(out, "\x00")
	}
	owners := []string{}
	for _, name := range names {
		if ownerFile.MatchString(name) {
			owners = append(owners, name)
		}
	}
	slices.Sort(owners)
	if len(owners) == 0 {
		return nil, fmt.Errorf("%s has no adr-NN.md owner files to name", ledger)
	}
	return owners, nil
}

type ledgerResolver struct {
	repo, head string
	owners     []string
}

func (r *ledgerResolver) row(ctx context.Context, path, text string) (string, error) {
	adr := adrPath.FindStringSubmatch(path)
	if adr == nil {
		adr = adrRef.FindStringSubmatch(text)
	}
	if adr == nil {
		return "its " + ledger + " row", nil
	}
	number, _ := strconv.Atoi(adr[1])
	if r.owners == nil {
		owners, err := ownerFiles(ctx, r.repo, r.head)
		if err != nil {
			return "", err
		}
		r.owners = owners
	}
	for _, name := range r.owners {
		m := ownerFile.FindStringSubmatch(name)
		first, _ := strconv.Atoi(m[1])
		last := first
		if m[2] != "" {
			last, _ = strconv.Atoi(m[2])
		}
		if first <= number && number <= last {
			return ledger + name, nil
		}
	}
	return "its " + ledger + " row", nil
}
func worktreeText(ctx context.Context, repo, path string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	file := filepath.Join(repo, path)
	info, err := os.Lstat(file)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return os.Readlink(file)
	}
	raw, err := os.ReadFile(file)
	return strings.ToValidUTF8(string(raw), "�"), err
}
func check(ctx context.Context, repo, base, head string) (string, []string, error) {
	revision := head
	if revision == "" {
		revision = "HEAD"
	}
	since, err := git(ctx, repo, revisionHint, "merge-base", base, revision)
	if err != nil {
		return "", nil, err
	}
	since = strings.TrimSpace(since)
	args := []string{"-c", "core.quotePath=false", "diff", "--no-color", "--no-ext-diff", "--no-textconv", "-U0", "-M", "--src-prefix=a/", "--dst-prefix=b/", "--diff-filter=AMRT", since}
	if head != "" {
		args = append(args, head)
	}
	args = append(args, "--", "*.md", "*.mdx")
	diff, err := git(ctx, repo, revisionHint, args...)
	if err != nil {
		return "", nil, err
	}
	changed, err := addedLines(diff)
	if err != nil {
		return "", nil, err
	}
	if head == "" {
		out, err := git(ctx, repo, "", "-c", "core.quotePath=false", "ls-files", "-z", "--others", "--exclude-standard", "--", "*.md", "*.mdx")
		if err != nil {
			return "", nil, err
		}
		for _, path := range strings.Split(out, "\x00") {
			if path != "" {
				changed[path] = nil
			}
		}
	}
	type located struct {
		path string
		finding
	}
	found := []located{}
	resolver := ledgerResolver{repo: repo, head: head}
	for path, added := range changed {
		if strings.HasPrefix(path, ledger) || strings.HasPrefix(path, "docs/evidence/") {
			continue
		}
		var text string
		if head != "" {
			text, err = git(ctx, repo, "", "cat-file", "blob", head+":"+path)
		} else {
			text, err = worktreeText(ctx, repo, path)
		}
		if err != nil {
			return "", nil, err
		}
		for _, block := range paragraphs(text, added) {
			if err := ctx.Err(); err != nil {
				return "", nil, err
			}
			for _, f := range scan(block) {
				if f.span != "" {
					row, err := resolver.row(ctx, path, f.span)
					if err != nil {
						return "", nil, err
					}
					f.message += "; link " + row + " instead"
				}
				found = append(found, located{path, f})
			}
		}
	}
	slices.SortFunc(found, func(a, b located) int {
		if n := strings.Compare(a.path, b.path); n != 0 {
			return n
		}
		if a.number != b.number {
			return a.number - b.number
		}
		return strings.Compare(a.message, b.message)
	})
	out := []string{}
	for _, f := range found {
		formatted := fmt.Sprintf("%s:%d: %s", f.path, f.number, f.message)
		if len(out) == 0 || out[len(out)-1] != formatted {
			out = append(out, formatted)
		}
	}
	return since, out, ctx.Err()
}

// flag stops at the first positional argument; the historical argparse CLI
// accepts --repo before, between, or after the revisions.
func interspersedArgs(args []string) ([]string, error) {
	var options, revisions []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			revisions = append(revisions, args[i+1:]...)
			break
		}
		if arg == "--repo" || arg == "-repo" {
			if i+1 == len(args) {
				return nil, fmt.Errorf("--repo requires a value")
			}
			options = append(options, arg, args[i+1])
			i++
		} else if strings.HasPrefix(arg, "-") {
			options = append(options, arg)
		} else {
			revisions = append(revisions, arg)
		}
	}
	return append(append(options, "--"), revisions...), nil
}
func run(ctx context.Context, args []string, stdout, stderr io.Writer) (int, error) {
	flags := flag.NewFlagSet("check-status-restated", flag.ContinueOnError)
	flags.SetOutput(stderr)
	repo := flags.String("repo", ".", "repository checkout")
	args, err := interspersedArgs(args)
	if err != nil {
		return 2, err
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0, nil
		}
		return 2, err
	}
	if flags.NArg() < 1 || flags.NArg() > 2 {
		return 2, fmt.Errorf("expected BASE [HEAD]")
	}
	base, head := flags.Arg(0), flags.Arg(1)
	since, findings, err := check(ctx, *repo, base, head)
	if err != nil {
		return 1, err
	}
	for _, f := range findings {
		if err := ctx.Err(); err != nil {
			return 1, err
		}
		if _, err := fmt.Fprintln(stdout, f); err != nil {
			return 1, err
		}
	}
	if err := ctx.Err(); err != nil {
		return 1, err
	}
	label := head
	if label == "" {
		label = "the working tree"
	}
	fmt.Fprintf(stderr, "check-status-restated: %d finding(s) in lines added since %.12s, the merge base of %s and %s\n", len(findings), since, base, label)
	if len(findings) > 0 {
		fmt.Fprintln(stderr, "Status lives in the reconciliation ledger: link its row, or add <!-- status-ok: reason --> to the line.")
		return 1, nil
	}
	return 0, nil
}
func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code, err := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	if err != nil {
		fmt.Fprintln(os.Stderr, nerrors.Wrap(nerrors.CodeGateUnmet, err, "check-status-restated"))
	}
	os.Exit(code)
}
