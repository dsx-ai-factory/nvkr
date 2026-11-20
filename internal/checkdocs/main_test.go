// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestScan(t *testing.T) {
	for _, tc := range []struct {
		name, text string
		kinds      []string
	}{
		{"void", "<br> <img src=\"x\"> <hr /> <br/> <bright> <BR>", []string{"void-element", "void-element", "placeholder"}},
		{"autolink", "<https://example.test/x> <http://example.test> <mailto:a@b>", []string{"autolink", "autolink"}},
		{"digit", "<100ms <٣ms", []string{"lt-digit", "lt-digit"}},
		{"placeholders", "<GPU_UUID> <A> <lower_case>", []string{"placeholder"}},
		{"braces", "{x} \\{safe} \\\\{also-skipped}", []string{"brace"}},
		{"comment", "<!-- x -->", []string{"html-comment"}},
		{"inline", "`<br> {x} <100 <GPU_UUID> <!--` and ``<img> ` a``", nil},
		{"unclosed inline", "` <br>", []string{"void-element"}},
		{"different ticks", "``<br>` remains code`` <br>", []string{"void-element"}},
		{"empty ticks", "`` <br>", []string{"void-element"}},
		{"fenced", "```md\n<br>\n~~~\n{a}\n```\n<br>", []string{"void-element"}},
		{"longer fence", "~~~~\n<br>\n~~~\n{a}\n~~~~\n", nil},
		{"closing info", "```\n<br>\n```still code\n{a}\n```", nil},
		{"invalid opening", "```has`tick <br>", []string{"void-element"}},
		{"frontmatter", "---\nkey: <br> {x}\n...\n<br>", []string{"void-element"}},
		{"unclosed frontmatter", "---\n<br>", []string{"void-element"}},
		{"JSX comment", "before {/* <br>\n{literal}\n*/} <br>", []string{"void-element"}},
		{"multiple JSX", "{/* <br> */} {/* {x} */}", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, got := scan(tc.text, false)
			var kinds []string
			for _, f := range got {
				kinds = append(kinds, f.kind)
			}
			if !slices.Equal(kinds, tc.kinds) {
				t.Fatalf("got %v want %v", got, tc.kinds)
			}
		})
	}
}
func TestFix(t *testing.T) {
	for _, tc := range []struct{ name, text, want string }{
		{"void", "<br> <img src=\"x\"  > <hr />", "<br /> <img src=\"x\" /> <hr />"},
		{"links", "<https://example.test/x?q=1> <http://example.test>", "[https://example.test/x?q=1](https://example.test/x?q=1) [http://example.test](http://example.test)"},
		{"code", "`<br>` and ``<https://x.test>`` <br>", "`<br>` and ``<https://x.test>`` <br />"},
		{"fence", "~~~\n<br>\n~~~\n<br>", "~~~\n<br>\n~~~\n<br />"},
		{"comment", "<br> {/* leave <br> */}", "<br> {/* leave <br> */}"},
		{"multiline comment", "{/* <br>\n<br>\n*/}\n<br>", "{/* <br>\n<br>\n*/}\n<br />"},
		{"manual only", "<100 <GPU_UUID> {x} <!-- x -->", "<100 <GPU_UUID> {x} <!-- x -->"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, _ := scan(tc.text, true)
			if got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
			again, _ := scan(got, true)
			if again != got {
				t.Fatal("fix is not idempotent")
			}
		})
	}
}
func writeDoc(t *testing.T, root, path, text string) string {
	t.Helper()
	file := filepath.Join(root, path)
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(text), 0o640); err != nil {
		t.Fatal(err)
	}
	return file
}
func TestCollectionAndCLI(t *testing.T) {
	root := t.TempDir()
	good := writeDoc(t, root, "docs/good.md", "`<br>`\n")
	bad := writeDoc(t, root, "docs/nested/bad.mdx", "<br>\n")
	writeDoc(t, root, "docs/skip.txt", "<br>")
	writeDoc(t, root, "docs/node_modules/bad.md", "<br>")
	writeDoc(t, root, ".git/bad.md", "<br>")
	files, err := collect(t.Context(), []string{root}, nil)
	if err != nil || !slices.Equal(files, []string{good, bad}) {
		t.Fatalf("%v %v", files, err)
	}
	files, err = collect(t.Context(), []string{root}, []string{"*/nested/*"})
	if err != nil || !slices.Equal(files, []string{good}) {
		t.Fatalf("%v %v", files, err)
	}
	for _, tc := range []struct {
		name    string
		args    []string
		code    int
		message string
	}{
		{"findings", []string{root}, 1, ":1: [void-element]"},
		{"exclude across directories", []string{root, "--exclude", "*/bad.mdx"}, 0, "MDX-safe"},
		{"missing", []string{filepath.Join(root, "missing")}, 2, ""},
		{"unknown option", []string{"--unknown"}, 2, ""},
		{"missing glob", []string{"--exclude"}, 2, ""},
		{"help", []string{"--help"}, 0, "Usage:"},
		{"fix", []string{bad, "--fix"}, 0, "MDX-safe"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out, stderr bytes.Buffer
			code, err := run(t.Context(), tc.args, &out, &stderr)
			if code != tc.code || (tc.code == 2 && err == nil) || !strings.Contains(out.String(), tc.message) {
				t.Fatalf("code %d error %v out %s stderr %s", code, err, out.String(), stderr.String())
			}
		})
	}
	raw, err := os.ReadFile(bad)
	if err != nil || string(raw) != "<br />\n" {
		t.Fatalf("fix %q %v", raw, err)
	}
	info, err := os.Stat(bad)
	if err != nil || info.Mode().Perm() != 0o640 {
		t.Fatalf("mode changed: %v %v", info, err)
	}
	invalid := writeDoc(t, root, "invalid.md", string([]byte{0xff}))
	if code, err := run(t.Context(), []string{invalid}, &bytes.Buffer{}, &bytes.Buffer{}); code != 1 || err == nil {
		t.Fatalf("invalid UTF8: %d %v", code, err)
	}
}
func TestExclusionGlobs(t *testing.T) {
	for _, tc := range []struct {
		file, glob string
		want       bool
	}{{"docs/design/deep/a.md", "docs/design/*", true}, {"docs/AGENTS.md", "*/AGENTS.md", true}, {"docs/a.md", "docs/?.md", true}, {"docs/a.md", "docs/[!b].md", true}, {"docs/b.md", "docs/[!b].md", false}, {"docs/a.md", "docs/*.mdx", false}} {
		got, err := excluded(tc.file, tc.glob)
		if err != nil || got != tc.want {
			t.Fatalf("%+v: %t %v", tc, got, err)
		}
	}
}

type cancelingWriter struct {
	bytes.Buffer
	cancel context.CancelFunc
}

func (w *cancelingWriter) Write(p []byte) (int, error) {
	w.cancel()
	return w.Buffer.Write(p)
}

func TestRunObservesCancellationDuringReporting(t *testing.T) {
	file := writeDoc(t, t.TempDir(), "last.md", "<br>\n")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	out := &cancelingWriter{cancel: cancel}
	var stderr bytes.Buffer
	code, err := run(ctx, []string{file}, out, &stderr)
	var coded *e.Error
	if code != 1 || !errors.Is(err, context.Canceled) || !errors.As(err, &coded) || coded.Code != e.CodeTimeout {
		t.Fatalf("canceled reporting returned code=%d error=%v", code, err)
	}
	if strings.Contains(out.String(), "OK:") || stderr.Len() != 0 {
		t.Fatalf("reported completion after cancellation: %q %q", out.String(), stderr.String())
	}
}
