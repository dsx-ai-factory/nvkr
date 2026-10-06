//go:build linux

package pipeline

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/NVIDIA/nke-recipes/internal/securepath"
	"github.com/NVIDIA/nke-recipes/internal/sourcegit"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func TestSourceFetchHeldGitRejectsRepositoryRemoteCredentialCode(t *testing.T) {
	for _, mode := range []string{"private", "public"} {
		t.Run(mode, func(t *testing.T) {
			node, dir, _ := pristineClosureFixture(t)
			checkout, _, err := securepath.OpenDirectory(dir)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = checkout.Close() })
			metadata, _, err := securepath.OpenChild(checkout, ".git", true)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = metadata.Close() })
			marker := filepath.Join(t.TempDir(), "remote-helper-observed-synthetic-token")
			helper := filepath.Join(t.TempDir(), "remote-helper")
			if err := os.WriteFile(helper, []byte("#!/bin/sh\nprintf executed > '"+marker+"'\nexit 1\n"), 0o700); err != nil {
				t.Fatal(err)
			}
			for _, args := range [][]string{{"config", "protocol.ext.allow", "always"}, {"remote", "add", "origin", "ext::" + helper}} {
				command := exec.CommandContext(t.Context(), "git", append([]string{"-C", dir}, args...)...)
				if raw, err := command.CombinedOutput(); err != nil {
					t.Fatalf("configuring controlled remote: %v %s", err, raw)
				}
			}
			options := RunOptions{Ctx: t.Context(), fetchDirectory: checkout, fetchGitDirectory: metadata,
				fetchRepository: "https://example.test/source", SourceGitEnvironment: sourceGitPrivateTestEnvironment(t, "https://example.test/source", os.Getenv("PATH"))}
			if mode == "public" {
				options.SourceGitEnvironment, err = sourcegit.Baseline(os.Getenv("PATH"))
				if err != nil {
					t.Fatal(err)
				}
			}
			if _, err := sourceFetchGitOut(options, dir, "fetch", "-q", "--depth", "1", "origin", node.Commit); err == nil {
				t.Fatal("controlled failing remote unexpectedly succeeded")
			}
			if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("repository remote executed external host code: %v", err)
			}
		})
	}
}

func TestSourceFetchHeldGitConfigurationQueryCancellation(t *testing.T) {
	_, dir, _ := pristineClosureFixture(t)
	checkout, _, err := securepath.OpenDirectory(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = checkout.Close() })
	metadata, _, err := securepath.OpenChild(checkout, ".git", true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = metadata.Close() })
	commands := t.TempDir()
	marker := filepath.Join(commands, "query-started")
	script := fmt.Sprintf("#!/bin/sh\nprintf started > %q\nwhile :; do /bin/sleep 1; done\n", marker)
	if err := os.WriteFile(filepath.Join(commands, "git"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	environment, err := sourcegit.Baseline(commands)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	t.Cleanup(cancel)
	go func() {
		_, err := sourceFetchGitOut(RunOptions{Ctx: ctx, fetchDirectory: checkout, fetchGitDirectory: metadata, SourceGitEnvironment: environment}, dir, "status", "--porcelain")
		done <- err
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("query did not join")
		}
	})
	deadline := time.After(5 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		select {
		case <-deadline:
			t.Fatal("configuration query did not start")
		case <-time.After(time.Millisecond):
		}
	}
	cancel()
	select {
	case err := <-done:
		if code(err) != e.CodeTimeout {
			t.Fatalf("query cancellation code = %s: %v", code(err), err)
		}
		// Cleanup can observe completion without waiting on a drained channel.
		done <- err
	case <-time.After(5 * time.Second):
		t.Fatal("configuration query cancellation did not join")
	}
}

func TestSourceFetchHeldGitRejectsRepositoryFilterHostCode(t *testing.T) {
	_, dir, _ := pristineClosureFixture(t)
	checkout, _, err := securepath.OpenDirectory(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = checkout.Close() })
	metadata, _, err := securepath.OpenChild(checkout, ".git", true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = metadata.Close() })
	marker := filepath.Join(t.TempDir(), "filter-host-code-executed")
	helper := filepath.Join(t.TempDir(), "filter-helper")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\nprintf executed > '"+marker+"'\ncat\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(t.Context(), "git", "-C", dir, "config", "filter.controlled.clean", helper)
	if raw, err := command.CombinedOutput(); err != nil {
		t.Fatalf("configuring controlled filter: %v %s", err, raw)
	}
	if err := os.WriteFile(filepath.Join(dir, ".git", "info", "attributes"), []byte("input filter=controlled\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Keep the indexed size so Git must hash through the clean filter rather
	// than immediately reporting a size mismatch without reading the content.
	if err := os.WriteFile(filepath.Join(dir, "input"), []byte("mutate bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = sourceFetchGitOut(RunOptions{Ctx: t.Context(), fetchDirectory: checkout, fetchGitDirectory: metadata}, dir, "status", "--porcelain")
	if code(err) != e.CodeEngineCapability {
		t.Fatalf("filter admission code = %s, want %s: %v", code(err), e.CodeEngineCapability, err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("repository filter executed host code during held status: %v", err)
	}
}

func TestSourceFetchHeldGitRejectsConfigurationDelegation(t *testing.T) {
	for _, key := range []string{"include.path", "includeIf.gitdir:/.path", "extensions.worktreeConfig", "extensions.partialClone", "remote.origin.promisor", "url.ext::controlled.insteadOf", "core.alternateRefsCommand"} {
		t.Run(key, func(t *testing.T) {
			_, dir, _ := pristineClosureFixture(t)
			command := exec.CommandContext(t.Context(), "git", "-C", dir, "config", key, "controlled")
			if raw, err := command.CombinedOutput(); err != nil {
				t.Fatalf("configuring delegation: %v %s", err, raw)
			}
			checkout, _, err := securepath.OpenDirectory(dir)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = checkout.Close() })
			metadata, _, err := securepath.OpenChild(checkout, ".git", true)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = metadata.Close() })
			_, err = sourceFetchGitOut(RunOptions{Ctx: t.Context(), fetchDirectory: checkout, fetchGitDirectory: metadata}, dir, "status", "--porcelain")
			if code(err) != e.CodeEngineCapability {
				t.Fatalf("delegation code = %s: %v", code(err), err)
			}
		})
	}
}
