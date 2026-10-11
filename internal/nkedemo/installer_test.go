// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nkedemo

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	nkderr "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"golang.org/x/sys/unix"
)

func TestPrepareInstallerPinnedAttempt(t *testing.T) {
	for _, name := range []string{"remote", "local", "sibling", "override", "diagnostic", "interactive", "interactive-auth-failed", "interactive-sudo-failed", "budget-minimum", "budget-maximum", "budget-low", "budget-high", "budget-overflow", "dirty", "wrong-pin", "preflight", "sudo", "fetch", "retained", "symlink", "canceled", "cancel-fetch", "kernel", "kernel-admission-missing", "output-inside-installer", "output-installers", "output-runs", "output-laptop-bootstrap", "output-bootstrap-attempt", "kind-missing", "kind-wrong-id", "kind-wrong-arch", "registry-missing", "registry-wrong-arch", "registry-empty-id", "registry-invalid-id", "registry-duplicate", "registry-malformed", "network-invalid-gateway", "simulator-port-busy"} {
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
			// A weaker cached `sudo -n true` would pass even in the failing
			// policy fixture. Only the exact cache-independent runtime probe
			// can expose the missing unattended authorization before Git/builds.
			write(filepath.Join(tools, "sudo"), "#!/bin/sh\nif [ \"$*\" = '-n true' ]; then exit 0; fi\n[ \"$*\" = '-n -k timeout --signal=TERM --kill-after=5 25 true' ] && [ \"${FAIL:-}\" != sudo ]\n")
			if strings.HasPrefix(name, "interactive") {
				write(filepath.Join(tools, "sudo"), fmt.Sprintf("#!/bin/sh\ntest -f %q || exit 99\n[ \"$*\" = '-n timeout --signal=TERM --kill-after=5 25 true' ] && [ \"${FAIL:-}\" != interactive-sudo-failed ]\n", filepath.Join(root, "authenticated")))
			}
			imageCalls := filepath.Join(root, "image-calls")
			write(filepath.Join(tools, "docker"), fmt.Sprintf("#!/bin/sh\nset -eu\nFAIL=%q\nIMAGE_CALLS=%q\nKIND=%q\nREGISTRY=%q\nKIND_ID=%q\n", name, imageCalls, kindNodeImage, registryImage, kindNodeID)+`
if [ "$1:$2:$3" = network:inspect:kind ]; then
  printf '[{"Id":"kind-network","Driver":"bridge","IPAM":{"Config":[{"Gateway":"invalid","Subnet":"172.19.0.0/16"}]}}]'
  exit 0
fi
[ "$1:$2" = image:inspect ] || exit 99
printf '%s\n' "$3" >>"$IMAGE_CALLS"
if [ "$3" = "$KIND" ]; then
  [ "$FAIL" != kind-missing ] || exit 1
  id="$KIND_ID"; arch=arm64
  [ "$FAIL" != kind-wrong-id ] || id=foreign
  [ "$FAIL" != kind-wrong-arch ] || arch=amd64
  printf '[{"Id":"%s","Architecture":"%s"}]\n' "$id" "$arch"
elif [ "$3" = "$REGISTRY" ]; then
  [ "$FAIL" != registry-missing ] || exit 1
  id="$KIND_ID"; arch=arm64
  [ "$FAIL" != registry-wrong-arch ] || arch=amd64
  [ "$FAIL" != registry-empty-id ] || id=
  [ "$FAIL" != registry-invalid-id ] || id=foreign
  if [ "$FAIL" = registry-malformed ]; then printf malformed; exit 0; fi
  if [ "$FAIL" = registry-duplicate ]; then printf '[{},{}]'; exit 0; fi
  printf '[{"Id":"%s","Architecture":"%s"}]\n' "$id" "$arch"
else exit 99
fi
`)
			// This command fixture tests ownership and exact invocation, not Git
			// authorization or an externally fetched commit.
			started := filepath.Join(root, "fetch-started")
			write(filepath.Join(tools, "git"), fmt.Sprintf("#!/bin/sh\nCALLS=%q\nPIN=%q\nFAIL=%q\nFETCH_STARTED=%q\n", calls, InstallerCommit, name, started)+`
set -eu
while [ "$#" -gt 0 ]; do
case "$1" in
--no-replace-objects|--literal-pathspecs|--no-pager) shift ;;
-C|-c) shift 2 ;;
*) break ;;
esac
done
if [ "$1" = config ]; then exit 1; fi
printf '%s\n' "$*" >>"$CALLS"
if [ "$1" = init ]; then mkdir "$3"; exit 0; fi
case "$1" in
fetch) if [ "${FAIL:-}" = cancel-fetch ]; then : > "$FETCH_STARTED"; exec sleep 60; fi; [ "${FAIL:-}" != fetch ] ;;
checkout) : ;;
rev-parse) if [ "${FAIL:-}" = wrong-pin ]; then printf foreign; else printf '%s' "$PIN"; fi ;;
status) if [ "${FAIL:-}" = dirty ]; then printf '?? unexpected'; fi ;;
*) exit 9 ;;
esac
`)
			// Only presence is checked; the downstream NKE installer, not this
			// helper, owns its use of Python.
			write(filepath.Join(tools, "python3"), "#!/bin/sh\nexit 9\n")
			runner := Runner{Env: []string{"PATH=" + tools + ":/usr/bin:/bin", "CALLS=" + calls, "PIN=" + InstallerCommit, "FAIL=" + name, "FETCH_STARTED=" + started}}
			authCalls := 0
			authFailure := errors.New("terminal authentication refused")
			if strings.HasPrefix(name, "interactive") {
				runner.Progress = &sudoAuthenticationProgress{authenticate: func(ctx context.Context) error {
					authCalls++
					if name == "interactive-auth-failed" {
						return authFailure
					}
					return os.WriteFile(filepath.Join(root, "authenticated"), []byte("admitted"), 0600)
				}}
			}
			build := BuildConfig{Repository: repo, AttemptPrefix: "fresh", Jobs: 2}
			run := Config{Out: filepath.Join(root, "out"), Timeout: time.Hour}
			switch name {
			case "budget-minimum":
				run.Timeout = 5 * time.Minute
			case "budget-maximum":
				run.Timeout = 2 * time.Hour
			case "budget-low":
				run.Timeout = 299 * time.Second
			case "budget-high":
				run.Timeout = 7201 * time.Second
			case "budget-overflow":
				run.Timeout = time.Duration(9223372036854775807)
			}
			if name == "diagnostic" || name == "network-invalid-gateway" {
				run.Network = "kind-calico"
			}
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
				// The marker uses a shell builtin so readiness itself cannot leave
				// an unrelated orphan child in the owned process group. Allow the
				// preceding admission commands to run under full-suite load.
				var deadlineCancel context.CancelFunc
				ctx, deadlineCancel = context.WithTimeout(ctx, 30*time.Second)
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
			imageFailure := strings.HasPrefix(name, "kind-") || strings.HasPrefix(name, "registry-")
			networkFailure := name == "network-invalid-gateway"
			var actual Config
			var err error
			if imageFailure || networkFailure {
				t.Setenv("PATH", tools+":/usr/bin:/bin")
				actual, err = PrepareInstaller(ctx, build, run)
			} else {
				admission := func() error { return nil }
				switch name {
				case "kernel":
					admission = func() error { return errors.New("controlled missing host modules") }
				case "kernel-admission-missing":
					admission = nil
				case "simulator-port-busy":
					listener, listenErr := net.Listen("tcp4", "127.0.0.1:18089")
					if listenErr != nil {
						t.Fatal(listenErr)
					}
					defer listener.Close()
					admission = func() error { return admitSimulatorPort(ctx, "127.0.0.1") }
				}
				actual, err = prepareInstaller(ctx, build, run, runner, admission)
			}
			success := name == "remote" || name == "local" || name == "sibling" || name == "override" || name == "diagnostic" || name == "interactive" || name == "budget-minimum" || name == "budget-maximum"
			if (err == nil) != success {
				t.Fatalf("result=%+v error=%v", actual, err)
			}
			if name == "cancel-fetch" {
				if _, startedErr := os.Stat(started); startedErr != nil {
					t.Fatalf("fetch cancellation fixture never reached the owned fetch: %v; acquisition error: %v", startedErr, err)
				}
			}
			if strings.HasPrefix(name, "interactive") && authCalls != 1 {
				t.Fatalf("authentication calls=%d, want exactly one", authCalls)
			}
			if name == "interactive-auth-failed" && !errors.Is(err, authFailure) {
				t.Fatalf("authentication cause lost: %v", err)
			}
			if !success {
				if name == "preflight" || name == "sudo" || strings.HasPrefix(name, "interactive-") || strings.HasPrefix(name, "output-") || imageFailure || networkFailure || strings.HasPrefix(name, "kernel") || name == "simulator-port-busy" || strings.HasPrefix(name, "budget-") {
					if _, e := os.Lstat(calls); !errors.Is(e, os.ErrNotExist) {
						t.Fatal("failed preflight reached Git acquisition")
					}
				}
				if strings.HasPrefix(name, "budget-") {
					if _, e := os.Lstat(imageCalls); !errors.Is(e, os.ErrNotExist) {
						t.Fatal("invalid budget reached image inspection")
					}
					if _, e := os.Lstat(run.Out); !errors.Is(e, os.ErrNotExist) {
						t.Fatal("invalid budget created diagnostic output")
					}
				}
				if strings.HasPrefix(name, "kernel") {
					for _, path := range []string{filepath.Join(repo, ".nkd"), run.Out} {
						if _, e := os.Lstat(path); !errors.Is(e, os.ErrNotExist) {
							t.Fatal("kernel admission failure created output or acquired source")
						}
					}
				}
				if imageFailure {
					var coded *nkderr.Error
					image := registryImage
					if strings.HasPrefix(name, "kind-") {
						image = kindNodeImage
					}
					if !errors.As(err, &coded) || coded.Code != nkderr.CodePreflightFailed || !strings.Contains(err.Error(), "docker pull "+image) || !strings.Contains(err.Error(), "same native Linux Docker daemon") {
						t.Fatalf("missing coded image admission or actionable guidance: %v", err)
					}
					if _, e := os.Lstat(filepath.Join(repo, ".nkd")); !errors.Is(e, os.ErrNotExist) {
						t.Fatal("image failure created attempt or build output")
					}
					if _, e := os.Lstat(run.Out); !errors.Is(e, os.ErrNotExist) {
						t.Fatal("image failure created diagnostic output")
					}
				}
				if networkFailure || name == "simulator-port-busy" {
					var coded *nkderr.Error
					if !errors.As(err, &coded) || coded.Code != nkderr.CodePreflightFailed {
						t.Fatalf("missing coded early network admission: %v", err)
					}
					for _, path := range []string{filepath.Join(repo, ".nkd"), run.Out} {
						if _, e := os.Lstat(path); !errors.Is(e, os.ErrNotExist) {
							t.Fatal("network admission failure created output or acquired source")
						}
					}
				}
				if name == "sudo" {
					for _, hint := range []string{networkSudoProbe, "host administrator", "sudo -v", "docs/local-nke-demo.md"} {
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
					t.Fatalf("failed acquisition left workspace: %v; acquisition error: %v", e, err)
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
				if !strings.Contains(string(log), "fetch --upload-pack=git-upload-pack --no-recurse-submodules --no-tags --depth=1 "+source+" "+InstallerCommit+"\n") || !strings.Contains(string(log), "checkout --detach "+InstallerCommit+"\n") {
					t.Fatal("Git did not fetch and detach the exact pin")
				}
			}
			if actual.Out != run.Out || actual.Timeout != run.Timeout || actual.Network != run.Network {
				t.Fatal("preparation changed deployment intent")
			}
			if raw, e := os.ReadFile(imageCalls); e != nil || string(raw) != kindNodeImage+"\n"+registryImage+"\n" {
				t.Fatalf("deployment images were not admitted by exact pinned references: %q %v", raw, e)
			}
		})
	}
}

func TestPrepareInstallerUnknownNetworkDoesNotStartWork(t *testing.T) {
	var progress bytes.Buffer
	result, err := PrepareInstaller(t.Context(), BuildConfig{}, Config{Network: "unknown", Progress: &progress})
	assertConfigUsage(t, err)
	if result != (Config{}) || progress.Len() != 0 {
		t.Fatal("invalid network produced prepared inputs or started progress")
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
