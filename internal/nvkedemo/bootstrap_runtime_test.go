// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestBootstrapLimaMounts(t *testing.T) {
	for _, test := range []struct {
		name, shared string
		images       []string
		want         string
	}{
		{"private stage and deduplicated read-only images", "/private/stage", []string{"/private/images/controller.tar", "/private/images/provider.tar"}, "/private/stage:w,/private/images"},
		{"relative stage", "stage", nil, ""},
		{"root stage", "/", nil, ""},
		{"unclean stage", "/private/../stage", nil, ""},
		{"mount option separator", "/private/stage,foreign", nil, ""},
		{"relative image", "/private/stage", []string{"image.tar"}, ""},
		{"root image", "/private/stage", []string{"/image.tar"}, ""},
		{"writable image overlap", "/private/stage", []string{"/private/stage/image.tar"}, ""},
		{"parent image overlap", "/private/stage", []string{"/private/image.tar"}, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := bootstrapLimaMounts(test.shared, test.images)
			var coded *e.Error
			if got != test.want || test.want == "" && (!errors.As(err, &coded) || coded.Code != e.CodeUsage) || test.want != "" && err != nil {
				t.Fatalf("mounts=%q err=%v; want %q", got, err, test.want)
			}
		})
	}
}

func TestBootstrapRuntimeBindsPrivateDependencies(t *testing.T) {
	base := Runner{Env: []string{"PATH=/private/tools:/usr/bin:/bin", "HOME=/private/home", "KIND_EXPERIMENTAL_PROVIDER=docker"}}
	backend := &hostBootstrap{root: "/private/runtime", view: "/private/runtime/bin", temporary: "/private/shared/tmp"}
	bound := backend.bind(base)
	if environmentValue(bound.Env, "LIMA_HOME") != "/private/runtime/state" || environmentValue(bound.Env, "LIMA_INSTANCE") != "bootstrap" || environmentValue(bound.Env, "HOME") != "/private/home" || bound.directory != "/private/shared" {
		t.Fatal("Mac bootstrap lost its private dependency binding")
	}
	if environmentValue(base.Env, "KIND_EXPERIMENTAL_PROVIDER") != "docker" {
		t.Fatal("bootstrap changed the caller environment")
	}
}

func TestBootstrapVersionAdmissionBeforeRuntime(t *testing.T) {
	t.Setenv("HOME", "")
	if err := os.Unsetenv("HOME"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("USER", "synthetic-foreign-user")
	t.Setenv("XDG_CACHE_HOME", "/synthetic/foreign-cache")
	for _, test := range []struct {
		platform, observed string
		accepted           bool
	}{
		{"darwin", "2.2.1", true},
		{"linux", "f47461e", true},
		{"linux", "2.2.1", false},
		{"darwin", "f47461e", false},
		{"windows", "f47461e", false},
	} {
		t.Run(test.platform+test.observed, func(t *testing.T) {
			root := privateTestDir(t)
			writeTenantFixture(t, filepath.Join(root, "limactl"), []byte("#!/bin/sh\nset -eu\ntest \"$1\" = --version\ntest -n \"$HOME\"\nprintf 'limactl version %s\\n' \"$VERSION\"\n"), 0700)
			writeTenantFixture(t, filepath.Join(root, "nerdctl.lima"), []byte("#!/bin/sh\nexit 89\n"), 0700)
			runner := Runner{Env: []string{"PATH=" + root + ":/usr/bin:/bin", "VERSION=" + test.observed}}
			_, _, err := requireBootstrapLima(t.Context(), runner, test.platform)
			if environmentValue(runner.Env, "HOME") != "" {
				t.Fatal("version admission mutated the caller environment")
			}
			if test.accepted {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				var coded *e.Error
				if !errors.As(err, &coded) || coded.Code != e.CodeEngineCapability {
					t.Fatalf("unsupported backend admitted: %v", err)
				}
			}
			entries, err := os.ReadDir(root)
			if err != nil || len(entries) != 2 {
				t.Fatal("version admission created runtime state", err)
			}
		})
	}
}

func TestBootstrapAccountEnvironment(t *testing.T) {
	account, err := user.LookupId(strconv.Itoa(os.Getuid()))
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		platform, cache string
		canceled        bool
		code            e.Code
	}{
		{"darwin", filepath.Join(account.HomeDir, "Library", "Caches"), false, ""},
		{"linux", filepath.Join(account.HomeDir, ".cache"), false, ""},
		{"windows", "", false, e.CodeEngineCapability},
		{"darwin", "", true, e.CodeTimeout},
		{"linux", "", true, e.CodeTimeout},
	} {
		t.Run(fmt.Sprintf("%s/canceled=%t", test.platform, test.canceled), func(t *testing.T) {
			base := []string{"HOME=/synthetic/foreign", "XDG_CACHE_HOME=/synthetic/cache", "LIMA_HOME=/private/state", "LIMA_INSTANCE=bootstrap", "XDG_CONFIG_HOME=/private/config"}
			before := slices.Clone(base)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if test.canceled {
				cancel()
			}
			got, err := bootstrapAccountEnvironment(ctx, test.platform, base)
			if test.code != "" {
				var coded *e.Error
				if got != nil || !errors.As(err, &coded) || coded.Code != test.code {
					t.Fatalf("environment=%v error=%v; want %s", got, err, test.code)
				}
			} else if err != nil || environmentValue(got, "HOME") != account.HomeDir || environmentValue(got, "XDG_CACHE_HOME") != test.cache || environmentValue(got, "LIMA_HOME") != "/private/state" || environmentValue(got, "LIMA_INSTANCE") != "bootstrap" || environmentValue(got, "XDG_CONFIG_HOME") != "/private/config" {
				t.Fatalf("account cache or private bootstrap binding lost: %v", err)
			}
			if !slices.Equal(base, before) {
				t.Fatal("bootstrap changed the caller environment")
			}
		})
	}
}

func TestBootstrapLimaInstalledVersionWithoutHome(t *testing.T) {
	if (runtime.GOOS != "darwin" && runtime.GOOS != "linux") || os.Getenv("NVKR_TEST_LIMA_VERSION") != "1" {
		t.Skip("requires explicitly selected installed Lima; version admission only, no VM")
	}
	t.Setenv("HOME", "")
	if err := os.Unsetenv("HOME"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("USER", "synthetic-foreign-user")
	t.Setenv("XDG_CACHE_HOME", "/synthetic/foreign-cache")
	runner := Runner{Env: NativeEnvironment()}
	before := slices.Clone(runner.Env)
	if _, _, err := requireBootstrapLima(t.Context(), runner, runtime.GOOS); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(runner.Env, before) || os.Getenv("HOME") != "" || os.Getenv("XDG_CACHE_HOME") != "/synthetic/foreign-cache" {
		t.Fatal("version-only admission changed the caller environment")
	}
}

func TestBootstrapLimaOwnsForegroundProcessThroughCleanup(t *testing.T) {
	for _, platform := range []string{"darwin", "linux"} {
		t.Run(platform, func(t *testing.T) { checkBootstrapOwnedLifecycle(t, platform) })
	}
}

func checkBootstrapOwnedLifecycle(t *testing.T, platform string) {
	t.Helper()
	t.Setenv("HOME", "")
	if err := os.Unsetenv("HOME"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("USER", "synthetic-foreign-user")
	t.Setenv("XDG_CACHE_HOME", "/synthetic/foreign-cache")
	account, err := user.LookupId(strconv.Itoa(os.Getuid()))
	if err != nil {
		t.Fatal(err)
	}
	cache := filepath.Join(account.HomeDir, ".cache")
	if platform == "darwin" {
		cache = filepath.Join(account.HomeDir, "Library", "Caches")
	}
	root := privateTestDir(t)
	bin, shared, images := filepath.Join(root, "bin"), filepath.Join(root, "shared"), filepath.Join(root, "images")
	for _, dir := range []string{bin, shared, filepath.Join(shared, "tmp"), images} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	writeTenantFixture(t, filepath.Join(bin, "limactl"), []byte(`#!/bin/sh
set -eu
test "$HOME" = "$EXPECTED_HOME"
test "$XDG_CACHE_HOME" = "$EXPECTED_CACHE"
case "$1" in
 --version) printf 'limactl version %s\n' "$VERSION";;
 start) trap 'exit 0' TERM INT; : > "$LIMA_HOME.fixture-ready"; while :; do sleep 1; done;;
 stop) exit 0;;
 --log-level=error)
  shift
  test "$1" = shell && test "$2" = --preserve-env && test "$3" = bootstrap && test "$4" = nerdctl
  test -f "$LIMA_HOME.fixture-ready"
  case "$5" in
   info) exit 0;;
   image) printf '%s %s\n' "$DIGEST" "$DIGEST";;
   fail) printf 'guest containerd failed\n' >&2; exit 23;;
   *) exit 44;;
  esac;;
 shell) printf 'Lima version warning\n' >&2; exit 45;;
 *) exit 42;;
esac
`), 0700)
	// Match the upstream wrapper, including its PATH lookup of limactl.
	writeTenantFixture(t, filepath.Join(bin, "nerdctl.lima"), []byte("#!/bin/sh\nset -eu\nexec limactl shell --preserve-env \"$LIMA_INSTANCE\" nerdctl \"$@\"\n"), 0700)
	version, _, err := bootstrapLimaPlatform(platform)
	if err != nil {
		t.Fatal(err)
	}
	runner := Runner{Work: root, Env: []string{"PATH=" + bin + ":/usr/bin:/bin", "HOME=" + root, "VERSION=" + version, "USER=synthetic-foreign-user", "XDG_CACHE_HOME=/synthetic/foreign-cache", "XDG_CONFIG_HOME=" + filepath.Join(root, "private-config"), "EXPECTED_HOME=" + account.HomeDir, "EXPECTED_CACHE=" + cache}}
	before := slices.Clone(runner.Env)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	backend, err := prepareHostBootstrap(ctx, runner, platform, shared, GuestGateway, []string{filepath.Join(images, "controller.tar")})
	if err != nil {
		t.Fatal(err)
	}
	owned := backend.root
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if err := backend.close(cleanup); err != nil {
			t.Error(err)
		}
	})
	if !slices.Equal(runner.Env, before) || environmentValue(backend.runner.Env, "LIMA_HOME") != filepath.Join(backend.root, "state") || environmentValue(backend.runner.Env, "LIMA_INSTANCE") != "bootstrap" || environmentValue(backend.runner.Env, "XDG_CONFIG_HOME") != filepath.Join(root, "private-config") {
		t.Fatal("bootstrap changed caller environment or lost private instance/config selection")
	}
	if exited, err := backend.process.Exited(); exited {
		t.Fatalf("backend exited before cleanup: %v", err)
	}
	// NVKE consumes combined output here. Exercise the upstream wrapper and
	// private transport as real subprocesses, preserving guest errors/status.
	digest := "sha256:" + strings.Repeat("a", 64)
	for _, test := range []struct {
		command, want string
		exit          int
	}{
		{"image", digest + " " + digest + "\n", 0},
		{"fail", "guest containerd failed\n", 23},
	} {
		command := exec.CommandContext(ctx, filepath.Join(backend.view, "nerdctl"), test.command)
		command.Env = SetEnv(backend.runner.Env, map[string]string{"DIGEST": digest})
		command.WaitDelay = time.Second
		output, err := command.CombinedOutput()
		exit := 0
		if err != nil {
			var failed *exec.ExitError
			if !errors.As(err, &failed) {
				t.Fatal(err)
			}
			exit = failed.ExitCode()
		}
		if string(output) != test.want || exit != test.exit {
			t.Fatalf("%s: combined output=%q exit=%d; want %q exit=%d", test.command, output, exit, test.want, test.exit)
		}
	}
	if err := backend.close(ctx); err != nil {
		t.Fatal(err)
	}
	// Exited preserves the original child status after a requested stop;
	// cleanup succeeds only when the owned process and its group have joined.
	if exited, _ := backend.process.Exited(); !exited {
		t.Fatal("backend did not join")
	}
	if alive, err := groupAlive(backend.process.PID()); err != nil || alive {
		t.Fatalf("backend process group survives: alive=%v err=%v", alive, err)
	}
	if _, err := os.Stat(owned); !os.IsNotExist(err) {
		t.Fatalf("owned backend staging remains: %v", err)
	}
}

func TestBootstrapLimaActualLifecycle(t *testing.T) {
	if (runtime.GOOS != "darwin" && runtime.GOOS != "linux") || runtime.GOARCH != "arm64" || os.Getenv("NVKR_TEST_LIMA_BOOTSTRAP") != "1" {
		t.Skip("requires explicitly admitted local Lima VM execution")
	}
	if runtime.GOOS == "linux" {
		if err := AdmitNativeDeployment(t.Context(), "kvm"); err != nil {
			t.Fatal(err)
		}
	}
	temporary := os.TempDir()
	gateway := "127.0.0.1"
	if runtime.GOOS == "darwin" {
		temporary, gateway = "/private/tmp", GuestGateway
	}
	root, err := os.MkdirTemp(temporary, "nvkr-lima-check-")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("private Lima lifecycle diagnostics: %s", root)
	// Keep original private logs on both success and failure. The backend owner
	// separately removes only its own instance after stop and join.
	shared, images := filepath.Join(root, "shared"), filepath.Join(root, "images")
	for _, dir := range []string{shared, filepath.Join(shared, "tmp"), images} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	marker := []byte("owned-bootstrap-mount-and-forwarding-" + filepath.Base(root) + "\n")
	image := filepath.Join(images, "controller.tar")
	writeTenantFixture(t, filepath.Join(shared, "marker"), marker, 0600)
	writeTenantFixture(t, image, marker, 0600)
	runner := Runner{Work: root, Env: SetEnv(NativeEnvironment(), map[string]string{"HOME": root})}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Minute)
	defer cancel()
	backend, err := prepareHostBootstrap(ctx, runner, runtime.GOOS, shared, gateway, []string{image})
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		if err := backend.close(cleanup); err != nil {
			t.Error(err)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := os.ReadFile(filepath.Join(backend.root, "state", "bootstrap", "lima.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	writeTenantFixture(t, filepath.Join(root, "rendered-lima.yaml"), rendered, 0600)
	version, err := backend.bind(runner).Capture(ctx, []string{"nerdctl", "version"}, "bootstrap-actual-version", 30*time.Second)
	if err != nil || !strings.Contains(string(version), "v2.4.1") {
		t.Fatalf("selected containerd frontend is unavailable: %v", err)
	}
	if runtime.GOOS == "linux" {
		probe := backend.bind(runner)
		guest := []string{backend.limactl, "shell", "--tty=false", "bootstrap", "--"}
		if err := probe.Run(ctx, append(append([]string{}, guest...), "sh", "-c", "command -v python3"), "bootstrap-actual-python", 30*time.Second); err != nil {
			t.Fatal("selected pilot image lacks the standard Python HTTP fixture prerequisite", err)
		}
		raw, err := probe.Capture(ctx, append(append([]string{}, guest...), "cat", filepath.Join(shared, "marker")), "bootstrap-actual-read", 30*time.Second)
		if err != nil || string(raw) != string(marker) {
			t.Fatalf("backend could not read the selected shared mount: %v", err)
		}
		if err := probe.Run(ctx, append(append([]string{}, guest...), "sh", "-c", `test -r "$1" && ! test -w "$1" && printf returned > "$2"`, "nvkr-pilot", image, filepath.Join(shared, "returned")), "bootstrap-actual-write", 30*time.Second); err != nil {
			t.Fatal("read-only image or writable staging contract failed", err)
		}
		if raw, err := os.ReadFile(filepath.Join(shared, "returned")); err != nil || string(raw) != "returned" {
			t.Fatal("backend write did not reach its selected staging mount", err)
		}
		server, err := probe.Start(ctx, append(append([]string{}, guest...), "python3", "-u", "-m", "http.server", "30081", "--bind", "0.0.0.0", "--directory", shared), "bootstrap-actual-http")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := server.Stop(cleanup); err != nil {
				t.Error(err)
			}
		})
		ready, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		client := &http.Client{Timeout: 2 * time.Second}
		for {
			if exited, err := server.Exited(); exited {
				t.Fatal("owned HTTP fixture exited before forwarding observation", err)
			}
			request, err := http.NewRequestWithContext(ready, http.MethodGet, "http://127.0.0.1:30081/marker", nil)
			if err != nil {
				t.Fatal(err)
			}
			response, err := client.Do(request)
			if err == nil {
				raw, readErr := io.ReadAll(io.LimitReader(response.Body, int64(len(marker)+1)))
				closeErr := response.Body.Close()
				if readErr == nil && closeErr == nil && response.StatusCode == http.StatusOK && string(raw) == string(marker) {
					if exited, err := server.Exited(); exited {
						t.Fatal("owned HTTP fixture exited during forwarding observation", err)
					}
					break
				}
			}
			select {
			case <-ready.Done():
				t.Fatal("owned loopback forwarding did not deliver the exact shared marker", ready.Err())
			case <-time.After(200 * time.Millisecond):
			}
		}
		if err := server.Stop(ctx); err != nil {
			t.Fatal(err)
		}
	}
	owned := backend.root
	if err := backend.close(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(owned); !os.IsNotExist(err) {
		t.Fatal("actual Lima runtime was not joined and removed")
	}
}

func TestBootstrapUnjoinedProbePreservesDependentBackend(t *testing.T) {
	root := privateTestDir(t)
	done := make(chan struct{})
	close(done)
	probe := &OwnedProcess{done: done, joinErr: ErrUnjoined}
	backend := &hostBootstrap{root: root, probe: probe}
	if err := backend.finishProbe(t.Context(), nil); !errors.Is(err, ErrUnjoined) || backend.probe != probe {
		t.Fatal("unjoined readiness probe was forgotten", err)
	}
	if err := backend.close(t.Context()); !errors.Is(err, ErrUnjoined) || backend.root != root {
		t.Fatal("unjoined dependent probe did not protect backend", err)
	}
	if _, err := os.Stat(root); err != nil {
		t.Fatal("dependent backend staging was removed", err)
	}
}

func TestBootstrapLimaStartupFailureReleasesOwnedState(t *testing.T) {
	for _, firstStopFails := range []bool{false, true} {
		t.Run(map[bool]string{false: "joined cleanup", true: "failed cleanup retains owner for retry"}[firstStopFails], func(t *testing.T) {
			root := privateTestDir(t)
			bin, shared := filepath.Join(root, "bin"), filepath.Join(root, "shared")
			for _, directory := range []string{bin, shared, filepath.Join(shared, "tmp")} {
				if err := os.Mkdir(directory, 0700); err != nil {
					t.Fatal(err)
				}
			}
			record := filepath.Join(root, "allocated")
			writeTenantFixture(t, filepath.Join(bin, "limactl"), []byte(`#!/bin/sh
set -eu
case "$1" in
 --version) printf 'limactl version 2.2.1\n';;
 start) printf '%s\n' "${LIMA_HOME%/state}" > "$RECORD"; exit 7;;
 stop)
  if test "$FAIL_STOP" = 1 && ! test -f "$RECORD.stopped"; then
   : > "$RECORD.stopped"
   exit 8
  fi;;
 *) exit 42;;
esac
`), 0700)
			writeTenantFixture(t, filepath.Join(bin, "nerdctl.lima"), []byte("#!/bin/sh\nexit 1\n"), 0700)
			failStop := "0"
			if firstStopFails {
				failStop = "1"
			}
			runner := Runner{Work: root, Env: []string{"PATH=" + bin + ":/usr/bin:/bin", "RECORD=" + record, "FAIL_STOP=" + failStop}}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			backend, err := prepareHostBootstrap(ctx, runner, "darwin", shared, GuestGateway, nil)
			if err == nil || (backend != nil) != firstStopFails {
				t.Fatalf("failed startup lost its cleanup owner: backend=%v error=%v", backend != nil, err)
			}
			if backend != nil {
				if err := backend.close(ctx); err != nil {
					t.Fatal(err)
				}
			}
			allocated, readErr := os.ReadFile(record)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if _, statErr := os.Stat(strings.TrimSpace(string(allocated))); !os.IsNotExist(statErr) {
				t.Fatalf("failed startup left private runtime state: %v", statErr)
			}
		})
	}
}
