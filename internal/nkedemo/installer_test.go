// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nkedemo

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	nkderr "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"golang.org/x/sys/unix"
)

func TestPrepareInstallerPinnedAttempt(t *testing.T) {
	for _, name := range []string{"remote", "local", "sibling", "override", "dirty", "wrong-pin", "preflight", "sudo", "fetch", "retained", "symlink", "canceled", "cancel-fetch", "output-inside-installer", "output-installers", "output-runs", "output-laptop-bootstrap", "output-bootstrap-attempt"} {
		t.Run(name, func(t *testing.T) {
			root := privateTestDir(t)
			repo, tools, calls := filepath.Join(root, "repo"), filepath.Join(root, "tools"), filepath.Join(root, "calls")
			for _, dir := range []string{filepath.Join(repo, "scripts"), tools, filepath.Join(root, "override")} {
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
			}
			write := func(path, body string) {
				t.Helper()
				if err := os.WriteFile(path, []byte(body), 0700); err != nil {
					t.Fatal(err)
				}
			}
			write(filepath.Join(repo, "scripts/nke-full-arm64-local.sh"), "#!/bin/sh\n[ \"$1:$2\" = --check:all ] && [ \"${FAIL:-}\" != preflight ]\n")
			write(filepath.Join(tools, "sudo"), "#!/bin/sh\n[ \"$1:$2\" = -n:true ] && [ \"${FAIL:-}\" != sudo ]\n")
			// This command fixture tests ownership and exact invocation, not Git
			// authorization or an externally fetched commit.
			write(filepath.Join(tools, "git"), `#!/bin/sh
set -eu
printf '%s\n' "$*" >>"$CALLS"
if [ "$1" = init ]; then mkdir "$3"; exit 0; fi
case "$3" in
fetch) if [ "${FAIL:-}" = cancel-fetch ]; then touch "$FETCH_STARTED"; exec sleep 60; fi; [ "${FAIL:-}" != fetch ] ;;
checkout) : ;;
rev-parse) if [ "${FAIL:-}" = wrong-pin ]; then printf foreign; else printf '%s' "$PIN"; fi ;;
status) if [ "${FAIL:-}" = dirty ]; then printf '?? unexpected'; fi ;;
*) exit 9 ;;
esac
`)
			// Only presence is checked; the downstream NKE installer, not this
			// helper, owns its use of Python.
			write(filepath.Join(tools, "python3"), "#!/bin/sh\nexit 9\n")
			started := filepath.Join(root, "fetch-started")
			runner := Runner{Env: []string{"PATH=" + tools + ":/usr/bin:/bin", "CALLS=" + calls, "PIN=" + InstallerCommit, "FAIL=" + name, "FETCH_STARTED=" + started}}
			build := BuildConfig{Repository: repo, AttemptPrefix: "fresh", Jobs: 2}
			run := Config{Out: filepath.Join(root, "out"), Timeout: time.Hour}
			if name == "local" {
				build.NKEClone = filepath.Join(root, "override")
			}
			if name == "sibling" {
				if err := os.Mkdir(filepath.Join(repo, ".nkd"), 0700); err != nil {
					t.Fatal(err)
				}
				run.Out = filepath.Join(repo, ".nkd/demo-fresh")
			}
			if name == "override" || name == "dirty" || name == "wrong-pin" || name == "output-inside-installer" {
				run.NKECheckout = filepath.Join(root, "override")
			}
			if name == "output-inside-installer" {
				run.Out = filepath.Join(run.NKECheckout, "out")
			}
			if name == "output-installers" || name == "output-runs" || name == "output-laptop-bootstrap" || name == "output-bootstrap-attempt" {
				if err := os.Mkdir(filepath.Join(repo, ".nkd"), 0700); err != nil {
					t.Fatal(err)
				}
				if name == "output-bootstrap-attempt" {
					if err := os.Mkdir(filepath.Join(repo, ".nkd/laptop-bootstrap"), 0700); err != nil {
						t.Fatal(err)
					}
					run.Out = filepath.Join(repo, ".nkd/laptop-bootstrap/fresh")
				} else {
					run.Out = filepath.Join(repo, ".nkd", strings.TrimPrefix(name, "output-"))
				}
			}
			work := filepath.Join(repo, ".nkd/installers/fresh")
			if name == "retained" {
				if err := os.MkdirAll(work, 0700); err != nil {
					t.Fatal(err)
				}
				write(filepath.Join(work, "keep"), "original")
			}
			if name == "symlink" {
				if err := os.MkdirAll(filepath.Join(repo, ".nkd"), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(root, "override"), filepath.Join(repo, ".nkd/installers")); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if name == "canceled" {
				cancel()
			}
			if name == "cancel-fetch" {
				// Cancel only once the owned command has actually started. The
				// deadline bounds the fixture even if the command never starts.
				var deadlineCancel context.CancelFunc
				ctx, deadlineCancel = context.WithTimeout(ctx, 5*time.Second)
				defer deadlineCancel()
				done := make(chan struct{})
				defer func() { cancel(); <-done }()
				go func() {
					defer close(done)
					ticker := time.NewTicker(10 * time.Millisecond)
					defer ticker.Stop()
					for {
						select {
						case <-ctx.Done():
							return
						case <-ticker.C:
							if _, e := os.Stat(started); e == nil {
								cancel()
								return
							}
						}
					}
				}()
			}
			actual, err := prepareInstaller(ctx, build, run, runner)
			success := name == "remote" || name == "local" || name == "sibling" || name == "override"
			if (err == nil) != success {
				t.Fatalf("result=%+v error=%v", actual, err)
			}
			if !success {
				if name == "preflight" || name == "sudo" || strings.HasPrefix(name, "output-") {
					if _, e := os.Lstat(calls); !errors.Is(e, os.ErrNotExist) {
						t.Fatal("failed preflight reached Git acquisition")
					}
				}
				if name == "sudo" {
					for _, hint := range []string{"sudo -n true", "host administrator", "sudo -v", "docs/local-nke-demo.md"} {
						if !strings.Contains(err.Error(), hint) {
							t.Fatalf("missing actionable sudo guidance %q: %v", hint, err)
						}
					}
				}
				if actual != (Config{}) {
					t.Fatal("failure returned prepared inputs")
				}
				if name == "retained" {
					if raw, e := os.ReadFile(filepath.Join(work, "keep")); e != nil || string(raw) != "original" {
						t.Fatal("retained attempt modified")
					}
				} else if _, e := os.Lstat(work); !errors.Is(e, os.ErrNotExist) {
					t.Fatalf("failed acquisition left workspace: %v", e)
				}
				if (name == "canceled" || name == "cancel-fetch") && !errors.Is(err, context.Canceled) {
					t.Fatalf("lost cancellation: %v", err)
				}
				return
			}
			if name == "override" {
				if actual.NKECheckout != run.NKECheckout {
					t.Fatal("explicit checkout replaced")
				}
				if _, e := os.Lstat(filepath.Join(repo, ".nkd")); !errors.Is(e, os.ErrNotExist) {
					t.Fatal("explicit override created an attempt")
				}
			} else {
				if actual.NKECheckout != filepath.Join(work, "source") {
					t.Fatal("checkout is not attempt-local")
				}
				info, e := os.Stat(work)
				if e != nil || info.Mode().Perm() != 0700 {
					t.Fatal("installer workspace is not private")
				}
				log, e := os.ReadFile(calls)
				if e != nil {
					t.Fatal(e)
				}
				source := "https://github.com/dsx-ai-factory/nke.git"
				if name == "local" {
					source = build.NKEClone
				}
				if !strings.Contains(string(log), "fetch --no-tags --depth=1 "+source+" "+InstallerCommit+"\n") || !strings.Contains(string(log), "checkout --detach "+InstallerCommit+"\n") {
					t.Fatal("Git did not fetch and detach the exact pin")
				}
			}
			if actual.Out != run.Out || actual.Timeout != run.Timeout {
				t.Fatal("preparation changed deployment intent")
			}
		})
	}
}

func TestPrepareInstallerCanceledAdmissionIsCoded(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	result, err := PrepareInstaller(ctx, BuildConfig{}, Config{})
	var coded *nkderr.Error
	if result != (Config{}) || !errors.As(err, &coded) || coded.Code != nkderr.CodeTimeout || !errors.Is(err, context.Canceled) {
		t.Fatalf("result=%+v error=%v", result, err)
	}
}

func TestPrepareInstallerAttemptDiagnostics(t *testing.T) {
	for _, root := range []string{"runs", "laptop-bootstrap", "installers"} {
		for _, blockedParent := range []bool{false, true} {
			t.Run(root+map[bool]string{false: "/existing", true: "/not-directory"}[blockedParent], func(t *testing.T) {
				repo := privateTestDir(t)
				tools := filepath.Join(repo, "tools")
				parent := filepath.Join(repo, ".nkd", root)
				if err := os.MkdirAll(filepath.Dir(parent), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(tools, 0700); err != nil {
					t.Fatal(err)
				}
				for _, name := range []string{"git", "bash", "python3"} {
					if err := os.WriteFile(filepath.Join(tools, name), []byte("#!/bin/sh\nexit 99\n"), 0700); err != nil {
						t.Fatal(err)
					}
				}
				t.Setenv("PATH", tools)
				owned := filepath.Join(parent, "retained")
				keep := owned
				if blockedParent {
					keep = parent
				} else if err := os.MkdirAll(parent, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(keep, []byte("original"), 0600); err != nil {
					t.Fatal(err)
				}
				result, err := PrepareInstaller(t.Context(), BuildConfig{Repository: repo, AttemptPrefix: "retained", Jobs: 2}, Config{Out: filepath.Join(repo, "out"), Timeout: time.Hour})
				var coded *nkderr.Error
				if result != (Config{}) || !errors.As(err, &coded) || coded.Code != nkderr.CodePreflightFailed || !strings.Contains(err.Error(), owned) {
					t.Fatalf("result=%+v error=%v", result, err)
				}
				if blockedParent {
					if !errors.Is(err, unix.ENOTDIR) || !strings.Contains(err.Error(), "cannot inspect") || strings.Contains(err.Error(), "already exists") {
						t.Fatalf("inspection cause obscured: %v", err)
					}
				} else if !strings.Contains(err.Error(), "already exists; choose a fresh --attempt") {
					t.Fatalf("existing path guidance absent: %v", err)
				}
				if raw, err := os.ReadFile(keep); err != nil || string(raw) != "original" {
					t.Fatal("preflight modified retained input")
				}
				if _, err := os.Lstat(filepath.Join(repo, "out")); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("preflight created output")
				}
			})
		}
	}
}
