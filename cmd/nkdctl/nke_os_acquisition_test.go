// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/catalog"
	"go.yaml.in/yaml/v3"
)

func TestOSAgentAcquisitionUsesGoTransportFallback(t *testing.T) {
	root := filepath.Dir(filepath.Dir(demoScript(t, "nke-os-local.sh")))
	raw, err := os.ReadFile(filepath.Join(root, "definitions/nke-os/catalog/registry.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var registry struct {
		Sources map[string]struct {
			VendorCommand []string `yaml:"vendorCommand"`
		}
	}
	if err := yaml.Unmarshal(raw, &registry); err != nil {
		t.Fatal(err)
	}
	command := registry.Sources["nke-os"].VendorCommand
	if len(command) != 3 || command[0] != "bash" || command[1] != "-ec" {
		t.Fatalf("unexpected agent preparation command: %v", command)
	}
	overlay, err := catalog.LoadOverlayContext(t.Context(), filepath.Join(root, "definitions/nke-os/arm64-kvm.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	producer := overlay.Components["nke-node-image"].Component
	if producer == nil || !slices.Equal(producer.VendorCommand, command) {
		t.Fatal("ARM64 KVM producer must retain the same tested acquisition command")
	}
	const module = "example.com/material"
	const version = "v1.0.0"
	const mod = "module " + module + "\n\ngo 1.27.0\n"
	var archive bytes.Buffer
	w := zip.NewWriter(&archive)
	for _, file := range []struct{ name, body string }{
		{"go.mod", mod}, {"material.go", "package material\nconst Value = 1\n"},
	} {
		f, err := w.Create(module + "@" + version + "/" + file.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write([]byte(file.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var failures, requests int
	var faultExtension, offlineMarker string
	var interruptBody, corruptArchive bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The official Go proxy accepts equivalent repeated leading slashes.
		// Match that behavior without altering the authored proxy URLs.
		path := "/" + strings.TrimLeft(r.URL.Path, "/")
		mu.Lock()
		if strings.HasSuffix(path, faultExtension) {
			requests++
		}
		fail := strings.HasSuffix(path, faultExtension) && requests <= failures
		interrupt, corrupt, marker := interruptBody, corruptArchive, offlineMarker
		mu.Unlock()
		if marker != "" {
			if _, err := os.Stat(marker); err == nil {
				t.Error("verification or vendoring made a network request after acquisition")
				http.Error(w, "fixture proxy is offline", http.StatusServiceUnavailable)
				return
			}
		}
		if fail {
			if interrupt {
				conn, _, err := w.(http.Hijacker).Hijack()
				if err != nil {
					t.Error(err)
					return
				}
				defer conn.Close()
				_, _ = fmt.Fprintf(conn, "HTTP/1.1 200 OK\r\nContent-Length: %d\r\n\r\n", archive.Len())
				_, _ = conn.Write(archive.Bytes()[:archive.Len()/2])
				return
			}
			http.Error(w, "temporary fixture failure", http.StatusServiceUnavailable)
			return
		}
		switch path {
		case "/" + module + "/@v/" + version + ".info":
			_, _ = fmt.Fprint(w, `{"Version":"v1.0.0","Time":"2026-01-01T00:00:00Z"}`)
		case "/" + module + "/@v/" + version + ".mod":
			_, _ = fmt.Fprint(w, mod)
		case "/" + module + "/@v/" + version + ".zip":
			if corrupt {
				_, _ = w.Write(archive.Bytes()[:archive.Len()/2])
				return
			}
			_, _ = w.Write(archive.Bytes())
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	// Only the fixture disables sumdb: Go computes the known fixture's hashes,
	// and every subsequent download must match those pinned go.sum entries.
	baseEnv := append(os.Environ(), "GOENV=off", "GOWORK=off", "GOTOOLCHAIN=local",
		"GOFLAGS=-mod=readonly", "GOPROXY="+server.URL, "GOSUMDB=off",
		"GOPRIVATE=", "GONOPROXY=", "GONOSUMDB=", "GOVCS=*:off")
	cacheEnv := func(t *testing.T, dir string) []string {
		t.Helper()
		env := append(slices.Clone(baseEnv), "GOMODCACHE="+filepath.Join(dir, "cache"), "GOCACHE="+filepath.Join(dir, "build-cache"))
		// Go's module cache has read-only directories. Let its own cleaner
		// release this exact fixture cache before TempDir cleanup.
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "go", "clean", "-modcache")
			cmd.Dir, cmd.Env = dir, env
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Errorf("clean fixture cache: %v: %s", err, out)
			}
		})
		return env
	}
	bootstrap := t.TempDir()
	cmd := exec.CommandContext(t.Context(), "go", "mod", "download", "-json", module+"@"+version)
	cmd.Dir = bootstrap
	cmd.Env = cacheEnv(t, bootstrap)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bootstrap known module checksums: %v: %s", err, out)
	}
	var sums struct{ Sum, GoModSum string }
	if err := json.Unmarshal(out, &sums); err != nil || sums.Sum == "" || sums.GoModSum == "" {
		t.Fatalf("known fixture checksum output: %v: %s", err, out)
	}
	for _, test := range []struct {
		name, extension, driftPhase, timeoutPhase string
		failures, wantRequests                    int
		wrongSum, interrupt, corrupt              bool
		wantError                                 string
	}{
		{name: "initial success", extension: ".zip", wantRequests: 1},
		{name: "zip retries recover", extension: ".zip", failures: 2, wantRequests: 3},
		{name: "info retries recover", extension: ".info", failures: 2, wantRequests: 3},
		{name: "mod retries recover", extension: ".mod", failures: 2, wantRequests: 3},
		{name: "interrupted zip retries recover", extension: ".zip", failures: 2, interrupt: true, wantRequests: 3},
		{name: "transport retries exhaust", extension: ".zip", failures: 3, wantRequests: 3, wantError: "agent module download failed"},
		{name: "checksum failure is not retried", extension: ".zip", wrongSum: true, wantRequests: 1, wantError: "checksum mismatch"},
		{name: "corrupt archive is not retried", extension: ".zip", corrupt: true, wantRequests: 1, wantError: "zip: not a valid zip file"},
		{name: "download pin drift fails", extension: ".zip", driftPhase: "download", wantRequests: 1, wantError: "pins changed during download"},
		{name: "vendor pin drift fails", extension: ".zip", driftPhase: "vendor", wantRequests: 1, wantError: "pins changed during vendoring"},
		{name: "download timeout retains exit code", extension: ".zip", timeoutPhase: "download", wantError: "download failed or exceeded 540s"},
		{name: "verify timeout retains exit code", extension: ".zip", timeoutPhase: "verify", wantRequests: 1, wantError: "cache verification failed or exceeded 60s"},
		{name: "vendor timeout retains exit code", extension: ".zip", timeoutPhase: "vendor", wantRequests: 1, wantError: "offline vendoring failed or exceeded 180s"},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			mu.Lock()
			failures, requests, faultExtension = test.failures, 0, test.extension
			interruptBody, offlineMarker = test.interrupt, filepath.Join(dir, "offline")
			corruptArchive = test.corrupt
			mu.Unlock()
			sum := sums.Sum
			if test.wrongSum {
				sum = "h1:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
			}
			pinned := fmt.Sprintf("%s %s %s\n%s %s/go.mod %s\n", module, version, sum, module, version, sums.GoModSum)
			for name, body := range map[string]string{
				"agent/go.mod":  "module example.com/agent\n\ngo 1.27.0\n\nrequire " + module + " " + version + "\n",
				"agent/go.sum":  pinned,
				"agent/main.go": "package agent\nimport \"" + module + "\"\nvar Value = material.Value\n",
				// The host may not have timeout (macOS). Assert its exact argv
				// here; bound all real fixture Go commands with the test context.
				"bin/timeout": `#!/bin/sh
printf '%s\n' "$*" >>"$TIMEOUT_CALLS"
test "$GOWORK" = off && test "$GOTOOLCHAIN" = local && test -z "$GOFLAGS" || exit 99
shift 2
phase=$3
if test "$phase" != download; then test "$GOPROXY" = off || exit 98; fi
if test "$phase" = "$TIMEOUT_PHASE"; then exit 124; fi
"$@"
status=$?
if test "$status" != 0; then exit "$status"; fi
if test "$phase" = download; then : >"$OFFLINE_MARKER"; fi
if test "$phase" = "$PIN_DRIFT"; then printf '\n' >>go.sum; fi
`,
			} {
				path := filepath.Join(dir, name)
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			env := append(cacheEnv(t, dir),
				"PATH="+filepath.Join(dir, "bin")+":"+os.Getenv("PATH"), "TIMEOUT_CALLS="+filepath.Join(dir, "timeouts"),
				"GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1")
			for _, args := range [][]string{{"init", "-q"}, {"add", "agent"},
				{"-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "pinned fixture"}} {
				cmd := exec.CommandContext(t.Context(), "git", args...)
				cmd.Dir, cmd.Env = dir, env
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("fixture git: %v: %s", err, out)
				}
			}
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			// Substitute only the public proxy URL with the fixture, retaining
			// the exact authored fallback syntax, verification and vendoring.
			cmd := exec.CommandContext(ctx, command[0], command[1], strings.ReplaceAll(command[2], "https://proxy.golang.org", server.URL))
			cmd.Dir = dir
			// Ownership and conflicting host settings apply only to acquisition,
			// not to fixture creation or Go's own cache cleaner.
			cmd.Env = append(env, "GIT_TEST_ASSUME_DIFFERENT_OWNER=1", "GOWORK=auto", "GOTOOLCHAIN=unavailable",
				"GOFLAGS=-mod=bogus", "PIN_DRIFT="+test.driftPhase, "TIMEOUT_PHASE="+test.timeoutPhase,
				"OFFLINE_MARKER="+filepath.Join(dir, "offline"))
			out, err := cmd.CombinedOutput()
			if (err != nil) != (test.wantError != "") || (test.wantError != "" && !strings.Contains(string(out), test.wantError)) {
				t.Fatalf("preparation error=%v, want %q: %s", err, test.wantError, out)
			}
			if test.timeoutPhase != "" {
				if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 124 {
					t.Fatalf("timeout exit=%v, want 124: %s", err, out)
				}
			}
			mu.Lock()
			gotRequests := requests
			mu.Unlock()
			if gotRequests != test.wantRequests {
				t.Fatalf("%s requests=%d, want %d", test.extension, gotRequests, test.wantRequests)
			}
			calls, err := os.ReadFile(filepath.Join(dir, "timeouts"))
			if err != nil {
				t.Fatal(err)
			}
			want := "--kill-after=10s 540s go mod download\n"
			if test.wantError == "" || test.driftPhase == "vendor" || test.timeoutPhase == "verify" || test.timeoutPhase == "vendor" {
				want += "--kill-after=10s 60s go mod verify\n"
			}
			if test.wantError == "" || test.driftPhase == "vendor" || test.timeoutPhase == "vendor" {
				want += "--kill-after=10s 180s go mod vendor\n"
			}
			if test.wantError == "" {
				if _, err := os.Stat(filepath.Join(dir, "agent/vendor/example.com/material/material.go")); err != nil {
					t.Fatalf("offline vendoring did not produce fixture module: %v", err)
				}
			}
			if string(calls) != want {
				t.Fatalf("unexpected execution/retry boundaries: %s", calls)
			}
			actual, err := os.ReadFile(filepath.Join(dir, "agent/go.sum"))
			if err != nil || (test.driftPhase == "" && string(actual) != pinned) {
				t.Fatalf("pinned sums changed: %v: %s", err, actual)
			}
		})
	}
}
