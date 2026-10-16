// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
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

func TestBootstrapRuntimeKeepsLinuxNativeAndBindsMacDependencies(t *testing.T) {
	base := Runner{Env: []string{"PATH=/private/tools:/usr/bin:/bin", "HOME=/private/home", "KIND_EXPERIMENTAL_PROVIDER=docker"}}
	backend, err := prepareHostBootstrap(t.Context(), base, "linux", "", GuestGateway, nil)
	if err != nil {
		t.Fatal(err)
	}
	bound := backend.bind(base)
	if environmentValue(bound.Env, "PATH") != environmentValue(base.Env, "PATH") || environmentValue(bound.Env, "KIND_EXPERIMENTAL_PROVIDER") != "nerdctl" || environmentValue(bound.Env, "LIMA_HOME") != "" {
		t.Fatal("Linux bootstrap changed its native dependency path")
	}
	backend = &hostBootstrap{root: "/private/runtime", view: "/private/runtime/bin", temporary: "/private/shared/tmp"}
	bound = backend.bind(base)
	if environmentValue(bound.Env, "LIMA_HOME") != "/private/runtime/state" || environmentValue(bound.Env, "LIMA_INSTANCE") != "bootstrap" || environmentValue(bound.Env, "HOME") != "/private/home" || bound.directory != "/private/shared" {
		t.Fatal("Mac bootstrap lost its private dependency binding")
	}
	if environmentValue(base.Env, "KIND_EXPERIMENTAL_PROVIDER") != "docker" {
		t.Fatal("bootstrap changed the caller environment")
	}
}

func TestBootstrapLimaOwnsForegroundProcessThroughCleanup(t *testing.T) {
	root := privateTestDir(t)
	bin, shared, images := filepath.Join(root, "bin"), filepath.Join(root, "shared"), filepath.Join(root, "images")
	for _, dir := range []string{bin, shared, filepath.Join(shared, "tmp"), images} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	writeTenantFixture(t, filepath.Join(bin, "limactl"), []byte(`#!/bin/sh
set -eu
case "$1" in
 --version) printf 'limactl version 2.2.1\n';;
 start) trap 'exit 0' TERM INT; while :; do sleep 1; done;;
 stop) exit 0;;
 *) exit 42;;
esac
`), 0700)
	writeTenantFixture(t, filepath.Join(bin, "nerdctl.lima"), []byte("#!/bin/sh\nexit 0\n"), 0700)
	runner := Runner{Work: root, Env: []string{"PATH=" + bin + ":/usr/bin:/bin", "HOME=" + root}}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	backend, err := prepareHostBootstrap(ctx, runner, "darwin", shared, GuestGateway, []string{filepath.Join(images, "controller.tar")})
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
	if exited, err := backend.process.Exited(); exited {
		t.Fatalf("backend exited before cleanup: %v", err)
	}
	if err := backend.close(ctx); err != nil {
		t.Fatal(err)
	}
	if exited, err := backend.process.Exited(); !exited || err != nil {
		t.Fatalf("backend did not join: exited=%v err=%v", exited, err)
	}
	if _, err := os.Stat(owned); !os.IsNotExist(err) {
		t.Fatalf("owned backend staging remains: %v", err)
	}
}

func TestBootstrapLimaActualLifecycle(t *testing.T) {
	if runtime.GOOS != "darwin" || os.Getenv("NVKR_TEST_LIMA_BOOTSTRAP") != "1" {
		t.Skip("requires explicitly admitted local Lima VM execution")
	}
	root, err := os.MkdirTemp("/private/tmp", "nvkr-lima-check-")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("private Lima lifecycle diagnostics: %s", root)
	t.Cleanup(func() {
		if !t.Failed() {
			if err := os.RemoveAll(root); err != nil {
				t.Error(err)
			}
		}
	})
	shared, images := filepath.Join(root, "shared"), filepath.Join(root, "images")
	for _, dir := range []string{shared, filepath.Join(shared, "tmp"), images} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	runner := Runner{Work: root, Env: SetEnv(NativeEnvironment(), map[string]string{"HOME": root})}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Minute)
	defer cancel()
	backend, err := prepareHostBootstrap(ctx, runner, "darwin", shared, GuestGateway, []string{filepath.Join(images, "controller.tar")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		if err := backend.close(cleanup); err != nil {
			t.Error(err)
		}
	})
	version, err := backend.bind(runner).Capture(ctx, []string{"nerdctl", "version"}, "bootstrap-actual-version", 30*time.Second)
	if err != nil || !strings.Contains(string(version), "v2.4.1") {
		t.Fatalf("selected containerd frontend is unavailable: %v", err)
	}
	owned := backend.root
	if err := backend.close(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(owned); !os.IsNotExist(err) {
		t.Fatal("actual Lima runtime was not joined and removed")
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
