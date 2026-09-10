package catalog

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	e "github.com/nhensley/nkdctl/pkg/nkd/errors"
)

// Exercise the real Git transport locally, including annotated-tag peeling. The
// repository and all Git configuration are test-owned; no upstream is contacted.
func TestResolvePinFromLocalGit(t *testing.T) {
	for _, withGo := range []bool{false, true} {
		name := "without go version"
		if withGo {
			name = "with go version"
		}
		t.Run(name, func(t *testing.T) {
			repo := t.TempDir()
			t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
			t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
			t.Setenv("GIT_AUTHOR_DATE", "1733939955 +0000")
			t.Setenv("GIT_COMMITTER_DATE", "1733939955 +0000")
			run := func(args ...string) string {
				t.Helper()
				cmd := exec.CommandContext(t.Context(), "git", append([]string{"-C", repo}, args...)...)
				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("git %v: %v: %s", args, err, out)
				}
				return strings.TrimSpace(string(out))
			}
			run("init", "-q")
			run("config", "user.name", "Pin Test")
			run("config", "user.email", "pin@example.invalid")
			file := "README"
			contents := "source fixture\n"
			if withGo {
				file, contents = ".go-version", "1.27.0\n"
			}
			if err := os.WriteFile(filepath.Join(repo, file), []byte(contents), 0o644); err != nil {
				t.Fatal(err)
			}
			run("add", file)
			run("-c", "commit.gpgsign=false", "commit", "-q", "-m", "fixture")
			commit, tree := run("rev-parse", "HEAD"), run("rev-parse", "HEAD^{tree}")
			run("tag", "cli/v1.0.0")
			run("-c", "tag.gpgsign=false", "tag", "-a", "cli/v1.1.0", "-m", "annotated fixture")
			ctx := context.Background()
			tags, err := ListTags(ctx, repo)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(tags, []string{"cli/v1.0.0", "cli/v1.1.0"}) {
				t.Fatalf("tags = %v", tags)
			}
			for _, tag := range []string{"v1.0.0", "cli/v1.1.0"} {
				pin, err := ResolvePin(ctx, Source{Repo: repo, TagPrefix: "cli/"}, tag)
				if err != nil {
					t.Fatal(err)
				}
				wantGo := ""
				if withGo {
					wantGo = "1.27.0"
				}
				if pin.Commit != commit || pin.TreeHash != tree || pin.SourceDateEpoch != 1733939955 || pin.GoVersion != wantGo || !strings.HasPrefix(pin.Tag, "cli/") || pin.ToolchainImage != "" {
					t.Fatalf("pin = %+v", pin)
				}
			}
			_, err = ResolvePin(ctx, Source{Repo: repo}, "missing")
			if pinErrorCode(err) != e.CodeFetchFailed {
				t.Fatalf("missing tag: %v", err)
			}
			if !withGo {
				_, err = ResolvePin(ctx, Source{Repo: repo, ToolchainBaseImage: "unused.invalid/go"}, "cli/v1.0.0")
				if pinErrorCode(err) != e.CodeUnpinnedSource {
					t.Fatalf("missing go version: %v", err)
				}
			}
			_, err = ListTags(ctx, filepath.Join(repo, "missing"))
			if pinErrorCode(err) != e.CodeFetchFailed {
				t.Fatalf("missing repository: %v", err)
			}
		})
	}
}

// A scripted Docker boundary checks argument handling and fail-closed parsing.
// It does not claim an external registry or a real Docker pull was exercised.
func TestResolveImageDigestSubprocessContract(t *testing.T) {
	digest := "example.invalid/go@sha256:" + strings.Repeat("a", 64)
	for _, tc := range []struct {
		name, script, want string
		code               e.Code
	}{
		{"pinned", "printf '%s\\n' '" + digest + "'", digest, ""},
		{"mutable", "printf '%s\\n' 'example.invalid/go:latest'", "", e.CodeFetchFailed},
		{"empty", "exit 0", "", e.CodeFetchFailed},
		{"inspect failure", "exit 7", "", e.CodeFetchFailed},
		{"pull failure", "exit 8", "", e.CodeFetchFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			pull := "exit 0"
			if tc.name == "pull failure" {
				pull = "exit 8"
			}
			script := "#!/bin/sh\ncase \"$1\" in\npull) [ \"$2\" = -q ] && [ \"$3\" = example.invalid/go:1.27.0 ] || exit 91; " + pull + ";;\ninspect) [ \"$2\" = example.invalid/go:1.27.0 ] && [ \"$3\" = --format ] && [ \"$4\" = '{{index .RepoDigests 0}}' ] || exit 92; " + tc.script + ";;\n*) exit 93;;\nesac\n"
			if err := os.WriteFile(filepath.Join(root, "docker"), []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", root)
			got, err := resolveImageDigest(context.Background(), "example.invalid/go:1.27.0")
			if tc.code != "" {
				if pinErrorCode(err) != tc.code {
					t.Fatalf("error = %v", err)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("digest = %q, err = %v", got, err)
			}
		})
	}
}
